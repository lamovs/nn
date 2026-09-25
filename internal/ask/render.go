package ask

import (
	"errors"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/lamovs/nn/internal/ai"
	"github.com/lamovs/nn/internal/platform"
)

// validateReply: an injected model or custom task prompt must not bypass this.
func validateReply(reply *ai.AskReply) error {
	bad := errors.New("ask: malformed model reply")
	if reply == nil || len(reply.Paragraphs) > 32 || !utf8.ValidString(reply.Query) || !utf8.ValidString(reply.Missing) || utf8.RuneCountInString(reply.Query) > 200 || utf8.RuneCountInString(reply.Missing) > 2000 {
		return bad
	}
	total := utf8.RuneCountInString(reply.Missing)
	for _, p := range reply.Paragraphs {
		n := utf8.RuneCountInString(p.Text)
		if !utf8.ValidString(p.Text) || literal(p.Text) == "" || n > 4000 || len(p.SourceIDs) == 0 || len(p.SourceIDs) > 32 {
			return bad
		}
		total += n
		seen := map[string]bool{}
		for _, id := range p.SourceIDs {
			if strings.TrimSpace(id) == "" || !utf8.ValidString(id) || utf8.RuneCountInString(id) > 64 || seen[id] {
				return bad
			}
			seen[id] = true
		}
	}
	if total > 16000 {
		return bad
	}
	switch reply.Action {
	case "answer":
		if reply.Query != "" || reply.Missing != "" || len(reply.Paragraphs) == 0 {
			return bad
		}
	case "insufficient":
		if reply.Query != "" || literal(reply.Missing) == "" {
			return bad
		}
	case "search":
		if strings.TrimSpace(reply.Query) == "" || reply.Missing != "" || len(reply.Paragraphs) != 0 {
			return bad
		}
	default:
		return bad
	}
	return nil
}

func (s *Session) render(reply *ai.AskReply) (string, error) {
	byID := map[string]source{}
	for _, src := range s.sources {
		byID[src.ID] = src
	}
	numbers := map[string]int{}
	var cited []source
	var out strings.Builder
	for _, paragraph := range reply.Paragraphs {
		var refs []int
		seen := map[string]bool{}
		for _, id := range paragraph.SourceIDs {
			src, exists := byID[id]
			if !exists {
				return "", errors.New("ask: model cited an unknown or unsent source")
			}
			if seen[src.Path] {
				continue
			}
			seen[src.Path] = true
			if numbers[src.Path] == 0 {
				cited = append(cited, src)
				numbers[src.Path] = len(cited)
			}
			refs = append(refs, numbers[src.Path])
		}
		out.WriteString(literal(paragraph.Text))
		for _, number := range refs {
			fmt.Fprintf(&out, " [%d]", number)
		}
		out.WriteString("\n\n")
	}
	if reply.Action == "insufficient" {
		out.WriteString("Not enough data: ")
		out.WriteString(literal(reply.Missing))
		out.WriteString("\n\n")
	}
	if len(cited) > 0 {
		out.WriteString("Sources:\n")
	}
	for _, src := range cited {
		title := src.Title
		if strings.TrimSpace(title) == "" {
			title = src.Path
		}
		fmt.Fprintf(&out, "[%d] [%s](%s) (%s)\n", numbers[src.Path], literal(title), platform.ObsidianURI(s.vault.Abs(src.Path)), literal(src.Path))
	}
	text := strings.TrimSpace(out.String()) + "\n"
	if reply.Action == "insufficient" {
		return text, ErrInsufficient
	}
	return text, nil
}

// literal: only the renderer creates links; model text is untrusted prose.
func literal(text string) string {
	text = strings.Map(func(r rune) rune {
		if unicode.IsSpace(r) {
			return ' '
		}
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			return -1
		}
		return r
	}, text)
	text = strings.Join(strings.Fields(text), " ")
	return strings.NewReplacer(
		"&", "&amp;",
		"\\", "\\\\", "[", "\\[", "]", "\\]", "(", "\\(", ")", "\\)",
		"<", "&lt;", ">", "&gt;", "`", "\\`", "*", "\\*", "_", "\\_", "!", "\\!", "#", "\\#",
		"://", "\\://", "www.", "www\\.",
	).Replace(text)
}
