package main

import (
	"strings"
	"testing"
)

func TestTagsCountsDescending(t *testing.T) {
	root := newTestVault(t)
	writeNote(t, root, "nn/a.md", "---\ntags: [docker]\n---\n\nbody\n")
	writeNote(t, root, "nn/b.md", "---\ntags: [docker, go]\n---\n\nbody\n")
	writeNote(t, root, "nn/c.md", "---\ntags: [go]\n---\n\nbody\n")

	stdout, _, code := runCmd(t, "", "tags")
	if code != 0 {
		t.Fatalf("code = %d", code)
	}
	lines := strings.Split(strings.TrimRight(stdout, "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("lines = %v", lines)
	}
	// docker and go are both used twice; tie broken alphabetically.
	if !strings.Contains(lines[0], "docker") || !strings.Contains(lines[0], "2") {
		t.Errorf("lines[0] = %q", lines[0])
	}
	if !strings.Contains(lines[1], "go") || !strings.Contains(lines[1], "2") {
		t.Errorf("lines[1] = %q", lines[1])
	}
}

func TestTagsEmptyVaultExitsOne(t *testing.T) {
	newTestVault(t)
	_, _, code := runCmd(t, "", "tags")
	if code != 1 {
		t.Errorf("code = %d, want 1", code)
	}
}

func TestTagsNamesOnly(t *testing.T) {
	root := newTestVault(t)
	writeNote(t, root, "nn/a.md", "---\ntags: [docker]\n---\n\nbody\n")

	stdout, _, code := runCmd(t, "", "tags", "--names")
	if code != 0 {
		t.Fatalf("code = %d", code)
	}
	if strings.TrimSpace(stdout) != "docker" {
		t.Errorf("stdout = %q, want just the bare name", stdout)
	}
}

func TestTagsSimilar(t *testing.T) {
	root := newTestVault(t)
	writeNote(t, root, "nn/a.md", "---\ntags: [go]\n---\n\nbody\n")
	writeNote(t, root, "nn/b.md", "---\ntags: [golang]\n---\n\nbody\n")

	stdout, _, code := runCmd(t, "", "tags", "--similar")
	if code != 0 {
		t.Fatalf("code = %d", code)
	}
	if !strings.Contains(stdout, "go~golang") && !strings.Contains(stdout, "golang~go") {
		t.Errorf("stdout = %q, want a go~golang pair", stdout)
	}
}

func TestTagsJSONAndTSV(t *testing.T) {
	root := newTestVault(t)
	writeNote(t, root, "nn/a.md", "---\ntags: [docker]\n---\n\nbody\n")

	stdout, _, code := runCmd(t, "", "tags", "--json")
	if code != 0 {
		t.Fatalf("code = %d", code)
	}
	rows := decodeJSONRows[tagRow](t, stdout)
	if len(rows) != 1 || rows[0].Tag != "docker" || rows[0].Count != 1 {
		t.Errorf("rows = %+v", rows)
	}

	stdout, _, code = runCmd(t, "", "tags", "--tsv")
	if code != 0 {
		t.Fatalf("code = %d", code)
	}
	if !strings.Contains(stdout, "tag\tcount\n") || !strings.Contains(stdout, "docker\t1\n") {
		t.Errorf("stdout = %q", stdout)
	}
}

func TestTagsEmptyJSONIsAnArray(t *testing.T) {
	newTestVault(t)

	stdout, _, code := runCmd(t, "", "tags", "--json")
	if code != 1 {
		t.Errorf("code = %d, want 1", code)
	}
	if got := strings.TrimSpace(stdout); got != "[]" {
		t.Errorf("stdout = %q, want []", got)
	}
	if rows := decodeJSONRows[tagRow](t, stdout); len(rows) != 0 {
		t.Errorf("rows = %+v, want none", rows)
	}
}

func TestTagsSimilarEmptyJSONIsAnArray(t *testing.T) {
	root := newTestVault(t)
	writeNote(t, root, "nn/a.md", "---\ntags: [docker]\n---\n\nbody\n")
	writeNote(t, root, "nn/b.md", "---\ntags: [kubernetes]\n---\n\nbody\n")

	stdout, _, code := runCmd(t, "", "tags", "--similar", "--json")
	if code != 1 {
		t.Errorf("code = %d, want 1", code)
	}
	if got := strings.TrimSpace(stdout); got != "[]" {
		t.Errorf("stdout = %q, want []", got)
	}
	if rows := decodeJSONRows[tagSimilarRow](t, stdout); len(rows) != 0 {
		t.Errorf("rows = %+v, want none", rows)
	}
}

func TestTagsEmptyTextPrintsNothing(t *testing.T) {
	root := newTestVault(t)

	stdout, _, code := runCmd(t, "", "tags")
	if code != 1 {
		t.Errorf("code = %d, want 1", code)
	}
	if stdout != "" {
		t.Errorf("stdout = %q, want nothing", stdout)
	}

	writeNote(t, root, "nn/a.md", "---\ntags: [docker]\n---\n\nbody\n")
	writeNote(t, root, "nn/b.md", "---\ntags: [kubernetes]\n---\n\nbody\n")

	stdout, _, code = runCmd(t, "", "tags", "--similar")
	if code != 1 {
		t.Errorf("--similar code = %d, want 1", code)
	}
	if stdout != "" {
		t.Errorf("--similar stdout = %q, want nothing", stdout)
	}
}
