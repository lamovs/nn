// Package search matches a Query against Docs and ranks the results.
package search

import (
	"cmp"
	"context"
	"runtime"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/lamovs/nn/internal/ocr"
)

type Line struct {
	Num  int
	Text string
}

type ImageDoc struct {
	Path  string
	Lines []ocr.Line
}

type Doc struct {
	Path, Title    string
	Aliases, Tags  []string
	Date, Modified time.Time
	Where, Repo    string
	InInbox        bool

	// Via is the note's "via" frontmatter value; not a Query filter.
	Via string

	Lines  []Line
	Images []ImageDoc
}

type Here struct {
	Cwd, Repo string
}

// Query: a whole-text shortcut like "cmd shift 4" matches chords.
type Query struct {
	Text          string
	Regex         bool
	CaseSensitive bool

	Tags  []string
	Since time.Time
	Here  *Here

	Inbox, ImagesOnly bool
	Limit             int

	// Near ranks Docs in this context higher without filtering out the rest.
	Near *Here

	// NoLayoutFallback disables the other-keyboard-layout retry.
	NoLayoutFallback bool
}

type Kind string

const (
	KindPhrase Kind = "phrase" // a line with the whole multi-word query
	KindTerms  Kind = "terms"  // a line with every query word, not as a phrase
	KindChord  Kind = "chord"  // a line with the queried shortcut
	KindTitle  Kind = "title"
	KindAlias  Kind = "alias"
	KindTag    Kind = "tag"
	KindBody   Kind = "body" // a note line with some of the query words
	KindOCR    Kind = "ocr"  // an OCR line with some of the query words
)

// Hit's Ranges are sorted, non-overlapping byte offsets into Text.
type Hit struct {
	Line   int
	Text   string
	Kind   Kind
	Image  string
	Box    *ocr.Box
	Ranges [][2]int
}

type Result struct {
	Doc    *Doc
	Hits   []Hit
	Score  float64
	Layout bool
}

type Ranker interface {
	Frecency(path string) float64
}

// Stream does not drain docs on cancellation; the producer must watch ctx.
func Stream(ctx context.Context, docs <-chan *Doc, q Query, emit func(Result) bool) error {
	m, err := compile(q)
	if err != nil {
		return err
	}
	var alt *matcher
	if text, ok := swappedText(q); ok {
		aq := q
		aq.Text = text
		alt, _ = compile(aq)
	}

	sc := new(scratch)
	emitted := 0
	var pending []Result
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		var d *Doc
		var ok bool
		select {
		case <-ctx.Done():
			return ctx.Err()
		case d, ok = <-docs:
		}
		if !ok {
			break
		}
		if d == nil {
			continue
		}
		if res := m.eval(d, sc); res != nil {
			alt, pending = nil, nil
			emitted++
			if !emit(*res) || (q.Limit > 0 && emitted >= q.Limit) {
				return nil
			}
			continue
		}
		if alt == nil {
			continue
		}
		if res := alt.eval(d, sc); res != nil {
			res.Layout = true
			pending = append(pending, *res)
			if q.Limit > 0 && len(pending) >= q.Limit {
				alt = nil
			}
		}
	}
	for _, res := range pending {
		if err := ctx.Err(); err != nil {
			return err
		}
		if !emit(res) {
			return nil
		}
	}
	return nil
}

func Search(ctx context.Context, docs []*Doc, q Query, r Ranker, now time.Time) ([]Result, error) {
	results, err := search(ctx, docs, q, r, now)
	if err != nil || len(results) > 0 {
		return results, err
	}
	text, ok := swappedText(q)
	if !ok {
		return results, nil
	}
	aq := q
	aq.Text = text
	alt, err := search(ctx, docs, aq, r, now)
	if err != nil {
		return nil, err
	}
	for i := range alt {
		alt[i].Layout = true
	}
	return alt, nil
}

const searchBatch = 64

func search(ctx context.Context, docs []*Doc, q Query, r Ranker, now time.Time) ([]Result, error) {
	m, err := compile(q)
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	found := make([]*Result, len(docs))
	workers := min(runtime.GOMAXPROCS(0), (len(docs)+searchBatch-1)/searchBatch)
	var next atomic.Int64
	var wg sync.WaitGroup
	for range workers {
		wg.Go(func() {
			sc := new(scratch)
			for ctx.Err() == nil {
				end := int(next.Add(searchBatch))
				start := end - searchBatch
				if start >= len(docs) {
					return
				}
				for i := start; i < min(end, len(docs)); i++ {
					if d := docs[i]; d != nil {
						found[i] = m.eval(d, sc)
					}
				}
			}
		})
	}
	wg.Wait()
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	results := make([]Result, 0, 16)
	for _, res := range found {
		if res == nil {
			continue
		}
		m.rank(res, r, now)
		results = append(results, *res)
	}
	slices.SortStableFunc(results, func(a, b Result) int {
		if c := cmp.Compare(b.Score, a.Score); c != 0 {
			return c
		}
		return strings.Compare(a.Doc.Path, b.Doc.Path)
	})
	if q.Limit > 0 && len(results) > q.Limit {
		results = results[:q.Limit]
	}
	return results, nil
}

func swappedText(q Query) (string, bool) {
	if q.NoLayoutFallback || q.Regex || strings.TrimSpace(q.Text) == "" {
		return "", false
	}
	text, ok := SwapLayout(q.Text)
	if !ok || text == q.Text {
		return "", false
	}
	return text, true
}
