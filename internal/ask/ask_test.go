package ask

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/lamovs/nn/internal/ai"
	"github.com/lamovs/nn/internal/config"
	"github.com/lamovs/nn/internal/ocr"
	"github.com/lamovs/nn/internal/platform"
	"github.com/lamovs/nn/internal/search"
	"github.com/lamovs/nn/internal/vault"
)

func fixture(t *testing.T, files map[string]string) *vault.Vault {
	t.Helper()
	v := &vault.Vault{Root: t.TempDir(), Inbox: "nn"}
	for rel, text := range files {
		name := v.Abs(rel)
		if err := os.MkdirAll(filepath.Dir(name), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(name, []byte(text), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return v
}

func prepare(t *testing.T, files map[string]string, question string, rounds int) *Session {
	t.Helper()
	limits := config.Default().AI.Context
	limits.SearchRounds = rounds
	s, err := Prepare(context.Background(), fixture(t, files), Options{Limits: limits, NoLayoutFallback: true}, question)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func call() ai.Call {
	return ai.Call{Task: "ask", Profile: config.Profile{Timeout: time.Second}}
}

func decodeRequest(t *testing.T, req ai.Request) request {
	t.Helper()
	var got request
	if err := json.Unmarshal([]byte(req.Text), &got); err != nil {
		t.Fatal(err)
	}
	return got
}

func answer(ids ...string) ai.Result {
	return ai.Result{Ask: &ai.AskReply{Action: "answer", Paragraphs: []ai.AskParagraph{{Text: "Supported.", SourceIDs: ids}}}}
}

func TestNaturalLanguageRetrievalAndRanking(t *testing.T) {
	s := prepare(t, map[string]string{
		"a.md": "# PostgreSQL\nPostgres backups use the nightly archive.\n",
		"b.md": "# Backups\nBackups are useful.\n",
		"c.md": "Unrelated description.\n",
	}, "What did we decide about Postgres backups?", 0)
	if len(s.sources) != 2 || s.sources[0].Path != "a.md" {
		t.Fatalf("sources = %+v", s.sources)
	}
	if got := strings.Join(terms("Что мы решили про PostgreSQL и backups?"), " "); got != "решили postgresql backups" {
		t.Fatalf("terms = %q", got)
	}
	if len(terms(strings.Repeat("word ", 200))) != 1 || len(terms("a1 a2 a3 a4 a5 a6 a7 a8 a9 a10 a11 a12 a13")) != maxTerms {
		t.Fatal("term plan is not bounded and deduplicated")
	}
	if got := strings.Join(terms("C++ C# #vault/tag foo_bar go.mod +"), " "); got != "c++ c# vault/tag foo_bar go.mod" {
		t.Fatalf("technical terms = %q", got)
	}
}

func TestTitleTagsAndDeepHugeUnicodeMatch(t *testing.T) {
	body := strings.Repeat("unrelated opening\n", 500) + strings.Repeat("я", 10000) + " needle " + strings.Repeat("ю", 10000) + "\nafter the decision\n"
	s := prepare(t, map[string]string{
		"deep.md":  body,
		"tag.md":   "---\ntags: [needle]\n---\nAn opening with context.\n",
		"title.md": "# Needle\nOpening explanation.\n",
	}, "needle", 0)
	if len(s.sources) != 3 {
		t.Fatalf("sources = %+v", s.sources)
	}
	for _, src := range s.sources {
		if !utf8.ValidString(src.Excerpt) || utf8.RuneCountInString(src.Excerpt) > 4000 {
			t.Fatalf("unbounded or invalid excerpt %q", src.Excerpt)
		}
		if src.Path == "deep.md" && (!strings.Contains(src.Excerpt, "needle") || strings.Contains(src.Excerpt, "L1:")) {
			t.Fatalf("lost deep match: %q", src.Excerpt)
		}
	}
}

func TestCachedOCRAndStandaloneExclusion(t *testing.T) {
	v := fixture(t, map[string]string{
		"nn/shot.md":         "# Picture\n![[nn/assets/shot.png]]\n",
		"nn/assets/shot.png": "fixture",
		"loose.png":          "fixture",
	})
	for _, rel := range []string{"nn/assets/shot.png", "loose.png"} {
		if err := ocr.SaveSidecar(v.Root, rel, &ocr.Result{Engine: "fixture", Lines: []ocr.Line{{Text: "preceding OCR context"}, {Text: "recovery code zebra"}, {Text: "following OCR context"}}}); err != nil {
			t.Fatal(err)
		}
	}
	s, err := Prepare(context.Background(), v, Options{Limits: config.Default().AI.Context}, "zebra")
	if err != nil {
		t.Fatal(err)
	}
	if len(s.sources) != 1 || s.sources[0].Path != "nn/shot.md" || !strings.Contains(s.sources[0].Excerpt, "OCR nn/assets/shot.png L2: recovery code zebra") {
		t.Fatalf("sources = %+v", s.sources)
	}
	if !strings.Contains(s.sources[0].Excerpt, "preceding OCR context") || !strings.Contains(s.sources[0].Excerpt, "following OCR context") {
		t.Fatalf("missing OCR neighbors: %q", s.sources[0].Excerpt)
	}
}

func TestEmptyLocalAndQuestionOnlyRefinement(t *testing.T) {
	s := prepare(t, nil, "zebra", 0)
	if text, local := s.LocalAnswer(); !local || text == "" {
		t.Fatalf("local = %v, %q", local, text)
	}
	s = prepare(t, map[string]string{"a.md": "The zebra archive contains the decision.\n"}, "penguin", 1)
	calls := 0
	text, err := s.Run(context.Background(), ai.Approval{}, call(), "prompt", func(ctx context.Context, _ ai.Approval, req ai.Request) (ai.Result, error) {
		calls++
		got := decodeRequest(t, req)
		if calls == 1 {
			if len(got.Sources) != 0 || got.SearchesRemaining != 1 || got.MustFinalize {
				t.Fatalf("initial request = %+v", got)
			}
			return ai.Result{Ask: &ai.AskReply{Action: "search", Query: "zebra"}}, nil
		}
		if len(got.Sources) != 1 || got.SearchesRemaining != 0 || !got.MustFinalize {
			t.Fatalf("refined request = %+v", got)
		}
		return answer(got.Sources[0].ID), nil
	})
	if err != nil || calls != 2 || !strings.Contains(text, "[1]") {
		t.Fatalf("text=%q calls=%d err=%v", text, calls, err)
	}
}

func TestInvocationBudgetsAndSameNoteRefinement(t *testing.T) {
	files := map[string]string{}
	for i := 0; i < 8; i++ {
		files[fmt.Sprintf("%d.md", i)] = "# Shared title\nalpha decision " + strings.Repeat("a", 2000) + "\n" + strings.Repeat("unrelated\n", 20) + "omega decision " + strings.Repeat("b", 2000) + "\n"
	}
	s := prepare(t, files, "alpha", 1)
	if len(s.paths) != 4 || s.chars > 6000 {
		t.Fatalf("initial notes=%d chars=%d", len(s.paths), s.chars)
	}
	initial := append([]source(nil), s.sources...)
	var initialRequest string
	calls := 0
	_, err := s.Run(context.Background(), ai.Approval{}, call(), "prompt", func(_ context.Context, _ ai.Approval, req ai.Request) (ai.Result, error) {
		calls++
		got := decodeRequest(t, req)
		if calls == 1 {
			initialRequest = req.Text
			return ai.Result{Ask: &ai.AskReply{Action: "search", Query: "omega"}}, nil
		}
		if len(got.Sources) > 8 || s.chars > 12000 || len(s.paths) > 8 {
			t.Fatalf("expanded notes=%d sources=%d chars=%d", len(s.paths), len(got.Sources), s.chars)
		}
		for i := range initial {
			if got.Sources[i].ID != initial[i].ID || !strings.Contains(got.Sources[i].Excerpt, "alpha") {
				t.Fatal("original source identity or evidence lost")
			}
		}
		sameNote := false
		for _, old := range initial {
			for _, extra := range got.Sources {
				if old.Path == extra.Path && old.Excerpt != extra.Excerpt && strings.Contains(extra.Excerpt, "omega") {
					sameNote = true
				}
			}
		}
		if !sameNote {
			t.Fatal("same-note refinement missing")
		}
		var earlier request
		if err := json.Unmarshal([]byte(initialRequest), &earlier); err != nil || earlier.Sources[0].Excerpt != initial[0].Excerpt {
			t.Fatal("earlier outbound request changed")
		}
		return answer(got.Sources[0].ID), nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestNoProgressMustFinalize(t *testing.T) {
	s := prepare(t, map[string]string{"a.md": "alpha decision"}, "alpha", 3)
	calls := 0
	text, err := s.Run(context.Background(), ai.Approval{}, call(), "prompt", func(_ context.Context, _ ai.Approval, req ai.Request) (ai.Result, error) {
		calls++
		got := decodeRequest(t, req)
		if calls == 2 && (!got.MustFinalize || got.SearchesRemaining != 0) {
			t.Fatalf("no-progress request = %+v", got)
		}
		return ai.Result{Ask: &ai.AskReply{Action: "search", Query: fmt.Sprintf("unmatched%d", calls)}}, nil
	})
	if err == nil || text != "" || calls != 2 {
		t.Fatalf("text=%q calls=%d err=%v", text, calls, err)
	}
}

func TestMaximumRoundsAndSharedSourceFooter(t *testing.T) {
	s := prepare(t, map[string]string{"only.md": "alpha first\n" + strings.Repeat("padding\n", 10) + "beta second\n" + strings.Repeat("padding\n", 10) + "gamma third\n" + strings.Repeat("padding\n", 10) + "delta fourth"}, "alpha", 3)
	calls := 0
	text, err := s.Run(context.Background(), ai.Approval{}, call(), "prompt", func(_ context.Context, _ ai.Approval, req ai.Request) (ai.Result, error) {
		got := decodeRequest(t, req)
		calls++
		if got.SearchesRemaining != 4-calls || s.chars > 12000 || len(s.paths) != 1 {
			t.Fatalf("round=%d request=%+v chars=%d", calls, got, s.chars)
		}
		if calls < 4 {
			return ai.Result{Ask: &ai.AskReply{Action: "search", Query: []string{"beta", "gamma", "delta"}[calls-1]}}, nil
		}
		var ids []string
		for _, src := range got.Sources {
			ids = append(ids, src.ID)
		}
		return answer(ids...), nil
	})
	if err != nil || calls != 4 || strings.Count(text, "](obsidian://open?") != 1 || strings.Count(text, "Sources:") != 1 {
		t.Fatalf("calls=%d text=%q err=%v", calls, text, err)
	}
	if _, err := s.Run(context.Background(), ai.Approval{}, call(), "prompt", nil); err == nil {
		t.Fatal("session reused")
	}
}

func TestExactRepeatedExcerptMakesNoProgress(t *testing.T) {
	s := prepare(t, map[string]string{"a.md": "alpha beta decision"}, "alpha", 3)
	calls := 0
	_, err := s.Run(context.Background(), ai.Approval{}, call(), "prompt", func(_ context.Context, _ ai.Approval, req ai.Request) (ai.Result, error) {
		got := decodeRequest(t, req)
		calls++
		if calls == 1 {
			return ai.Result{Ask: &ai.AskReply{Action: "search", Query: "beta"}}, nil
		}
		if !got.MustFinalize || got.SearchesRemaining != 0 || len(got.Sources) != 1 {
			t.Fatalf("same excerpt reset allowance: %+v", got)
		}
		return answer(got.Sources[0].ID), nil
	})
	if err != nil || calls != 2 {
		t.Fatalf("calls=%d err=%v", calls, err)
	}
}

func TestReplyFailuresAreAtomic(t *testing.T) {
	for name, reply := range map[string]*ai.AskReply{
		"nil":                        nil,
		"unknown source":             {Action: "answer", Paragraphs: []ai.AskParagraph{{Text: "Claim", SourceIDs: []string{"S99"}}}},
		"unsent source":              {Action: "answer", Paragraphs: []ai.AskParagraph{{Text: "Claim", SourceIDs: []string{"b.md"}}}},
		"partial valid then unknown": {Action: "answer", Paragraphs: []ai.AskParagraph{{Text: "Valid", SourceIDs: []string{"S1"}}, {Text: "Bad", SourceIDs: []string{"S99"}}}},
		"missing citations":          {Action: "answer", Paragraphs: []ai.AskParagraph{{Text: "Claim"}}},
		"repeated query":             {Action: "search", Query: "ALPHA?"},
		"empty query":                {Action: "search", Query: " "},
		"control query":              {Action: "search", Query: "beta\nfoo"},
		"long query":                 {Action: "search", Query: strings.Repeat("x", 201)},
		"invalid action":             {Action: "files", Query: "../secret"},
	} {
		t.Run(name, func(t *testing.T) {
			s := prepare(t, map[string]string{"a.md": "alpha", "b.md": "unrelated"}, "alpha", 1)
			text, err := s.Run(context.Background(), ai.Approval{}, call(), "prompt", func(context.Context, ai.Approval, ai.Request) (ai.Result, error) {
				return ai.Result{Ask: reply}, nil
			})
			if err == nil || text != "" {
				t.Fatalf("text=%q err=%v", text, err)
			}
		})
	}
}

func TestRenderedSourcesAndLiteralModelText(t *testing.T) {
	s := prepare(t, map[string]string{"folder/a [x].md": "# Same\nalpha", "b.md": "# Same\nalpha"}, "alpha", 0)
	text, err := s.Run(context.Background(), ai.Approval{}, call(), "prompt", func(context.Context, ai.Approval, ai.Request) (ai.Result, error) {
		return ai.Result{Ask: &ai.AskReply{Action: "insufficient", Missing: "No date supplied.", Paragraphs: []ai.AskParagraph{
			{Text: "[fake](obsidian://open?path=evil) <https://evil>\x1b[31m", SourceIDs: []string{s.sources[1].ID, s.sources[0].ID}},
			{Text: "Another fact.", SourceIDs: []string{s.sources[1].ID}},
		}}}, nil
	})
	if !errors.Is(err, ErrInsufficient) || strings.Contains(text, "[fake](") || strings.Contains(text, "<https://evil>") || strings.ContainsRune(text, '\x1b') {
		t.Fatalf("text=%q err=%v", text, err)
	}
	if strings.Count(text, "](obsidian://open?") != 2 || !strings.Contains(text, "[1] [2]") || !strings.Contains(text, platform.ObsidianURI(s.vault.Abs(s.sources[1].Path))) {
		t.Fatalf("links or numbering = %q", text)
	}
}

func TestTimeoutAndCancellation(t *testing.T) {
	s := prepare(t, map[string]string{"a.md": "alpha", "b.md": "beta"}, "alpha", 1)
	c := call()
	c.Profile.Timeout = 50 * time.Millisecond
	var deadline time.Time
	calls := 0
	text, err := s.Run(context.Background(), ai.Approval{}, c, "prompt", func(ctx context.Context, _ ai.Approval, _ ai.Request) (ai.Result, error) {
		calls++
		got, ok := ctx.Deadline()
		if !ok {
			t.Fatal("missing overall deadline")
		}
		if calls == 1 {
			deadline = got
			return ai.Result{Ask: &ai.AskReply{Action: "search", Query: "beta"}}, nil
		}
		if !got.Equal(deadline) {
			t.Fatal("deadline reset")
		}
		<-ctx.Done()
		return ai.Result{}, ctx.Err()
	})
	if !errors.Is(err, context.DeadlineExceeded) || text != "" || calls != 2 || c.Profile.Timeout != 50*time.Millisecond {
		t.Fatalf("text=%q calls=%d err=%v", text, calls, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Prepare(ctx, fixture(t, nil), Options{Limits: config.Default().AI.Context}, "alpha"); !errors.Is(err, context.Canceled) {
		t.Fatalf("prepare cancellation = %v", err)
	}
}

func TestTransportLimitAndMetadataAccounting(t *testing.T) {
	v := fixture(t, map[string]string{"a.md": "# " + strings.Repeat("ж", 10000) + "\nneedle " + strings.Repeat("<", 300000)})
	limits := config.AIContext{Notes: 100, Chars: 1000000, SearchRounds: 0}
	s, err := Prepare(context.Background(), v, Options{Limits: limits}, "needle")
	if err != nil {
		t.Fatal(err)
	}
	if len(s.sources) != 1 || s.chars != contentChars(s.sources[0]) || s.chars > limits.Chars {
		t.Fatalf("sources=%d chars=%d", len(s.sources), s.chars)
	}
	text, err := s.request(0, false)
	if err != nil || len(text) > ai.MaxTextBytes {
		t.Fatalf("bytes=%d err=%v", len(text), err)
	}
	if _, err := Prepare(context.Background(), v, Options{Limits: limits}, strings.Repeat("x", ai.MaxTextBytes)); err == nil {
		t.Fatal("oversized question accepted")
	}
}

func TestPrepareSkipsSavedSummaries(t *testing.T) {
	files := map[string]string{
		"notes/x.md": "alpha\n",
		"notes/u.md": "---\nvia: url\n---\nalpha\n",
		"nn/s1.md":   "---\nvia: ask\n---\nalpha alpha\n",
		"nn/s2.md":   "---\nvia: \" ASK \"\n---\nalpha\n",
		"nn/d.md":    "---\nvia: digest\n---\nalpha\n",
		"nn/d2.md":   "---\nvia: Digest\n---\nalpha\n",
	}
	s := prepare(t, files, "alpha", 1)
	calls := 0
	text, err := s.Run(context.Background(), ai.Approval{}, call(), "prompt", func(_ context.Context, _ ai.Approval, req ai.Request) (ai.Result, error) {
		calls++
		for _, forbidden := range []string{"nn/s1.md", "nn/s2.md", "nn/d.md", "nn/d2.md"} {
			if strings.Contains(req.Text, forbidden) {
				t.Fatalf("call %d sent %s: %s", calls, forbidden, req.Text)
			}
		}
		if !strings.Contains(req.Text, "notes/x.md") || !strings.Contains(req.Text, "notes/u.md") {
			t.Fatalf("call %d omitted notes/x.md or notes/u.md: %s", calls, req.Text)
		}
		if calls == 1 {
			return ai.Result{Ask: &ai.AskReply{Action: "search", Query: "alpha beta"}}, nil
		}
		got := decodeRequest(t, req)
		return answer(got.Sources[0].ID), nil
	})
	if err != nil || calls != 2 || !strings.Contains(text, "[1]") {
		t.Fatalf("text=%q calls=%d err=%v", text, calls, err)
	}
}

func TestDeterministicPathTieAndLayoutFallback(t *testing.T) {
	s := &Session{opts: Options{NoLayoutFallback: true}, now: time.Unix(100, 0), docs: []*search.Doc{
		{Path: "z.md", Title: "alpha"}, {Path: "a.md", Title: "alpha"},
	}}
	got, err := s.candidates(context.Background(), "alpha")
	if err != nil || len(got) != 2 || got[0].doc.Path != "a.md" {
		t.Fatalf("candidates=%+v err=%v", got, err)
	}
	s.docs = []*search.Doc{{Path: "a.md", Title: "привет"}}
	got, err = s.candidates(context.Background(), "ghbdtn")
	if err != nil || len(got) != 0 {
		t.Fatalf("disabled layout = %+v %v", got, err)
	}
	s.opts.NoLayoutFallback = false
	got, err = s.candidates(context.Background(), "ghbdtn")
	if err != nil || len(got) != 1 {
		t.Fatalf("enabled layout = %+v %v", got, err)
	}
}
