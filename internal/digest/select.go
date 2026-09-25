package digest

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/lamovs/nn/internal/search"
)

type candidate struct {
	doc  *search.Doc
	hits []search.Hit
	date time.Time // effective: Date, else Modified
}

func (s *Session) selectNotes(ctx context.Context, docs []*search.Doc) ([]candidate, error) {
	sel := s.sel
	var universe []*search.Doc
	position := map[string]int{}
	if sel.Paths == nil {
		for _, doc := range docs {
			if doc != nil && isNote(doc.Path) {
				universe = append(universe, doc)
			}
		}
	} else {
		var err error
		if universe, err = s.listed(docs); err != nil {
			return nil, err
		}
		for i, doc := range universe {
			position[doc.Path] = i
		}
	}
	results, err := search.Search(ctx, universe, search.Query{
		Text: sel.Topic, Tags: sel.Tags, Since: sel.Since, Inbox: sel.Inbox, Here: sel.Here,
		NoLayoutFallback: s.opts.NoLayoutFallback,
	}, nil, s.opts.Now)
	if err != nil {
		return nil, err
	}
	var found []candidate
	for _, result := range results {
		c := candidate{doc: result.Doc, hits: result.Hits, date: effectiveDate(result.Doc)}
		if !sel.Until.IsZero() && (c.date.IsZero() || !c.date.Before(sel.Until)) {
			continue
		}
		// Skip digest notes so digests do not summarize summaries.
		if sel.Paths == nil && strings.EqualFold(strings.TrimSpace(c.doc.Via), "digest") {
			continue
		}
		s.report.Layout = s.report.Layout || result.Layout
		found = append(found, c)
	}
	switch {
	case sel.Topic != "":
	case sel.Paths != nil:
		sort.SliceStable(found, func(i, j int) bool { return position[found[i].doc.Path] < position[found[j].doc.Path] })
	default:
		sort.SliceStable(found, func(i, j int) bool {
			a, b := found[i], found[j]
			if !a.date.Equal(b.date) {
				return a.date.After(b.date)
			}
			return a.doc.Path < b.doc.Path
		})
	}
	s.report.Matched = len(found)
	return found, nil
}

func (s *Session) listed(docs []*search.Doc) ([]*search.Doc, error) {
	byPath := map[string]*search.Doc{}
	images := map[string]bool{}
	for _, doc := range docs {
		if doc == nil {
			continue
		}
		byPath[doc.Path] = doc
		for _, img := range doc.Images {
			images[img.Path] = true
		}
	}
	var result []*search.Doc
	seen := map[string]bool{}
	unknown, first := 0, ""
	for _, p := range s.sel.Paths {
		if seen[p] {
			continue
		}
		seen[p] = true
		doc := byPath[p]
		switch {
		case doc != nil && isNote(p):
			result = append(result, doc)
		case doc != nil || images[p]:
			s.report.SkippedNonNote++
		default:
			if unknown == 0 {
				first = p
			}
			unknown++
		}
	}
	if unknown == 1 {
		return nil, fmt.Errorf("listed path not found in the vault: %q", printable(first))
	}
	if unknown > 1 {
		return nil, fmt.Errorf("%d listed paths not found in the vault, first: %q", unknown, printable(first))
	}
	return result, nil
}

func isNote(p string) bool {
	return strings.HasSuffix(strings.ToLower(p), ".md")
}

func effectiveDate(doc *search.Doc) time.Time {
	if doc.Date.IsZero() {
		return doc.Modified
	}
	return doc.Date
}

// printable sanitizes untrusted input echoed in an error.
func printable(s string) string {
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			return -1
		}
		return r
	}, s)
	return prefix(s, 200)
}

func (sel Selection) describe() string {
	var parts []string
	if sel.Paths != nil {
		parts = append(parts, "listed notes")
	}
	if !sel.Since.IsZero() {
		parts = append(parts, "since "+sinceText(sel.Since))
	}
	if !sel.Until.IsZero() {
		// Until is the exclusive end; the inclusive date is the day before.
		parts = append(parts, "until "+sel.Until.Add(-time.Nanosecond).Format(dateLayout))
	}
	switch len(sel.Tags) {
	case 0:
	case 1:
		parts = append(parts, "tag "+sel.Tags[0])
	default:
		parts = append(parts, "tags "+strings.Join(sel.Tags, ", "))
	}
	if sel.Inbox {
		parts = append(parts, "inbox")
	}
	if sel.Here != nil {
		parts = append(parts, "here")
	}
	if sel.Topic != "" {
		parts = append(parts, `topic "`+sel.Topic+`"`)
	}
	return strings.Join(parts, "; ")
}

const dateLayout = "2006-01-02"

func sinceText(t time.Time) string {
	if t.Hour() == 0 && t.Minute() == 0 && t.Second() == 0 && t.Nanosecond() == 0 {
		return t.Format(dateLayout)
	}
	return t.Format(dateLayout + " 15:04")
}
