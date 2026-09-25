package ask

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/lamovs/nn/internal/ai"
	"github.com/lamovs/nn/internal/config"
	"github.com/lamovs/nn/internal/links"
	"github.com/lamovs/nn/internal/platform"
	"github.com/lamovs/nn/internal/vault"
)

const (
	rightToLeft   = "\xe2\x80\xae" // U+202E
	lineSeparator = "\xe2\x80\xa8" // U+2028
)

// alphaFixture returns a fresh session over a small vault where every note
// matches "alpha", plus the sent source IDs by path and a helper to build a
// note's Obsidian URI.
func alphaFixture(t *testing.T) (s *Session, id map[string]string, uri func(string) string) {
	t.Helper()
	files := map[string]string{
		"nn/a.md":    "# PGO *in* Go\nProfiling alpha.\n",
		"notes/b.md": "alpha beta\n",
		"c.md":       "Root alpha note.\n",
	}
	s = prepare(t, files, "Where is alpha?", 0)
	id = map[string]string{}
	for _, src := range s.sources {
		id[src.Path] = src.ID
	}
	uri = func(rel string) string { return platform.ObsidianURI(s.vault.Abs(rel)) }
	return s, id, uri
}

func replyModel(r *ai.AskReply) Model {
	return func(context.Context, ai.Approval, ai.Request) (ai.Result, error) { return ai.Result{Ask: r}, nil }
}

func TestNeutral(t *testing.T) {
	for in, want := range map[string]string{
		"key:: v":   "key:\\: v",
		"a:::b":     "a:\\:\\:b",
		"x::::y":    "x:\\:\\:\\:y",
		"a:b":       "a:b",
		"%% hidden": "&#37;&#37; hidden",
		"100%":      "100&#37;",
		"a\\://b":   "a\\://b",
		":":         ":",
	} {
		got := neutral(in)
		if got != want || strings.Contains(got, "::") || strings.Contains(got, "%%") {
			t.Errorf("neutral(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestNoteLineBlockSafe(t *testing.T) {
	for in, want := range map[string]string{
		"~~~ x": "\\~~~ x",
		"$$ y":  "\\$$ y",
		"a ~~~": "a ~~~",
		"- ~~~": "- ~~~",
		"%%":    "&#37;&#37;",
	} {
		if got := NoteLine(in); got != want {
			t.Errorf("NoteLine(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestAnswerStdoutGolden pins nn ask's stdout bytes: the answer and both
// insufficient variants.
func TestAnswerStdoutGolden(t *testing.T) {
	t.Run("answer", func(t *testing.T) {
		s, id, uri := alphaFixture(t)
		a, b, c := id["nn/a.md"], id["notes/b.md"], id["c.md"]
		full := &ai.AskReply{Action: "answer", Paragraphs: []ai.AskParagraph{
			{Text: "[t](obsidian://x) and <%* x %> and `code`", SourceIDs: []string{a, c}},
			{Text: "[[x]] #tag https://e.test www.e.test", SourceIDs: []string{b}},
			{Text: "key:: v\n- [ ] x a:::b x::::y", SourceIDs: []string{a}},
			{Text: "%% hidden 100%% \\[x\\] &lt; ==x==", SourceIDs: []string{c, a, b}},
			{Text: "\x1b]8;;http://e\x07osc" + rightToLeft + "rtl" + lineSeparator + "sep", SourceIDs: []string{b}},
			{Text: "~~~ fence", SourceIDs: []string{c}},
			{Text: "$$ x", SourceIDs: []string{c}},
		}}
		text, err := s.Run(context.Background(), ai.Approval{}, call(), "prompt", replyModel(full))
		want := "\\[t\\]\\(obsidian\\://x\\) and &lt;%\\* x %&gt; and \\`code\\` [1] [2]\n" +
			"\n" +
			"\\[\\[x\\]\\] \\#tag https\\://e.test www\\.e.test [3]\n" +
			"\n" +
			"key:: v - \\[ \\] x a:::b x::::y [1]\n" +
			"\n" +
			"%% hidden 100%% \\\\\\[x\\\\\\] &amp;lt; ==x== [2] [1] [3]\n" +
			"\n" +
			"\\]8;;http\\://eoscrtl sep [3]\n" +
			"\n" +
			"~~~ fence [2]\n" +
			"\n" +
			"$$ x [2]\n" +
			"\n" +
			"Sources:\n" +
			"[1] [PGO \\*in\\* Go](" + uri("nn/a.md") + ") (nn/a.md)\n" +
			"[2] [c](" + uri("c.md") + ") (c.md)\n" +
			"[3] [b](" + uri("notes/b.md") + ") (notes/b.md)\n"
		if text != want {
			t.Fatalf("stdout\n got:\n%s\nwant:\n%s", text, want)
		}
		if err != nil {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("insufficient with paragraphs", func(t *testing.T) {
		s, id, uri := alphaFixture(t)
		a, c := id["nn/a.md"], id["c.md"]
		partial := &ai.AskReply{Action: "insufficient", Missing: "No date %% [x]",
			Paragraphs: []ai.AskParagraph{{Text: "~~~ fence", SourceIDs: []string{c, a}}}}
		text, err := s.Run(context.Background(), ai.Approval{}, call(), "prompt", replyModel(partial))
		want := "~~~ fence [1] [2]\n" +
			"\n" +
			"Not enough data: No date %% \\[x\\]\n" +
			"\n" +
			"Sources:\n" +
			"[1] [c](" + uri("c.md") + ") (c.md)\n" +
			"[2] [PGO \\*in\\* Go](" + uri("nn/a.md") + ") (nn/a.md)\n"
		if text != want {
			t.Fatalf("stdout\n got:\n%s\nwant:\n%s", text, want)
		}
		if !errors.Is(err, ErrInsufficient) {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("insufficient without paragraphs", func(t *testing.T) {
		s, _, _ := alphaFixture(t)
		none := &ai.AskReply{Action: "insufficient", Missing: "Nothing  relevant", Paragraphs: []ai.AskParagraph{}}
		text, err := s.Run(context.Background(), ai.Approval{}, call(), "prompt", replyModel(none))
		want := "Not enough data: Nothing relevant\n"
		if text != want {
			t.Fatalf("stdout = %q, want %q", text, want)
		}
		if !errors.Is(err, ErrInsufficient) {
			t.Fatalf("err = %v", err)
		}
		if strings.Contains(text, "Sources:") {
			t.Fatal("unexpected Sources section")
		}
	})
}

// TestNoteGolden pins the saved-note body for the same reply as A1: the
// escaped paragraphs must match what digest pins for the identical text
// (render_test.go, save_test.go); a mismatch there is a bug in the move,
// not a golden to update.
func TestNoteGolden(t *testing.T) {
	s, id, _ := alphaFixture(t)
	a, b, c := id["nn/a.md"], id["notes/b.md"], id["c.md"]
	full := &ai.AskReply{Action: "answer", Paragraphs: []ai.AskParagraph{
		{Text: "[t](obsidian://x) and <%* x %> and `code`", SourceIDs: []string{a, c}},
		{Text: "[[x]] #tag https://e.test www.e.test", SourceIDs: []string{b}},
		{Text: "key:: v\n- [ ] x a:::b x::::y", SourceIDs: []string{a}},
		{Text: "%% hidden 100%% \\[x\\] &lt; ==x==", SourceIDs: []string{c, a, b}},
		{Text: "\x1b]8;;http://e\x07osc" + rightToLeft + "rtl" + lineSeparator + "sep", SourceIDs: []string{b}},
		{Text: "~~~ fence", SourceIDs: []string{c}},
		{Text: "$$ x", SourceIDs: []string{c}},
	}}
	if _, err := s.Run(context.Background(), ai.Approval{}, call(), "prompt", replyModel(full)); err != nil {
		t.Fatal(err)
	}
	notes, err := s.vault.LoadAll(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var paths []string
	for _, n := range notes {
		paths = append(paths, n.Path)
	}
	g := links.Build(notes)
	note, err := s.Note(g, paths, "nn")
	if err != nil {
		t.Fatal(err)
	}
	want := "\\[t\\]\\(obsidian\\://x\\) and &lt;&#37;\\* x &#37;&gt; and \\`code\\` [1] [2]\n" +
		"\n" +
		"\\[\\[x\\]\\] \\#tag https\\://e.test www\\.e.test [3]\n" +
		"\n" +
		"key:\\: v - \\[ \\] x a:\\:\\:b x:\\:\\:\\:y [1]\n" +
		"\n" +
		"&#37;&#37; hidden 100&#37;&#37; \\\\\\[x\\\\\\] &amp;lt; ==x== [2] [1] [3]\n" +
		"\n" +
		"\\]8;;http\\://eoscrtl sep [3]\n" +
		"\n" +
		"\\~~~ fence [2]\n" +
		"\n" +
		"\\$$ x [2]\n" +
		"\n" +
		"## Sources\n" +
		"\n" +
		"1. [[./a.md]] PGO \\*in\\* Go\n" +
		"2. [[../c.md]] c\n" +
		"3. [[../notes/b.md]] b\n"
	if note.Body != want {
		t.Fatalf("saved body\n got:\n%s\nwant:\n%s", note.Body, want)
	}
	if note.Title != "Where is alpha?" || note.Via != "ask" || len(note.Tags) != 0 ||
		len(note.TrailingTags) != 0 || !note.Now.IsZero() {
		t.Fatalf("note = %+v", note)
	}
	parsed := links.Parse(note.Body, 1)
	if len(parsed) != 3 {
		t.Fatalf("links = %+v", parsed)
	}
	var resolved []string
	for _, l := range parsed {
		target, ok := g.Resolve("nn/answer.md", l.Target)
		if !ok {
			t.Fatalf("link %+v does not resolve", l)
		}
		resolved = append(resolved, target)
	}
	slices.Sort(resolved)
	if want := []string{"c.md", "nn/a.md", "notes/b.md"}; !slices.Equal(resolved, want) {
		t.Fatalf("resolved links = %v, want %v", resolved, want)
	}
	if tags := vault.InlineTags(note.Body); len(tags) != 0 {
		t.Fatalf("inline tags = %v", tags)
	}
	for _, bad := range []string{"::", "%", "<"} {
		if strings.Contains(note.Body, bad) {
			t.Errorf("body contains %q", bad)
		}
	}
	for _, line := range strings.Split(note.Body, "\n") {
		if strings.HasPrefix(line, "#") && line != "## Sources" {
			t.Errorf("unexpected heading line: %q", line)
		}
		if strings.HasPrefix(line, "~") || strings.HasPrefix(line, "$") {
			t.Errorf("unescaped block-start line: %q", line)
		}
	}
}

// TestNoteLinks ports digest's TestSavedLinks (internal/digest/save_test.go)
// without date annotations, since a saved answer has none.
func TestNoteLinks(t *testing.T) {
	files := map[string]string{
		"root.md":       "alpha\n",
		"nn/a.md":       "alpha\n",
		"nn/deep/b.md":  "alpha\n",
		"work/c.md":     "alpha\n",
		"work/d#e.md":   "alpha\n",
		"work/100%%.md": "alpha\n",
		"work/a::b.md":  "alpha\n",
		"work/Case.md":  "---\naliases: [\"x:::y %% z\"]\n---\nalpha\n",
		"work/a<%x.md":  "alpha\n",
		"work/y%>.md":   "alpha\n",
	}
	v := fixture(t, files)
	limits := config.Default().AI.Context
	limits.Notes = 16
	limits.SearchRounds = 0
	s, err := Prepare(context.Background(), v, Options{Limits: limits, NoLayoutFallback: true}, "alpha")
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, src := range s.sources {
		ids = append(ids, src.ID)
	}
	full := &ai.AskReply{Action: "answer", Paragraphs: []ai.AskParagraph{{Text: "All notes.", SourceIDs: ids}}}
	if _, err := s.Run(context.Background(), ai.Approval{}, call(), "prompt", replyModel(full)); err != nil {
		t.Fatal(err)
	}
	notes, err := v.LoadAll(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var paths []string
	for _, n := range notes {
		paths = append(paths, n.Path)
	}
	// A case-distinct entry, as on a case-sensitive file system.
	paths = append(paths, "work/CASE.md")
	g := links.Build(notes)
	note, err := s.Note(g, paths, "nn")
	if err != nil {
		t.Fatal(err)
	}
	body := note.Body
	for _, want := range []string{
		"[[../root.md]] root\n",
		"[[./a.md]] a\n",
		"[[./deep/b.md]] b\n",
		"[[../work/c.md]] c\n",
		" work/d\\#e.md d\\#e (no link: ambiguous or unsafe path)\n",
		" work/100&#37;&#37;.md 100&#37;&#37; (no link: ambiguous or unsafe path)\n",
		" work/a:\\:b.md a:\\:b (no link: ambiguous or unsafe path)\n",
		" work/Case.md x:\\:\\:y &#37;&#37; z (no link: ambiguous or unsafe path)\n",
		" work/a&lt;&#37;x.md a&lt;&#37;x (no link: ambiguous or unsafe path)\n",
		" work/y&#37;&gt;.md y&#37;&gt; (no link: ambiguous or unsafe path)\n",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("body lacks %q:\n%s", want, body)
		}
	}
	if strings.Contains(body, "::") || strings.Contains(body, "%%") || strings.Contains(body, "<%") || strings.Contains(body, "%>") || strings.Contains(body, "%") {
		t.Fatalf("body keeps markup:\n%s", body)
	}
	var resolved []string
	for _, l := range links.Parse(body, 1) {
		target, ok := g.Resolve("nn/answer.md", l.Target)
		if !ok || l.Embed || l.Display != "" || l.Heading != "" {
			t.Fatalf("link %+v resolves to %q, %v", l, target, ok)
		}
		resolved = append(resolved, target)
	}
	slices.Sort(resolved)
	if !slices.Equal(resolved, []string{"nn/a.md", "nn/deep/b.md", "root.md", "work/c.md"}) {
		t.Fatalf("links = %v", resolved)
	}
}

func TestNoteRequiresFullAnswer(t *testing.T) {
	assertNoNote := func(t *testing.T, s *Session) {
		t.Helper()
		if _, err := s.Note(nil, nil, "nn"); err == nil {
			t.Fatal("expected an error")
		}
	}

	t.Run("before Run", func(t *testing.T) {
		s, _, _ := alphaFixture(t)
		assertNoNote(t, s)
	})

	t.Run("after a failed Run", func(t *testing.T) {
		s, _, _ := alphaFixture(t)
		bad := &ai.AskReply{Action: "answer", Paragraphs: []ai.AskParagraph{{Text: "Claim", SourceIDs: []string{"S99"}}}}
		if _, err := s.Run(context.Background(), ai.Approval{}, call(), "prompt", replyModel(bad)); err == nil {
			t.Fatal("expected the run to fail")
		}
		assertNoNote(t, s)
	})

	t.Run("after an insufficient reply with paragraphs", func(t *testing.T) {
		s, id, _ := alphaFixture(t)
		partial := &ai.AskReply{Action: "insufficient", Missing: "No date.",
			Paragraphs: []ai.AskParagraph{{Text: "Some evidence.", SourceIDs: []string{id["c.md"]}}}}
		if _, err := s.Run(context.Background(), ai.Approval{}, call(), "prompt", replyModel(partial)); !errors.Is(err, ErrInsufficient) {
			t.Fatalf("err = %v", err)
		}
		assertNoNote(t, s)
	})

	t.Run("after the LocalAnswer path", func(t *testing.T) {
		s := prepare(t, nil, "zebra", 0)
		if _, local := s.LocalAnswer(); !local {
			t.Fatal("expected a local answer")
		}
		if _, err := s.Run(context.Background(), ai.Approval{}, call(), "prompt", nil); !errors.Is(err, ErrInsufficient) {
			t.Fatalf("err = %v", err)
		}
		assertNoNote(t, s)
	})
}

func TestSummaryNote(t *testing.T) {
	for via, want := range map[string]bool{
		"ask": true, "digest": true, " ASK ": true, "Digest": true,
		"": false, "text": false, "editor": false, "stdin": false, "clip": false,
		"shot": false, "url": false, "asked": false, "digests": false, "ask digest": false,
	} {
		if got := SummaryNote(via); got != want {
			t.Errorf("SummaryNote(%q) = %v, want %v", via, got, want)
		}
	}
}

func TestNoteTitle(t *testing.T) {
	cases := map[string]string{
		" What  about\tdocker?\x1b ": "What about docker?",
		"a" + zeroWidthSpace + "b":   "a b",
		"## docker cache ##":         "docker cache",
		"###":                        "Answer",
	}
	for in, want := range cases {
		if got := noteTitle(in); got != want {
			t.Errorf("noteTitle(%q) = %q, want %q", in, got, want)
		}
	}
	long := noteTitle(strings.Repeat("\xd0\xb6", 300))
	if n := utf8.RuneCountInString(long); n != 160 || !utf8.ValidString(long) {
		t.Fatalf("long title: %d runes, valid=%v", n, utf8.ValidString(long))
	}
}
