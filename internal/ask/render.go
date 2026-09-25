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

// bound is a validated reply's paragraphs and sources, bound to the sources
// that were actually sent and numbered by first citation.
type bound struct {
	paragraphs []boundParagraph
	cited      []source
}

// boundParagraph is one paragraph with its source numbers, in stdout order.
type boundParagraph struct {
	text string
	refs []int
}

// bind maps every cited source ID to a sent source and numbers sources by
// first citation; an unknown or unsent ID is an error.
func (s *Session) bind(reply *ai.AskReply) (*bound, error) {
	byID := map[string]source{}
	for _, src := range s.sources {
		byID[src.ID] = src
	}
	numbers := map[string]int{}
	var cited []source
	var paragraphs []boundParagraph
	for _, paragraph := range reply.Paragraphs {
		var refs []int
		seen := map[string]bool{}
		for _, id := range paragraph.SourceIDs {
			src, exists := byID[id]
			if !exists {
				return nil, errors.New("ask: model cited an unknown or unsent source")
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
		paragraphs = append(paragraphs, boundParagraph{text: paragraph.Text, refs: refs})
	}
	return &bound{paragraphs: paragraphs, cited: cited}, nil
}

func (s *Session) render(reply *ai.AskReply) (string, error) {
	b, err := s.bind(reply)
	if err != nil {
		return "", err
	}
	if reply.Action == "answer" {
		s.bound = b
	}
	var out strings.Builder
	for _, p := range b.paragraphs {
		out.WriteString(literal(p.text))
		for _, number := range p.refs {
			fmt.Fprintf(&out, " [%d]", number)
		}
		out.WriteString("\n\n")
	}
	if reply.Action == "insufficient" {
		out.WriteString("Not enough data: ")
		out.WriteString(literal(reply.Missing))
		out.WriteString("\n\n")
	}
	if len(b.cited) > 0 {
		out.WriteString("Sources:\n")
	}
	for i, src := range b.cited {
		title := src.Title
		if strings.TrimSpace(title) == "" {
			title = src.Path
		}
		fmt.Fprintf(&out, "[%d] [%s](%s) (%s)\n", i+1, literal(title), platform.ObsidianURI(s.vault.Abs(src.Path)), literal(src.Path))
	}
	text := strings.TrimSpace(out.String()) + "\n"
	if reply.Action == "insufficient" {
		return text, ErrInsufficient
	}
	return text, nil
}

// literal: model text is untrusted prose; links come only from nn itself
// (render and Session.Note).
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
