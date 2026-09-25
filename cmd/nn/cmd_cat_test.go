package main

import (
	"strings"
	"testing"
)

func TestCatPrintsBodyWithoutFrontmatter(t *testing.T) {
	root := newTestVault(t)
	writeNote(t, root, "nn/a.md", "---\ntags: [x]\n---\n\n# A\n\nbody text\n")

	stdout, _, code := runCmd(t, "", "cat", "a")
	if code != 0 {
		t.Fatalf("code = %d", code)
	}
	if strings.Contains(stdout, "tags:") {
		t.Errorf("stdout = %q, should not contain frontmatter", stdout)
	}
	if !strings.Contains(stdout, "body text") {
		t.Errorf("stdout = %q", stdout)
	}
}

func TestCatFullIncludesFrontmatter(t *testing.T) {
	root := newTestVault(t)
	writeNote(t, root, "nn/a.md", "---\ntags: [x]\n---\n\n# A\n\nbody text\n")

	stdout, _, code := runCmd(t, "", "cat", "--full", "a")
	if code != 0 {
		t.Fatalf("code = %d", code)
	}
	if !strings.Contains(stdout, "tags: [x]") {
		t.Errorf("stdout = %q, want frontmatter with --full", stdout)
	}
}

func TestCatMultipleNotesSeparatedByBlankLine(t *testing.T) {
	root := newTestVault(t)
	writeNote(t, root, "nn/a.md", "# A\n\nbody a\n")
	writeNote(t, root, "nn/b.md", "# B\n\nbody b\n")

	stdout, _, code := runCmd(t, "nn/a.md\nnn/b.md\n", "cat", "-")
	if code != 0 {
		t.Fatalf("code = %d", code)
	}
	if !strings.Contains(stdout, "body a\n\n# B") {
		t.Errorf("stdout = %q, want a blank line between the two bodies", stdout)
	}
}

func TestCatJoinsWords(t *testing.T) {
	root := newTestVault(t)
	writeNote(t, root, "nn/docker-cleanup.md", "---\naliases: [docker cleanup]\n---\n\n# Docker cleanup\n\nbody text\n")
	writeNote(t, root, "nn/docker.md", "# Docker\n\nanother note\n")

	stdout, _, code := runCmd(t, "", "cat", "docker", "cleanup")
	if code != 0 {
		t.Fatalf("code = %d, stdout = %q", code, stdout)
	}
	if n := strings.Count(stdout, "body text"); n != 1 {
		t.Errorf("stdout = %q, want the aliased note's body exactly once, got %d", stdout, n)
	}
	if strings.Contains(stdout, "another note") {
		t.Errorf("stdout = %q, must not also print the note called just docker", stdout)
	}
}

func TestCatFromStdin(t *testing.T) {
	root := newTestVault(t)
	writeNote(t, root, "nn/a.md", "# A\n\nbody a\n")
	writeNote(t, root, "nn/b.md", "# B\n\nbody b\n")

	stdout, _, code := runCmd(t, "nn/a.md\nnn/b.md\n", "cat", "-")
	if code != 0 {
		t.Fatalf("code = %d", code)
	}
	if !strings.Contains(stdout, "body a") || !strings.Contains(stdout, "body b") {
		t.Errorf("stdout = %q", stdout)
	}
}

func TestCatNotFoundFails(t *testing.T) {
	newTestVault(t)
	_, _, code := runCmd(t, "", "cat", "nowhere-at-all")
	if code != 2 {
		t.Errorf("code = %d, want 2", code)
	}
}

func TestCatNoArgsIsUsageError(t *testing.T) {
	newTestVault(t)
	_, _, code := runCmd(t, "", "cat")
	if code != 2 {
		t.Errorf("code = %d, want 2", code)
	}
}
