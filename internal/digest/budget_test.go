package digest

import (
	"context"
	"slices"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/lamovs/nn/internal/ai"
	"github.com/lamovs/nn/internal/ocr"
	"github.com/lamovs/nn/internal/search"
	"github.com/lamovs/nn/internal/vault"
)

const token = "ghp_" + "abcdefghijklmnopqrstuvwxyz0123456789ABCD"

func synthetic(path string, date int, body string) *search.Doc {
	doc := &search.Doc{Path: path, Title: strings.TrimSuffix(path, ".md"), Date: day(date)}
	for i, line := range strings.Split(body, "\n") {
		doc.Lines = append(doc.Lines, search.Line{Num: i + 1, Text: line})
	}
	return doc
}

func emptyVault(t *testing.T) *vault.Vault {
	return &vault.Vault{Root: t.TempDir(), Inbox: "nn"}
}

func TestSecretGate(t *testing.T) {
	v := emptyVault(t)
	docs := []*search.Doc{
		synthetic("a.md", 20, "clean A"),
		synthetic("b.md", 19, "key: "+token),
		synthetic("c.md", 18, "clean C"),
		synthetic("d.md", 17, "clean D"),
	}
	docs[3].Title = "title " + token
	opts := options()
	opts.Notes = 2
	s := mustPrepare(t, v, docs, Selection{Since: day(1)}, opts)
	if got := sourcePaths(s); !slices.Equal(got, []string{"c.md", "a.md"}) {
		t.Fatalf("sources: %v", got)
	}
	if r := s.Report(); r.Matched != 4 || r.Included != 2 || r.SkippedSecret != 1 || r.BeyondLimit != 1 {
		t.Fatalf("report: %+v", r)
	}
	if strings.Contains(s.Request("p").Text, token) || strings.Contains(s.Request("p").Text, "b.md") {
		t.Fatal("secret-looking note sent")
	}
	if req := decode(t, s); req.OmittedNotes != 2 {
		t.Fatalf("omitted_notes = %d", req.OmittedNotes)
	}
	if want := len("c.md") + 1 + 10 + 7 + len("a.md") + 1 + 10 + 7; s.chars != want {
		t.Fatalf("chars = %d, want %d", s.chars, want)
	}
	d := run(t, s, para("x", "S1"))
	if !strings.Contains(d.Text(), "\nNot included: 1 matching note beyond the limit of 2 notes / 12000 characters; 1 note that may contain credentials (--allow-secret includes them).\n") {
		t.Fatalf("disclosure:\n%s", d.Text())
	}

	opts.AllowSecret = true
	s = mustPrepare(t, v, docs, Selection{Since: day(1)}, opts)
	if got := sourcePaths(s); !slices.Equal(got, []string{"b.md", "a.md"}) || s.Report().SkippedSecret != 0 || !strings.Contains(s.Request("p").Text, token) {
		t.Fatalf("allow secret: %v %+v", got, s.Report())
	}

	tagged := synthetic("t.md", 20, "clean")
	tagged.Tags = []string{"x", token}
	if s := mustPrepare(t, v, []*search.Doc{tagged, synthetic("u.md", 19, "clean")}, Selection{Since: day(1)}, options()); s.Report().SkippedSecret != 1 {
		t.Fatalf("tag secret: %+v", s.Report())
	}

	// The path is scanned too, but its value is never printed.
	pathHit := synthetic("nn/"+token+".md", 20, "clean content")
	pathHit.Title = "clean"
	s = mustPrepare(t, v, []*search.Doc{pathHit, synthetic("u.md", 19, "clean")}, Selection{Since: day(1)}, options())
	if r := s.Report(); r.SkippedSecret != 1 || r.Included != 1 || r.BeyondLimit != 0 || strings.Contains(s.Request("p").Text, token) {
		t.Fatalf("path secret: %+v", r)
	}
	_, err := Prepare(context.Background(), v, []*search.Doc{pathHit}, Selection{Since: day(1)}, options())
	if err == nil || err.Error() != "no matching note can be sent: 1 may contain credentials (review them and use --allow-secret)" || strings.Contains(err.Error(), "ghp_") {
		t.Fatalf("path secret only: %v", err)
	}

	_, err = Prepare(context.Background(), v, docs[1:2], Selection{Since: day(1)}, options())
	if err == nil || err.Error() != "no matching note can be sent: 1 may contain credentials (review them and use --allow-secret)" {
		t.Fatalf("secret only: %v", err)
	}
	huge := synthetic(strings.Repeat("p", 400)+".md", 10, "body")
	huge.Title = strings.Repeat("t", 300)
	for range 16 {
		huge.Tags = append(huge.Tags, strings.Repeat("g", 80))
	}
	opts = options()
	opts.Chars = MinChars
	_, err = Prepare(context.Background(), v, []*search.Doc{docs[1], huge, docs[3]}, Selection{Since: day(1)}, opts)
	if err == nil || err.Error() != "no matching note can be sent: 2 may contain credentials (review them and use --allow-secret), 1 does not fit --chars" {
		t.Fatalf("secret and large: %v", err)
	}
	if strings.Contains(err.Error(), token) || strings.Contains(err.Error(), "ghp_") {
		t.Fatal("secret value in the error")
	}
	_, err = Prepare(context.Background(), v, []*search.Doc{huge}, Selection{Since: day(1)}, opts)
	if err == nil || err.Error() != "context too small for any matching note; raise --chars" {
		t.Fatalf("too large: %v", err)
	}
	s = mustPrepare(t, v, []*search.Doc{huge, synthetic("small.md", 1, "small")}, Selection{Since: day(1)}, opts)
	if got := sourcePaths(s); !slices.Equal(got, []string{"small.md"}) || s.Report().BeyondLimit != 1 {
		t.Fatalf("skip too large: %v %+v", got, s.Report())
	}
}

func TestBudget(t *testing.T) {
	v := emptyVault(t)
	long := func(n int) string { return strings.Repeat("w", n) }
	opts := options()
	opts.Chars = MinChars

	var docs []*search.Doc
	for i := range 5 {
		doc := synthetic(string(rune('a'+i))+".md", 20-i, long(1000))
		doc.Title = long(240)
		docs = append(docs, doc)
	}
	s := mustPrepare(t, v, docs, Selection{Since: day(1)}, opts)
	// (4 + 240 + 10) + 200 = 454 per note: two fit in 1000, three do not.
	if got := sourcePaths(s); !slices.Equal(got, []string{"b.md", "a.md"}) {
		t.Fatalf("drop: %v", got)
	}
	if r := s.Report(); r.Included != 2 || r.BeyondLimit != 3 || r.Truncated != 2 {
		t.Fatalf("report: %+v", r)
	}
	for _, src := range s.sources {
		if n := utf8.RuneCountInString(src.Excerpt); n < 200 || !src.Truncated {
			t.Fatalf("floor: %d runes, truncated %v", n, src.Truncated)
		}
	}
	if s.chars > MinChars {
		t.Fatalf("chars %d over budget", s.chars)
	}

	docs = []*search.Doc{synthetic("a.md", 20, long(100)), synthetic("b.md", 19, long(5000)), synthetic("c.md", 18, long(300)), synthetic("d.md", 17, long(5000))}
	opts.Chars = 2000
	s = mustPrepare(t, v, docs, Selection{Since: day(1)}, opts)
	meta := 4 + 1 + 10
	// 2000 - 4*15 = 1940: a 100, c 300, then b and d 770 each.
	want := map[string]int{"a.md": 100, "c.md": 300, "b.md": 770, "d.md": 770}
	for _, src := range s.sources {
		if n := utf8.RuneCountInString(src.Excerpt); n != want[src.Path] || src.Truncated != (n == 770) {
			t.Fatalf("%s: %d runes, truncated %v", src.Path, n, src.Truncated)
		}
	}
	if s.chars != 4*meta+1940 || s.Report().Truncated != 2 {
		t.Fatalf("chars %d, report %+v", s.chars, s.Report())
	}
}

func TestAllocateTable(t *testing.T) {
	cases := []struct {
		chars int
		sizes []int
		want  []int
	}{
		{1000, []int{100, 500, 900}, []int{100, 450, 450}},
		{1000, []int{10, 20, 30}, []int{10, 20, 30}},
		{900, []int{400, 400, 400}, []int{300, 300, 300}},
		{1000, []int{0, 2000}, []int{0, 1000}},
		{1001, []int{2000, 2000}, []int{500, 501}},
	}
	for _, tc := range cases {
		s := &Session{opts: Options{Chars: tc.chars}}
		var entries []*entry
		for i, size := range tc.sizes {
			entries = append(entries, &entry{size: size, priority: i})
		}
		s.allocate(entries)
		var got []int
		for _, e := range entries {
			got = append(got, e.budget)
		}
		if !slices.Equal(got, tc.want) {
			t.Errorf("allocate(%d, %v) = %v, want %v", tc.chars, tc.sizes, got, tc.want)
		}
	}
}

func TestTopicWindow(t *testing.T) {
	v := emptyVault(t)
	var lines []string
	for i := range 60 {
		lines = append(lines, "filler line number "+itoa(i)+" with padding text")
	}
	lines = append(lines, "the needle is here", "after the needle")
	body := strings.Join(lines, "\n")
	opts := options()
	opts.Chars = MinChars
	doc := synthetic("hay.md", 20, body)

	s := mustPrepare(t, v, []*search.Doc{doc}, Selection{Topic: "needle"}, opts)
	excerpt := s.sources[0].Excerpt
	budget := MinChars - len("hay.md") - len("hay") - 10
	if !s.sources[0].Truncated || utf8.RuneCountInString(excerpt) > budget || !strings.Contains(excerpt, "the needle is here") {
		t.Fatalf("window: %q", excerpt)
	}
	hit := strings.Index(body, "the needle is here")
	start := strings.Index(body, excerpt)
	if start <= 0 || body[start-1] != '\n' || hit < start || !strings.HasSuffix(body, excerpt) || utf8.RuneCountInString(excerpt) < budget-60 {
		t.Fatalf("window starts at %d, hit at %d, budget %d, excerpt %d runes", start, hit, budget, utf8.RuneCountInString(excerpt))
	}
	midBody := strings.Join(append(append(lines[:60:60], "the needle is here"), lines[:60]...), "\n")
	s = mustPrepare(t, v, []*search.Doc{synthetic("mid.md", 20, midBody)}, Selection{Topic: "needle"}, opts)
	excerpt = s.sources[0].Excerpt
	budget = MinChars - len("mid.md") - len("mid") - 10
	hit, start = strings.Index(midBody, "the needle is here"), strings.Index(midBody, excerpt)
	if start <= 0 || midBody[start-1] != '\n' || hit-start > budget/3 || hit-start < budget/3-60 || utf8.RuneCountInString(excerpt) != budget {
		t.Fatalf("mid window starts at %d, hit at %d, budget %d", start, hit, budget)
	}

	s = mustPrepare(t, v, []*search.Doc{doc}, Selection{Since: day(1)}, opts)
	if !strings.HasPrefix(body, s.sources[0].Excerpt) || !s.sources[0].Truncated {
		t.Fatal("prefix excerpt")
	}
	early := synthetic("early.md", 20, "needle first\n"+body)
	s = mustPrepare(t, v, []*search.Doc{early}, Selection{Topic: "needle"}, opts)
	if !strings.HasPrefix(s.sources[0].Excerpt, "needle first\n") {
		t.Fatal("early hit moved the window")
	}

	pic := synthetic("pic.md", 20, strings.Join(lines[:60], "\n"))
	pic.Images = []search.ImageDoc{{Path: "nn/assets/p.png", Lines: []ocr.Line{{Text: "screen text"}, {Text: "the needle on screen"}}}}
	s = mustPrepare(t, v, []*search.Doc{pic}, Selection{Topic: "needle"}, opts)
	if got := s.sources[0].Excerpt; !strings.Contains(got, "the needle on screen") || strings.HasPrefix(got, "filler line number 0 ") {
		t.Fatalf("ocr window: %q", got)
	}
	pic.Images[0].Lines = pic.Images[0].Lines[:1]
	s = mustPrepare(t, v, []*search.Doc{pic}, Selection{Since: day(1)}, maxBudget())
	if got := s.sources[0].Excerpt; !strings.HasSuffix(got, "\nOCR nn/assets/p.png:\nscreen text") || s.sources[0].Truncated {
		t.Fatalf("ocr content: %q", got)
	}
}

func maxBudget() Options {
	opts := options()
	opts.Notes, opts.Chars = MaxNotes, MaxChars
	return opts
}

func TestRuneBoundaries(t *testing.T) {
	v := emptyVault(t)
	opts := options()
	opts.Chars = MinChars
	for _, unit := range []string{"\xd0\xb6", "\U0001F600", "e\xcc\x81", "\xd0\xb6\U0001F600a\xcc\x81"} {
		doc := synthetic("r.md", 20, strings.Repeat(unit, 2000))
		s := mustPrepare(t, v, []*search.Doc{doc}, Selection{Since: day(1)}, opts)
		excerpt := s.sources[0].Excerpt
		want := MinChars - len("r.md") - 1 - 10
		if !utf8.ValidString(excerpt) || utf8.RuneCountInString(excerpt) != want || !strings.HasPrefix(strings.Repeat(unit, 2000), excerpt) {
			t.Fatalf("%q: %d runes, valid %v", unit, utf8.RuneCountInString(excerpt), utf8.ValidString(excerpt))
		}
	}
}

// Worst-case JSON escaping (six bytes per character) under the transport limit.
func TestWorstCaseRequestFits(t *testing.T) {
	v := emptyVault(t)
	var small []string
	for i := range 16 {
		small = append(small, string(rune('a'+i))+strings.Repeat("<", 9))
	}
	var many []*search.Doc
	for i := range 64 {
		doc := synthetic(strings.Repeat(lineSeparator, 20)+itoa(100+i)+".md", 20, strings.Repeat("<"+lineSeparator, 20000))
		doc.Title = strings.Repeat("<", 20)
		doc.Tags = small
		many = append(many, doc)
	}
	var long []string
	for i := range 16 {
		long = append(long, string(rune('a'+i))+strings.Repeat("<", 63))
	}
	var few []*search.Doc
	for i := range 64 {
		doc := synthetic(strings.Repeat(lineSeparator, 400)+itoa(100+i)+".md", 20, strings.Repeat(lineSeparator+"<", 20000))
		doc.Title = strings.Repeat("<", 300)
		doc.Tags = long
		few = append(few, doc)
	}
	opts := maxBudget()
	opts.AllowSecret = true
	for name, tc := range map[string]struct {
		docs []*search.Doc
		sel  Selection
		n    int
	}{
		"many sources":  {many, Selection{Since: day(1), Until: day(25)}, 64},
		"long metadata": {few, Selection{Tags: long, Since: day(1)}, 17},
	} {
		s := mustPrepare(t, v, tc.docs, tc.sel, opts)
		req := s.Request(strings.Repeat("p", 4000))
		if err := ai.ValidateRequest(req); err != nil || len(req.Text) > ai.MaxTextBytes {
			t.Fatalf("%s: %d bytes, %v", name, len(req.Text), err)
		}
		if s.chars > MaxChars || s.chars < MaxChars-100 || s.Report().Included != tc.n {
			t.Fatalf("%s: chars %d, report %+v", name, s.chars, s.Report())
		}
		t.Logf("%s: %d bytes for %d sources", name, len(req.Text), s.Report().Included)
	}
}
