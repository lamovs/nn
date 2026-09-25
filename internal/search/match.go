package search

import (
	"bytes"
	"cmp"
	"math"
	"math/bits"
	"os"
	"path"
	"regexp"
	"slices"
	"strings"
	"time"
)

const (
	weightPhrase = 100
	weightChord  = 100
	weightTerms  = 85
	weightTitle  = 70
	weightAlias  = 60
	weightTag    = 45
	weightBody   = 30
	weightOCR    = 20

	bonusWholeWord  = 2
	bonusWordStart  = 1
	bonusExactTitle = 2
	bonusCoverage   = 2
	bonusPerHit     = 0.25
	maxHitBonus     = 4
	penaltyImage    = 2

	weightFrecency = 6
	bonusHere      = 12
	bonusInbox     = 2
	bonusFresh     = 3
	freshHalfLife  = 30 * 24 * time.Hour
)

const maxTerms = 64

func kindWeight(k Kind) float64 {
	switch k {
	case KindPhrase:
		return weightPhrase
	case KindChord:
		return weightChord
	case KindTerms:
		return weightTerms
	case KindTitle:
		return weightTitle
	case KindAlias:
		return weightAlias
	case KindTag:
		return weightTag
	case KindBody:
		return weightBody
	default:
		return weightOCR
	}
}

type mode uint8

const (
	modeAll mode = iota
	modeText
	modeRegex
	modeChord
)

// matcher is read-only after compile, so workers can share it.
type matcher struct {
	q    Query
	mode mode
	cs   bool

	terms [][]byte // distinct normalized words
	full  []byte   // normalized query, words joined by one space
	multi bool     // the query has more than one word
	all   uint64   // mask with a bit per term

	re    *regexp.Regexp
	chord chordKey

	tags       [][]byte // normalized filter tags
	here, near *place
}

type scratch struct {
	norm   []byte
	offs   []int32
	fields []fieldMask
	ranges [][2]int
}

type fieldMask struct {
	f    field
	mask uint64
}

func compile(q Query) (*matcher, error) {
	m := &matcher{q: q, cs: q.CaseSensitive}
	for _, t := range q.Tags {
		t = strings.TrimPrefix(strings.TrimSpace(t), "#")
		if t != "" {
			m.tags = append(m.tags, normString(t, false))
		}
	}
	if q.Here != nil || q.Near != nil {
		home, _ := os.UserHomeDir()
		m.here = newPlace(q.Here, home)
		m.near = newPlace(q.Near, home)
	}

	text := strings.TrimSpace(q.Text)
	switch {
	case text == "":
		m.mode = modeAll
	case q.Regex:
		re, err := regexp.Compile(q.Text)
		if err == nil && !q.CaseSensitive {
			re, err = regexp.Compile("(?i)" + q.Text)
		}
		if err != nil {
			return nil, err
		}
		m.mode, m.re = modeRegex, re
	default:
		if c, ok := parseChord(text); ok {
			m.mode, m.chord = modeChord, c
			break
		}
		m.mode = modeText
		words := strings.Fields(text)
		m.multi = len(words) > 1
		for _, w := range words {
			n := normString(w, m.cs)
			if len(m.terms) < maxTerms && !slices.ContainsFunc(m.terms, func(t []byte) bool { return bytes.Equal(t, n) }) {
				m.terms = append(m.terms, n)
			}
		}
		m.full = normString(strings.Join(words, " "), m.cs)
		m.all = ^uint64(0) >> (64 - len(m.terms))
	}
	return m, nil
}

func (m *matcher) eval(d *Doc, sc *scratch) *Result {
	if !m.keep(d, sc) {
		return nil
	}
	switch m.mode {
	case modeText:
		return m.evalText(d, sc)
	case modeRegex:
		return m.evalRegex(d)
	case modeChord:
		return m.evalChord(d)
	default:
		return &Result{Doc: d}
	}
}

func (m *matcher) keep(d *Doc, sc *scratch) bool {
	if m.q.Inbox && !d.InInbox {
		return false
	}
	if m.q.ImagesOnly && len(d.Images) == 0 {
		return false
	}
	if !m.q.Since.IsZero() {
		t := d.Date
		if t.IsZero() {
			t = d.Modified
		}
		if t.IsZero() || t.Before(m.q.Since) {
			return false
		}
	}
	if m.here != nil && !m.here.contains(d) {
		return false
	}
	for _, want := range m.tags {
		if !m.hasTag(d, want, sc) {
			return false
		}
	}
	return true
}

func (m *matcher) hasTag(d *Doc, want []byte, sc *scratch) bool {
	for _, t := range d.Tags {
		sc.norm, _ = appendNorm(sc.norm[:0], nil, strings.TrimPrefix(t, "#"), false, false)
		n := sc.norm
		if bytes.Equal(n, want) || len(n) > len(want) && n[len(want)] == '/' && bytes.HasPrefix(n, want) {
			return true
		}
	}
	return false
}

func (m *matcher) evalText(d *Doc, sc *scratch) *Result {
	sc.fields = sc.fields[:0]
	var union uint64
	m.each(d, func(f field, text string) {
		sc.norm, _ = appendNorm(sc.norm[:0], nil, text, m.cs, false)
		var mask uint64
		for i, t := range m.terms {
			if bytes.Contains(sc.norm, t) {
				mask |= 1 << i
			}
		}
		if mask != 0 {
			union |= mask
			sc.fields = append(sc.fields, fieldMask{f, mask})
		}
	})
	if union != m.all {
		return nil
	}

	most := 0
	for _, fm := range sc.fields {
		most = max(most, bits.OnesCount64(fm.mask))
	}
	res := &Result{Doc: d}
	best := math.Inf(-1)
	for _, fm := range sc.fields {
		if bits.OnesCount64(fm.mask) != most {
			continue
		}
		h, quality := m.textHit(d, fm.f, most, sc)
		res.Hits = append(res.Hits, h)
		best = max(best, quality)
	}
	coverage := float64(most) / float64(len(m.terms))
	res.Score = best + bonusCoverage*coverage + hitBonus(len(res.Hits))
	sortHits(res.Hits)
	return res
}

func (m *matcher) textHit(d *Doc, f field, count int, sc *scratch) (Hit, float64) {
	h := f.hit(d)
	sc.norm, sc.offs = appendNorm(sc.norm[:0], sc.offs[:0], h.Text, m.cs, true)
	n := sc.norm
	sc.ranges = sc.ranges[:0]

	whole, start := false, false
	for _, t := range m.terms {
		for off := 0; ; {
			i := bytes.Index(n[off:], t)
			if i < 0 {
				break
			}
			a, b := off+i, off+i+len(t)
			sc.ranges = append(sc.ranges, [2]int{a, b})
			if atWordStart(n, a) {
				start = true
				whole = whole || atWordEnd(n, b)
			}
			off = b
		}
	}

	lineLike := f.kind == fieldLine || f.kind == fieldOCR
	if lineLike && m.multi {
		for off := 0; ; {
			i := bytes.Index(n[off:], m.full)
			if i < 0 {
				break
			}
			h.Kind = KindPhrase
			sc.ranges = append(sc.ranges, [2]int{off + i, off + i + len(m.full)})
			off += i + len(m.full)
		}
	}
	if lineLike && h.Kind != KindPhrase && len(m.terms) > 1 && count == len(m.terms) {
		h.Kind = KindTerms
	}

	h.Ranges = mergeRanges(sc.ranges)
	for i, r := range h.Ranges {
		h.Ranges[i] = sourceRange(sc.offs, len(h.Text), r[0], r[1])
	}

	quality := kindWeight(h.Kind)
	switch {
	case whole:
		quality += bonusWholeWord
	case start:
		quality += bonusWordStart
	}
	if f.kind == fieldOCR && h.Kind != KindOCR {
		quality -= penaltyImage
	}
	if (f.kind == fieldTitle || f.kind == fieldAlias) && bytes.Equal(bytes.TrimSpace(n), m.full) {
		quality += bonusExactTitle
	}
	return h, quality
}

func (m *matcher) evalRegex(d *Doc) *Result {
	var res *Result
	best := math.Inf(-1)
	m.each(d, func(f field, text string) {
		if !m.re.MatchString(text) {
			return
		}
		h := f.hit(d)
		for _, loc := range m.re.FindAllStringIndex(text, -1) {
			if loc[1] > loc[0] {
				h.Ranges = append(h.Ranges, [2]int{loc[0], loc[1]})
			}
		}
		if res == nil {
			res = &Result{Doc: d}
		}
		res.Hits = append(res.Hits, h)
		best = max(best, kindWeight(h.Kind))
	})
	if res == nil {
		return nil
	}
	res.Score = best + bonusCoverage + hitBonus(len(res.Hits))
	sortHits(res.Hits)
	return res
}

func (m *matcher) evalChord(d *Doc) *Result {
	var res *Result
	best := math.Inf(-1)
	m.each(d, func(f field, text string) {
		if f.kind == fieldTag {
			return
		}
		var h Hit
		found := false
		scanChords(text, true, func(k chordKey, a, b int) bool {
			if k == m.chord {
				if !found {
					h, found = f.hit(d), true
				}
				h.Ranges = append(h.Ranges, [2]int{a, b})
			}
			return true
		})
		if !found {
			return
		}
		quality := float64(bonusWholeWord)
		switch f.kind {
		case fieldLine:
			h.Kind = KindChord
		case fieldOCR:
			h.Kind = KindChord
			quality -= penaltyImage
		}
		quality += kindWeight(h.Kind)
		if res == nil {
			res = &Result{Doc: d}
		}
		res.Hits = append(res.Hits, h)
		best = max(best, quality)
	})
	if res == nil {
		return nil
	}
	res.Score = best + bonusCoverage + hitBonus(len(res.Hits))
	sortHits(res.Hits)
	return res
}

func (m *matcher) rank(res *Result, r Ranker, now time.Time) {
	d := res.Doc
	if r != nil {
		if f := r.Frecency(d.Path); f > 0 {
			res.Score += weightFrecency * math.Log2(1+f)
		}
	}
	near := m.near
	if near == nil {
		near = m.here
	}
	if near != nil && near.contains(d) {
		res.Score += bonusHere
	}
	if d.InInbox {
		res.Score += bonusInbox
	}
	t := d.Modified
	if t.IsZero() {
		t = d.Date
	}
	if !t.IsZero() && !now.IsZero() {
		age := max(now.Sub(t), 0)
		res.Score += bonusFresh * math.Pow(0.5, float64(age)/float64(freshHalfLife))
	}
}

func hitBonus(hits int) float64 {
	return bonusPerHit * float64(min(max(hits-1, 0), maxHitBonus))
}

func sortHits(hits []Hit) {
	slices.SortStableFunc(hits, func(a, b Hit) int {
		return cmp.Compare(kindWeight(b.Kind), kindWeight(a.Kind))
	})
}

func mergeRanges(ranges [][2]int) [][2]int {
	if len(ranges) == 0 {
		return nil
	}
	slices.SortFunc(ranges, func(a, b [2]int) int {
		return cmp.Or(cmp.Compare(a[0], b[0]), cmp.Compare(a[1], b[1]))
	})
	out := [][2]int{ranges[0]}
	for _, r := range ranges[1:] {
		last := &out[len(out)-1]
		if r[0] <= last[1] {
			last[1] = max(last[1], r[1])
			continue
		}
		out = append(out, r)
	}
	return out
}

type fieldKind uint8

const (
	fieldTitle fieldKind = iota
	fieldAlias
	fieldTag
	fieldLine
	fieldOCR
)

type field struct {
	kind fieldKind
	i    int // index into Aliases, Tags, Lines, or Images[img].Lines
	img  int
}

func (m *matcher) each(d *Doc, fn func(f field, text string)) {
	if !m.q.ImagesOnly {
		if d.Title != "" {
			fn(field{kind: fieldTitle}, d.Title)
		}
		for i, a := range d.Aliases {
			if a != "" && a != d.Title {
				fn(field{kind: fieldAlias, i: i}, a)
			}
		}
		for i, t := range d.Tags {
			fn(field{kind: fieldTag, i: i}, t)
		}
		for i := range d.Lines {
			fn(field{kind: fieldLine, i: i}, d.Lines[i].Text)
		}
	}
	for img := range d.Images {
		lines := d.Images[img].Lines
		for i := range lines {
			fn(field{kind: fieldOCR, i: i, img: img}, lines[i].Text)
		}
	}
}

func (f field) hit(d *Doc) Hit {
	switch f.kind {
	case fieldTitle:
		return Hit{Text: d.Title, Kind: KindTitle}
	case fieldAlias:
		return Hit{Text: d.Aliases[f.i], Kind: KindAlias}
	case fieldTag:
		return Hit{Text: d.Tags[f.i], Kind: KindTag}
	case fieldLine:
		l := d.Lines[f.i]
		return Hit{Line: l.Num, Text: l.Text, Kind: KindBody}
	default:
		img := &d.Images[f.img]
		l := img.Lines[f.i]
		box := l.Box
		return Hit{Text: l.Text, Kind: KindOCR, Image: img.Path, Box: &box}
	}
}

type place struct {
	cwd, repo, home string
}

func newPlace(h *Here, home string) *place {
	if h == nil {
		return nil
	}
	p := &place{repo: h.Repo, home: home}
	if h.Cwd != "" {
		p.cwd = expandHome(h.Cwd, home)
	}
	return p
}

func (p *place) contains(d *Doc) bool {
	if p.repo != "" && d.Repo == p.repo {
		return true
	}
	if p.cwd == "" || d.Where == "" {
		return false
	}
	where := expandHome(d.Where, p.home)
	return where == p.cwd || p.cwd == "/" || strings.HasPrefix(where, p.cwd+"/")
}

func expandHome(p, home string) string {
	if home != "" && (p == "~" || strings.HasPrefix(p, "~/")) {
		p = home + p[1:]
	}
	return path.Clean(p)
}
