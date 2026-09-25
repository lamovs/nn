package digest

import (
	"strings"
	"testing"
	"time"

	"github.com/lamovs/nn/internal/ai"
	"github.com/lamovs/nn/internal/platform"
	"github.com/lamovs/nn/internal/search"
)

func goldenVault(t *testing.T) (*Session, func() *Session) {
	t.Helper()
	v := writeVault(t, map[string]string{
		"nn/a.md":    md("2026-09-17", "go", "# PGO *in* Go", "Profiling [notes]."),
		"notes/b.md": md("2026-09-18", "", strings.Repeat("z", 3000)),
		"c.md":       md("2026-09-18", "", "Root note."),
	})
	docs := load(t, v)
	opts := options()
	opts.Chars = MinChars
	prepare := func() *Session { return mustPrepare(t, v, docs, Selection{Since: day(17)}, opts) }
	return prepare(), prepare
}

// adversarial points: model text that must stay inert prose.
func adversarial() []ai.AskParagraph {
	return []ai.AskParagraph{
		para("[t](obsidian://x) and <%* x %> and `code`", "S3", "S1"),
		para("[[x]] #tag https://e.test www.e.test", "S2"),
		para("key:: v\n- [ ] x a:::b x::::y", "S1"),
		para("%% hidden 100%% \\[x\\] &lt; ==x==", "S1", "S2", "S3"),
		para("\x1b]8;;http://e\x07osc"+rightToLeft+"rtl"+lineSeparator+"sep", "S2"),
		para("~~~ x", "S1"),
		para("$$ y", "S3"),
	}
}

func TestRenderGolden(t *testing.T) {
	s, _ := goldenVault(t)
	d := run(t, s, adversarial()...)
	uri := func(rel string) string { return platform.ObsidianURI(s.vault.Abs(rel)) }
	want := "Digest of 3 notes dated 2026-09-17 to 2026-09-18; selection: since 2026-09-17\n" +
		"\n" +
		"- \\[t\\]\\(obsidian\\://x\\) and &lt;%\\* x %&gt; and \\`code\\` [1] [3]\n" +
		"- \\[\\[x\\]\\] \\#tag https\\://e.test www\\.e.test [2]\n" +
		"- key:: v - \\[ \\] x a:::b x::::y [1]\n" +
		"- %% hidden 100%% \\\\\\[x\\\\\\] &amp;lt; ==x== [1] [2] [3]\n" +
		"- \\]8;;http\\://eoscrtl sep [2]\n" +
		"- ~~~ x [1]\n" +
		"- $$ y [3]\n" +
		"\n" +
		"Sources:\n" +
		"[1] [PGO \\*in\\* Go](" + uri("nn/a.md") + ") (nn/a.md, 2026-09-17)\n" +
		"[2] [c](" + uri("c.md") + ") (c.md, 2026-09-18)\n" +
		"[3] [b](" + uri("notes/b.md") + ") (notes/b.md, 2026-09-18, excerpt)\n"
	if got := d.Text(); got != want {
		t.Fatalf("stdout\n got:\n%s\nwant:\n%s", got, want)
	}
	for _, bad := range []string{"](obsidian://x", "[[", "\x1b", "\x07", rightToLeft, lineSeparator, "<%"} {
		if strings.Contains(d.Text(), bad) {
			t.Errorf("stdout contains %q", bad)
		}
	}
	if strings.Count(d.Text(), "`") != strings.Count(d.Text(), "\\`") {
		t.Error("unescaped backtick")
	}
}

func TestSameInputsSameBytes(t *testing.T) {
	_, prepare := goldenVault(t)
	first, second := prepare(), prepare()
	if first.Request("p").Text != second.Request("p").Text {
		t.Fatal("requests differ")
	}
	a, b := run(t, first, adversarial()...), run(t, second, adversarial()...)
	if a.Text() != b.Text() {
		t.Fatal("stdout differs")
	}
	today := time.Date(2026, 9, 24, 0, 0, 0, 0, time.Local)
	if a.Note(nil, nil, "nn", today).Body != b.Note(nil, nil, "nn", today).Body {
		t.Fatal("saved bodies differ")
	}
}

func TestHeaderVariants(t *testing.T) {
	v := emptyVault(t)
	secret := synthetic("s.md", 19, "key: "+token)
	one := synthetic("one.md", 20, "One.")
	two := synthetic("two.md", 20, "Two.")
	opts := options()
	cases := []struct {
		docs []*search.Doc
		sel  Selection
		opts func(*Options)
		want string
	}{
		{[]*search.Doc{one}, Selection{Since: time.Date(2026, 9, 17, 14, 5, 0, 0, time.Local), Until: day(25)},
			nil, "Digest of 1 note dated 2026-09-20; selection: since 2026-09-17 14:05; until 2026-09-24\n\n"},
		{[]*search.Doc{one, two}, Selection{Since: day(1)},
			func(o *Options) { o.Notes = 1 }, "Digest of 1 note dated 2026-09-20; selection: since 2026-09-01\nNot included: 1 matching note beyond the limit of 1 note / 12000 characters.\n\n"},
		{[]*search.Doc{one, secret}, Selection{Since: day(1)},
			nil, "Digest of 1 note dated 2026-09-20; selection: since 2026-09-01\nNot included: 1 note that may contain credentials (--allow-secret includes them).\n\n"},
		{[]*search.Doc{one, two, synthetic("s2.md", 21, "key: "+token), synthetic("x.md", 1, "X."), synthetic("y.md", 2, "Y.")}, Selection{Since: day(1)},
			func(o *Options) { o.Notes = 2 }, "Digest of 2 notes dated 2026-09-20; selection: since 2026-09-01\nNot included: 2 matching notes beyond the limit of 2 notes / 12000 characters; 1 note that may contain credentials (--allow-secret includes them).\n\n"},
		{[]*search.Doc{one, synthetic("t.md", 1, "T #devops")}, Selection{Tags: []string{"dev[ops]"}, Topic: "T"},
			nil, ""},
	}
	for i, tc := range cases {
		o := opts
		if tc.opts != nil {
			tc.opts(&o)
		}
		s := mustPrepare(t, v, tc.docs, tc.sel, o)
		if tc.want == "" {
			if !s.Empty() {
				t.Errorf("%d: not empty", i)
			}
			continue
		}
		d := run(t, s, para("x", "S1"))
		if got := d.Text(); !strings.HasPrefix(got, tc.want+"- x [1]\n\nSources:\n") {
			t.Errorf("%d: header\n got: %q\nwant: %q", i, got, tc.want)
		}
	}
	// Filter text is escaped like model text.
	tagged := synthetic("m.md", 20, "Body.")
	tagged.Tags = []string{"a[b]"}
	s := mustPrepare(t, v, []*search.Doc{tagged}, Selection{Tags: []string{"a[b]"}, Topic: "body"}, opts)
	if got := run(t, s, para("x", "S1")).Text(); !strings.HasPrefix(got, "Digest of 1 note dated 2026-09-20; selection: tag a\\[b\\]; topic \"body\"\n") {
		t.Fatalf("escaped header: %q", got)
	}
}
