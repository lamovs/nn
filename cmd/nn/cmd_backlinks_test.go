package main

import (
	"strings"
	"testing"
)

func TestBacklinksEndOfOptions(t *testing.T) {
	root := newTestVault(t)
	writeNote(t, root, "nn/plank.md", "---\naliases: [-5 min plank]\n---\n\n# Plank\n\nbody\n")
	writeNote(t, root, "nn/a.md", "# A\n\n[[plank]]\n")

	stdout, stderr, code := runCmd(t, "", "backlinks", "--", "-5", "min", "plank")
	if code != 0 {
		t.Fatalf("code = %d, stderr = %q", code, stderr)
	}
	if !strings.Contains(stdout, "<- nn/a.md:3") {
		t.Errorf("stdout = %q, want the backlink from nn/a.md:3", stdout)
	}

	if _, stderr, code := runCmd(t, "", "backlinks", "--", "plank"); code != 0 {
		t.Errorf("backlinks -- plank: code = %d, stderr = %q", code, stderr)
	}
}

func TestBacklinksEmptyJSONIsAnArray(t *testing.T) {
	root := newTestVault(t)
	writeNote(t, root, "nn/a.md", "# A\n\nnothing links here\n")

	stdout, _, code := runCmd(t, "", "backlinks", "a", "--json")
	if code != 1 {
		t.Errorf("code = %d, want 1", code)
	}
	if got := strings.TrimSpace(stdout); got != "[]" {
		t.Errorf("stdout = %q, want []", got)
	}
}
