package vault

import (
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"unicode"
	"unicode/utf8"
)

// CodeBlock is a fenced code block, with 1-based file line numbers.
type CodeBlock struct {
	Lang, Code         string
	StartLine, EndLine int
}

type fence struct {
	char   byte
	n      int
	indent int
}

func openFence(line string) (f fence, info string, ok bool) {
	t := strings.TrimLeft(line, " \t")
	if len(t) < 3 || t[0] != '`' && t[0] != '~' {
		return fence{}, "", false
	}
	n := 0
	for n < len(t) && t[n] == t[0] {
		n++
	}
	if n < 3 {
		return fence{}, "", false
	}
	info = strings.TrimSpace(t[n:])
	if t[0] == '`' && strings.Contains(info, "`") {
		return fence{}, "", false
	}
	return fence{char: t[0], n: n, indent: len(line) - len(t)}, info, true
}

func (f fence) closedBy(line string) bool {
	t := strings.TrimLeft(line, " \t")
	n := 0
	for n < len(t) && t[n] == f.char {
		n++
	}
	return n >= f.n && strings.TrimSpace(t[n:]) == ""
}

func splitLines(body string) []string {
	body = strings.TrimSuffix(body, "\n")
	if body == "" {
		return nil
	}
	lines := strings.Split(body, "\n")
	for i, l := range lines {
		lines[i] = strings.TrimSuffix(l, "\r")
	}
	return lines
}

func CodeBlocks(body string, bodyStartLine int) []CodeBlock {
	lines := splitLines(body)
	var blocks []CodeBlock
	for i := 0; i < len(lines); i++ {
		f, info, ok := openFence(lines[i])
		if !ok {
			continue
		}
		start := i
		end := len(lines) - 1
		var code []string
		for i++; i < len(lines); i++ {
			if f.closedBy(lines[i]) {
				end = i
				break
			}
			code = append(code, dedent(lines[i], f.indent))
		}
		lang, _, _ := strings.Cut(info, " ")
		lang = strings.Trim(lang, "{}.")
		blocks = append(blocks, CodeBlock{
			Lang:      lang,
			Code:      strings.Join(code, "\n"),
			StartLine: bodyStartLine + start,
			EndLine:   bodyStartLine + end,
		})
	}
	return blocks
}

func dedent(line string, n int) string {
	i := 0
	for i < n && i < len(line) && (line[i] == ' ' || line[i] == '\t') {
		i++
	}
	return line[i:]
}

// MaskCode returns body with code blocks and spans blanked byte for byte, so offsets still match.
func MaskCode(body string) string {
	var b strings.Builder
	b.Grow(len(body))
	var open *fence
	for _, line := range strings.SplitAfter(body, "\n") {
		content := strings.TrimRight(line, "\r\n")
		switch {
		case open != nil:
			if open.closedBy(content) {
				open = nil
			}
			b.WriteString(blank(line))
		default:
			if f, _, ok := openFence(content); ok {
				open = &f
				b.WriteString(blank(line))
				continue
			}
			b.WriteString(maskInlineCode(line))
		}
	}
	return b.String()
}

func blank(s string) string {
	out := []byte(s)
	for i, c := range out {
		if c != '\n' && c != '\r' {
			out[i] = ' '
		}
	}
	return string(out)
}

func maskInlineCode(line string) string {
	if !strings.Contains(line, "`") {
		return line
	}
	out := []byte(line)
	for i := 0; i < len(out); {
		if out[i] != '`' {
			i++
			continue
		}
		n := 0
		for i+n < len(out) && out[i+n] == '`' {
			n++
		}
		closeAt := -1
		for j := i + n; j < len(out); {
			if out[j] != '`' {
				j++
				continue
			}
			m := 0
			for j+m < len(out) && out[j+m] == '`' {
				m++
			}
			if m == n {
				closeAt = j
				break
			}
			j += m
		}
		if closeAt < 0 {
			i += n
			continue
		}
		for k := i; k < closeAt+n; k++ {
			out[k] = ' '
		}
		i = closeAt + n
	}
	return string(out)
}

// InlineTags returns the #tags in body, without "#", first-appearance order, case-fold deduplicated.
func InlineTags(body string) []string {
	masked := MaskCode(body)
	var tags []string
	seen := map[string]bool{}
	for _, line := range strings.Split(masked, "\n") {
		for i := 0; i < len(line); i++ {
			if line[i] != '#' {
				continue
			}
			if i > 0 {
				prev, _ := utf8.DecodeLastRuneInString(line[:i])
				if !unicode.IsSpace(prev) {
					continue
				}
			}
			j := i + 1
			for j < len(line) {
				r, size := utf8.DecodeRuneInString(line[j:])
				if !isTagRune(r) {
					break
				}
				j += size
			}
			tag := strings.TrimRight(line[i+1:j], "/")
			i = j - 1
			if !validTag(tag) {
				continue
			}
			key := strings.ToLower(tag)
			if !seen[key] {
				seen[key] = true
				tags = append(tags, tag)
			}
		}
	}
	return tags
}

func isTagRune(r rune) bool {
	return unicode.IsLetter(r) || unicode.IsDigit(r) || unicode.IsMark(r) || r == '_' || r == '-' || r == '/'
}

func validTag(tag string) bool {
	alnum, nonDigit := false, false
	for _, r := range tag {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			alnum = true
		}
		if !unicode.IsDigit(r) {
			nonDigit = true
		}
	}
	return alnum && nonDigit
}

func firstH1(body string) string {
	masked := strings.Split(MaskCode(body), "\n")
	lines := strings.Split(body, "\n")
	for i, m := range masked {
		t := strings.TrimLeft(m, " ")
		if len(m)-len(t) > 3 || !(strings.HasPrefix(t, "# ") || strings.HasPrefix(t, "#\t")) {
			continue
		}
		text := strings.TrimSpace(strings.TrimRight(lines[i], "\r"))[1:]
		text = strings.TrimSpace(text)
		if trimmed := strings.TrimRight(text, "#"); trimmed != text && (trimmed == "" || strings.HasSuffix(trimmed, " ")) {
			text = strings.TrimSpace(trimmed)
		}
		if text != "" {
			return text
		}
	}
	return ""
}

func (v *Vault) resolveEmbeds(rel, body string, images *lazyImages) []string {
	var out []string
	seen := map[string]bool{}
	add := func(target string, markdown bool) {
		if !IsImage(target) {
			return
		}
		p := v.resolveEmbed(rel, target, markdown, images)
		if !seen[p] {
			seen[p] = true
			out = append(out, p)
		}
	}
	for _, line := range strings.Split(MaskCode(body), "\n") {
		for i := 0; i < len(line); i++ {
			if line[i] != '!' || i+1 >= len(line) || line[i+1] != '[' {
				continue
			}
			if strings.HasPrefix(line[i+1:], "[[") {
				end := strings.Index(line[i+3:], "]]")
				if end < 0 {
					continue
				}
				inner := line[i+3 : i+3+end]
				target, _, _ := strings.Cut(inner, "|")
				target, _, _ = strings.Cut(target, "#")
				add(strings.TrimSuffix(strings.TrimSpace(target), "\\"), false)
				i += 3 + end + 1
				continue
			}
			if _, dest, next, ok := MarkdownLinkAt(line, i+1); ok {
				if local, ok := LocalTarget(dest); ok {
					add(local, true)
				}
				i = next - 1
			}
		}
	}
	return out
}

// MarkdownLinkAt parses a [text](destination) link whose "[" is at line[i].
func MarkdownLinkAt(line string, i int) (text, dest string, next int, ok bool) {
	depth := 0
	j := i
	for ; j < len(line); j++ {
		switch line[j] {
		case '\\':
			j++
			continue
		case '[':
			depth++
		case ']':
			depth--
		}
		if depth == 0 {
			break
		}
	}
	if j+1 >= len(line) || line[j+1] != '(' {
		return "", "", 0, false
	}
	text = line[i+1 : j]
	start := j + 2
	if start < len(line) && line[start] == '<' {
		end := strings.IndexByte(line[start:], '>')
		if end < 0 {
			return "", "", 0, false
		}
		paren := strings.IndexByte(line[start+end:], ')')
		if paren < 0 {
			return "", "", 0, false
		}
		return text, line[start+1 : start+end], start + end + paren + 1, true
	}
	depth = 1
	k := start
	for ; k < len(line); k++ {
		switch line[k] {
		case '\\':
			k++
			continue
		case '(':
			depth++
		case ')':
			depth--
		}
		if depth == 0 {
			break
		}
	}
	if k >= len(line) {
		return "", "", 0, false
	}
	dest = strings.TrimSpace(line[start:k])
	if sp := strings.IndexAny(dest, " \t"); sp >= 0 {
		dest = dest[:sp]
	}
	return text, dest, k + 1, true
}

// LocalTarget turns a markdown link destination into a vault path, or rejects a URL or bare "#anchor".
func LocalTarget(dest string) (string, bool) {
	dest = strings.TrimSpace(dest)
	if dest == "" || dest[0] == '#' {
		return "", false
	}
	if colon := strings.IndexByte(dest, ':'); colon > 0 {
		scheme := dest[:colon]
		isScheme := true
		for _, r := range scheme {
			if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '+' || r == '-' || r == '.') {
				isScheme = false
				break
			}
		}
		if isScheme {
			return "", false
		}
	}
	if decoded, err := url.PathUnescape(dest); err == nil {
		dest = decoded
	}
	return dest, true
}

var cyrillic = [32]string{
	"a", "b", "v", "g", "d", "e", "zh", "z", "i", "y", "k", "l", "m", "n", "o", "p",
	"r", "s", "t", "u", "f", "h", "c", "ch", "sh", "sch", "", "y", "", "e", "yu", "ya",
}

var extraLetters = map[rune]string{
	0x0451: "e", 0x0454: "e", 0x0456: "i", 0x0457: "yi", 0x0491: "g", 0x045e: "u",
	0x00e0: "a", 0x00e1: "a", 0x00e2: "a", 0x00e3: "a", 0x00e4: "a", 0x00e5: "a",
	0x00e6: "ae", 0x00e7: "c", 0x00e8: "e", 0x00e9: "e", 0x00ea: "e", 0x00eb: "e",
	0x00ec: "i", 0x00ed: "i", 0x00ee: "i", 0x00ef: "i", 0x00f1: "n", 0x00f2: "o",
	0x00f3: "o", 0x00f4: "o", 0x00f5: "o", 0x00f6: "o", 0x00f8: "o", 0x00f9: "u",
	0x00fa: "u", 0x00fb: "u", 0x00fc: "u", 0x00fd: "y", 0x00ff: "y", 0x00df: "ss",
}

// latinExtendedA spells Latin Extended-A (U+0100-U+017F) in ASCII, indexed by offset.
var latinExtendedA = [0x80]string{
	"a", "a", "a", "a", "a", "a",
	"c", "c", "c", "c", "c", "c", "c", "c",
	"d", "d", "d", "d",
	"e", "e", "e", "e", "e", "e", "e", "e", "e", "e",
	"g", "g", "g", "g", "g", "g", "g", "g",
	"h", "h", "h", "h",
	"i", "i", "i", "i", "i", "i", "i", "i", "i", "i",
	"ij", "ij",
	"j", "j",
	"k", "k", "k",
	"l", "l", "l", "l", "l", "l", "l", "l", "l", "l",
	"n", "n", "n", "n", "n", "n", "n",
	"ng", "ng",
	"o", "o", "o", "o", "o", "o",
	"oe", "oe",
	"r", "r", "r", "r", "r", "r",
	"s", "s", "s", "s", "s", "s", "s", "s",
	"t", "t", "t", "t", "t", "t",
	"u", "u", "u", "u", "u", "u", "u", "u", "u", "u", "u", "u",
	"w", "w",
	"y", "y", "y",
	"z", "z", "z", "z", "z", "z",
	"s",
}

// precomposed holds letters a decomposed (NFD) base+mark pair spells.
var precomposed = map[[2]rune]rune{
	{0x0435, 0x0300}: 0x0450, {0x0435, 0x0308}: 0x0451, {0x0433, 0x0301}: 0x0453,
	{0x0456, 0x0308}: 0x0457, {0x043a, 0x0301}: 0x045c, {0x0438, 0x0300}: 0x045d,
	{0x0438, 0x0306}: 0x0439, {0x0443, 0x0306}: 0x045e,
}

func isMark(r rune) bool { return r >= 0x0300 && unicode.Is(unicode.M, r) }

// Transliterate lowercases s and spells Cyrillic/accented Latin letters in ASCII, folding NFD text back first.
func Transliterate(s string) string {
	var b strings.Builder
	lower := strings.ToLower(s)
	for i := 0; i < len(lower); {
		r, size := utf8.DecodeRuneInString(lower[i:])
		i += size
		for i < len(lower) {
			mark, n := utf8.DecodeRuneInString(lower[i:])
			if !isMark(mark) {
				break
			}
			if composed, ok := precomposed[[2]rune{r, mark}]; ok {
				r = composed
			}
			i += n
		}
		switch {
		case isMark(r):
			// A mark with no letter in front of it spells nothing.
		case r >= 0x0430 && r <= 0x044f:
			b.WriteString(cyrillic[r-0x0430])
		case r >= 0x0100 && r <= 0x017f:
			b.WriteString(latinExtendedA[r-0x0100])
		default:
			if latin, ok := extraLetters[r]; ok {
				b.WriteString(latin)
			} else {
				b.WriteRune(r)
			}
		}
	}
	return b.String()
}

const maxSlug = 60

// Slug turns s into a file stem: transliterated, lowercase, [a-z0-9-], at most 60 characters.
func Slug(s string) string {
	var b strings.Builder
	dash := false
	for _, r := range Transliterate(s) {
		switch {
		case r >= 'a' && r <= 'z' || r >= '0' && r <= '9':
			if dash && b.Len() > 0 {
				b.WriteByte('-')
			}
			dash = false
			b.WriteRune(r)
		case r == '\'' || r == 0x2019:
		default:
			dash = true
		}
	}
	out := b.String()
	if len(out) > maxSlug {
		cut := out[:maxSlug]
		if out[maxSlug] != '-' {
			if k := strings.LastIndexByte(cut, '-'); k >= maxSlug/2 {
				cut = cut[:k]
			}
		}
		out = strings.TrimRight(cut, "-")
	}
	return out
}

// Context returns cwd (with "~" for home) and its enclosing git work tree, if any.
func Context(cwd string) (where, repo string) {
	if cwd == "" {
		wd, err := os.Getwd()
		if err != nil {
			return "", ""
		}
		cwd = wd
	}
	cwd = filepath.Clean(cwd)
	where = cwd
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		home = filepath.Clean(home)
		switch {
		case cwd == home:
			where = "~"
		case strings.HasPrefix(cwd, home+string(filepath.Separator)):
			where = "~" + cwd[len(home):]
		}
	}
	for dir := cwd; ; {
		if _, err := os.Lstat(filepath.Join(dir, ".git")); err == nil {
			repo = filepath.Base(dir)
			break
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return filepath.ToSlash(where), repo
}
