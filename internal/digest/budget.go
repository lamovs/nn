package digest

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/lamovs/nn/internal/capture"
	"github.com/lamovs/nn/internal/search"
)

// floorRunes is the smallest excerpt a note is admitted with.
const floorRunes = 200

type entry struct {
	candidate
	meta     source // metadata only, as sent
	metaSize int
	content  string
	starts   []int // rune offset of every line of content
	size     int   // runes in content
	hitAt    int   // rune offset of the line of the first body or OCR hit, or -1
	priority int
	budget   int
}

func (s *Session) admit(ctx context.Context, candidates []candidate) error {
	var admitted []*entry
	for i, c := range candidates {
		if len(admitted) == s.opts.Notes {
			break
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		e := newEntry(c, i)
		if !s.opts.AllowSecret && len(capture.FindSecrets(c.doc.Path+"\n"+c.doc.Title+"\n"+strings.Join(c.doc.Tags, "\n")+"\n"+e.content)) > 0 {
			s.report.SkippedSecret++
			continue
		}
		if e.metaSize+min(e.size, floorRunes) > s.opts.Chars {
			continue
		}
		admitted = append(admitted, e)
	}
	for {
		total := 0
		for _, e := range admitted {
			total += e.metaSize + min(e.size, floorRunes)
		}
		if total <= s.opts.Chars || len(admitted) == 0 {
			break
		}
		admitted = admitted[:len(admitted)-1]
	}
	s.report.Included = len(admitted)
	s.report.BeyondLimit = s.report.Matched - s.report.Included - s.report.SkippedSecret
	if len(admitted) == 0 {
		if s.report.SkippedSecret > 0 {
			msg := fmt.Sprintf("no matching note can be sent: %d may contain credentials (review them and use --allow-secret)", s.report.SkippedSecret)
			if s.report.BeyondLimit > 0 {
				verb := "do not fit"
				if s.report.BeyondLimit == 1 {
					verb = "does not fit"
				}
				msg += fmt.Sprintf(", %d %s --chars", s.report.BeyondLimit, verb)
			}
			return errors.New(msg)
		}
		return errors.New("context too small for any matching note; raise --chars")
	}
	s.allocate(admitted)

	sort.SliceStable(admitted, func(i, j int) bool {
		a, b := admitted[i], admitted[j]
		if !a.date.Equal(b.date) {
			return a.date.Before(b.date)
		}
		return a.doc.Path < b.doc.Path
	})
	for i, e := range admitted {
		src := e.meta
		src.ID = fmt.Sprintf("S%d", i+1)
		src.Excerpt, src.Truncated = e.excerpt(s.sel.Topic != "")
		if src.Truncated {
			s.report.Truncated++
		}
		s.chars += e.metaSize + utf8.RuneCountInString(src.Excerpt)
		s.byID[src.ID] = i + 1
		s.sources = append(s.sources, src)
	}
	return nil
}

func (s *Session) allocate(admitted []*entry) {
	remaining := s.opts.Chars
	order := make([]*entry, len(admitted))
	copy(order, admitted)
	for _, e := range admitted {
		remaining -= e.metaSize
	}
	sort.SliceStable(order, func(i, j int) bool {
		if order[i].size != order[j].size {
			return order[i].size < order[j].size
		}
		return order[i].priority < order[j].priority
	})
	for i, e := range order {
		e.budget = min(e.size, remaining/(len(order)-i))
		remaining -= e.budget
	}
}

func newEntry(c candidate, priority int) *entry {
	e := &entry{candidate: c, priority: priority, hitAt: -1}
	e.meta = source{Path: c.doc.Path, Title: prefix(c.doc.Title, 240), Tags: []string{}, date: c.date}
	if !c.date.IsZero() {
		e.meta.Date = c.date.Format(dateLayout)
	}
	for _, tag := range c.doc.Tags[:min(16, len(c.doc.Tags))] {
		e.meta.Tags = append(e.meta.Tags, prefix(tag, 64))
	}
	e.metaSize = utf8.RuneCountInString(e.meta.Path) + utf8.RuneCountInString(e.meta.Title) + utf8.RuneCountInString(e.meta.Date)
	for _, tag := range e.meta.Tags {
		e.metaSize += utf8.RuneCountInString(tag)
	}

	var lines []string
	bodyLine := map[int]int{}
	for _, line := range c.doc.Lines {
		if _, ok := bodyLine[line.Num]; !ok {
			bodyLine[line.Num] = len(lines)
		}
		lines = append(lines, line.Text)
	}
	imageLine := map[string]int{}
	for _, img := range c.doc.Images {
		if len(img.Lines) == 0 {
			continue
		}
		lines = append(lines, "OCR "+prefix(img.Path, 160)+":")
		if _, ok := imageLine[img.Path]; !ok {
			imageLine[img.Path] = len(lines)
		}
		for _, line := range img.Lines {
			lines = append(lines, line.Text)
		}
	}
	e.content = strings.Join(lines, "\n")
	e.starts = make([]int, len(lines))
	offset := 0
	for i, line := range lines {
		e.starts[i] = offset
		offset += utf8.RuneCountInString(line) + 1
	}
	e.size = utf8.RuneCountInString(e.content)

	for _, hit := range c.hits {
		index := -1
		switch {
		case hit.Image != "":
			first, ok := imageLine[hit.Image]
			if !ok {
				continue
			}
			for i := first; i < len(lines) && i-first < ocrLines(c.doc, hit.Image); i++ {
				if lines[i] == hit.Text {
					index = i
					break
				}
			}
		case hit.Line > 0:
			if i, ok := bodyLine[hit.Line]; ok {
				index = i
			}
		}
		if index >= 0 && (e.hitAt < 0 || e.starts[index] < e.hitAt) {
			e.hitAt = e.starts[index]
		}
	}
	return e
}

func ocrLines(doc *search.Doc, image string) int {
	for _, img := range doc.Images {
		if img.Path == image && len(img.Lines) > 0 {
			return len(img.Lines)
		}
	}
	return 0
}

// excerpt windows a late topic hit a third of the budget before it.
func (e *entry) excerpt(topic bool) (string, bool) {
	if e.budget >= e.size {
		return e.content, false
	}
	start := 0
	if topic && e.hitAt >= 0 && 3*e.hitAt > 2*e.budget {
		target := min(e.hitAt-e.budget/3, e.size-e.budget)
		for _, offset := range e.starts {
			if offset >= target {
				start = offset
				break
			}
		}
	}
	return runeSlice(e.content, start, start+e.budget), true
}

func runeSlice(s string, from, to int) string {
	begin, end, n := len(s), len(s), 0
	for offset := range s {
		if n == from {
			begin = offset
		}
		if n == to {
			end = offset
			break
		}
		n++
	}
	if begin > end {
		return ""
	}
	return s[begin:end]
}

// prefix truncates to at most limit runes without splitting one.
func prefix(s string, limit int) string {
	if limit <= 0 {
		return ""
	}
	count := 0
	for offset := range s {
		if count == limit {
			return s[:offset]
		}
		count++
	}
	return s
}
