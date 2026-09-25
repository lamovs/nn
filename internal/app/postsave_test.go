package app

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func captureStderr(t *testing.T) func() string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	orig := os.Stderr
	os.Stderr = w
	t.Cleanup(func() {
		if os.Stderr == w {
			os.Stderr = orig
		}
	})
	return func() string {
		w.Close()
		os.Stderr = orig
		var buf bytes.Buffer
		io.Copy(&buf, r)
		return buf.String()
	}
}

func TestPostSaveRunsHookWithEnv(t *testing.T) {
	env := newTestEnv(t)
	writeNote(t, env.Vault.Root, "nn/x.md", "# X\n")
	out := filepath.Join(t.TempDir(), "out.txt")
	env.Cfg.Hooks.PostSave = fmt.Sprintf(`printf '%%s|%%s|%%s' "$NN_NOTE" "$NN_ROOT" "$NN_ACTION" > %q`, out)

	if err := env.PostSave(context.Background(), "nn/x.md", "create"); err != nil {
		t.Fatalf("PostSave returned an error: %v", err)
	}

	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("hook did not run: %v", err)
	}
	want := env.Vault.Abs("nn/x.md") + "|" + env.Vault.Root + "|create"
	if string(data) != want {
		t.Errorf("hook saw %q, want %q", data, want)
	}
}

func TestPostSaveHookFailureIsOnlyAWarning(t *testing.T) {
	env := newTestEnv(t)
	env.Cfg.Hooks.PostSave = "exit 7"

	stop := captureStderr(t)
	err := env.PostSave(context.Background(), "nn/x.md", "append")
	stderr := stop()

	if err != nil {
		t.Fatalf("PostSave returned an error: %v, want nil (only a warning)", err)
	}
	if !strings.Contains(stderr, "post_save hook") {
		t.Errorf("stderr = %q, want a warning mentioning the hook", stderr)
	}
}

func TestPostSaveTimesOut(t *testing.T) {
	env := newTestEnv(t)
	env.Cfg.Hooks.PostSave = "sleep 5"
	env.Cfg.Hooks.PostSaveTimeout = 50 * time.Millisecond

	start := time.Now()
	if err := env.PostSave(context.Background(), "nn/x.md", "create"); err != nil {
		t.Fatalf("PostSave returned an error: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Errorf("PostSave took %s, want it to give up around the timeout", elapsed)
	}
}

func TestPostSaveNoHookIsANoop(t *testing.T) {
	env := newTestEnv(t)
	if err := env.PostSave(context.Background(), "nn/x.md", "create"); err != nil {
		t.Fatalf("PostSave returned an error: %v", err)
	}
}
