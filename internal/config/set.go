package config

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"time"
)

// ManualEditError is Set declining to edit the file: nothing was written.
type ManualEditError struct {
	File    string
	Key     string
	Section string
	Line    string
	Reason  string
}

func (e *ManualEditError) Error() string {
	return fmt.Sprintf("cannot edit %s in place: %s", e.File, e.Reason)
}

// Set edits the file in place, reading it back to verify every other key
// decodes unchanged; where it cannot, the file is left untouched and the
// error is a *ManualEditError.
func Set(key, raw string) error {
	segs, value, err := prepare(key, raw)
	if err != nil {
		return err
	}

	path, err := Path()
	if err != nil {
		return err
	}
	// A config kept as a symlink stays one: the edit goes to its target.
	target, err := resolveLink(path)
	if err != nil {
		return err
	}
	unlock, err := lockConfig(target)
	if err != nil {
		return err
	}
	defer unlock()

	leaf := segs[len(segs)-1]
	manual := &ManualEditError{
		File:    path,
		Key:     joinKey(segs),
		Section: joinKey(section(segs)),
		Line:    TOMLKey(leaf) + " = " + literal(value),
	}

	src, err := os.ReadFile(target)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	// The decoder skips a leading BOM, so the line scanner must too.
	body, hadBOM := bytes.CutPrefix(src, utf8BOM)

	before, err := decode(body)
	if err != nil {
		manual.Reason = "it does not parse: " + err.Error()
		return manual
	}
	edited, reason := editInPlace(body, segs, literal(value))
	if reason != "" {
		manual.Reason = reason
		return manual
	}
	// A shape the scanner misreads must cost a manual edit, not the config.
	after, err := decode(edited)
	if err != nil || !onlyChanged(before, after, segs, value) {
		manual.Reason = "the edit would change more than " + manual.Key
		return manual
	}
	for _, p := range check(after).problems {
		if p.Key == manual.Key {
			return errors.New(p.detail)
		}
	}

	if hadBOM {
		edited = append(slices.Clone(utf8BOM), edited...)
	}
	// Only now, with every check passed, is anything written.
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return err
	}
	return atomicWrite(target, edited)
}

func Assignment(key, raw string) (string, error) {
	segs, value, err := prepare(key, raw)
	if err != nil {
		return "", err
	}
	return joinKey(segs) + " = " + literal(value), nil
}

func prepare(key, raw string) ([]string, any, error) {
	segs := strings.Split(key, ".")
	k, err := schemaKey(segs)
	if err != nil {
		return nil, nil, err
	}
	value, err := parseRaw(k, raw)
	if err != nil {
		return nil, nil, err
	}
	if _, kind, want := convert(k, value); kind != "" {
		return nil, nil, fmt.Errorf("%s = %s: want %s", key, literal(value), want)
	}
	if s, ok := value.(string); ok && s == "" && (k.Path == "vault.root" || k.Path == "vault.inbox") {
		return nil, nil, fmt.Errorf("%s must not be empty", key)
	}
	return segs, value, nil
}

const maxLinks = 40 // resolveLink calls the chain a loop past this

func resolveLink(path string) (string, error) {
	for range maxLinks {
		if real, err := filepath.EvalSymlinks(path); err == nil {
			return real, nil
		}
		info, err := os.Lstat(path)
		switch {
		case errors.Is(err, fs.ErrNotExist):
			return path, nil
		case err != nil:
			return "", err
		case info.Mode()&fs.ModeSymlink == 0:
			return path, nil
		}
		dest, err := os.Readlink(path)
		if err != nil {
			return "", err
		}
		if !filepath.IsAbs(dest) {
			dir := filepath.Dir(path)
			if real, err := filepath.EvalSymlinks(dir); err == nil {
				dir = real
			}
			dest = filepath.Join(dir, dest)
		}
		path = dest
	}
	return "", fmt.Errorf("%s: too many levels of symbolic links", path)
}

var lockWait = 5 * time.Second // a variable so a test can wait less

const lockPoll = 10 * time.Millisecond

// lockPath names the lock after a hash of target's canonical path, so it is
// the same lock before the config exists and after.
func lockPath(target string) (string, error) {
	canon, err := canonical(target)
	if err != nil {
		return "", err
	}
	dataDir := os.Getenv("XDG_DATA_HOME")
	if !filepath.IsAbs(dataDir) {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		dataDir = filepath.Join(home, ".local", "share")
	}
	sum := sha256.Sum256([]byte(canon))
	return filepath.Join(dataDir, dirName, "locks", "config-"+hex.EncodeToString(sum[:])[:16]+".lock"), nil
}

// canonical resolves symlinks on path's deepest existing ancestor only.
func canonical(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	rest := ""
	for dir := abs; ; {
		if real, err := filepath.EvalSymlinks(dir); err == nil {
			return filepath.Join(real, rest), nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return abs, nil
		}
		rest = filepath.Join(filepath.Base(dir), rest)
		dir = parent
	}
}

// lockConfig serializes concurrent Set calls so neither loses the other's key.
// The lock is its own file under nn's data dir: the config is replaced by
// rename, which would drop a lock held on it, and a lock beside it would land
// in a dotfiles repo. Best effort: with nowhere to put it, the edit goes
// ahead unlocked.
func lockConfig(target string) (unlock func(), err error) {
	noLock := func() {}
	file, err := lockPath(target)
	if err != nil {
		return noLock, nil
	}
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		return noLock, nil
	}
	f, err := os.OpenFile(file, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return noLock, nil
	}
	deadline := time.Now().Add(lockWait)
	for {
		switch err := lockFile(f); {
		case err == nil:
			return func() {
				_ = unlockFile(f)
				_ = f.Close()
			}, nil
		case !errors.Is(err, errLocked):
			// errNoLocking, or a file system that refuses flock.
			_ = f.Close()
			return noLock, nil
		}
		if time.Now().After(deadline) {
			_ = f.Close()
			return nil, fmt.Errorf("%s is being edited by another nn; try again", filepath.Base(target))
		}
		time.Sleep(lockPoll)
	}
}

var utf8BOM = []byte{0xEF, 0xBB, 0xBF}

// onlyChanged reports whether after differs from before at target only.
func onlyChanged(before, after map[string]any, target []string, value any) bool {
	b, a := flatten(before), flatten(after)
	t := strings.Join(target, "\x00")
	got, ok := a[t]
	if !ok || !reflect.DeepEqual(got, value) {
		return false
	}
	// Also drops a now-nonempty section's own empty-table leaf.
	drop := func(m map[string]any) {
		for k := range m {
			if k == t || strings.HasPrefix(k, t+"\x00") || strings.HasPrefix(t, k+"\x00") {
				delete(m, k)
			}
		}
	}
	drop(b)
	drop(a)
	return reflect.DeepEqual(a, b)
}

// flatten: keys are NUL-joined so a quoted key with a dot stays one
// segment. NaN becomes nanValue, which compares equal to itself.
func flatten(doc map[string]any) map[string]any {
	out := map[string]any{}
	var walk func(prefix string, m map[string]any)
	walk = func(prefix string, m map[string]any) {
		for k, v := range m {
			p := k
			if prefix != "" {
				p = prefix + "\x00" + k
			}
			if sub, ok := v.(map[string]any); ok {
				if len(sub) == 0 {
					out[p] = emptyTable{}
				}
				walk(p, sub)
				continue
			}
			out[p] = withoutNaN(v)
		}
	}
	walk("", doc)
	return out
}

type (
	emptyTable struct{}
	nanValue   struct{}
)

func withoutNaN(v any) any {
	switch x := v.(type) {
	case float64:
		if math.IsNaN(x) {
			return nanValue{}
		}
	case []any:
		out := make([]any, len(x))
		for i, item := range x {
			out[i] = withoutNaN(item)
		}
		return out
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, item := range x {
			out[k] = withoutNaN(item)
		}
		return out
	case []map[string]any:
		out := make([]map[string]any, len(x))
		for i, item := range x {
			out[i] = withoutNaN(item).(map[string]any)
		}
		return out
	}
	return v
}

// editInPlace: reason says why it could not, when it could not.
func editInPlace(body []byte, target []string, value string) (edited []byte, reason string) {
	sec := section(target)
	line := TOMLKey(target[len(target)-1]) + " = " + value
	header := "[" + joinKey(sec) + "]"
	if len(bytes.TrimSpace(body)) == 0 {
		eol := fileEOL(splitLines(body))
		return []byte(header + eol + line + eol), ""
	}

	lines, stmts, err := scanTOML(body)
	if err != nil {
		return nil, err.Error()
	}

	var table []string
	inArray := false
	found, headerLine, lastLine, dottedLine := -1, -1, -1, -1
	indent := ""
	for i, s := range stmts {
		if s.kind != stmtKeyValue {
			table, inArray = s.key, s.kind == stmtArrayTable
			switch {
			case inArray && hasPrefix(target, table):
				return nil, fmt.Sprintf("[[%s]] on line %d is an array of tables", joinKey(table), s.line+1)
			case inArray && hasPrefix(table, target):
				return nil, fmt.Sprintf("[[%s]] on line %d makes %s an array of tables", joinKey(table), s.line+1, joinKey(target))
			case !inArray && hasPrefix(table, target):
				return nil, fmt.Sprintf("[%s] on line %d makes %s a table", joinKey(table), s.line+1, joinKey(target))
			case !inArray && slices.Equal(table, sec):
				headerLine, lastLine = s.line, s.line
			}
			continue
		}
		if inArray {
			continue
		}
		full := append(slices.Clone(table), s.key...)
		switch {
		case slices.Equal(full, target) && len(s.key) > 1:
			return nil, fmt.Sprintf("%s is set with a dotted key on line %d", joinKey(target), s.line+1)
		case slices.Equal(full, target) && s.valEnd < 0:
			return nil, fmt.Sprintf("%s on line %d spans more than one line", joinKey(target), s.line+1)
		case slices.Equal(full, target):
			found = i
		case hasPrefix(target, full) && strings.HasPrefix(lines[s.line].text[s.valStart:], "{"):
			return nil, fmt.Sprintf("%s on line %d is an inline table", joinKey(full), s.line+1)
		case hasPrefix(target, full):
			return nil, fmt.Sprintf("%s on line %d is a value, not a table", joinKey(full), s.line+1)
		case hasPrefix(full, target):
			return nil, fmt.Sprintf("line %d makes %s a table", s.line+1, joinKey(target))
		case len(s.key) > 1 && len(table) < len(sec) && hasPrefix(full[:len(full)-1], sec):
			dottedLine = s.line
		}
		if slices.Equal(table, sec) {
			lastLine = s.last
			indent = leadingSpace(lines[s.line].text)
		}
	}

	eol := fileEOL(lines)
	switch {
	case found >= 0:
		s := stmts[found]
		l := &lines[s.line]
		l.text = l.text[:s.valStart] + value + l.text[s.valEnd:]
	case headerLine >= 0:
		lines = insertLine(lines, lastLine+1, indent+line, eol)
	case dottedLine >= 0:
		return nil, fmt.Sprintf("[%s] is made with dotted keys on line %d", joinKey(sec), dottedLine+1)
	default:
		if last := lines[len(lines)-1]; strings.TrimSpace(last.text) != "" {
			lines = insertLine(lines, len(lines), "", eol)
		}
		lines = insertLine(lines, len(lines), header, eol)
		lines = insertLine(lines, len(lines), line, eol)
	}
	return joinLines(lines), ""
}

func hasPrefix(path, prefix []string) bool {
	return len(path) >= len(prefix) && slices.Equal(path[:len(prefix)], prefix)
}

func leadingSpace(s string) string {
	return s[:len(s)-len(strings.TrimLeft(s, " \t"))]
}

type rawLine struct {
	text string
	eol  string
}

func splitLines(src []byte) []rawLine {
	var lines []rawLine
	s := string(src)
	for len(s) > 0 {
		i := strings.IndexByte(s, '\n')
		if i < 0 {
			lines = append(lines, rawLine{text: s})
			break
		}
		text, eol := s[:i], "\n"
		if strings.HasSuffix(text, "\r") {
			text, eol = text[:len(text)-1], "\r\n"
		}
		lines = append(lines, rawLine{text: text, eol: eol})
		s = s[i+1:]
	}
	return lines
}

func joinLines(lines []rawLine) []byte {
	var b strings.Builder
	for _, l := range lines {
		b.WriteString(l.text)
		b.WriteString(l.eol)
	}
	return []byte(b.String())
}

func fileEOL(lines []rawLine) string {
	for _, l := range lines {
		if l.eol != "" {
			return l.eol
		}
	}
	return "\n"
}

func insertLine(lines []rawLine, i int, text, eol string) []rawLine {
	if i == len(lines) && i > 0 && lines[i-1].eol == "" {
		lines[i-1].eol = eol
	}
	return slices.Insert(lines, i, rawLine{text: text, eol: eol})
}

type stmtKind int

const (
	stmtTable stmtKind = iota
	stmtArrayTable
	stmtKeyValue
)

type stmt struct {
	kind stmtKind
	key  []string

	line, last int // differ for a value spread over several lines

	valStart, valEnd int // valEnd is -1 when the value does not end on this line
}

// scanTOML is a line scanner, not a parser; body must not start with a BOM.
func scanTOML(body []byte) ([]rawLine, []stmt, error) {
	lines := splitLines(body)
	var stmts []stmt
	var lx lexer
	for i, l := range lines {
		if lx.open() {
			lx.run(l.text)
			stmts[len(stmts)-1].last = i
			continue
		}
		trimmed := strings.TrimLeft(l.text, " \t")
		switch {
		case trimmed == "" || trimmed[0] == '#':
		case trimmed[0] == '[':
			s, ok := parseHeader(l.text)
			if !ok {
				return nil, nil, fmt.Errorf("line %d is not a table header nn can read", i+1)
			}
			s.line, s.last = i, i
			stmts = append(stmts, s)
		default:
			s, ok := parseKeyValue(l.text)
			if !ok {
				return nil, nil, fmt.Errorf("line %d is not a key nn can read", i+1)
			}
			s.line, s.last = i, i
			lx.run(l.text[s.valStart:])
			stmts = append(stmts, s)
		}
	}
	return lines, stmts, nil
}

func parseHeader(text string) (stmt, bool) {
	t := strings.TrimLeft(text, " \t")
	s := stmt{kind: stmtTable}
	open, closing := "[", "]"
	if strings.HasPrefix(t, "[[") {
		s.kind, open, closing = stmtArrayTable, "[[", "]]"
	}
	key, rest, ok := parseKey(t[len(open):])
	if !ok {
		return s, false
	}
	rest, ok = strings.CutPrefix(strings.TrimLeft(rest, " \t"), closing)
	if !ok {
		return s, false
	}
	if rest = strings.TrimLeft(rest, " \t"); rest != "" && rest[0] != '#' {
		return s, false
	}
	s.key = key
	return s, true
}

func parseKeyValue(text string) (stmt, bool) {
	s := stmt{kind: stmtKeyValue}
	key, rest, ok := parseKey(text)
	if !ok {
		return s, false
	}
	rest, ok = strings.CutPrefix(strings.TrimLeft(rest, " \t"), "=")
	if !ok {
		return s, false
	}
	rest = strings.TrimLeft(rest, " \t")
	s.key = key
	s.valStart = len(text) - len(rest)
	s.valEnd = valueEnd(text, s.valStart)
	return s, true
}

func parseKey(s string) (segs []string, rest string, ok bool) {
	for {
		s = strings.TrimLeft(s, " \t")
		var seg string
		switch {
		case strings.HasPrefix(s, `"`):
			end := basicStringEnd(s)
			if end < 0 {
				return nil, "", false
			}
			unq, err := strconv.Unquote(s[:end])
			if err != nil {
				return nil, "", false
			}
			seg, s = unq, s[end:]
		case strings.HasPrefix(s, "'"):
			j := strings.IndexByte(s[1:], '\'')
			if j < 0 {
				return nil, "", false
			}
			seg, s = s[1:1+j], s[j+2:]
		default:
			n := strings.IndexFunc(s, func(r rune) bool {
				return !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '-')
			})
			if n < 0 {
				n = len(s)
			}
			if n == 0 {
				return nil, "", false
			}
			seg, s = s[:n], s[n:]
		}
		segs = append(segs, seg)
		after := strings.TrimLeft(s, " \t")
		if !strings.HasPrefix(after, ".") {
			return segs, s, true
		}
		s = after[1:]
	}
}

// basicStringEnd is -1 when the string does not close on this line.
func basicStringEnd(s string) int {
	for i := 1; i < len(s); i++ {
		switch s[i] {
		case '\\':
			i++
		case '"':
			return i + 1
		}
	}
	return -1
}

// valueEnd is -1 for a multi-line string or a value that does not end here.
func valueEnd(text string, start int) int {
	s := text[start:]
	switch {
	case strings.HasPrefix(s, `"""`), strings.HasPrefix(s, "'''"):
		return -1
	case strings.HasPrefix(s, `"`):
		if end := basicStringEnd(s); end >= 0 {
			return start + end
		}
		return -1
	case strings.HasPrefix(s, "'"):
		if j := strings.IndexByte(s[1:], '\''); j >= 0 {
			return start + j + 2
		}
		return -1
	case strings.HasPrefix(s, "["), strings.HasPrefix(s, "{"):
		var lx lexer
		for i := 0; i < len(s); i++ {
			i = lx.step(s, i)
			if i < 0 {
				return -1
			}
			if !lx.open() {
				return start + i + 1
			}
		}
		return -1
	}
	end := len(s)
	if i := strings.IndexByte(s, '#'); i >= 0 {
		end = i
	}
	return start + len(strings.TrimRight(s[:end], " \t"))
}

type lexer struct {
	mlBasic, mlLiteral bool
	depth              int
}

func (lx *lexer) open() bool { return lx.mlBasic || lx.mlLiteral || lx.depth > 0 }

func (lx *lexer) run(text string) {
	for i := 0; i < len(text); i++ {
		if i = lx.step(text, i); i < 0 {
			return
		}
	}
}

// step returns -1 when the rest of the line is a comment or an unclosed string.
func (lx *lexer) step(text string, i int) int {
	s := text[i:]
	switch {
	case lx.mlBasic:
		if s[0] == '\\' {
			return i + 1
		}
		if strings.HasPrefix(s, `"""`) {
			lx.mlBasic = false
			return i + quoteRun(s, '"') - 1
		}
	case lx.mlLiteral:
		if strings.HasPrefix(s, "'''") {
			lx.mlLiteral = false
			return i + quoteRun(s, '\'') - 1
		}
	case s[0] == '#':
		return -1
	case strings.HasPrefix(s, `"""`):
		lx.mlBasic = true
		return i + 2
	case strings.HasPrefix(s, "'''"):
		lx.mlLiteral = true
		return i + 2
	case s[0] == '"':
		end := basicStringEnd(s)
		if end < 0 {
			return -1
		}
		return i + end - 1
	case s[0] == '\'':
		j := strings.IndexByte(s[1:], '\'')
		if j < 0 {
			return -1
		}
		return i + j + 1
	case s[0] == '[' || s[0] == '{':
		lx.depth++
	case s[0] == ']' || s[0] == '}':
		lx.depth--
	}
	return i
}

// quoteRun caps at five: up to two quotes of its own before the closing three.
func quoteRun(s string, q byte) int {
	n := 0
	for n < len(s) && n < 5 && s[n] == q {
		n++
	}
	return n
}
