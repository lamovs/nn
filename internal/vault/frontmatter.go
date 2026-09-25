package vault

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"
)

// ErrFrontmatter wraps a frontmatter block that is present but does not parse.
var ErrFrontmatter = errors.New("invalid frontmatter")

const bom = "\xef\xbb\xbf"

// ParseFrontmatter splits src into its YAML frontmatter and body. Without a
// frontmatter block, fields is nil and body is src whole; a block that
// does not parse gives the same result plus an error wrapping ErrFrontmatter.
func ParseFrontmatter(src string) (fields map[string]any, body string, bodyStartLine int, err error) {
	text := strings.TrimPrefix(src, bom)
	first, _, hasNewline := strings.Cut(text, "\n")
	if !hasNewline || strings.TrimRight(first, " \t\r") != "---" {
		return nil, text, 1, nil
	}

	pos := len(first) + 1
	var raw []string
	for line := 2; pos <= len(text); line++ {
		cur, _, ok := strings.Cut(text[pos:], "\n")
		if strings.TrimRight(cur, " \t\r") == "---" {
			fields, perr := parseYAML(raw, 2)
			if perr != nil {
				return nil, text, 1, perr
			}
			end := pos + len(cur)
			if ok {
				end++
			}
			return fields, text[end:], line + 1, nil
		}
		if !ok {
			break
		}
		raw = append(raw, cur)
		pos += len(cur) + 1
	}
	return nil, text, 1, nil
}

type yamlLine struct {
	num    int
	indent int
	text   string // indentation and trailing blanks removed
	raw    string // trailing CR removed
}

type yamlParser struct {
	lines []yamlLine
	pos   int
}

func parseYAML(raw []string, firstLine int) (map[string]any, error) {
	p := &yamlParser{lines: make([]yamlLine, len(raw))}
	for i, r := range raw {
		r = strings.TrimRight(r, "\r")
		t := strings.TrimLeft(r, " \t")
		p.lines[i] = yamlLine{
			num:    firstLine + i,
			indent: len(r) - len(t),
			text:   strings.TrimRight(t, " \t"),
			raw:    r,
		}
	}
	first, ok := p.peek()
	if !ok {
		return map[string]any{}, nil
	}
	m, err := p.mapping(first.indent)
	if err != nil {
		return nil, err
	}
	if l, ok := p.peek(); ok {
		return nil, p.errorf(l, "unexpected indentation")
	}
	return m, nil
}

func (p *yamlParser) errorf(l yamlLine, format string, args ...any) error {
	return fmt.Errorf("%w: line %d: %s", ErrFrontmatter, l.num, fmt.Sprintf(format, args...))
}

func (p *yamlParser) peek() (yamlLine, bool) {
	for p.pos < len(p.lines) {
		l := p.lines[p.pos]
		if l.text != "" && l.text[0] != '#' {
			return l, true
		}
		p.pos++
	}
	return yamlLine{}, false
}

func (p *yamlParser) mapping(indent int) (map[string]any, error) {
	m := map[string]any{}
	for {
		l, ok := p.peek()
		if !ok || l.indent < indent {
			return m, nil
		}
		if l.indent > indent {
			return nil, p.errorf(l, "unexpected indentation")
		}
		if isSeqItem(l.text) {
			return nil, p.errorf(l, "unexpected list item")
		}
		key, rest, ok := splitKey(l.text)
		if !ok {
			return nil, p.errorf(l, "expected key: value")
		}
		p.pos++
		val, err := p.value(l, rest, true)
		if err != nil {
			return nil, err
		}
		m[key] = val
	}
}

func (p *yamlParser) sequence(indent int) ([]any, error) {
	list := []any{}
	for {
		l, ok := p.peek()
		if !ok || l.indent < indent || l.indent == indent && !isSeqItem(l.text) {
			return list, nil
		}
		if l.indent > indent {
			return nil, p.errorf(l, "unexpected indentation")
		}
		item := strings.TrimLeft(l.text[1:], " \t")
		offset := len(l.text) - len(item)

		var val any
		var err error
		_, _, isKey := splitKey(item)
		switch {
		case item == "" || item[0] == '#':
			p.pos++
			val, err = p.value(l, "", false)
		case isSeqItem(item) || isKey:
			p.lines[p.pos].indent = l.indent + offset
			p.lines[p.pos].text = item
			if isSeqItem(item) {
				val, err = p.sequence(l.indent + offset)
			} else {
				val, err = p.mapping(l.indent + offset)
			}
		default:
			p.pos++
			val, err = p.value(l, item, false)
		}
		if err != nil {
			return nil, err
		}
		list = append(list, val)
	}
}

func (p *yamlParser) value(l yamlLine, rest string, inMapping bool) (any, error) {
	switch {
	case rest == "" || rest[0] == '#':
		n, ok := p.peek()
		if !ok {
			return nil, nil
		}
		if n.indent > l.indent {
			if isSeqItem(n.text) {
				return p.sequence(n.indent)
			}
			return p.mapping(n.indent)
		}
		if inMapping && n.indent == l.indent && isSeqItem(n.text) {
			return p.sequence(n.indent)
		}
		return nil, nil

	case rest[0] == '|' || rest[0] == '>':
		return p.blockScalar(l, rest)

	case rest[0] == '[' || rest[0] == '{' || rest[0] == '"' || rest[0] == '\'':
		closed := flowClosed
		if rest[0] == '"' || rest[0] == '\'' {
			closed = quoteClosed
		}
		s := rest
		for !closed(s) {
			if p.pos >= len(p.lines) {
				return nil, p.errorf(l, "unterminated value %q", rest)
			}
			s += " " + strings.TrimSpace(p.lines[p.pos].raw)
			p.pos++
		}
		f := &flowParser{s: s}
		v, err := f.value(false)
		if err == nil {
			err = f.end()
		}
		if err != nil {
			return nil, p.errorf(l, "%v", err)
		}
		return v, nil
	}

	s := stripComment(rest)
	for {
		n, ok := p.peek()
		if !ok || n.indent <= l.indent {
			break
		}
		if _, _, isKey := splitKey(n.text); isKey || isSeqItem(n.text) {
			return nil, p.errorf(n, "unexpected indentation")
		}
		s += " " + stripComment(n.text)
		p.pos++
	}
	return resolvePlain(s), nil
}

func (p *yamlParser) blockScalar(l yamlLine, header string) (any, error) {
	style := header[0]
	var chomp byte
	for _, c := range []byte(stripComment(header[1:])) {
		switch {
		case c == '-' || c == '+':
			chomp = c
		case c >= '1' && c <= '9':
		default:
			return nil, p.errorf(l, "bad block scalar header %q", header)
		}
	}

	var lines []string
	indent := -1
	for p.pos < len(p.lines) {
		r := p.lines[p.pos]
		if strings.TrimSpace(r.raw) == "" {
			lines = append(lines, "")
			p.pos++
			continue
		}
		if r.indent <= l.indent || indent >= 0 && r.indent < indent {
			break
		}
		if indent < 0 {
			indent = r.indent
		}
		lines = append(lines, r.raw[indent:])
		p.pos++
	}

	var text string
	if style == '|' {
		text = strings.Join(lines, "\n")
	} else {
		var b strings.Builder
		for i, line := range lines {
			switch {
			case line == "":
				b.WriteByte('\n')
			case i > 0 && lines[i-1] != "":
				b.WriteByte(' ')
				b.WriteString(line)
			default:
				b.WriteString(line)
			}
		}
		text = b.String()
	}
	trimmed := strings.TrimRight(text, "\n")
	switch {
	case chomp == '-':
		return trimmed, nil
	case chomp == '+':
		return text + "\n", nil
	case trimmed == "":
		return "", nil
	}
	return trimmed + "\n", nil
}

func isSeqItem(text string) bool {
	return text == "-" || strings.HasPrefix(text, "- ") || strings.HasPrefix(text, "-\t")
}

func splitKey(text string) (key, rest string, ok bool) {
	if text == "" {
		return "", "", false
	}
	if text[0] == '"' || text[0] == '\'' {
		f := &flowParser{s: text}
		k, err := f.quoted()
		if err != nil {
			return "", "", false
		}
		after := strings.TrimLeft(text[f.i:], " \t")
		if after == ":" || strings.HasPrefix(after, ": ") || strings.HasPrefix(after, ":\t") {
			return k, strings.TrimSpace(after[1:]), true
		}
		return "", "", false
	}
	if strings.IndexByte("[{#&*!|>%@`", text[0]) >= 0 {
		return "", "", false
	}
	for i := 0; i < len(text); i++ {
		switch {
		case text[i] == ':' && (i+1 == len(text) || text[i+1] == ' ' || text[i+1] == '\t'):
			key = strings.TrimSpace(text[:i])
			if key == "" {
				return "", "", false
			}
			return key, strings.TrimSpace(text[i+1:]), true
		case (text[i] == ' ' || text[i] == '\t') && i+1 < len(text) && text[i+1] == '#':
			return "", "", false
		}
	}
	return "", "", false
}

func stripComment(s string) string {
	for i := 0; i < len(s); i++ {
		if s[i] == '#' && (i == 0 || s[i-1] == ' ' || s[i-1] == '\t') {
			return strings.TrimSpace(s[:i])
		}
	}
	return strings.TrimSpace(s)
}

func flowClosed(s string) bool {
	depth := 0
	var quote, prev byte
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case quote == '"':
			if c == '\\' {
				i++
			} else if c == '"' {
				quote = 0
			}
		case quote == '\'':
			if c == '\'' {
				if i+1 < len(s) && s[i+1] == '\'' {
					i++
				} else {
					quote = 0
				}
			}
		case (c == '"' || c == '\'') && strings.IndexByte("[{,:", prev) >= 0:
			quote = c
		case c == '[' || c == '{':
			depth++
		case c == ']' || c == '}':
			depth--
			if depth <= 0 {
				return true
			}
		}
		if c != ' ' && c != '\t' {
			prev = c
		}
	}
	return false
}

func quoteClosed(s string) bool {
	f := &flowParser{s: s}
	_, err := f.quoted()
	return err == nil
}

type flowParser struct {
	s string
	i int
}

func (f *flowParser) skipSpace() {
	for f.i < len(f.s) && (f.s[f.i] == ' ' || f.s[f.i] == '\t') {
		f.i++
	}
}

func (f *flowParser) end() error {
	f.skipSpace()
	if f.i < len(f.s) && f.s[f.i] != '#' {
		return fmt.Errorf("unexpected %q after value", f.s[f.i:])
	}
	return nil
}

func (f *flowParser) value(mapKey bool) (any, error) {
	f.skipSpace()
	if f.i >= len(f.s) {
		return nil, nil
	}
	switch f.s[f.i] {
	case '[':
		return f.seq()
	case '{':
		return f.mapping()
	case '"', '\'':
		return f.quoted()
	}
	return resolvePlain(f.plain(mapKey)), nil
}

func (f *flowParser) plain(mapKey bool) string {
	start := f.i
	for f.i < len(f.s) {
		c := f.s[f.i]
		if strings.IndexByte(",[]{}", c) >= 0 {
			break
		}
		if c == '#' && f.i > start && (f.s[f.i-1] == ' ' || f.s[f.i-1] == '\t') {
			break
		}
		if mapKey && c == ':' && (f.i+1 == len(f.s) || strings.IndexByte(" \t,}", f.s[f.i+1]) >= 0) {
			break
		}
		f.i++
	}
	return strings.TrimSpace(f.s[start:f.i])
}

func (f *flowParser) seq() ([]any, error) {
	f.i++
	list := []any{}
	for {
		f.skipSpace()
		if f.i >= len(f.s) {
			return nil, errors.New("unterminated [")
		}
		if f.s[f.i] == ']' {
			f.i++
			return list, nil
		}
		v, err := f.value(false)
		if err != nil {
			return nil, err
		}
		list = append(list, v)
		f.skipSpace()
		if f.i < len(f.s) && f.s[f.i] == ',' {
			f.i++
			continue
		}
		if f.i < len(f.s) && f.s[f.i] == ']' {
			f.i++
			return list, nil
		}
		return nil, errors.New("expected , or ] in flow list")
	}
}

func (f *flowParser) mapping() (map[string]any, error) {
	f.i++
	m := map[string]any{}
	for {
		f.skipSpace()
		if f.i >= len(f.s) {
			return nil, errors.New("unterminated {")
		}
		if f.s[f.i] == '}' {
			f.i++
			return m, nil
		}
		k, err := f.value(true)
		if err != nil {
			return nil, err
		}
		key := ""
		if k != nil {
			key = fmt.Sprint(k)
		}
		var v any
		f.skipSpace()
		if f.i < len(f.s) && f.s[f.i] == ':' {
			f.i++
			if v, err = f.value(false); err != nil {
				return nil, err
			}
		}
		m[key] = v
		f.skipSpace()
		if f.i < len(f.s) && f.s[f.i] == ',' {
			f.i++
			continue
		}
		if f.i < len(f.s) && f.s[f.i] == '}' {
			f.i++
			return m, nil
		}
		return nil, errors.New("expected , or } in flow map")
	}
}

func (f *flowParser) quoted() (string, error) {
	q := f.s[f.i]
	f.i++
	var b strings.Builder
	for f.i < len(f.s) {
		c := f.s[f.i]
		switch {
		case q == '\'' && c == '\'':
			if f.i+1 < len(f.s) && f.s[f.i+1] == '\'' {
				b.WriteByte('\'')
				f.i += 2
				continue
			}
			f.i++
			return b.String(), nil
		case q == '"' && c == '"':
			f.i++
			return b.String(), nil
		case q == '"' && c == '\\' && f.i+1 < len(f.s):
			n, err := unescape(&b, f.s[f.i+1:])
			if err != nil {
				return "", err
			}
			f.i += 1 + n
		default:
			b.WriteByte(c)
			f.i++
		}
	}
	return "", errors.New("unterminated quoted string")
}

var simpleEscapes = map[byte]string{
	'0': "\x00", 'a': "\a", 'b': "\b", 't': "\t", '\t': "\t", 'n': "\n",
	'v': "\v", 'f': "\f", 'r': "\r", 'e': "\x1b", ' ': " ", '"': "\"",
	'/': "/", '\\': "\\", 'N': "\xc2\x85", '_': "\xc2\xa0",
}

func unescape(b *strings.Builder, s string) (int, error) {
	if out, ok := simpleEscapes[s[0]]; ok {
		b.WriteString(out)
		return 1, nil
	}
	width := map[byte]int{'x': 2, 'u': 4, 'U': 8}[s[0]]
	if width == 0 || len(s) < 1+width {
		return 0, fmt.Errorf("bad escape \\%c", s[0])
	}
	code, err := strconv.ParseUint(s[1:1+width], 16, 32)
	if err != nil || !utf8.ValidRune(rune(code)) {
		return 0, fmt.Errorf("bad escape \\%s", s[:1+width])
	}
	b.WriteRune(rune(code))
	return 1 + width, nil
}

var (
	intPattern   = regexp.MustCompile(`^[-+]?[0-9]+$`)
	floatPattern = regexp.MustCompile(`^[-+]?(\.[0-9]+|[0-9]+\.[0-9]*)([eE][-+]?[0-9]+)?$|^[-+]?[0-9]+[eE][-+]?[0-9]+$`)
)

func resolvePlain(s string) any {
	switch s {
	case "", "~", "null", "Null", "NULL":
		return nil
	case "true", "True", "TRUE":
		return true
	case "false", "False", "FALSE":
		return false
	}
	if intPattern.MatchString(s) {
		if n, err := strconv.ParseInt(s, 10, 64); err == nil {
			return int(n)
		}
		return s
	}
	if floatPattern.MatchString(s) {
		if n, err := strconv.ParseFloat(s, 64); err == nil {
			return n
		}
	}
	return s
}

func yamlString(s string, inFlow bool) string {
	if !needsQuotes(s, inFlow) {
		return s
	}
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch {
		case r == '"' || r == '\\':
			b.WriteByte('\\')
			b.WriteRune(r)
		case r == '\n':
			b.WriteString(`\n`)
		case r == '\t':
			b.WriteString(`\t`)
		case r < 0x20 || r == 0x7f:
			fmt.Fprintf(&b, `\x%02x`, r)
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String()
}

func needsQuotes(s string, inFlow bool) bool {
	if s == "" || s != strings.TrimSpace(s) {
		return true
	}
	if _, isString := resolvePlain(s).(string); !isString || intPattern.MatchString(s) {
		return true
	}
	if strings.IndexByte("-?:,[]{}#&*!|>'\"%@`", s[0]) >= 0 {
		return true
	}
	if strings.Contains(s, ": ") || strings.Contains(s, " #") || strings.HasSuffix(s, ":") {
		return true
	}
	if inFlow && (strings.ContainsAny(s, ",[]{}") || opensFlowQuote(s)) {
		return true
	}
	for _, r := range s {
		if r < 0x20 || r == 0x7f || r == 0x85 || r == 0xfeff {
			return true
		}
	}
	return false
}

func opensFlowQuote(s string) bool {
	var prev byte
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c == '"' || c == '\'') && strings.IndexByte("[{,:", prev) >= 0 {
			return true
		}
		if c != ' ' && c != '\t' {
			prev = c
		}
	}
	return false
}

func yamlFlowList(items []string) string {
	quoted := make([]string, len(items))
	for i, item := range items {
		quoted[i] = yamlString(item, true)
	}
	return "[" + strings.Join(quoted, ", ") + "]"
}
