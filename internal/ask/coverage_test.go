package ask

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/lamovs/nn/internal/ai"
	"github.com/lamovs/nn/internal/config"
	"github.com/lamovs/nn/internal/ocr"
	"github.com/lamovs/nn/internal/search"
)

func assertLedger(t *testing.T, s *Session) {
	t.Helper()
	cost := 0
	paths := map[string]bool{}
	for _, src := range s.sources {
		if paths[src.Path] {
			t.Fatalf("duplicate source for %s", src.Path)
		}
		paths[src.Path] = true
		if got := renderFragments(src.spans); src.Excerpt != got {
			t.Fatalf("source differs from coordinates: %q != %q", src.Excerpt, got)
		}
		for i, f := range src.spans {
			if f.start >= f.end || !utf8.ValidString(f.text[f.start:f.end]) {
				t.Fatalf("invalid interval %+v", f)
			}
			if i > 0 {
				prior := src.spans[i-1]
				if before(f.origin, prior.origin) || prior.origin == f.origin && prior.end >= f.start {
					t.Fatalf("noncanonical or overlapping intervals: %+v %+v", prior, f)
				}
			}
		}
		cost += contentChars(src)
	}
	if s.chars != cost || cost > s.opts.Limits.Chars || len(paths) > s.opts.Limits.Notes {
		t.Fatalf("invalid ledger: chars=%d exact=%d notes=%d", s.chars, cost, len(paths))
	}
}

func TestReorderedIdenticalOriginsAreNoProgress(t *testing.T) {
	s := prepare(t, map[string]string{"a.md": "alpha first\nbeta second\n"}, "alpha", 3)
	initialChars, initialID, initialText := s.chars, s.sources[0].ID, s.sources[0].Excerpt
	calls := 0
	text, err := s.Run(context.Background(), ai.Approval{}, call(), "prompt", func(_ context.Context, _ ai.Approval, req ai.Request) (ai.Result, error) {
		calls++
		got := decodeRequest(t, req)
		if calls == 1 {
			return ai.Result{Ask: &ai.AskReply{Action: "search", Query: "beta"}}, nil
		}
		assertLedger(t, s)
		if len(got.Sources) != 1 || got.Sources[0].ID != initialID || got.Sources[0].Excerpt != initialText || s.chars != initialChars || got.SearchesRemaining != 0 || !got.MustFinalize {
			t.Fatalf("reordered same origins progressed: chars %d -> %d, request=%+v", initialChars, s.chars, got)
		}
		return answer(initialID), nil
	})
	if err != nil || text == "" || calls != 2 {
		t.Fatalf("text=%q calls=%d err=%v", text, calls, err)
	}
}

func intervalSession(t *testing.T, text string, useOCR bool) *Session {
	t.Helper()
	doc := &search.Doc{Path: "only.md", Title: "Long metadata title", Tags: []string{"long-metadata-tag", "other"}}
	if useOCR {
		doc.Images = []search.ImageDoc{{Path: "image.png", Lines: []ocr.Line{{Text: text}}}}
	} else {
		doc.Lines = []search.Line{{Num: 700, Text: text}}
	}
	s := &Session{vault: fixture(t, nil), opts: Options{Limits: config.AIContext{Notes: 1, Chars: 12000, SearchRounds: 3}, NoLayoutFallback: true}, docs: []*search.Doc{doc}, question: "alpha", sources: []source{}, paths: map[string]bool{}, now: time.Unix(100, 0)}
	if progress, err := s.retrieve(context.Background(), "alpha", 4); err != nil || !progress {
		t.Fatalf("initial progress=%v err=%v", progress, err)
	}
	return s
}

func TestBodyAndOCRUnicodeIntervalUnion(t *testing.T) {
	line := strings.Repeat("я", 300) + "alpha" + strings.Repeat("ю", 150) + "beta" + strings.Repeat("ж", 700) + "omega" + strings.Repeat("э", 800)
	for _, useOCR := range []bool{false, true} {
		name := "body"
		if useOCR {
			name = "ocr"
		}
		t.Run(name, func(t *testing.T) {
			s := intervalSession(t, line, useOCR)
			original := s.sources[0]
			originalChars := s.chars
			oldRequest, err := s.request(3, false)
			if err != nil {
				t.Fatal(err)
			}
			if progress, err := s.retrieve(context.Background(), "beta", 3); err != nil || !progress {
				t.Fatalf("overlap progress=%v err=%v", progress, err)
			}
			assertLedger(t, s)
			grown := s.sources[0]
			if len(s.sources) != 1 || grown.ID != original.ID || len(grown.spans) != 1 || !strings.Contains(grown.Excerpt, "alpha") || !strings.Contains(grown.Excerpt, "beta") {
				t.Fatalf("incorrect overlap union: %+v", grown)
			}
			oldSpan, newSpan := original.spans[0], grown.spans[0]
			if newSpan.start != oldSpan.start || newSpan.end <= oldSpan.end {
				t.Fatalf("overlap did not extend only novel suffix: old=%+v new=%+v", oldSpan, newSpan)
			}
			newRunes := utf8.RuneCountInString(line[oldSpan.end:newSpan.end])
			if s.chars-originalChars != newRunes {
				t.Fatalf("overlap or metadata charged twice: delta=%d novel=%d", s.chars-originalChars, newRunes)
			}
			if !strings.Contains(oldRequest, "alpha") || strings.Contains(original.Excerpt, "omega") || original.spans[0].end != oldSpan.end {
				t.Fatal("previous request or immutable source snapshot changed")
			}
			if progress, err := s.retrieve(context.Background(), "omega", 2); err != nil || !progress {
				t.Fatalf("distant progress=%v err=%v", progress, err)
			}
			assertLedger(t, s)
			if len(s.sources) != 1 || s.sources[0].ID != original.ID || len(s.sources[0].spans) != 2 || !strings.Contains(s.sources[0].Excerpt, "omega") {
				t.Fatalf("distant interval lost: %+v", s.sources[0])
			}
			for _, f := range grown.spans {
				if len(uncovered(f, s.sources[0].spans)) != 0 {
					t.Fatal("old covered coordinates dropped")
				}
			}
			before := s.chars
			if progress, err := s.retrieve(context.Background(), "beta", 1); err != nil || progress || before != s.chars {
				t.Fatalf("repeated intervals progressed=%v chars=%d->%d err=%v", progress, before, s.chars, err)
			}
		})
	}
}

func TestEqualOCRTextKeepsDistinctLineOrigins(t *testing.T) {
	doc := &search.Doc{Path: "note.md", Title: "OCR", Images: []search.ImageDoc{{Path: "image.png", Lines: []ocr.Line{
		{Text: "same alpha"}, {Text: "beta neighbor"}, {Text: "same alpha"}, {Text: "last context"},
	}}}}
	s := &Session{vault: fixture(t, nil), opts: Options{Limits: config.Default().AI.Context, NoLayoutFallback: true}, docs: []*search.Doc{doc}, question: "alpha", sources: []source{}, paths: map[string]bool{}, now: time.Unix(100, 0)}
	if progress, err := s.retrieve(context.Background(), "alpha", 2); err != nil || !progress {
		t.Fatalf("progress=%v err=%v", progress, err)
	}
	assertLedger(t, s)
	src := s.sources[0]
	if len(src.spans) != 4 || strings.Count(src.Excerpt, "same alpha") != 2 || !strings.Contains(src.Excerpt, "OCR image.png L1:") || !strings.Contains(src.Excerpt, "OCR image.png L3:") {
		t.Fatalf("equal OCR origins collapsed: %+v", src)
	}
	before := s.chars
	if progress, err := s.retrieve(context.Background(), "beta", 1); err != nil || progress || s.chars != before {
		t.Fatalf("OCR reordering progressed=%v chars=%d->%d err=%v", progress, before, s.chars, err)
	}
}

func TestTransportFitPreservesOnlySentCoordinates(t *testing.T) {
	var body strings.Builder
	body.WriteString("alpha first\nsmall neighbor\nseparator\n")
	for range 20 {
		body.WriteString(strings.Repeat("<", 1000) + "beta" + strings.Repeat(">", 1000) + "\n")
	}
	v := fixture(t, map[string]string{"a.md": body.String()})
	question := strings.Repeat("<", 40000) + " alpha"
	s, err := Prepare(context.Background(), v, Options{Limits: config.AIContext{Notes: 1, Chars: 100000, SearchRounds: 1}, NoLayoutFallback: true}, question)
	if err != nil {
		t.Fatal(err)
	}
	initial := s.sources[0]
	if progress, err := s.retrieve(context.Background(), "beta", 1); err != nil || !progress {
		t.Fatalf("progress=%v err=%v", progress, err)
	}
	assertLedger(t, s)
	for _, f := range initial.spans {
		if len(uncovered(f, s.sources[0].spans)) != 0 {
			t.Fatal("transport fit removed previously sent coordinates")
		}
	}
	text, err := s.request(0, true)
	if err != nil || len(text) > ai.MaxTextBytes {
		t.Fatalf("request bytes=%d err=%v", len(text), err)
	}
	found, err := s.candidates(context.Background(), "beta")
	if err != nil {
		t.Fatal(err)
	}
	unsent := 0
	for _, proposed := range plannedFragments(found[0]) {
		unsent += len(uncovered(proposed, s.sources[0].spans))
	}
	if unsent == 0 {
		t.Fatal("test failed to exercise byte trimming, or unsent coordinates were marked covered")
	}
	got := decodeRequest(t, ai.Request{Text: text})
	if len(got.Sources) != 1 || got.Sources[0].ID != initial.ID || got.Sources[0].Excerpt != s.sources[0].Excerpt {
		t.Fatal("wire context differs from admitted coordinates")
	}
}

func TestInvisibleModelProseIsRejected(t *testing.T) {
	for _, action := range []string{"answer", "insufficient"} {
		t.Run(action, func(t *testing.T) {
			s := prepare(t, map[string]string{"a.md": "alpha"}, "alpha", 0)
			text, err := s.Run(context.Background(), ai.Approval{}, call(), "prompt", func(context.Context, ai.Approval, ai.Request) (ai.Result, error) {
				reply := &ai.AskReply{Action: action}
				if action == "answer" {
					reply.Paragraphs = []ai.AskParagraph{{Text: "\x00\u200b", SourceIDs: []string{"S1"}}}
				} else {
					reply.Missing = "\x00\u200b"
				}
				return ai.Result{Ask: reply}, nil
			})
			if err == nil || errors.Is(err, ErrInsufficient) || text != "" {
				t.Fatalf("invisible %s accepted: text=%q err=%v", action, text, err)
			}
		})
	}
	s := prepare(t, map[string]string{"a.md": "alpha"}, "alpha", 0)
	text, err := s.Run(context.Background(), ai.Approval{}, call(), "prompt", func(context.Context, ai.Approval, ai.Request) (ai.Result, error) {
		return ai.Result{Ask: &ai.AskReply{Action: "insufficient", Missing: "Missing\x00\u200b date.", Paragraphs: []ai.AskParagraph{{Text: "Known\x00\u200b fact.", SourceIDs: []string{"S1"}}}}}, nil
	})
	if !errors.Is(err, ErrInsufficient) || !strings.Contains(text, "Known fact.") || !strings.Contains(text, "Missing date.") {
		t.Fatalf("visible mixed prose lost: text=%q err=%v", text, err)
	}
}
