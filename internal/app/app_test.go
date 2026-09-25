package app

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/lamovs/nn/internal/state"
)

func TestOpenRequiresRootAndBuildsEnv(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("NN_CONFIG", "")
	t.Setenv("NN_ROOT", "")
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "xdg-config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, "xdg-data"))

	if _, err := Open(); err == nil {
		t.Fatal("expected an error when root is not set")
	}

	root := t.TempDir()
	t.Setenv("NN_ROOT", root)

	env, err := Open()
	if err != nil {
		t.Fatal(err)
	}
	if env.Vault == nil || env.Vault.Root != root {
		t.Errorf("Vault = %+v", env.Vault)
	}
	if env.State == nil {
		t.Fatal("State is nil")
	}
	if env.Now == nil {
		t.Fatal("Now is nil")
	}
}

func TestOpenSurvivesUnreadableState(t *testing.T) {
	home := t.TempDir()
	data := filepath.Join(home, "xdg-data")
	t.Setenv("HOME", home)
	t.Setenv("NN_CONFIG", "")
	t.Setenv("NN_ROOT", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "xdg-config"))
	t.Setenv("XDG_DATA_HOME", data)

	// A directory makes Load fail as unreadable, not not-found.
	if err := os.MkdirAll(filepath.Join(data, "nn", "state.json"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := state.Load(14 * 24 * time.Hour); err == nil {
		t.Fatal("state.Load succeeded; the test no longer exercises a load failure")
	}

	env, err := Open()
	if err != nil {
		t.Fatalf("Open = %v, want a warning and an empty history", err)
	}
	if env.State == nil {
		t.Fatal("State is nil")
	}
	if f := env.State.Frecency("nn/a.md"); f != 0 {
		t.Errorf("Frecency = %v, want an empty history", f)
	}
	env.RecordOpen("nn/a.md")
}

func TestNotesAndDocsAreCachedPerEnv(t *testing.T) {
	env := newTestEnv(t)
	writeNote(t, env.Vault.Root, "nn/one.md", "# One\n\nFirst note.\n")

	ctx := context.Background()
	notes, err := env.Notes(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(notes) != 1 {
		t.Fatalf("Notes = %d, want 1", len(notes))
	}
	docs, err := env.Docs(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(docs) != 1 {
		t.Fatalf("Docs = %d, want 1", len(docs))
	}

	// Not re-read: this note must not show up in a second load.
	writeNote(t, env.Vault.Root, "nn/two.md", "# Two\n")

	notes, err = env.Notes(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(notes) != 1 {
		t.Errorf("Notes after cache warm = %d, want the cached 1", len(notes))
	}
	docs, err = env.Docs(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(docs) != 1 {
		t.Errorf("Docs after cache warm = %d, want the cached 1", len(docs))
	}
}
