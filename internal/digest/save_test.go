package digest

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/lamovs/nn/internal/links"
)

func TestSavedGolden(t *testing.T) {
	s, _ := goldenVault(t)
	d := run(t, s, adversarial()...)
	notes, err := s.vault.LoadAll(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var paths []string
	for _, n := range notes {
		paths = append(paths, n.Path)
	}
	note := d.Note(links.Build(notes), paths, "nn", day(24))
	want := "Digest of 3 notes dated 2026-09-17 to 2026-09-18; selection: since 2026-09-17\n" +
		"\n" +
		"- \\[t\\]\\(obsidian\\://x\\) and &lt;&#37;\\* x &#37;&gt; and \\`code\\` [1] [3]\n" +
		"- \\[\\[x\\]\\] \\#tag https\\://e.test www\\.e.test [2]\n" +
		"- key:\\: v - \\[ \\] x a:\\:\\:b x:\\:\\:\\:y [1]\n" +
		"- &#37;&#37; hidden 100&#37;&#37; \\\\\\[x\\\\\\] &amp;lt; ==x== [1] [2] [3]\n" +
		"- \\]8;;http\\://eoscrtl sep [2]\n" +
		"\n" +
		"## Sources\n" +
		"\n" +
		"1. [[./a.md]] PGO \\*in\\* Go (2026-09-17)\n" +
		"2. [[../c.md]] c (2026-09-18)\n" +
		"3. [[../notes/b.md]] b (2026-09-18, excerpt)\n"
	if note.Body != want {
		t.Fatalf("saved body\n got:\n%s\nwant:\n%s", note.Body, want)
	}
	if note.Title != "Digest 2026-09-24" || note.Via != "digest" || len(note.Tags) != 0 || len(note.TrailingTags) != 0 {
		t.Fatalf("note = %+v", note)
	}
}

func TestSavedLinks(t *testing.T) {
	files := map[string]string{
		"root.md":       md("2026-09-17", "", "Root."),
		"nn/a.md":       md("2026-09-17", "", "Inbox."),
		"nn/deep/b.md":  md("2026-09-17", "", "Nested."),
		"work/c.md":     md("2026-09-17", "", "Sibling folder."),
		"work/d#e.md":   md("2026-09-17", "", "Unsafe."),
		"work/100%%.md": md("2026-09-17", "", "Percent."),
		"work/a::b.md":  md("2026-09-17", "", "Colons."),
		"work/Case.md":  "---\ndate: 2026-09-17\naliases: [\"x:::y %% z\"]\n---\nCase.\n",
		"work/a<%x.md":  md("2026-09-17", "", "Templater open."),
		"work/y%>.md":   md("2026-09-17", "", "Templater close."),
	}
	v := writeVault(t, files)
	docs := load(t, v)
	s := mustPrepare(t, v, docs, Selection{Since: day(1), Topic: ""}, maxBudget())
	var ids []string
	for _, src := range s.sources {
		ids = append(ids, src.ID)
	}
	d := run(t, s, para("All notes a:::b 100%%.", ids...))
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
	body := d.Note(g, paths, "nn", day(24)).Body
	for _, want := range []string{
		"[[../root.md]] root (2026-09-17)\n",
		"[[./a.md]] a (2026-09-17)\n",
		"[[./deep/b.md]] b (2026-09-17)\n",
		"[[../work/c.md]] c (2026-09-17)\n",
		" work/d\\#e.md d\\#e (2026-09-17) (no link: ambiguous or unsafe path)\n",
		" work/100&#37;&#37;.md 100&#37;&#37; (2026-09-17) (no link: ambiguous or unsafe path)\n",
		" work/a:\\:b.md a:\\:b (2026-09-17) (no link: ambiguous or unsafe path)\n",
		" work/Case.md x:\\:\\:y &#37;&#37; z (2026-09-17) (no link: ambiguous or unsafe path)\n",
		" work/a&lt;&#37;x.md a&lt;&#37;x (2026-09-17) (no link: ambiguous or unsafe path)\n",
		" work/y&#37;&gt;.md y&#37;&gt; (2026-09-17) (no link: ambiguous or unsafe path)\n",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("body lacks %q:\n%s", want, body)
		}
	}
	if strings.Contains(body, "::") || strings.Contains(body, "%%") || strings.Contains(body, "<%") || strings.Contains(body, "%>") || strings.Contains(body, "%") {
		t.Fatalf("body keeps markup:\n%s", body)
	}
	// Every link parses back and resolves, from the inbox, to its source.
	var resolved []string
	for _, l := range links.Parse(body, 1) {
		target, ok := g.Resolve("nn/digest.md", l.Target)
		if !ok || l.Embed || l.Display != "" || l.Heading != "" {
			t.Fatalf("link %+v resolves to %q, %v", l, target, ok)
		}
		resolved = append(resolved, target)
	}
	slices.Sort(resolved)
	if !slices.Equal(resolved, []string{"nn/a.md", "nn/deep/b.md", "root.md", "work/c.md"}) {
		t.Fatalf("links = %v", resolved)
	}

	// via: digest notes are skipped by later digests unless listed.
	note := d.Note(g, paths, v.Inbox, day(24))
	note.Now = testNow
	saved, err := v.Create(note)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(saved.Path, "nn/") || saved.Via != "digest" || saved.Title != "Digest 2026-09-24" {
		t.Fatalf("saved %+v", saved)
	}
	docs = load(t, v)
	if s := mustPrepare(t, v, docs, Selection{Since: day(1)}, maxBudget()); slices.Contains(sourcePaths(s), saved.Path) || s.Report().Matched != len(files) {
		t.Fatalf("digest selected again: %v", sourcePaths(s))
	}
	if s := mustPrepare(t, v, docs, Selection{Paths: []string{saved.Path}}, maxBudget()); !slices.Equal(sourcePaths(s), []string{saved.Path}) {
		t.Fatalf("listed digest: %v", sourcePaths(s))
	}
}

func TestNoteTitle(t *testing.T) {
	today := time.Date(2026, 9, 24, 23, 0, 0, 0, time.Local)
	if got := noteTitle(today, ""); got != "Digest 2026-09-24" {
		t.Fatalf("title = %q", got)
	}
	if got := noteTitle(today, " docker\t compose\x1b "+zeroWidthSpace+"#"); got != "Digest 2026-09-24 docker compose" {
		t.Fatalf("title = %q", got)
	}
	long := noteTitle(today, strings.Repeat("\xd0\xb6", 300))
	if utf8.RuneCountInString(long) != 160 || !utf8.ValidString(long) {
		t.Fatalf("long title: %d runes", utf8.RuneCountInString(long))
	}
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
