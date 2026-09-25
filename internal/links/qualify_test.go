package links

import (
	"testing"

	"github.com/lamovs/nn/internal/vault"
)

func TestQualifiedLink(t *testing.T) {
	punct := "(alpha) & beta +_-.md"
	notes := []*vault.Note{
		note("root.md", "Root", nil, ""),
		note("nn/a.md", "A", nil, ""),
		note("nn/deep/b.md", "B", nil, ""),
		note("work/review.md", "Review", []string{"a"}, ""),
		note("reference/"+punct, "Punct", nil, ""),
		note("other/"+punct, "Other punct", nil, ""),
		note("nn/b#bad.md", "Bad", nil, ""),
		note("nn/b`x`.md", "Code", nil, ""),
		note("nn/ spaced.md", "Spaced", nil, ""),
		note(" lead.md", "Lead", nil, ""),
		note("nn/Case.md", "Case", nil, ""),
	}
	paths := make([]string, 0, len(notes)+1)
	for _, n := range notes {
		paths = append(paths, n.Path)
	}
	// A case-distinct entry the walk found but could not load.
	paths = append(paths, "nn/CASE.md")
	g := Build(notes)
	tests := []struct {
		from, target, want, err string
	}{
		{"nn/digest.md", "root.md", "[[../root.md]]", ""},
		{"nn/digest.md", "nn/a.md", "[[./a.md]]", ""},
		{"nn/digest.md", "nn/deep/b.md", "[[./deep/b.md]]", ""},
		{"nn/digest.md", "work/review.md", "[[../work/review.md]]", ""},
		{"nn/deep/x.md", "nn/a.md", "[[../a.md]]", ""},
		{"digest.md", "root.md", "[[./root.md]]", ""},
		{"digest.md", "nn/a.md", "[[./nn/a.md]]", ""},
		{"nn/digest.md", "reference/" + punct, "[[../reference/" + punct + "]]", ""},
		{"nn/digest.md", "nn/b#bad.md", "", "path cannot be represented safely in wiki syntax"},
		{"nn/digest.md", "nn/b`x`.md", "", "path cannot be represented safely in wiki syntax"},
		{"nn/digest.md", "nn/ spaced.md", "[[./ spaced.md]]", ""},
		{"nn/digest.md", " lead.md", "", "path cannot be represented safely in wiki syntax"},
		{"nn/digest.md", "nn/Case.md", "", "case-ambiguous target"},
		{"nn/digest.md", "nn/missing.md", "", "case-ambiguous target"},
	}
	for _, tt := range tests {
		got, err := QualifiedLink(g, paths, tt.from, tt.target)
		if tt.err != "" {
			if err == nil || err.Error() != tt.err || got != "" {
				t.Errorf("%s -> %s: got %q, %v; want error %q", tt.from, tt.target, got, err, tt.err)
			}
			continue
		}
		if err != nil || got != tt.want {
			t.Errorf("%s -> %s: got %q, %v; want %q", tt.from, tt.target, got, err, tt.want)
			continue
		}
		parsed := Parse(got, 1)
		if len(parsed) != 1 || parsed[0].Embed {
			t.Errorf("%q parsed as %+v", got, parsed)
			continue
		}
		if resolved, ok := g.Resolve(tt.from, parsed[0].Target); !ok || resolved != tt.target {
			t.Errorf("%q resolves to %q, %v", got, resolved, ok)
		}
	}
	// A path the walk knows but the graph does not resolve is refused.
	if _, err := QualifiedLink(g, append(paths, "nn/ghost.md"), "nn/digest.md", "nn/ghost.md"); err == nil || err.Error() != "qualified link does not resolve to the intended target" {
		t.Fatalf("unresolved target: %v", err)
	}
	if _, err := QualifiedLink(nil, paths, "nn/digest.md", "nn/a.md"); err == nil {
		t.Fatal("nil graph accepted")
	}
}
