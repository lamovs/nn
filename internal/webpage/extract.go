package webpage

import (
	"html"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	maxTextBytes  = 128 << 10 // the model summarizes the start
	maxFieldBytes = 4 << 10   // title and description
	mainMinRunes  = 500       // for a main/article element to replace the body
)

var skippedElements = map[string]bool{
	"script": true, "style": true, "noscript": true, "template": true, "svg": true,
	"math": true, "iframe": true, "object": true, "canvas": true, "nav": true,
	"aside": true, "footer": true, "button": true, "select": true,
}

var (
	paragraphElements = map[string]bool{
		"p": true, "h1": true, "h2": true, "h3": true, "h4": true, "h5": true, "h6": true,
		"section": true, "article": true, "header": true, "blockquote": true, "pre": true,
		"ul": true, "ol": true, "table": true, "hr": true,
	}
	lineElements = map[string]bool{
		"div": true, "li": true, "tr": true, "dt": true, "dd": true, "figcaption": true,
	}
)

// extractHTML falls back to og:title/og:description and main/article.
func extractHTML(doc string) (title, description, text string, truncated bool) {
	var (
		z                   = newTokenizer(doc)
		all                 = &textWriter{}
		region              *textWriter
		regions             []string
		skip                = map[string]int{}
		skipping            int
		mainDepth, preDepth int
		inTitle             bool
		ogTitle, ogDesc     string
		haveTitle, haveDesc bool
	)
	writers := func(fn func(w *textWriter)) {
		fn(all)
		if region != nil {
			fn(region)
		}
	}
	for {
		t, ok := z.next()
		if !ok {
			break
		}
		switch t.kind {
		case textToken:
			if inTitle {
				inTitle = false
				if skipping == 0 && !haveTitle {
					title = cleanField(html.UnescapeString(t.text))
					haveTitle = title != ""
				}
				continue
			}
			if skipping > 0 {
				continue
			}
			s := html.UnescapeString(t.text)
			writers(func(w *textWriter) { w.text(s, preDepth > 0) })
		case startTagToken:
			if skippedElements[t.name] {
				if !(t.selfClosing && (t.name == "svg" || t.name == "math")) {
					skip[t.name]++
					skipping++
				}
				continue
			}
			if skipping > 0 {
				continue
			}
			switch t.name {
			case "title":
				inTitle = true
				continue
			case "meta":
				content, _ := t.attr("content")
				name, _ := t.attr("name")
				prop, _ := t.attr("property")
				name, prop = strings.ToLower(strings.TrimSpace(name)), strings.ToLower(strings.TrimSpace(prop))
				switch {
				case name == "description" && !haveDesc:
					description = cleanField(content)
					haveDesc = description != ""
				case (prop == "og:title" || name == "og:title") && ogTitle == "":
					ogTitle = cleanField(content)
				case (prop == "og:description" || name == "og:description") && ogDesc == "":
					ogDesc = cleanField(content)
				}
				continue
			case "main", "article":
				if mainDepth == 0 {
					region = &textWriter{}
				}
				mainDepth++
			case "pre":
				preDepth++
			case "br":
				writers(func(w *textWriter) { w.lineBreak() })
				continue
			case "li":
				writers(func(w *textWriter) { w.bullet() })
				continue
			case "td", "th":
				writers(func(w *textWriter) { w.space = true })
				continue
			}
			writers(func(w *textWriter) { w.block(t.name) })
		case endTagToken:
			if skip[t.name] > 0 {
				skip[t.name]--
				skipping--
				continue
			}
			if skipping > 0 {
				continue
			}
			writers(func(w *textWriter) { w.block(t.name) })
			switch t.name {
			case "main", "article":
				if mainDepth > 0 {
					mainDepth--
					if mainDepth == 0 {
						regions = append(regions, region.String())
						region = nil
					}
				}
			case "pre":
				if preDepth > 0 {
					preDepth--
				}
			}
		}
	}
	if region != nil {
		regions = append(regions, region.String())
	}
	if !haveTitle {
		title = ogTitle
	}
	if !haveDesc {
		description = ogDesc
	}
	text = all.String()
	if mainHoldsText(regions) {
		var kept []string
		for _, r := range regions {
			if r != "" {
				kept = append(kept, r)
			}
		}
		text = strings.Join(kept, "\n\n")
	}
	text, truncated = capText(text, maxTextBytes)
	return title, description, text, truncated
}

func mainHoldsText(regions []string) bool {
	for _, r := range regions {
		if utf8.RuneCountInString(r) >= mainMinRunes {
			return true
		}
	}
	return false
}

func extractPlain(doc string) (text string, truncated bool) {
	doc = strings.ReplaceAll(doc, "\r\n", "\n")
	doc = strings.ReplaceAll(doc, "\r", "\n")
	doc = strings.Map(func(r rune) rune {
		if r != '\n' && r != '\t' && unicode.IsControl(r) {
			return -1
		}
		return r
	}, doc)
	return capText(strings.TrimSpace(doc), maxTextBytes)
}

// capText cuts s to at most max bytes at a rune boundary.
func capText(s string, max int) (string, bool) {
	if len(s) <= max {
		return s, false
	}
	cut := max
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut], true
}

func cleanField(s string) string {
	s = strings.Join(strings.FieldsFunc(s, unicode.IsSpace), " ")
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, s)
	s, _ = capText(strings.TrimSpace(s), maxFieldBytes)
	return s
}

type textWriter struct {
	sb     strings.Builder
	breaks int  // pending line breaks, 0-2
	space  bool // pending space
	dash   bool // pending list bullet
}

func (w *textWriter) String() string { return w.sb.String() }

func (w *textWriter) block(name string) {
	switch {
	case paragraphElements[name]:
		w.breaks = 2
	case lineElements[name] && w.breaks < 1:
		w.breaks = 1
	}
}

func (w *textWriter) lineBreak() {
	if w.breaks < 2 {
		w.breaks++
	}
}

func (w *textWriter) bullet() {
	if w.breaks < 1 {
		w.breaks = 1
	}
	w.dash = true
}

func (w *textWriter) text(s string, pre bool) {
	for _, r := range s {
		switch {
		case r == '\n' && pre:
			w.lineBreak()
		case unicode.IsSpace(r):
			w.space = true
		case unicode.IsControl(r):
		default:
			w.flush()
			w.sb.WriteRune(r)
		}
	}
}

func (w *textWriter) flush() {
	lineStart := w.sb.Len() == 0
	if !lineStart && w.breaks > 0 {
		w.sb.WriteString("\n\n"[:w.breaks])
		lineStart = true
	}
	switch {
	case w.dash:
		w.sb.WriteString("- ")
	case w.space && !lineStart:
		w.sb.WriteByte(' ')
	}
	w.breaks, w.space, w.dash = 0, false, false
}

type tokenKind int

const (
	textToken tokenKind = iota
	startTagToken
	endTagToken
)

type attribute struct{ name, value string }

type token struct {
	kind        tokenKind
	name        string // lowercase tag name
	attrs       []attribute
	selfClosing bool
	text        string // raw text, entities not decoded
}

// attr returns the entity-decoded value of the first attribute named name.
func (t token) attr(name string) (string, bool) {
	for _, a := range t.attrs {
		if a.name == name {
			return html.UnescapeString(a.value), true
		}
	}
	return "", false
}

func (t token) hasAttr(name string) bool {
	for _, a := range t.attrs {
		if a.name == name {
			return true
		}
	}
	return false
}

// maxAttributes caps a hostile tag's cost to linear time.
const maxAttributes = 32

var rawTextElements = map[string]bool{
	"script": true, "style": true, "noscript": true, "iframe": true, "xmp": true,
	"title": true, "textarea": true,
}

type tokenizer struct {
	s     string
	pos   int
	raw   string          // raw text element whose content comes next
	noEnd map[string]bool // raw text elements with no end tag after pos
}

func newTokenizer(s string) *tokenizer { return &tokenizer{s: s} }

func (z *tokenizer) next() (token, bool) {
	if z.raw != "" {
		name := z.raw
		z.raw = ""
		return z.rawText(name), true
	}
	for z.pos < len(z.s) {
		s := z.s
		i := z.pos
		if s[i] != '<' {
			end := z.nextMarkup(i + 1)
			z.pos = end
			return token{kind: textToken, text: s[i:end]}, true
		}
		switch {
		case strings.HasPrefix(s[i:], "<!--"):
			z.pos = commentEnd(s, i+4)
		case strings.HasPrefix(s[i:], "<![CDATA["):
			z.pos = indexFrom(s, i+9, "]]>")
		case strings.HasPrefix(s[i:], "<!"), strings.HasPrefix(s[i:], "<?"):
			z.pos = indexFrom(s, i+2, ">")
		case i+1 < len(s) && isASCIILetter(s[i+1]):
			t, end, ok := parseTag(s, i+1, startTagToken)
			z.pos = end
			if !ok {
				return token{}, false
			}
			if rawTextElements[t.name] {
				z.raw = t.name
			}
			return t, true
		case i+2 < len(s) && s[i+1] == '/' && isASCIILetter(s[i+2]):
			t, end, ok := parseTag(s, i+2, endTagToken)
			z.pos = end
			if !ok {
				return token{}, false
			}
			return t, true
		default:
			end := z.nextMarkup(i + 1)
			z.pos = end
			return token{kind: textToken, text: s[i:end]}, true
		}
	}
	return token{}, false
}

func (z *tokenizer) nextMarkup(i int) int {
	s := z.s
	for {
		j := strings.IndexByte(s[i:], '<')
		if j < 0 {
			return len(s)
		}
		i += j
		if i+1 < len(s) && (isASCIILetter(s[i+1]) || s[i+1] == '!' || s[i+1] == '?' ||
			s[i+1] == '/' && i+2 < len(s) && isASCIILetter(s[i+2])) {
			return i
		}
		i++
	}
}

func (z *tokenizer) rawText(name string) token {
	s := z.s
	i := z.pos
	for j := i; !z.noEnd[name]; {
		k := strings.Index(s[j:], "</")
		if k < 0 {
			break
		}
		k += j
		e := k + 2 + len(name)
		if e <= len(s) && strings.EqualFold(s[k+2:e], name) &&
			(e == len(s) || isHTMLSpace(s[e]) || s[e] == '/' || s[e] == '>') {
			z.pos = k
			return token{kind: textToken, text: s[i:k]}
		}
		j = k + 2
	}
	if z.noEnd == nil {
		z.noEnd = map[string]bool{}
	}
	z.noEnd[name] = true
	end := len(s)
	if name == "title" || name == "textarea" {
		if k := strings.IndexByte(s[i:], '<'); k >= 0 {
			end = i + k
		}
	}
	z.pos = end
	return token{kind: textToken, text: s[i:end]}
}

func parseTag(s string, i int, kind tokenKind) (token, int, bool) {
	j := i
	for j < len(s) && !isHTMLSpace(s[j]) && s[j] != '/' && s[j] != '>' {
		j++
	}
	t := token{kind: kind, name: strings.ToLower(s[i:j])}
	for {
		for j < len(s) && isHTMLSpace(s[j]) {
			j++
		}
		if j >= len(s) {
			return token{}, len(s), false
		}
		switch s[j] {
		case '>':
			return t, j + 1, true
		case '/':
			if j+1 < len(s) && s[j+1] == '>' {
				t.selfClosing = true
				return t, j + 2, true
			}
			j++
			continue
		}
		k := j
		j++ // a leading "=" belongs to the name
		for j < len(s) && !isHTMLSpace(s[j]) && s[j] != '/' && s[j] != '>' && s[j] != '=' {
			j++
		}
		name := strings.ToLower(s[k:j])
		for j < len(s) && isHTMLSpace(s[j]) {
			j++
		}
		value := ""
		if j < len(s) && s[j] == '=' {
			j++
			for j < len(s) && isHTMLSpace(s[j]) {
				j++
			}
			if j < len(s) && (s[j] == '"' || s[j] == '\'') {
				end := strings.IndexByte(s[j+1:], s[j])
				if end < 0 {
					return token{}, len(s), false
				}
				value = s[j+1 : j+1+end]
				j += end + 2
			} else {
				v := j
				for j < len(s) && !isHTMLSpace(s[j]) && s[j] != '>' {
					j++
				}
				value = s[v:j]
			}
		}
		if kind == startTagToken && len(t.attrs) < maxAttributes && !t.hasAttr(name) {
			t.attrs = append(t.attrs, attribute{name, value})
		}
	}
}

func commentEnd(s string, i int) int {
	switch {
	case strings.HasPrefix(s[i:], ">"):
		return i + 1
	case strings.HasPrefix(s[i:], "->"):
		return i + 2
	}
	return indexFrom(s, i, "-->")
}

func indexFrom(s string, i int, sep string) int {
	if i > len(s) {
		return len(s)
	}
	k := strings.Index(s[i:], sep)
	if k < 0 {
		return len(s)
	}
	return i + k + len(sep)
}

func isASCIILetter(c byte) bool { return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' }

func isHTMLSpace(c byte) bool {
	return c == ' ' || c == '\t' || c == '\n' || c == '\f' || c == '\r'
}
