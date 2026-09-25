package search

import (
	"context"
	"fmt"
	"math/rand/v2"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lamovs/nn/internal/ocr"
)

const (
	benchDocs     = 10000
	benchLines    = 50
	benchOCRLines = 15
)

var (
	benchOnce   sync.Once
	benchCorpus []*Doc
)

func syntheticCorpus() []*Doc {
	benchOnce.Do(func() {
		rng := rand.New(rand.NewPCG(1, 2))
		latC, latV := []rune("bdfgklmnprstvz"), []rune("aeiou")
		cyrC, cyrV := []rune("бвгдзклмнпрстф"), []rune("аеиоуыя")
		vocab := make([]string, 0, 3000)
		for i := range cap(vocab) {
			cons, vows := latC, latV
			if i%3 == 0 {
				cons, vows = cyrC, cyrV
			}
			var sb strings.Builder
			for range 2 + rng.IntN(3) {
				sb.WriteRune(cons[rng.IntN(len(cons))])
				sb.WriteRune(vows[rng.IntN(len(vows))])
			}
			vocab = append(vocab, sb.String())
		}
		sentence := func(words int) string {
			parts := make([]string, words)
			for i := range parts {
				parts[i] = vocab[rng.IntN(len(vocab))]
			}
			return strings.Join(parts, " ")
		}

		docs := make([]*Doc, benchDocs)
		for i := range docs {
			d := &Doc{
				Path:     fmt.Sprintf("notes/%05d.md", i),
				Title:    sentence(3),
				Aliases:  []string{sentence(3)},
				Tags:     []string{vocab[rng.IntN(len(vocab))], vocab[rng.IntN(len(vocab))]},
				Modified: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC).Add(time.Duration(i) * time.Hour),
				InInbox:  i%3 == 0,
			}
			for n := range benchLines {
				d.Lines = append(d.Lines, Line{Num: n + 6, Text: sentence(6 + rng.IntN(6))})
			}
			if i%4 == 0 {
				img := ImageDoc{Path: fmt.Sprintf("notes/assets/%05d.png", i)}
				for n := range benchOCRLines {
					img.Lines = append(img.Lines, ocr.Line{Text: sentence(4), Box: ocr.Box{Y: float64(n) / benchOCRLines}})
				}
				d.Images = append(d.Images, img)
			}
			plant := func(word string) {
				l := &d.Lines[rng.IntN(len(d.Lines))]
				l.Text += " " + word
			}
			if rng.IntN(10) == 0 {
				plant("docker")
				if rng.IntN(3) == 0 {
					plant("compose")
				}
			}
			if rng.IntN(100) == 0 {
				plant("kubeadm")
			}
			if rng.IntN(100) == 0 {
				plant("привет")
			}
			if rng.IntN(100) == 0 {
				plant("Cmd+Shift+4")
			}
			docs[i] = d
		}
		benchCorpus = docs
	})
	return benchCorpus
}

type benchRanker struct{}

func (benchRanker) Frecency(path string) float64 {
	if strings.HasSuffix(path, "7.md") {
		return 1.5
	}
	return 0
}

func benchSearch(b *testing.B, q Query) {
	docs := syntheticCorpus()
	now := time.Date(2026, 9, 16, 0, 0, 0, 0, time.UTC)
	b.ResetTimer()
	var n int
	for b.Loop() {
		res, err := Search(context.Background(), docs, q, benchRanker{}, now)
		if err != nil {
			b.Fatal(err)
		}
		n = len(res)
	}
	b.ReportMetric(float64(n), "results")
}

func BenchmarkSearchRareWord(b *testing.B)   { benchSearch(b, Query{Text: "kubeadm"}) }
func BenchmarkSearchCommonWord(b *testing.B) { benchSearch(b, Query{Text: "docker"}) }
func BenchmarkSearchMultiWord(b *testing.B)  { benchSearch(b, Query{Text: "docker compose"}) }
func BenchmarkSearchCyrillic(b *testing.B)   { benchSearch(b, Query{Text: "ПРИВЕТ"}) }
func BenchmarkSearchChord(b *testing.B)      { benchSearch(b, Query{Text: "cmd shift 4"}) }
func BenchmarkSearchRegex(b *testing.B)      { benchSearch(b, Query{Text: `kube\w+`, Regex: true}) }

func BenchmarkSearchNoMatch(b *testing.B) { benchSearch(b, Query{Text: "zzqqxx"}) }

func BenchmarkStreamFirstResult(b *testing.B) {
	docs := syntheticCorpus()
	var total time.Duration
	for b.Loop() {
		ctx, cancel := context.WithCancel(context.Background())
		ch := make(chan *Doc, 64)
		go func() {
			defer close(ch)
			for _, d := range docs {
				select {
				case ch <- d:
				case <-ctx.Done():
					return
				}
			}
		}()
		start := time.Now()
		err := Stream(ctx, ch, Query{Text: "kubeadm"}, func(Result) bool {
			total += time.Since(start)
			return false
		})
		cancel()
		if err != nil {
			b.Fatal(err)
		}
	}
	b.ReportMetric(float64(total.Microseconds())/float64(b.N), "us/first")
}

func BenchmarkStreamFull(b *testing.B) {
	docs := syntheticCorpus()
	for b.Loop() {
		n := 0
		err := Stream(context.Background(), feed(docs), Query{Text: "docker"}, func(Result) bool {
			n++
			return true
		})
		if err != nil || n == 0 {
			b.Fatal(err, n)
		}
	}
}
