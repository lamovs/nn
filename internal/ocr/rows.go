package ocr

import (
	"cmp"
	"slices"
	"strings"
)

// mergeRows joins observations on one visual row into a line, left to
// right: search works on lines, and both engines report a table cell by cell.
func mergeRows(lines []Line) []Line {
	if len(lines) < 2 {
		return lines
	}
	order := slices.SortedStableFunc(slices.Values(lines), func(a, b Line) int {
		return cmp.Compare(a.Box.Y, b.Box.Y)
	})

	var rows [][]Line
	var boxes []Box
	for _, l := range order {
		best, bestOverlap := -1, 0.0
		for i, b := range boxes {
			overlap := verticalOverlap(b, l.Box)
			if overlap > 0.5*min(b.H, l.Box.H) && overlap > bestOverlap {
				best, bestOverlap = i, overlap
			}
		}
		if best < 0 {
			rows = append(rows, []Line{l})
			boxes = append(boxes, l.Box)
			continue
		}
		rows[best] = append(rows[best], l)
		boxes[best] = unionBox(boxes[best], l.Box)
	}

	out := make([]Line, 0, len(rows))
	for i, parts := range rows {
		slices.SortStableFunc(parts, func(a, b Line) int { return cmp.Compare(a.Box.X, b.Box.X) })
		texts := make([]string, 0, len(parts))
		confidence := 0.0
		for _, p := range parts {
			if text := strings.TrimSpace(p.Text); text != "" {
				texts = append(texts, text)
			}
			confidence += p.Confidence
		}
		out = append(out, Line{
			Text:       strings.Join(texts, " "),
			Box:        boxes[i],
			Confidence: confidence / float64(len(parts)),
		})
	}
	return out
}

// verticalOverlap is negative when the boxes do not meet at all.
func verticalOverlap(a, b Box) float64 {
	return min(a.Y+a.H, b.Y+b.H) - max(a.Y, b.Y)
}

func unionBox(a, b Box) Box {
	x, y := min(a.X, b.X), min(a.Y, b.Y)
	return Box{
		X: x,
		Y: y,
		W: max(a.X+a.W, b.X+b.W) - x,
		H: max(a.Y+a.H, b.Y+b.H) - y,
	}
}
