package main

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/lamovs/nn/internal/state"
)

func TestEditResolvesAndOpensRecordingOpen(t *testing.T) {
	root := newTestVault(t)
	writeNote(t, root, "nn/docker-cleanup.md", "---\naliases: [Docker cleanup]\ndate: 2026-01-01\n---\n\nbody\n")
	record := installFakeEditor(t)

	_, stderr, code := runCmd(t, "", "edit", "docker", "cleanup")
	if code != 0 {
		t.Fatalf("code = %d, stderr = %q", code, stderr)
	}
	data, err := os.ReadFile(record)
	if err != nil {
		t.Fatalf("editor was not invoked: %v", err)
	}
	if !strings.Contains(string(data), "docker-cleanup.md") {
		t.Errorf("recorded editor args = %q, want the note's path", string(data))
	}

	statePath, err := state.Path()
	if err != nil {
		t.Fatal(err)
	}
	st, err := state.LoadFile(statePath, 14*24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if f := st.Frecency("nn/docker-cleanup.md"); f <= 0 {
		t.Errorf("Frecency = %v, want the open to have been recorded", f)
	}
}

func TestEditTrackOpensFalseDoesNotWriteState(t *testing.T) {
	root := newTestVault(t)
	writeConfig(t, "[state]\ntrack_opens = false\n")
	writeNote(t, root, "nn/docker-cleanup.md", "---\naliases: [Docker cleanup]\ndate: 2026-01-01\n---\n\nbody\n")
	record := installFakeEditor(t)

	_, stderr, code := runCmd(t, "", "edit", "docker", "cleanup")
	if code != 0 {
		t.Fatalf("code = %d, stderr = %q", code, stderr)
	}
	if _, err := os.ReadFile(record); err != nil {
		t.Fatalf("editor was not invoked: %v", err)
	}

	statePath, err := state.Path()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(statePath); !os.IsNotExist(err) {
		t.Errorf("state.json exists despite state.track_opens = false: %v", err)
	}
}

func TestEditNoArgsOpensMostRecentInboxNote(t *testing.T) {
	root := newTestVault(t)
	writeNote(t, root, "nn/old.md", "old\n")
	writeNote(t, root, "nn/new.md", "new\n")
	setModified(t, root, "nn/old.md", time.Now().Add(-2*time.Hour))
	setModified(t, root, "nn/new.md", time.Now())
	record := installFakeEditor(t)

	_, stderr, code := runCmd(t, "", "edit")
	if code != 0 {
		t.Fatalf("code = %d, stderr = %q", code, stderr)
	}
	data, err := os.ReadFile(record)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "new.md") || strings.Contains(string(data), "old.md") {
		t.Errorf("recorded editor args = %q, want new.md (most recently modified)", string(data))
	}
}

func TestEditNoArgsEmptyInboxFails(t *testing.T) {
	newTestVault(t)
	_, stderr, code := runCmd(t, "", "edit")
	if code != 2 {
		t.Errorf("code = %d, want 2, stderr = %q", code, stderr)
	}
}

func TestEditLineFlagPassesLineNumber(t *testing.T) {
	root := newTestVault(t)
	writeNote(t, root, "nn/docker.md", "body\n")
	record := installFakeEditor(t)

	_, stderr, code := runCmd(t, "", "edit", "docker", "-l", "12")
	if code != 0 {
		t.Fatalf("code = %d, stderr = %q", code, stderr)
	}
	data, err := os.ReadFile(record)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "+12") {
		t.Errorf("recorded editor args = %q, want +12", string(data))
	}
}

func TestEditNotFoundFails(t *testing.T) {
	newTestVault(t)
	_, stderr, code := runCmd(t, "", "edit", "does-not-exist")
	if code != 2 {
		t.Errorf("code = %d, want 2, stderr = %q", code, stderr)
	}
}

func TestEditAmbiguousListsCandidates(t *testing.T) {
	root := newTestVault(t)
	writeNote(t, root, "nn/a/docker.md", "a\n")
	writeNote(t, root, "nn/b/docker.md", "b\n")

	_, stderr, code := runCmd(t, "", "edit", "docker")
	if code != 2 {
		t.Fatalf("code = %d, want 2, stderr = %q", code, stderr)
	}
	if !strings.Contains(stderr, "nn/a/docker.md") || !strings.Contains(stderr, "nn/b/docker.md") {
		t.Errorf("stderr = %q, want both candidates listed", stderr)
	}
}

func TestEditUnknownOptionIsUsageError(t *testing.T) {
	newTestVault(t)
	_, stderr, code := runCmd(t, "", "edit", "--bogus")
	if code != 2 {
		t.Errorf("code = %d, want 2, stderr = %q", code, stderr)
	}
}
