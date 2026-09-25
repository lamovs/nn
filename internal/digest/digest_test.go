package digest

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lamovs/nn/internal/ai"
	"github.com/lamovs/nn/internal/config"
	"github.com/lamovs/nn/internal/corpus"
	"github.com/lamovs/nn/internal/search"
	"github.com/lamovs/nn/internal/vault"
)

// Invisible and bidirectional characters, spelled as bytes.
const (
	zeroWidthSpace = "\xe2\x80\x8b" // U+200B
	rightToLeft    = "\xe2\x80\xae" // U+202E
	lineSeparator  = "\xe2\x80\xa8" // U+2028
	softHyphen     = "\xc2\xad"     // U+00AD
)

var testNow = time.Date(2026, 9, 24, 15, 30, 0, 0, time.Local)

func day(d int) time.Time { return time.Date(2026, 9, d, 0, 0, 0, 0, time.Local) }

func writeVault(t *testing.T, files map[string]string) *vault.Vault {
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

func load(t *testing.T, v *vault.Vault) []*search.Doc {
	t.Helper()
	docs, err := corpus.Load(context.Background(), v)
	if err != nil {
		t.Fatal(err)
	}
	return docs
}

func md(date string, tags string, lines ...string) string {
	front := "---\ndate: " + date + "\n"
	if tags != "" {
		front += "tags: [" + tags + "]\n"
	}
	return front + "---\n" + strings.Join(lines, "\n") + "\n"
}

func options() Options {
	return Options{Notes: 8, Chars: 12000, NoLayoutFallback: true, Now: testNow}
}

func mustPrepare(t *testing.T, v *vault.Vault, docs []*search.Doc, sel Selection, opts Options) *Session {
	t.Helper()
	s, err := Prepare(context.Background(), v, docs, sel, opts)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func call() ai.Call {
	return ai.Call{Task: "digest", Profile: config.Profile{Timeout: time.Second}}
}

func para(text string, ids ...string) ai.AskParagraph {
	return ai.AskParagraph{Text: text, SourceIDs: ids}
}

func replying(calls *atomic.Int32, points ...ai.AskParagraph) Model {
	return func(context.Context, ai.Approval, ai.Request) (ai.Result, error) {
		calls.Add(1)
		return ai.Result{Digest: &ai.DigestReply{Points: points}}, nil
	}
}

func run(t *testing.T, s *Session, points ...ai.AskParagraph) *Digest {
	t.Helper()
	var calls atomic.Int32
	d, err := s.Run(context.Background(), ai.Approval{}, call(), "prompt", replying(&calls, points...))
	if err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 1 {
		t.Fatalf("model calls = %d", calls.Load())
	}
	return d
}

func sourcePaths(s *Session) []string {
	var paths []string
	for _, src := range s.sources {
		paths = append(paths, src.Path)
	}
	return paths
}

func decode(t *testing.T, s *Session) request {
	t.Helper()
	var req request
	if err := json.Unmarshal([]byte(s.Request("p").Text), &req); err != nil {
		t.Fatal(err)
	}
	return req
}

func TestRequestIsWhatRunSends(t *testing.T) {
	v := writeVault(t, map[string]string{
		"nn/a.md": md("2026-09-18", "go", "# Alpha", "Alpha body <b>."),
		"nn/b.md": md("2026-09-17", "", "Beta body."),
	})
	s := mustPrepare(t, v, load(t, v), Selection{Since: day(17)}, options())
	var sent ai.Request
	model := func(_ context.Context, _ ai.Approval, req ai.Request) (ai.Result, error) {
		sent = req
		return ai.Result{Digest: &ai.DigestReply{Points: []ai.AskParagraph{para("x", "S1")}}}, nil
	}
	if _, err := s.Run(context.Background(), ai.Approval{}, call(), "the prompt", model); err != nil {
		t.Fatal(err)
	}
	if want := s.Request("the prompt"); sent.System != want.System || sent.Text != want.Text || sent.Image != nil {
		t.Fatalf("sent %+v, Request %+v", sent, want)
	}
	want := `{"selection":"since 2026-09-17","sources":[` +
		`{"id":"S1","path":"nn/b.md","title":"b","date":"2026-09-17","tags":[],"excerpt":"Beta body.","truncated":false},` +
		`{"id":"S2","path":"nn/a.md","title":"Alpha","date":"2026-09-18","tags":["go"],"excerpt":"# Alpha\nAlpha body \u003cb\u003e.","truncated":false}` +
		`],"omitted_notes":0}`
	if sent.Text != want {
		t.Fatalf("request\n got %s\nwant %s", sent.Text, want)
	}
	chars := len("nn/b.md") + 1 + 10 + len("Beta body.") + len("nn/a.md") + len("Alpha") + 10 + 2 + len("# Alpha\nAlpha body <b>.")
	if got := s.ContextDescription(); got != "the selection and the paths, titles, dates, tags and excerpts of 2 selected notes ("+itoa(chars)+" characters)" {
		t.Fatalf("description = %q", got)
	}
}

func itoa(n int) string {
	data, _ := json.Marshal(n)
	return string(data)
}

func TestRunContract(t *testing.T) {
	v := writeVault(t, map[string]string{
		"nn/a.md": md("2026-09-17", "", "Alpha."),
		"nn/b.md": md("2026-09-18", "", "Beta."),
		"nn/c.md": md("2026-09-19", "", "Gamma."),
	})
	docs := load(t, v)
	opts := options()
	opts.Notes = 2 // nn/c.md and nn/b.md are sent as S2 and S1; nn/a.md is not
	prepare := func() *Session { return mustPrepare(t, v, docs, Selection{Since: day(1)}, opts) }

	for name, c := range map[string]ai.Call{
		"wrong task": {Task: "ask", Profile: config.Profile{Timeout: time.Second}},
		"no timeout": {Task: "digest"},
	} {
		var calls atomic.Int32
		if _, err := prepare().Run(context.Background(), ai.Approval{}, c, "p", replying(&calls, para("x", "S1"))); err == nil || calls.Load() != 0 {
			t.Errorf("%s: err=%v calls=%d", name, err, calls.Load())
		}
	}

	longText := strings.Repeat("x", 2001)
	var many []ai.AskParagraph
	for range 33 {
		many = append(many, para("x", "S1"))
	}
	var heavy []ai.AskParagraph
	for range 9 {
		heavy = append(heavy, para(strings.Repeat("y", 1800), "S1"))
	}
	bad := map[string]ai.Result{
		"nil reply":         {},
		"no points":         {Digest: &ai.DigestReply{}},
		"too many points":   {Digest: &ai.DigestReply{Points: many}},
		"empty text":        {Digest: &ai.DigestReply{Points: []ai.AskParagraph{para("", "S1")}}},
		"invisible text":    {Digest: &ai.DigestReply{Points: []ai.AskParagraph{para(zeroWidthSpace+rightToLeft+" \n", "S1")}}},
		"long text":         {Digest: &ai.DigestReply{Points: []ai.AskParagraph{para(longText, "S1")}}},
		"combined too long": {Digest: &ai.DigestReply{Points: heavy}},
		"invalid utf8":      {Digest: &ai.DigestReply{Points: []ai.AskParagraph{para("a\xff", "S1")}}},
		"no ids":            {Digest: &ai.DigestReply{Points: []ai.AskParagraph{para("x")}}},
		"duplicate id":      {Digest: &ai.DigestReply{Points: []ai.AskParagraph{para("x", "S1", "S1")}}},
		"unknown id":        {Digest: &ai.DigestReply{Points: []ai.AskParagraph{para("x", "S1", "N1")}}},
		"unsent id":         {Digest: &ai.DigestReply{Points: []ai.AskParagraph{para("x", "S3")}}},
		"path as id":        {Digest: &ai.DigestReply{Points: []ai.AskParagraph{para("x", "nn/a.md")}}},
		"padded id":         {Digest: &ai.DigestReply{Points: []ai.AskParagraph{para("x", " S1")}}},
	}
	for name, result := range bad {
		var calls atomic.Int32
		model := func(context.Context, ai.Approval, ai.Request) (ai.Result, error) {
			calls.Add(1)
			return result, nil
		}
		d, err := prepare().Run(context.Background(), ai.Approval{}, call(), "p", model)
		if err == nil || d != nil || calls.Load() != 1 {
			t.Errorf("%s: digest=%v err=%v calls=%d", name, d, err, calls.Load())
		}
	}

	d := run(t, prepare(), para("First line\nsecond"+softHyphen+"line", "S2", "S1"))
	if !strings.Contains(d.Text(), "\n- First line secondline [1] [2]\n") {
		t.Fatalf("text:\n%s", d.Text())
	}

	s := prepare()
	run(t, s, para("x", "S1"))
	if _, err := s.Run(context.Background(), ai.Approval{}, call(), "p", replying(new(atomic.Int32), para("x", "S1"))); err == nil {
		t.Fatal("session reused")
	}

	engineErr := errors.New("engine failed")
	if _, err := prepare().Run(context.Background(), ai.Approval{}, call(), "p", func(context.Context, ai.Approval, ai.Request) (ai.Result, error) {
		return ai.Result{}, engineErr
	}); !errors.Is(err, engineErr) {
		t.Fatalf("engine error = %v", err)
	}

	blocking := func(ctx context.Context, _ ai.Approval, _ ai.Request) (ai.Result, error) {
		<-ctx.Done()
		return ai.Result{}, ctx.Err()
	}
	short := call()
	short.Profile.Timeout = 20 * time.Millisecond
	if _, err := prepare().Run(context.Background(), ai.Approval{}, short, "p", blocking); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("timeout = %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(10 * time.Millisecond)
		cancel()
	}()
	if _, err := prepare().Run(ctx, ai.Approval{}, call(), "p", blocking); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel = %v", err)
	}
	// A reply that arrives after cancellation is discarded.
	ctx, cancel = context.WithCancel(context.Background())
	late := func(context.Context, ai.Approval, ai.Request) (ai.Result, error) {
		cancel()
		return ai.Result{Digest: &ai.DigestReply{Points: []ai.AskParagraph{para("x", "S1")}}}, nil
	}
	if d, err := prepare().Run(ctx, ai.Approval{}, call(), "p", late); !errors.Is(err, context.Canceled) || d != nil {
		t.Fatalf("late reply: %v %v", d, err)
	}
}

func TestPrepareRejectsInvalidInput(t *testing.T) {
	v := writeVault(t, map[string]string{"nn/a.md": md("2026-09-17", "", "Alpha.")})
	docs := load(t, v)
	long := strings.Repeat("t", 65)
	var tags []string
	for range 17 {
		tags = append(tags, "t")
	}
	for name, tc := range map[string]struct {
		sel  Selection
		opts func(*Options)
	}{
		"no selector":      {sel: Selection{}},
		"long topic":       {sel: Selection{Topic: strings.Repeat("w", 201)}},
		"too many tags":    {sel: Selection{Tags: tags}},
		"long tag":         {sel: Selection{Tags: []string{long}}},
		"empty tag":        {sel: Selection{Tags: []string{" "}}},
		"until before":     {sel: Selection{Since: day(20), Until: day(19)}},
		"topic and list":   {sel: Selection{Topic: "x", Paths: []string{"nn/a.md"}}},
		"no notes":         {sel: Selection{Inbox: true}, opts: func(o *Options) { o.Notes = 0 }},
		"too many notes":   {sel: Selection{Inbox: true}, opts: func(o *Options) { o.Notes = MaxNotes + 1 }},
		"too few chars":    {sel: Selection{Inbox: true}, opts: func(o *Options) { o.Chars = MinChars - 1 }},
		"too many chars":   {sel: Selection{Inbox: true}, opts: func(o *Options) { o.Chars = MaxChars + 1 }},
		"topic of 200 ok?": {sel: Selection{Topic: strings.Repeat("w", 200)}, opts: nil},
	} {
		opts := options()
		if tc.opts != nil {
			tc.opts(&opts)
		}
		_, err := Prepare(context.Background(), v, docs, tc.sel, opts)
		if name == "topic of 200 ok?" {
			if err != nil {
				t.Errorf("%s: %v", name, err)
			}
			continue
		}
		if err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if _, err := Prepare(context.Background(), nil, docs, Selection{Inbox: true}, options()); err == nil {
		t.Fatal("nil vault accepted")
	}
}
