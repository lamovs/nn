package digest

import (
	"fmt"
	"path"
	"strings"
	"time"
	"unicode"

	"github.com/lamovs/nn/internal/ask"
	"github.com/lamovs/nn/internal/links"
	"github.com/lamovs/nn/internal/vault"
)

// Note writes a source as text when it cannot be linked safely.
func (d *Digest) Note(g *links.Graph, notePaths []string, inbox string, today time.Time) vault.NewNote {
	s := d.session
	esc := ask.NoteLiteral
	var body strings.Builder
	d.writeHead(&body, esc, ask.NoteLine)
	body.WriteString("## Sources\n\n")
	from := path.Join(inbox, "digest.md")
	for i, src := range s.sources {
		title := src.Title
		if strings.TrimSpace(title) == "" {
			title = src.Path
		}
		link, ok := ask.NoteLink(g, notePaths, from, src.Path)
		fmt.Fprintf(&body, "%d. %s %s", i+1, link, esc(title))
		if extra := annotations(src); len(extra) > 0 {
			fmt.Fprintf(&body, " (%s)", strings.Join(extra, ", "))
		}
		if !ok {
			body.WriteString(" " + ask.NoLink)
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
