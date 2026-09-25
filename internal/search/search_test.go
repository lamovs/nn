package search

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/lamovs/nn/internal/ocr"
)

var testNow = time.Date(2026, 9, 16, 12, 0, 0, 0, time.UTC)

func doc(path, title string, lines ...string) *Doc {
	d := &Doc{Path: path, Title: title}
	for i, l := range lines {
		d.Lines = append(d.Lines, Line{Num: i + 1, Text: l})
	}
	return d
}

func withImage(d *Doc, path string, lines ...string) *Doc {
	img := ImageDoc{Path: path}
	for i, l := range lines {
		img.Lines = append(img.Lines, ocr.Line{Text: l, Box: ocr.Box{X: 0.1, Y: float64(i) / 10, W: 0.5, H: 0.05}})
	}
	d.Images = append(d.Images, img)
	return d
}

func run(t *testing.T, docs []*Doc, q Query, r Ranker) []Result {
	t.Helper()
	res, err := Search(context.Background(), docs, q, r, testNow)
	if err != nil {
		t.Fatalf("Search(%+v): %v", q, err)
	}
	return res
}

func paths(res []Result) []string {
	var out []string
	for _, r := range res {
		out = append(out, r.Doc.Path)
	}
	return out
}

func highlighted(h Hit) []string {
	var out []string
	for _, r := range h.Ranges {
		out = append(out, h.Text[r[0]:r[1]])
	}
	return out
}

type frecency map[string]float64

func (f frecency) Frecency(path string) float64 { return f[path] }

func TestFoldingCaseAndYo(t *testing.T) {
	docs := []*Doc{
		doc("a.md", "", "Новая Ёлка во дворе"),
		doc("b.md", "", "елка без буквы"),
		doc("c.md", "", "ДОКЕР и Straße"),
	}
	tests := []struct {
		q    string
		want []string
	}{
		{"елка", []string{"a.md", "b.md"}},
		{"ёлка", []string{"a.md", "b.md"}},
		{"ЁЛКА", []string{"a.md", "b.md"}},
		{"докер", []string{"c.md"}},
		{"STRASSE", nil},
		{"straße", []string{"c.md"}},
		{"STRAẞE", []string{"c.md"}},
	}
	for _, tt := range tests {
		got := paths(run(t, docs, Query{Text: tt.q}, nil))
		slices.Sort(got)
		if !slices.Equal(got, tt.want) {
			t.Errorf("%q matched %v, want %v", tt.q, got, tt.want)
		}
	}
}

func TestDecomposedYoAndShortI(t *testing.T) {
	yo := "е" + string(rune(0x308)) + "лка"
	shortI := "и" + string(rune(0x306)) + "од"
	docs := []*Doc{doc("a.md", "", "моя "+yo+" тут"), doc("b.md", "", shortI)}
	res := run(t, docs, Query{Text: "Ёлка"}, nil)
	if len(res) != 1 || res[0].Doc.Path != "a.md" {
		t.Fatalf("decomposed yo: got %v", paths(res))
	}
	if got := highlighted(res[0].Hits[0]); !slices.Equal(got, []string{yo}) {
		t.Errorf("ranges cover %q, want %q", got, yo)
	}
	if got := paths(run(t, docs, Query{Text: "йод"}, nil)); !slices.Equal(got, []string{"b.md"}) {
		t.Errorf("decomposed short i: got %v", got)
	}
	if got := paths(run(t, docs, Query{Text: "иод"}, nil)); len(got) != 0 {
		t.Errorf("short i must not match plain i: got %v", got)
	}
}

func TestRangesCyrillic(t *testing.T) {
	docs := []*Doc{doc("a.md", "", "Как почистить Docker-кэш: docker system prune", "Ёжик   в\tтумане")}
	res := run(t, docs, Query{Text: "ДОКЕР docker"}, nil)
	if len(res) != 0 {
		t.Fatalf("unexpected match: %v", paths(res))
	}

	res = run(t, docs, Query{Text: "docker"}, nil)
	if len(res) != 1 {
		t.Fatalf("got %d results", len(res))
	}
	h := res[0].Hits[0]
	if got := highlighted(h); !slices.Equal(got, []string{"Docker", "docker"}) {
		t.Errorf("ranges cover %q", got)
	}

	res = run(t, docs, Query{Text: "кэш"}, nil)
	if got := highlighted(res[0].Hits[0]); !slices.Equal(got, []string{"кэш"}) {
		t.Errorf("ranges cover %q", got)
	}

	res = run(t, docs, Query{Text: "ежик в тумане"}, nil)
	if len(res) != 1 {
		t.Fatalf("phrase across whitespace: got %d results", len(res))
	}
	h = res[0].Hits[0]
	if h.Kind != KindPhrase || h.Line != 2 {
		t.Errorf("hit = %+v, want phrase on line 2", h)
	}
	if got := highlighted(h); !slices.Equal(got, []string{"Ёжик   в\tтумане"}) {
		t.Errorf("ranges cover %q", got)
	}
}

func TestMultiWordNeedsEveryWord(t *testing.T) {
	docs := []*Doc{
		doc("both-line.md", "", "intro", "docker prune everything", "just docker"),
		doc("split.md", "", "docker here", "prune there"),
		doc("title.md", "Docker", "prune it"),
		doc("one.md", "", "docker only"),
	}
	res := run(t, docs, Query{Text: "docker prune"}, nil)
	got := paths(res)
	slices.Sort(got)
	if want := []string{"both-line.md", "split.md", "title.md"}; !slices.Equal(got, want) {
		t.Fatalf("matched %v, want %v", got, want)
	}
	byPath := map[string]Result{}
	for _, r := range res {
		byPath[r.Doc.Path] = r
	}

	both := byPath["both-line.md"]
	if len(both.Hits) != 1 || both.Hits[0].Line != 2 || both.Hits[0].Kind != KindPhrase {
		t.Errorf("both-line hits = %+v, want only the phrase on line 2", both.Hits)
	}
	split := byPath["split.md"]
	if len(split.Hits) != 2 || split.Hits[0].Kind != KindBody || split.Hits[1].Kind != KindBody {
		t.Errorf("split hits = %+v, want two body hits", split.Hits)
	}
	title := byPath["title.md"]
	if len(title.Hits) != 2 || title.Hits[0].Kind != KindTitle || title.Hits[0].Line != 0 || title.Hits[1].Line != 1 {
		t.Errorf("title hits = %+v, want title then line 1", title.Hits)
	}
	if paths(res)[0] != "both-line.md" {
		t.Errorf("order = %v, want the phrase first", paths(res))
	}
}

func TestRankingKinds(t *testing.T) {
	docs := []*Doc{
		doc("a-terms.md", "", "prune the docker"),
		doc("z-phrase.md", "", "run docker prune now"),
	}
	if got := paths(run(t, docs, Query{Text: "docker prune"}, nil)); !slices.Equal(got, []string{"z-phrase.md", "a-terms.md"}) {
		t.Errorf("phrase vs terms order = %v", got)
	}

	docs = []*Doc{
		doc("a-ocr.md", "", "nothing"),
		doc("b-body.md", "", "about docker"),
		doc("c-tag.md", ""),
		doc("d-alias.md", "Something"),
		doc("e-title.md", "Docker notes"),
	}
	withImage(docs[0], "img.png", "docker ps")
	docs[2].Tags = []string{"docker"}
	docs[3].Aliases = []string{"Docker cheat sheet"}
	want := []string{"e-title.md", "d-alias.md", "c-tag.md", "b-body.md", "a-ocr.md"}
	if got := paths(run(t, docs, Query{Text: "docker"}, nil)); !slices.Equal(got, want) {
		t.Errorf("kind order = %v, want %v", got, want)
	}
}

func TestRankingFrecencyAndHere(t *testing.T) {
	t.Setenv("HOME", "/home/u")
	mk := func() []*Doc {
		return []*Doc{doc("a.md", "", "docker"), doc("b.md", "", "docker"), doc("c.md", "", "docker")}
	}

	if got := paths(run(t, mk(), Query{Text: "docker"}, nil)); !slices.Equal(got, []string{"a.md", "b.md", "c.md"}) {
		t.Errorf("tie order = %v, want by path", got)
	}
	if got := paths(run(t, mk(), Query{Text: "docker"}, frecency{"c.md": 1})); got[0] != "c.md" {
		t.Errorf("frecency order = %v, want c.md first", got)
	}

	docs := mk()
	docs[2].Repo = "tt"
	if got := paths(run(t, docs, Query{Text: "docker", Near: &Here{Repo: "tt"}}, nil)); got[0] != "c.md" || len(got) != 3 {
		t.Errorf("near repo order = %v, want c.md first of 3", got)
	}

	docs = mk()
	docs[1].Where = "~/projects/tt/cmd"
	docs[2].Where = "~/elsewhere"
	got := paths(run(t, docs, Query{Text: "docker", Near: &Here{Cwd: "/home/u/projects/tt"}}, nil))
	if !slices.Equal(got, []string{"b.md", "a.md", "c.md"}) {
		t.Errorf("near cwd order = %v, want b.md first", got)
	}

	got = paths(run(t, docs, Query{Text: "docker", Here: &Here{Cwd: "~/projects"}}, nil))
	if !slices.Equal(got, []string{"b.md"}) {
		t.Errorf("here filter = %v, want only b.md", got)
	}
}

func TestRankingInboxAndFreshness(t *testing.T) {
	docs := []*Doc{doc("a.md", "", "docker"), doc("b.md", "", "docker")}
	docs[1].InInbox = true
	if got := paths(run(t, docs, Query{Text: "docker"}, nil)); got[0] != "b.md" {
		t.Errorf("inbox order = %v", got)
	}
	docs = []*Doc{doc("a.md", "", "docker"), doc("b.md", "", "docker")}
	docs[0].Modified = testNow.AddDate(-1, 0, 0)
	docs[1].Modified = testNow.Add(-time.Hour)
	if got := paths(run(t, docs, Query{Text: "docker"}, nil)); got[0] != "b.md" {
		t.Errorf("freshness order = %v", got)
	}
}

func TestFilters(t *testing.T) {
	day := func(n int) time.Time { return testNow.AddDate(0, 0, -n) }
	docs := []*Doc{
		{Path: "old-date.md", Date: day(30), Modified: day(1), Tags: []string{"Docker"}, Lines: []Line{{1, "docker"}}},
		{Path: "new-mod.md", Modified: day(1), Tags: []string{"docker", "go/generics"}, InInbox: true, Lines: []Line{{1, "docker"}}},
		{Path: "new-date.md", Date: day(2), Tags: []string{"#GO"}, Lines: []Line{{1, "docker"}}},
		{Path: "undated.md", Lines: []Line{{1, "docker"}}},
	}
	tests := []struct {
		name string
		q    Query
		want []string
	}{
		{"since uses date first", Query{Text: "docker", Since: day(7)}, []string{"new-date.md", "new-mod.md"}},
		{"tags and", Query{Text: "docker", Tags: []string{"DOCKER", "go"}}, []string{"new-mod.md"}},
		{"tag case", Query{Text: "docker", Tags: []string{"docker"}}, []string{"new-mod.md", "old-date.md"}},
		{"tag hash and nesting", Query{Text: "docker", Tags: []string{"#go"}}, []string{"new-date.md", "new-mod.md"}},
		{"nested tag is not a prefix match", Query{Text: "docker", Tags: []string{"gen"}}, nil},
		{"inbox", Query{Text: "docker", Inbox: true}, []string{"new-mod.md"}},
		{"empty text filters only", Query{Tags: []string{"go"}}, []string{"new-date.md", "new-mod.md"}},
		{"limit", Query{Text: "docker", Limit: 2}, []string{"new-date.md", "new-mod.md"}},
	}
	for _, tt := range tests {
		got := paths(run(t, docs, tt.q, nil))
		if tt.q.Limit == 0 {
			slices.Sort(got)
		}
		if len(got) != len(tt.want) || (tt.q.Limit == 0 && !slices.Equal(got, tt.want)) {
			t.Errorf("%s: got %v, want %v", tt.name, got, tt.want)
		}
	}
}

func TestImagesOnly(t *testing.T) {
	docs := []*Doc{
		withImage(doc("shot.md", "Docker", "docker in the body"), "nn/assets/shot.png", "header", "docker ps -a"),
		doc("text.md", "", "docker"),
		{Path: "nn/assets/lonely.png", Images: []ImageDoc{{Path: "nn/assets/lonely.png"}}},
	}
	res := run(t, docs, Query{Text: "docker", ImagesOnly: true}, nil)
	if got := paths(res); !slices.Equal(got, []string{"shot.md"}) {
		t.Fatalf("got %v", got)
	}
	hits := res[0].Hits
	if len(hits) != 1 {
		t.Fatalf("hits = %+v, want only the OCR line", hits)
	}
	h := hits[0]
	if h.Kind != KindOCR || h.Image != "nn/assets/shot.png" || h.Line != 0 || h.Box == nil || h.Box.Y != 0.1 {
		t.Errorf("hit = %+v", h)
	}
	if got := highlighted(h); !slices.Equal(got, []string{"docker"}) {
		t.Errorf("ranges cover %q", got)
	}
	if got := paths(run(t, docs, Query{ImagesOnly: true}, nil)); !slices.Equal(got, []string{"nn/assets/lonely.png", "shot.md"}) {
		t.Errorf("images without text query: %v", got)
	}
}

func TestRegexAndCase(t *testing.T) {
	docs := []*Doc{doc("a.md", "", "Docker Compose"), doc("b.md", "", "dockerfile tips")}
	if got := paths(run(t, docs, Query{Text: `dock\w+`, Regex: true}, nil)); len(got) != 2 {
		t.Errorf("regex matched %v", got)
	}
	res := run(t, docs, Query{Text: `^Dock\w+`, Regex: true, CaseSensitive: true}, nil)
	if got := paths(res); !slices.Equal(got, []string{"a.md"}) {
		t.Fatalf("case-sensitive regex matched %v", got)
	}
	if got := highlighted(res[0].Hits[0]); !slices.Equal(got, []string{"Docker"}) {
		t.Errorf("regex ranges cover %q", got)
	}
	if got := paths(run(t, docs, Query{Text: "Docker", CaseSensitive: true}, nil)); !slices.Equal(got, []string{"a.md"}) {
		t.Errorf("case-sensitive text matched %v", got)
	}
	if _, err := Search(context.Background(), docs, Query{Text: "(", Regex: true}, nil, testNow); err == nil {
		t.Error("invalid regex: want error")
	}
}

func TestChordSearch(t *testing.T) {
	docs := []*Doc{
		doc("a.md", "", "Screenshot of an area: Cmd+Shift+4"),
		withImage(doc("b.md", ""), "hotkeys.png", g("{cmd}{shift}4  Capture area")),
		doc("c.md", "", "Cmd+Shift+5 records the screen", "cmd shift 4 later"),
		doc("d.md", "", "shift 4 cmd"),
	}
	for _, q := range []string{"cmd shift 4", "Command-Shift-4", g("{shift}{cmd}4")} {
		res := run(t, docs, Query{Text: q}, nil)
		got := paths(res)
		slices.Sort(got)
		if !slices.Equal(got, []string{"a.md", "b.md", "c.md"}) {
			t.Errorf("%q matched %v", q, got)
			continue
		}
		for _, r := range res {
			h := r.Hits[0]
			if h.Kind != KindChord || len(h.Ranges) != 1 {
				t.Errorf("%q: %s hit = %+v", q, r.Doc.Path, h)
			}
		}
	}
	res := run(t, docs, Query{Text: "cmd shift 4"}, nil)
	for _, r := range res {
		want := map[string]string{"a.md": "Cmd+Shift+4", "b.md": g("{cmd}{shift}4"), "c.md": "cmd shift 4"}[r.Doc.Path]
		if got := highlighted(r.Hits[0]); !slices.Equal(got, []string{want}) {
			t.Errorf("%s ranges cover %q, want %q", r.Doc.Path, got, want)
		}
	}
	if b := res[slices.IndexFunc(res, func(r Result) bool { return r.Doc.Path == "b.md" })]; b.Hits[0].Image != "hotkeys.png" {
		t.Errorf("OCR chord hit = %+v", b.Hits[0])
	}
}

func TestLayoutFallback(t *testing.T) {
	docs := []*Doc{doc("a.md", "", "docker ps"), doc("b.md", "", "привет мир")}
	res := run(t, docs, Query{Text: "вщслук"}, nil)
	if got := paths(res); !slices.Equal(got, []string{"a.md"}) || !res[0].Layout {
		t.Errorf("вщслук: %v layout=%v", got, len(res) > 0 && res[0].Layout)
	}
	res = run(t, docs, Query{Text: "ghbdtn"}, nil)
	if got := paths(res); !slices.Equal(got, []string{"b.md"}) || !res[0].Layout {
		t.Errorf("ghbdtn: %v", got)
	}
	res = run(t, docs, Query{Text: "docker"}, nil)
	if len(res) != 1 || res[0].Layout {
		t.Errorf("direct match must not set Layout: %+v", res)
	}
	if res := run(t, docs, Query{Text: "вщслук", Regex: true}, nil); len(res) != 0 {
		t.Errorf("regex must not fall back: %v", paths(res))
	}
	if res := run(t, docs, Query{Text: "nothing"}, nil); len(res) != 0 {
		t.Errorf("got %v", paths(res))
	}
}

func TestNoLayoutFallback(t *testing.T) {
	docs := []*Doc{doc("a.md", "", "docker ps")}
	if res := run(t, docs, Query{Text: "вщслук", NoLayoutFallback: true}, nil); len(res) != 0 {
		t.Errorf("NoLayoutFallback still retried: %v", paths(res))
	}
	if res := run(t, docs, Query{Text: "вщслук"}, nil); len(res) == 0 || !res[0].Layout {
		t.Errorf("the default (fallback on) stopped working: %v", res)
	}
}

func TestAliasEqualToTitleIsNotRepeated(t *testing.T) {
	d := doc("a.md", "Docker cleanup")
	d.Aliases = []string{"Docker cleanup", "prune"}
	res := run(t, []*Doc{d}, Query{Text: "docker"}, nil)
	if len(res) != 1 || len(res[0].Hits) != 1 || res[0].Hits[0].Kind != KindTitle {
		t.Errorf("hits = %+v", res)
	}
}

func TestSearchDeterministic(t *testing.T) {
	var docs []*Doc
	for i := range 2000 {
		// Paths in scrambled order, identical content: only Path breaks ties.
		docs = append(docs, doc(fmt.Sprintf("n/%04d.md", (i*7919)%2000), "", "docker compose up"))
	}
	first := paths(run(t, docs, Query{Text: "docker"}, nil))
	if !slices.IsSorted(first) || len(first) != 2000 {
		t.Fatalf("results not ordered by path (%d results)", len(first))
	}
	for range 5 {
		if again := paths(run(t, docs, Query{Text: "docker"}, nil)); !slices.Equal(again, first) {
			t.Fatal("results differ between runs")
		}
	}
}

func TestSearchCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := Search(ctx, []*Doc{doc("a.md", "", "docker")}, Query{Text: "docker"}, nil, testNow)
	if !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want context.Canceled", err)
	}
}

func feed(docs []*Doc) <-chan *Doc {
	ch := make(chan *Doc, len(docs))
	for _, d := range docs {
		ch <- d
	}
	close(ch)
	return ch
}

func TestStreamEmitsInArrivalOrder(t *testing.T) {
	docs := []*Doc{doc("z.md", "", "docker"), doc("m.md", "", "nope"), doc("a.md", "", "docker")}
	var got []string
	err := Stream(context.Background(), feed(docs), Query{Text: "docker"}, func(r Result) bool {
		got = append(got, r.Doc.Path)
		return true
	})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(got, []string{"z.md", "a.md"}) {
		t.Errorf("emitted %v", got)
	}
}

func TestStreamStops(t *testing.T) {
	docs := []*Doc{doc("a.md", "", "docker"), doc("b.md", "", "docker"), doc("c.md", "", "docker")}
	n := 0
	err := Stream(context.Background(), feed(docs), Query{Text: "docker"}, func(Result) bool {
		n++
		return false
	})
	if err != nil || n != 1 {
		t.Errorf("emit=false: n=%d err=%v", n, err)
	}
	n = 0
	err = Stream(context.Background(), feed(docs), Query{Text: "docker", Limit: 2}, func(Result) bool {
		n++
		return true
	})
	if err != nil || n != 2 {
		t.Errorf("limit: n=%d err=%v", n, err)
	}
}

func TestStreamCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch := make(chan *Doc)
	done := make(chan error)
	var emitted []string
	go func() {
		done <- Stream(ctx, ch, Query{Text: "docker"}, func(r Result) bool {
			emitted = append(emitted, r.Doc.Path)
			return true
		})
	}()
	ch <- doc("a.md", "", "docker")
	ch <- doc("b.md", "", "docker")
	// The producer stalls; cancelling must unblock Stream.
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("err = %v, want context.Canceled", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Stream did not return after cancel")
	}
	if len(emitted) < 1 {
		t.Errorf("emitted %v before cancel, want the delivered docs", emitted)
	}
}

func TestStreamLayoutFallback(t *testing.T) {
	collect := func(docs []*Doc, text string) []Result {
		var out []Result
		if err := Stream(context.Background(), feed(docs), Query{Text: text}, func(r Result) bool {
			out = append(out, r)
			return true
		}); err != nil {
			t.Fatal(err)
		}
		return out
	}
	res := collect([]*Doc{doc("a.md", "", "docker"), doc("b.md", "", "none")}, "вщслук")
	if len(res) != 1 || res[0].Doc.Path != "a.md" || !res[0].Layout {
		t.Errorf("fallback results = %+v", res)
	}
	// A direct match later in the stream wins over held-back layout matches.
	res = collect([]*Doc{doc("a.md", "", "docker"), doc("b.md", "", "вщслук")}, "вщслук")
	if len(res) != 1 || res[0].Doc.Path != "b.md" || res[0].Layout {
		t.Errorf("direct results = %+v", res)
	}
}

func TestViaIsNotAFilter(t *testing.T) {
	docs := []*Doc{
		{Path: "digest.md", Via: "digest", Lines: []Line{{1, "docker"}}},
		{Path: "plain.md", Lines: []Line{{1, "docker"}}},
	}
	for _, q := range []Query{{Text: "docker"}, {}, {Text: "digest"}} {
		got := paths(run(t, docs, q, nil))
		slices.Sort(got)
		want := []string{"digest.md", "plain.md"}
		if q.Text == "digest" {
			want = nil
		}
		if !slices.Equal(got, want) {
			t.Errorf("%+v: got %v, want %v", q, got, want)
		}
	}
}
