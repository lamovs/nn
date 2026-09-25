package app

import (
	"context"
	"errors"
	"testing"

	"github.com/lamovs/nn/internal/vault"
)

func TestResolveExactPathStemAndAlias(t *testing.T) {
	env := newTestEnv(t)
	writeNote(t, env.Vault.Root, "nn/widget.md", "---\naliases: [gizmo]\n---\n\n# Widget\n\nBody text.\n")
	ctx := context.Background()

	if rel, err := env.Resolve(ctx, "nn/widget.md"); err != nil || rel != "nn/widget.md" {
		t.Fatalf("exact path: rel=%q err=%v", rel, err)
	}
	if rel, err := env.Resolve(ctx, "widget"); err != nil || rel != "nn/widget.md" {
		t.Fatalf("stem: rel=%q err=%v", rel, err)
	}
	if rel, err := env.Resolve(ctx, "gizmo"); err != nil || rel != "nn/widget.md" {
		t.Fatalf("alias: rel=%q err=%v", rel, err)
	}
}

func TestResolveFallsBackToSearch(t *testing.T) {
	env := newTestEnv(t)
	writeNote(t, env.Vault.Root, "nn/gadget.md", "# Gadget\n\nSome highly unique token xyzzyword shows up here.\n")

	rel, err := env.Resolve(context.Background(), "xyzzyword")
	if err != nil {
		t.Fatal(err)
	}
	if rel != "nn/gadget.md" {
		t.Errorf("rel = %q, want nn/gadget.md", rel)
	}
}

func TestResolveNotFound(t *testing.T) {
	env := newTestEnv(t)
	writeNote(t, env.Vault.Root, "nn/gadget.md", "# Gadget\n\nNothing relevant in here.\n")

	if _, err := env.Resolve(context.Background(), "nowhere-at-all"); !errors.Is(err, vault.ErrNotFound) {
		t.Fatalf("err = %v, want vault.ErrNotFound", err)
	}
}

func TestResolveAmbiguousPassesThrough(t *testing.T) {
	env := newTestEnv(t)
	writeNote(t, env.Vault.Root, "nn/a/dup.md", "# A\n")
	writeNote(t, env.Vault.Root, "nn/b/dup.md", "# B\n")

	_, err := env.Resolve(context.Background(), "dup")
	var ambErr *vault.AmbiguousError
	if !errors.As(err, &ambErr) {
		t.Fatalf("err = %v, want *vault.AmbiguousError", err)
	}
}
