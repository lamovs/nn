package digest

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/lamovs/nn/internal/output"
	"github.com/lamovs/nn/internal/search"
	"github.com/lamovs/nn/internal/vault"
)

func selectionVault(t *testing.T) *vault.Vault {
	t.Helper()
	return writeVault(t, map[string]string{
		"nn/compose.md":      md("2026-09-20", "devops/ci", "Docker compose files for the stack."),
		"nn/docker.md":       md("2026-09-18", "devops", "Docker cleanup routine.", "![[nn/assets/shot.png]]"),
		"nn/old-digest.md":   "---\ndate: 2026-09-21\nvia: digest\ntags: [devops]\n---\nDocker digest.\n",
		"notes/go.md":        md("2026-09-10", "go", "Go generics and docker images."),
		"notes/repo.md":      "---\ndate: 2026-09-22\nrepo: demo\ntags: [devops]\n---\nDocker in the demo repo.\n",
		"notes/untagged.md":  md("2026-09-23", "", "Compose without the other word."),
		"nn/assets/shot.png": "png",
		"images/loose.png":   "png",
		"notes/Readme.MD":    md("2026-09-19", "devops", "Upper case extension, docker."),
		"notes/devopsish.md": md("2026-09-19", "devopsish", "Docker but not the nested tag."),
	})
}

func TestSelectionParity(t *testing.T) {
	v := selectionVault(t)
	docs := load(t, v)
	var notes []*search.Doc
	for _, doc := range docs {
		if isNote(doc.Path) {
			notes = append(notes, doc)
		}
	}
	opts := options()
	opts.Notes, opts.Chars = MaxNotes, MaxChars
	cases := []Selection{
		{Topic: "docker compose"},
		{Topic: "docker"},
		{Tags: []string{"devops"}},
		{Tags: []string{"#DevOps"}, Topic: "docker"},
		{Since: day(19)},
		{Since: time.Date(2026, 9, 19, 12, 0, 0, 0, time.Local)},
		{Inbox: true},
		{Here: &search.Here{Repo: "demo"}},
		{Inbox: true, Tags: []string{"devops"}, Since: day(15)},
	}
	for _, sel := range cases {
		s := mustPrepare(t, v, docs, sel, opts)
		results, err := search.Search(context.Background(), notes, search.Query{
			Text: sel.Topic, Tags: sel.Tags, Since: sel.Since, Inbox: sel.Inbox, Here: sel.Here, NoLayoutFallback: true,
		}, nil, testNow)
		if err != nil {
			t.Fatal(err)
		}
		var want []string
		for _, r := range results {
			if r.Doc.Via != "digest" {
				want = append(want, r.Doc.Path)
			}
		}
		got := sourcePaths(s)
		slices.Sort(got)
		slices.Sort(want)
		if len(want) == 0 || !slices.Equal(got, want) || s.Report().Matched != len(want) || s.Report().Layout {
			t.Errorf("%+v: got %v, want %v (report %+v)", sel, got, want, s.Report())
		}
		for _, p := range got {
			if strings.HasSuffix(p, ".png") || p == "nn/old-digest.md" {
				t.Errorf("%+v: %s selected", sel, p)
			}
		}
	}

	opts.NoLayoutFallback = false
	s := mustPrepare(t, v, docs, Selection{Topic: "\xd0\xb2\xd1\x89\xd1\x81\xd0\xbb\xd1\x83\xd0\xba"}, opts)
	if !s.Report().Layout || !slices.Contains(sourcePaths(s), "nn/docker.md") {
		t.Fatalf("layout retry: %v %+v", sourcePaths(s), s.Report())
	}
	opts.NoLayoutFallback = true
	if s := mustPrepare(t, v, docs, Selection{Topic: "\xd0\xb2\xd1\x89\xd1\x81\xd0\xbb\xd1\x83\xd0\xba"}, opts); !s.Empty() || s.Report().Layout {
		t.Fatalf("layout retry without fallback: %+v", s.Report())
	}
	if s := mustPrepare(t, v, docs, Selection{Topic: "nothing-matches-this"}, opts); !s.Empty() || s.Request("p").Text != "" {
		t.Fatal("empty selection is not Empty")
	}
}

// --until is an exclusive next-midnight bound on the effective date.
func TestUntil(t *testing.T) {
	v := writeVault(t, map[string]string{
		"a.md": md("2026-09-20", "", "A."),
		"b.md": "---\ndate: 2026-09-21\ntime: \"23:59\"\n---\nB.\n",
		"c.md": md("2026-09-22", "", "C."),
	})
	docs := append(load(t, v), &search.Doc{Path: "zero.md", Lines: []search.Line{{Num: 1, Text: "Undated."}}})
	s := mustPrepare(t, v, docs, Selection{Until: day(22)}, options())
	if got := sourcePaths(s); !slices.Equal(got, []string{"a.md", "b.md"}) {
		t.Fatalf("until: %v", got)
	}
	s = mustPrepare(t, v, docs, Selection{Since: day(21), Until: day(23)}, options())
	if got := sourcePaths(s); !slices.Equal(got, []string{"b.md", "c.md"}) {
		t.Fatalf("since and until: %v", got)
	}
	s = mustPrepare(t, v, docs, Selection{Topic: "undated"}, options())
	if got := sourcePaths(s); !slices.Equal(got, []string{"zero.md"}) {
		t.Fatalf("undated: %v", got)
	}
}

func TestListedPaths(t *testing.T) {
	v := selectionVault(t)
	docs := load(t, v)
	refs, err := output.ReadRefs(strings.NewReader("notes/go.md:3\nnn/docker.md\nnotes/go.md\nnn/assets/shot.png\nimages/loose.png\nnn/old-digest.md\nnn/compose.md\n"))
	if err != nil {
		t.Fatal(err)
	}
	var paths []string
	for _, ref := range refs {
		paths = append(paths, ref.Path)
	}
	opts := options()
	opts.Notes = 3
	s := mustPrepare(t, v, docs, Selection{Paths: paths}, opts)
	// Input order decides who gets in; IDs are chronological.
	if got := sourcePaths(s); !slices.Equal(got, []string{"notes/go.md", "nn/docker.md", "nn/old-digest.md"}) {
		t.Fatalf("listed: %v", got)
	}
	if r := s.Report(); r.Matched != 4 || r.Included != 3 || r.BeyondLimit != 1 || r.SkippedNonNote != 2 {
		t.Fatalf("report: %+v", r)
	}
	if !strings.Contains(s.Request("p").Text, `"selection":"listed notes"`) {
		t.Fatalf("selection: %s", s.Request("p").Text)
	}

	s = mustPrepare(t, v, docs, Selection{Paths: paths, Tags: []string{"devops"}}, options())
	if got := sourcePaths(s); !slices.Equal(got, []string{"nn/docker.md", "nn/compose.md", "nn/old-digest.md"}) {
		t.Fatalf("filtered list: %v", got)
	}

	// An empty list is an empty selection, not an error.
	if s := mustPrepare(t, v, docs, Selection{Paths: []string{}}, options()); !s.Empty() {
		t.Fatal("empty list")
	}
	if s := mustPrepare(t, v, docs, Selection{Paths: []string{}, Since: day(1)}, options()); !s.Empty() {
		t.Fatal("empty list with a filter selected notes")
	}
	if s := mustPrepare(t, v, docs, Selection{Paths: []string{"images/loose.png"}}, options()); !s.Empty() || s.Report().SkippedNonNote != 1 {
		t.Fatalf("only images: %+v", s.Report())
	}

	_, err = Prepare(context.Background(), v, docs, Selection{Paths: []string{"nn/docker.md", "./nn/compose.md"}}, options())
	if err == nil || err.Error() != `listed path not found in the vault: "./nn/compose.md"` {
		t.Fatalf("unknown path: %v", err)
	}
	evil := "missing\x1b[31m" + rightToLeft + strings.Repeat("x", 300) + ".md"
	_, err = Prepare(context.Background(), v, docs, Selection{Paths: []string{evil, "nn/Docker.md", "/abs/nn/docker.md"}}, options())
	if err == nil || !strings.HasPrefix(err.Error(), `3 listed paths not found in the vault, first: "missing[31m`) || strings.ContainsAny(err.Error(), "\x1b") || strings.Contains(err.Error(), rightToLeft) || strings.Count(err.Error(), "x") != 200-len("missing[31m") {
		t.Fatalf("unknown paths: %v", err)
	}
}

func TestPriorityAndIDs(t *testing.T) {
	v := writeVault(t, map[string]string{
		"old.md":    md("2026-09-01", "", "# Kubernetes", "Kubernetes rollout notes."),
		"mid.md":    md("2026-09-10", "", "Mentions kubernetes once."),
		"new.md":    md("2026-09-20", "", "Also mentions kubernetes."),
		"newest.md": md("2026-09-22", "", "Unrelated."),
	})
	docs := load(t, v)
	opts := options()
	opts.Notes = 2

	s := mustPrepare(t, v, docs, Selection{Since: day(1)}, opts)
	if got := sourcePaths(s); !slices.Equal(got, []string{"new.md", "newest.md"}) {
		t.Fatalf("filters: %v", got)
	}
	// Topic: search rank; the title match wins although it is oldest.
	opts.Notes = 1
	s = mustPrepare(t, v, docs, Selection{Topic: "kubernetes"}, opts)
	if got := sourcePaths(s); !slices.Equal(got, []string{"old.md"}) {
		t.Fatalf("topic: %v", got)
	}
	opts.Notes = 2
	s = mustPrepare(t, v, docs, Selection{Paths: []string{"newest.md", "old.md", "mid.md"}}, opts)
	if got := sourcePaths(s); !slices.Equal(got, []string{"old.md", "newest.md"}) {
		t.Fatalf("list: %v", got)
	}
	for i, src := range s.sources {
		if src.ID != "S"+itoa(i+1) || s.byID[src.ID] != i+1 {
			t.Fatalf("ids: %+v", s.sources)
		}
	}
	v = writeVault(t, map[string]string{"b.md": md("2026-09-20", "", "B."), "a.md": md("2026-09-20", "", "A.")})
	s = mustPrepare(t, v, load(t, v), Selection{Since: day(1)}, options())
	if got := sourcePaths(s); !slices.Equal(got, []string{"a.md", "b.md"}) {
		t.Fatalf("ties: %v", got)
	}
}

func TestSelectionText(t *testing.T) {
	cases := []struct {
		sel  Selection
		want string
	}{
		{Selection{Since: day(17)}, "since 2026-09-17"},
		{Selection{Since: time.Date(2026, 9, 17, 14, 5, 33, 0, time.Local)}, "since 2026-09-17 14:05"},
		{Selection{Until: day(1).AddDate(0, 0, 30)}, "until 2026-09-30"},
		{Selection{Since: day(17), Until: day(25), Tags: []string{"devops"}, Topic: `do "cker"`}, `since 2026-09-17; until 2026-09-24; tag devops; topic "do "cker""`},
		{Selection{Paths: []string{}, Tags: []string{"a", "b/c"}, Inbox: true, Here: &search.Here{Repo: "demo"}}, "listed notes; tags a, b/c; inbox; here"},
	}
	for _, tc := range cases {
		if got := tc.sel.describe(); got != tc.want {
			t.Errorf("describe(%+v) = %q, want %q", tc.sel, got, tc.want)
		}
	}
}
