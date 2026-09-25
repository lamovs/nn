package digest

import (
	"errors"
	"fmt"
	"path"
	"strings"
	"time"
	"unicode"

	"github.com/lamovs/nn/internal/ask"
	"github.com/lamovs/nn/internal/links"
	"github.com/lamovs/nn/internal/vault"
)

var errUnsafePath = errors.New("path would break the note's markup")

// Note writes a source as text when it cannot be linked safely.
func (d *Digest) Note(g *links.Graph, notePaths []string, inbox string, today time.Time) vault.NewNote {
	s := d.session
	esc := func(text string) string { return neutral(ask.Literal(text)) }
	var body strings.Builder
	d.writeHead(&body, esc)
	body.WriteString("## Sources\n\n")
	from := path.Join(inbox, "digest.md")
	for i, src := range s.sources {
		title := src.Title
		if strings.TrimSpace(title) == "" {
			title = src.Path
		}
		// Unneutralized, so a path with "%" or "::" is not linked.
		link, err := "", errUnsafePath
		if !strings.Contains(src.Path, "%") && !strings.Contains(src.Path, "::") {
			link, err = links.QualifiedLink(g, notePaths, from, src.Path)
		}
		if err != nil {
			link = esc(src.Path)
		}
		fmt.Fprintf(&body, "%d. %s %s", i+1, link, esc(title))
		if extra := annotations(src); len(extra) > 0 {
			fmt.Fprintf(&body, " (%s)", strings.Join(extra, ", "))
		}
		if err != nil {
			body.WriteString(" (no link: ambiguous or unsafe path)")
		}
		body.WriteString("\n")
	}
	return vault.NewNote{Title: noteTitle(today, s.sel.Topic), Body: body.String(), Via: "digest"}
}

func noteTitle(today time.Time, topic string) string {
	title := "Digest " + today.Format(dateLayout)
	topic = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			return ' '
		}
		return r
	}, topic)
	if topic = strings.Join(strings.Fields(topic), " "); topic != "" {
		title += " " + topic
	}
	return strings.TrimRight(prefix(title, 160), "# `")
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
