package triage

import (
	"testing"

	"github.com/lamovs/nn/internal/links"
)

// links.QualifiedLink is a port of (*Session).link for callers outside
// triage. This test keeps the two copies from drifting apart.
func TestQualifiedLinkParity(t *testing.T) {
	punct := "(alpha) & beta +_-.md"
	fixtures := []struct {
		files map[string]string
		extra []string // walk entries without a loadable note
	}{
		{files: map[string]string{"inbox/a.md": "A", "inbox/b#bad.md": "B"}},
		{files: map[string]string{
			"inbox/a.md":    "Manual [[./b`x`.md]] remains unchanged.\n",
			"inbox/b`x`.md": "Intended target.\n",
			"inbox/b   .md": "Different target after code masking.\n",
		}},
		{files: map[string]string{
			"inbox/deep/a.md":    "Source.\n",
			"reference/" + punct: "Intended target.\n",
			"other/" + punct:     "Different note with the same basename.\n",
		}},
		{files: map[string]string{"inbox/a.md": "A", "inbox/b.md": "B"}, extra: []string{"inbox/B.md"}},
		{files: map[string]string{
			"root.md":          "---\naliases: [deep]\n---\nRoot.\n",
			"inbox/a.md":       "[[deep]]\n",
			"inbox/sub/c.md":   "C.\n",
			"work/sub/deep.md": "Deep.\n",
			"work/review.md":   "[[./sub/deep]]\n",
		}},
	}
	checked := 0
	for i, fx := range fixtures {
		s := prepare(t, fixture(t, fx.files))
		s.notePaths = append(s.notePaths, fx.extra...)
		froms := append([]string{"inbox/digest.md", "digest.md"}, s.notePaths...)
		for _, from := range froms {
			for _, target := range s.notePaths {
				want, wantErr := s.link(&source{Path: from}, &source{Path: target})
				got, gotErr := links.QualifiedLink(s.graph, s.notePaths, from, target)
				if got != want || (gotErr == nil) != (wantErr == nil) || (gotErr != nil && gotErr.Error() != wantErr.Error()) {
					t.Errorf("fixture %d, %s -> %s: QualifiedLink = %q, %v; link = %q, %v", i, from, target, got, gotErr, want, wantErr)
				}
				checked++
			}
		}
	}
	if checked < 40 {
		t.Fatalf("only %d pairs checked", checked)
	}
}
