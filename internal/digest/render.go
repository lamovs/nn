package digest

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/lamovs/nn/internal/ai"
	"github.com/lamovs/nn/internal/ask"
	"github.com/lamovs/nn/internal/platform"
)

type Digest struct {
	session *Session
	points  []point
}

type point struct {
	text string // as the model wrote it; rendered through esc
	refs []int  // source numbers, ascending and unique
}

// validate re-checks the reply and binds every cited ID to a sent source.
func (s *Session) validate(reply *ai.DigestReply) ([]point, error) {
	bad := errors.New("malformed model reply")
	if reply == nil || len(reply.Points) == 0 || len(reply.Points) > 32 {
		return nil, bad
	}
	total := 0
	points := make([]point, 0, len(reply.Points))
	for _, p := range reply.Points {
		n := utf8.RuneCountInString(p.Text)
		if !utf8.ValidString(p.Text) || n == 0 || n > 2000 || len(p.SourceIDs) == 0 || len(p.SourceIDs) > 32 {
			return nil, bad
		}
		if ask.Literal(p.Text) == "" {
			return nil, bad
		}
		total += n
		seen := map[string]bool{}
		refs := make([]int, 0, len(p.SourceIDs))
		for _, id := range p.SourceIDs {
			if !utf8.ValidString(id) || id == "" || utf8.RuneCountInString(id) > 64 || seen[id] {
				return nil, bad
			}
			seen[id] = true
			number, ok := s.byID[id]
			if !ok {
				return nil, errors.New("model cited an unknown or unsent source")
			}
			refs = append(refs, number)
		}
		sort.Ints(refs)
		points = append(points, point{text: p.Text, refs: refs})
	}
	if total > 16000 {
		return nil, bad
	}
	return points, nil
}

func (d *Digest) Text() string {
	s := d.session
	var out strings.Builder
	d.writeHead(&out, ask.Literal, ask.Literal)
	out.WriteString("Sources:\n")
	for i, src := range s.sources {
		title := src.Title
		if strings.TrimSpace(title) == "" {
			title = src.Path
		}
		fmt.Fprintf(&out, "[%d] [%s](%s) (%s)\n", i+1, ask.Literal(title), platform.ObsidianURI(s.vault.Abs(src.Path)), details(ask.Literal(src.Path), src))
	}
	return out.String()
}

// writeHead: esc renders every model or note string; point renders points.
func (d *Digest) writeHead(out *strings.Builder, esc, point func(string) string) {
	s := d.session
	r := s.report
	fmt.Fprintf(out, "Digest of %s%s; selection: %s\n", count(len(s.sources), "note"), s.dated(), esc(s.sel.describe()))
	var omitted []string
	if r.BeyondLimit > 0 {
		omitted = append(omitted, fmt.Sprintf("%s beyond the limit of %s / %d characters", count(r.BeyondLimit, "matching note"), count(s.opts.Notes, "note"), s.opts.Chars))
	}
	if r.SkippedSecret > 0 {
		that := "notes that may"
		if r.SkippedSecret == 1 {
			that = "note that may"
		}
		omitted = append(omitted, fmt.Sprintf("%d %s contain credentials (--allow-secret includes them)", r.SkippedSecret, that))
	}
	if len(omitted) > 0 {
		fmt.Fprintf(out, "Not included: %s.\n", strings.Join(omitted, "; "))
	}
	out.WriteString("\n")
	for _, p := range d.points {
		out.WriteString("- ")
		out.WriteString(point(p.text))
		for _, ref := range p.refs {
			fmt.Fprintf(out, " [%d]", ref)
		}
		out.WriteString("\n")
	}
	out.WriteString("\n")
}

// dated is " dated A to B", or " dated A" when notes share a day.
func (s *Session) dated() string {
	first, last := "", ""
	for _, src := range s.sources {
		if src.Date == "" {
			continue
		}
		if first == "" || src.Date < first {
			first = src.Date
		}
		if src.Date > last {
			last = src.Date
		}
	}
	switch {
	case first == "":
		return ""
	case first == last:
		return " dated " + first
	}
	return " dated " + first + " to " + last
}

func details(path string, src source) string {
	return strings.Join(append([]string{path}, annotations(src)...), ", ")
}

func annotations(src source) []string {
	var parts []string
	if src.Date != "" {
		parts = append(parts, src.Date)
	}
	if src.Truncated {
		parts = append(parts, "excerpt")
	}
	return parts
}
