package app

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/lamovs/nn/internal/config"
	"github.com/lamovs/nn/internal/state"
	"github.com/lamovs/nn/internal/vault"
)

// newTestEnv never touches the real config or state locations.
func newTestEnv(t *testing.T) *Env {
	t.Helper()
	root := t.TempDir()
	cfg := config.Default()
	cfg.Vault.Root = root
	v, err := vault.Open(cfg)
	if err != nil {
		t.Fatal(err)
	}
	st, err := state.LoadFile(filepath.Join(t.TempDir(), "state.json"), cfg.State.HalfLife)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 16, 12, 0, 0, 0, time.UTC)
	return &Env{
		Cfg:   cfg,
		Vault: v,
		State: st,
		Now:   func() time.Time { return now },
	}
}

func writeNote(t *testing.T, root, rel, body string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}
