package main

import (
	"strings"
	"testing"
)

func TestLinksOutgoingResolvedAndUnresolved(t *testing.T) {
	root := newTestVault(t)
	writeNote(t, root, "nn/a.md", "# A\n\nSee [[b]] and [[nowhere]].\n")
	writeNote(t, root, "nn/b.md", "# B\n\nbody\n")

	stdout, _, code := runCmd(t, "", "links", "a")
	if code != 0 {
		t.Fatalf("code = %d", code)
	}
	if !strings.Contains(stdout, "-> nn/b.md") {
		t.Errorf("stdout = %q, want the resolved link", stdout)
	}
	if !strings.Contains(stdout, "-> nowhere (unresolved)") {
		t.Errorf("stdout = %q, want the unresolved link", stdout)
	}
}

func TestLinksNoOutgoingIsNotFound(t *testing.T) {
	root := newTestVault(t)
	writeNote(t, root, "nn/a.md", "# A\n\nno links here\n")
	_, _, code := runCmd(t, "", "links", "a")
	if code != 1 {
		t.Errorf("code = %d, want 1", code)
	}
}

func TestLinksUnresolvedWholeVault(t *testing.T) {
	root := newTestVault(t)
	writeNote(t, root, "nn/a.md", "# A\n\n[[gone]]\n")
	writeNote(t, root, "nn/b.md", "# B\n\n[[missing]]\n")

	stdout, _, code := runCmd(t, "", "links", "--unresolved")
	if code != 0 {
		t.Fatalf("code = %d", code)
	}
	if !strings.Contains(stdout, "nn/a.md") || !strings.Contains(stdout, "gone (unresolved)") {
		t.Errorf("stdout = %q, want a.md's broken link listed with its source", stdout)
	}
	if !strings.Contains(stdout, "nn/b.md") || !strings.Contains(stdout, "missing (unresolved)") {
		t.Errorf("stdout = %q, want b.md's broken link listed with its source", stdout)
	}
}

func TestLinksNoArgsAndNoUnresolvedIsUsageError(t *testing.T) {
	newTestVault(t)
	_, _, code := runCmd(t, "", "links")
	if code != 2 {
		t.Errorf("code = %d, want 2", code)
	}
}

func TestLinksJSON(t *testing.T) {
	root := newTestVault(t)
	writeNote(t, root, "nn/a.md", "# A\n\n[[b]]\n")
	writeNote(t, root, "nn/b.md", "# B\n\nbody\n")

	stdout, _, code := runCmd(t, "", "links", "a", "--json")
	if code != 0 {
		t.Fatalf("code = %d", code)
	}
	rows := decodeJSONRows[linksRow](t, stdout)
	if len(rows) != 1 || !rows[0].Resolved || rows[0].To != "nn/b.md" {
		t.Errorf("rows = %+v", rows)
	}
}

func TestBacklinksIncoming(t *testing.T) {
	root := newTestVault(t)
	writeNote(t, root, "nn/a.md", "# A\n\n[[b]]\n")
	writeNote(t, root, "nn/b.md", "# B\n\nbody\n")

	stdout, _, code := runCmd(t, "", "backlinks", "b")
	if code != 0 {
		t.Fatalf("code = %d", code)
	}
	if !strings.Contains(stdout, "<- nn/a.md:3") {
		t.Errorf("stdout = %q, want a backlink from nn/a.md:3", stdout)
	}
}

func TestBacklinksNoneIsNotFound(t *testing.T) {
	root := newTestVault(t)
	writeNote(t, root, "nn/a.md", "# A\n\nbody\n")
	_, _, code := runCmd(t, "", "backlinks", "a")
	if code != 1 {
		t.Errorf("code = %d, want 1", code)
	}
}

func TestBacklinksNoArgsIsUsageError(t *testing.T) {
	newTestVault(t)
	_, _, code := runCmd(t, "", "backlinks")
	if code != 2 {
		t.Errorf("code = %d, want 2", code)
	}
}

func TestBacklinksNotFoundNote(t *testing.T) {
	newTestVault(t)
	_, _, code := runCmd(t, "", "backlinks", "nowhere-at-all")
	if code != 2 {
		t.Errorf("code = %d, want 2", code)
	}
}

func TestLinksEmptyJSONIsAnArray(t *testing.T) {
	root := newTestVault(t)
	writeNote(t, root, "nn/a.md", "# A\n\nno links here\n")

	stdout, _, code := runCmd(t, "", "links", "a", "--json")
	if code != 1 {
		t.Errorf("code = %d, want 1", code)
	}
	if got := strings.TrimSpace(stdout); got != "[]" {
		t.Errorf("stdout = %q, want []", got)
	}
	if rows := decodeJSONRows[linksRow](t, stdout); len(rows) != 0 {
		t.Errorf("rows = %+v, want none", rows)
	}
}

func TestLinksUnresolvedEmptyJSONIsAnArray(t *testing.T) {
	root := newTestVault(t)
	writeNote(t, root, "nn/a.md", "# A\n\n[[b]]\n")
	writeNote(t, root, "nn/b.md", "# B\n\nbody\n")

	stdout, _, code := runCmd(t, "", "links", "--unresolved", "--json")
	if code != 1 {
		t.Errorf("code = %d, want 1", code)
	}
	if got := strings.TrimSpace(stdout); got != "[]" {
		t.Errorf("stdout = %q, want []", got)
	}
}

func TestLinksEmptyTextPrintsNothing(t *testing.T) {
	root := newTestVault(t)
	writeNote(t, root, "nn/a.md", "# A\n\nno links here\n")

	stdout, _, code := runCmd(t, "", "links", "a")
	if code != 1 {
		t.Errorf("code = %d, want 1", code)
	}
	if stdout != "" {
		t.Errorf("stdout = %q, want nothing", stdout)
	}
}
