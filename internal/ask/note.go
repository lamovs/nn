package ask

import (
	"errors"
	"fmt"
	"path"
	"strings"
	"unicode"

	"github.com/lamovs/nn/internal/links"
	"github.com/lamovs/nn/internal/vault"
)

// NoLink marks a source whose path could not be linked safely.
const NoLink = "(no link: ambiguous or unsafe path)"

// NoteLiteral renders model text or note metadata for a saved note: the same
// inert prose as Literal, additionally kept from reading as a Dataview field.
func NoteLiteral(text string) string {
	return neutral(literal(text))
}

// blockSafe keeps a line starting with "~" or "$" from opening a CommonMark
// tilde fence or an Obsidian display-math block.
func blockSafe(line string) string {
	if strings.HasPrefix(line, "~") || strings.HasPrefix(line, "$") {
		return "\\" + line
	}
	return line
}

// NoteLine renders one saved paragraph or point: NoteLiteral, block-safe.
func NoteLine(text string) string {
	return blockSafe(NoteLiteral(text))
}

// NoteLink returns a verified relative wikilink to target, or, when the path
// cannot be represented safely (unneutralized, so a "%" or "::" is caught
// before qualifying), NoteLiteral(target) with ok false.
func NoteLink(g *links.Graph, notePaths []string, from, target string) (string, bool) {
	if strings.Contains(target, "%") || strings.Contains(target, "::") {
		return NoteLiteral(target), false
	}
	link, err := links.QualifiedLink(g, notePaths, from, target)
	if err != nil {
		return NoteLiteral(target), false
	}
	return link, true
}

// SummaryNote reports whether via names a note nn itself saved as a summary
// (nn ask --save or nn digest --save): via "ask" or "digest", case-insensitive.
func SummaryNote(via string) bool {
	via = strings.TrimSpace(via)
	return strings.EqualFold(via, "ask") || strings.EqualFold(via, "digest")
}

// Note builds the inbox note ask --save writes: the answer with the same
// source markers as stdout, then a Sources section of verified relative
// wikilinks. Run must have returned a full answer; anything else is an error.
func (s *Session) Note(g *links.Graph, notePaths []string, inbox string) (vault.NewNote, error) {
	if s.bound == nil {
		return vault.NewNote{}, errors.New("ask: no answer to save")
	}
	var body strings.Builder
	for _, p := range s.bound.paragraphs {
		body.WriteString(NoteLine(p.text))
		for _, number := range p.refs {
			fmt.Fprintf(&body, " [%d]", number)
		}
		body.WriteString("\n\n")
	}
	body.WriteString("## Sources\n\n")
	from := path.Join(inbox, "answer.md")
	for i, src := range s.bound.cited {
		title := src.Title
		if strings.TrimSpace(title) == "" {
			title = src.Path
		}
		link, ok := NoteLink(g, notePaths, from, src.Path)
		fmt.Fprintf(&body, "%d. %s %s", i+1, link, NoteLiteral(title))
		if !ok {
			body.WriteString(" " + NoLink)
		}
		body.WriteString("\n")
	}
	return vault.NewNote{Title: noteTitle(s.question), Body: body.String(), Via: "ask"}, nil
}

// noteTitle restates the noteai.Title rule (ask must not import noteai):
// control and format runes become spaces, whitespace collapses, the result
// is cut to 160 runes and trimmed of "#" and "`"; empty falls back to "Answer".
func noteTitle(question string) string {
	title := strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			return ' '
		}
		return r
	}, question)
	title = strings.Join(strings.Fields(title), " ")
	title = strings.Trim(prefix(title, 160), "# `")
	if title == "" {
		return "Answer"
	}
	return title
}

// neutral keeps Obsidian from reading text as a Dataview field.
func neutral(text string) string {
	var out strings.Builder
	out.Grow(len(text))
	prev := rune(0)
	for _, r := range text {
		switch {
		case r == '%':
			out.WriteString("&#37;")
		case r == ':' && prev == ':':
			out.WriteString(`\:`)
		default:
			out.WriteRune(r)
		}
		prev = r
	}
	return out.String()
}
