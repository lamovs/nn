package ask

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/lamovs/nn/internal/search"
)

// origin identifies a frozen corpus field, never a rendering or matching text.
type origin struct {
	kind  int // body, OCR, title, alias, tag
	image string
	index int // file line number, sidecar index, or metadata field index
}

type fragment struct {
	origin
	text, label string
	start, end  int
	focus       int
}

func (s *Session) admit(ctx context.Context, c *candidate, ceiling int) (bool, error) {
	index := -1
	src := metadata(c.doc)
	src.ID = fmt.Sprintf("S%d", len(s.sources)+1)
	oldCost := 0
	for i, old := range s.sources {
		if old.Path == c.doc.Path {
			index, src, oldCost = i, old, contentChars(old)
			break
		}
	}
	progress := false
	for _, proposed := range plannedFragments(c) {
		if err := ctx.Err(); err != nil {
			return false, err
		}
		for _, novel := range uncovered(proposed, src.spans) {
			try := func(f fragment) (source, bool) {
				trial := src
				trial.spans = union(src.spans, f)
				trial.Excerpt = renderFragments(trial.spans)
				if s.chars-oldCost+contentChars(trial) > ceiling {
					return source{}, false
				}
				// Coordinates already sent to the model cannot be truncated.
				var saved source
				if index < 0 {
					s.sources = append(s.sources, trial)
				} else {
					saved = s.sources[index]
					s.sources[index] = trial
				}
				_, err := s.request(s.opts.Limits.SearchRounds, false)
				if index < 0 {
					s.sources = s.sources[:len(s.sources)-1]
				} else {
					s.sources[index] = saved
				}
				return trial, err == nil
			}
			if trial, ok := try(novel); ok {
				src, progress = trial, true
				continue
			}
			// Fit a smaller interval around the match, not a prefix of it.
			low, high := 1, utf8.RuneCountInString(novel.text[novel.start:novel.end])-1
			var best source
			found := false
			for low <= high {
				middle := low + (high-low)/2
				trial, ok := try(shrink(novel, middle))
				if ok {
					best, found, low = trial, true, middle+1
				} else {
					high = middle - 1
				}
			}
			if found {
				src, progress = best, true
			}
		}
	}
	if !progress {
		return false, nil
	}
	if index < 0 {
		s.sources = append(s.sources, src)
	} else {
		s.sources[index] = src
	}
	s.paths[src.Path] = true
	s.chars += contentChars(src) - oldCost
	return true, nil
}

func before(a, b origin) bool {
	if a.kind != b.kind {
		return a.kind < b.kind
	}
	if a.image != b.image {
		return a.image < b.image
	}
	return a.index < b.index
}

func union(old []fragment, next fragment) []fragment {
	all := make([]fragment, 0, len(old)+1)
	all = append(all, old...)
	all = append(all, next)
	sort.Slice(all, func(i, j int) bool {
		if all[i].origin != all[j].origin {
			return before(all[i].origin, all[j].origin)
		}
		return all[i].start < all[j].start
	})
	result := all[:0]
	for _, f := range all {
		if len(result) > 0 {
			last := &result[len(result)-1]
			if last.origin == f.origin && last.end >= f.start {
				last.end = max(last.end, f.end)
				continue
			}
		}
		result = append(result, f)
	}
	return result
}

func uncovered(f fragment, old []fragment) []fragment {
	var result []fragment
	start := f.start
	for _, prior := range old {
		if prior.origin != f.origin || prior.end <= start || prior.start >= f.end {
			continue
		}
		if prior.start > start {
			part := f
			part.start, part.end = start, min(prior.start, f.end)
			result = append(result, part)
		}
		start = max(start, prior.end)
		if start >= f.end {
			break
		}
	}
	if start < f.end {
		f.start = start
		result = append(result, f)
	}
	return result
}

func shrink(f fragment, chars int) fragment {
	start := min(max(f.focus, f.start), f.end)
	for i := 0; i < chars/3 && start > f.start; i++ {
		_, size := utf8.DecodeLastRuneInString(f.text[f.start:start])
		start -= size
	}
	end := start + len(prefix(f.text[start:f.end], chars))
	// A focus near the end should still use the available character space.
	missing := chars - utf8.RuneCountInString(f.text[start:end])
	for missing > 0 && start > f.start {
		_, size := utf8.DecodeLastRuneInString(f.text[f.start:start])
		start -= size
		missing--
	}
	f.start, f.end = start, end
	return f
}

func renderFragments(spans []fragment) string {
	var out strings.Builder
	for i, f := range spans {
		if i > 0 {
			out.WriteByte('\n')
		}
		out.WriteString(f.label)
		if f.start > 0 {
			out.WriteString("...")
		}
		out.WriteString(f.text[f.start:f.end])
		if f.end < len(f.text) {
			out.WriteString("...")
		}
	}
	return out.String()
}

func plannedFragments(c *candidate) []fragment {
	var result []fragment
	type key struct {
		origin
		start, end int
	}
	seen := map[key]bool{}
	add := func(f fragment, focus int) {
		if f.text == "" || len(result) >= 96 {
			return
		}
		f.start, f.end, f.focus = 0, len(f.text), focus
		f = shrink(f, min(600, utf8.RuneCountInString(f.text)))
		k := key{f.origin, f.start, f.end}
		if !seen[k] {
			seen[k] = true
			result = append(result, f)
		}
	}
	body := map[int]int{}
	for i, line := range c.doc.Lines {
		body[line.Num] = i
	}
	bodyFragment := func(i int) fragment {
		line := c.doc.Lines[i]
		return fragment{origin: origin{kind: 0, index: line.Num}, text: line.Text, label: fmt.Sprintf("L%d: ", line.Num)}
	}
	ocrFragment := func(image, line int) fragment {
		img := c.doc.Images[image]
		return fragment{origin: origin{kind: 1, image: img.Path, index: line}, text: img.Lines[line].Text, label: fmt.Sprintf("OCR %s L%d: ", prefix(img.Path, 160), line+1)}
	}
	for _, hit := range c.hits {
		var matched []fragment
		if hit.Image != "" {
			for image, img := range c.doc.Images {
				if img.Path != hit.Image {
					continue
				}
				for line, value := range img.Lines {
					if value.Text == hit.Text && (hit.Box == nil || value.Box == *hit.Box) {
						matched = append(matched, ocrFragment(image, line))
					}
				}
			}
		} else if i, ok := body[hit.Line]; hit.Line > 0 && ok {
			matched = append(matched, bodyFragment(i))
		}
		for _, f := range matched {
			if len(hit.Ranges) == 0 {
				add(f, 0)
			}
			for _, r := range hit.Ranges[:min(8, len(hit.Ranges))] {
				add(f, r[0])
			}
		}
	}
	// Hits choose admission priority; rendering always follows origin order.
	matched := append([]fragment(nil), result...)
	for _, f := range matched {
		if f.kind == 0 {
			i := body[f.index]
			for _, neighbor := range []int{i - 1, i + 1} {
				if neighbor >= 0 && neighbor < len(c.doc.Lines) {
					add(bodyFragment(neighbor), 0)
				}
			}
		} else {
			for image, img := range c.doc.Images {
				if img.Path != f.image {
					continue
				}
				for _, neighbor := range []int{f.index - 1, f.index + 1} {
					if neighbor >= 0 && neighbor < len(img.Lines) {
						add(ocrFragment(image, neighbor), 0)
					}
				}
			}
		}
	}
	if len(result) == 0 {
		for i := range min(3, len(c.doc.Lines)) {
			add(bodyFragment(i), 0)
		}
	}
	if len(result) == 0 {
		for _, hit := range c.hits {
			if hit.Kind == search.KindTitle {
				add(fragment{origin: origin{kind: 2}, text: c.doc.Title, label: "title: "}, 0)
			}
			for i, alias := range c.doc.Aliases {
				if alias == hit.Text {
					add(fragment{origin: origin{kind: 3, index: i}, text: alias, label: "alias: "}, 0)
				}
			}
			for i, tag := range c.doc.Tags {
				if tag == hit.Text {
					add(fragment{origin: origin{kind: 4, index: i}, text: tag, label: "tag: "}, 0)
				}
			}
		}
	}
	return result
}
