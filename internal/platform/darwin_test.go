package platform

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/lamovs/nn/internal/config"
)

func stubHelper(t *testing.T, path string, err error) {
	t.Helper()
	prev := findHelper
	findHelper = func() (string, error) { return path, err }
	t.Cleanup(func() { findHelper = prev })
}

func TestDarwinScreenshot(t *testing.T) {
	f := newFakeSystem(t, "darwin", map[string]string{})
	f.handle = func(c call) (string, string, error) {
		writeArg(t, c, pngData)
		return "", "", nil
	}
	data, ext, err := Screenshot(context.Background(), config.Config{})
	if err != nil || string(data) != pngData || ext != "png" {
		t.Fatalf("got %q %q %v", data, ext, err)
	}
	c := f.calls[0]
	if c.name != "/usr/sbin/screencapture" || !slices.Equal(c.args[:2], []string{"-i", "-x"}) {
		t.Errorf("ran %q", c.String())
	}
	if !c.group {
		t.Error("screencapture must use a cancellable process group")
	}
	if _, err := os.Stat(filepath.Dir(c.args[len(c.args)-1])); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("capture temporary directory remains: %v", err)
	}

	f.handle = nil // Esc: exit 0 and no file
	if _, _, err := Screenshot(context.Background(), config.Config{}); !errors.Is(err, ErrCancelled) {
		t.Errorf("err = %v", err)
	}
}

func TestDarwinScreenshotCancellationCleanup(t *testing.T) {
	for _, deadline := range []bool{false, true} {
		t.Run(fmtBool(deadline), func(t *testing.T) {
			newFakeSystem(t, "darwin", map[string]string{})
			ctx, cancel := context.WithCancel(context.Background())
			if deadline {
				ctx, cancel = context.WithTimeout(context.Background(), 20*time.Millisecond)
			}
			defer cancel()
			var dir string
			runCommand = func(ctx context.Context, c command) ([]byte, []byte, error) {
				path := c.args[len(c.args)-1]
				dir = filepath.Dir(path)
				if err := os.WriteFile(path, []byte("partial image"), 0o600); err != nil {
					t.Fatal(err)
				}
				if !deadline {
					cancel()
				}
				<-ctx.Done()
				return nil, nil, ctx.Err()
			}
			_, _, err := Screenshot(ctx, config.Config{})
			if !errors.Is(err, ErrCancelled) || !errors.Is(err, ctx.Err()) {
				t.Fatalf("cancellation = %v", err)
			}
			if _, err := os.Stat(dir); !errors.Is(err, os.ErrNotExist) {
				t.Errorf("capture temporary directory remains: %v", err)
			}
		})
	}
}

func fmtBool(deadline bool) string {
	if deadline {
		return "deadline"
	}
	return "cancel"
}

func TestDarwinClipboardImage(t *testing.T) {
	stubHelper(t, "/opt/nn/nn-vision", nil)
	f := newFakeSystem(t, "darwin", map[string]string{})
	f.handle = func(c call) (string, string, error) {
		writeArg(t, c, pngData)
		return "", "", nil
	}
	data, ext, err := ClipboardImage(context.Background())
	if err != nil || string(data) != pngData || ext != "png" {
		t.Fatalf("got %q %q %v", data, ext, err)
	}
	if c := f.calls[0]; c.name != "/opt/nn/nn-vision" || c.args[0] != "clipboard-image" {
		t.Errorf("ran %q", c.String())
	}

	f.handle = func(c call) (string, string, error) {
		return "", "nn-vision: no image on the clipboard\n", exitError(t, 3)
	}
	if _, _, err := ClipboardImage(context.Background()); !errors.Is(err, ErrNoImage) {
		t.Errorf("exit 3: err = %v", err)
	}

	f.handle = func(c call) (string, string, error) { return "", "boom\n", exitError(t, 1) }
	if _, _, err := ClipboardImage(context.Background()); err == nil || errors.Is(err, ErrNoImage) {
		t.Errorf("exit 1: err = %v", err)
	}

	stubHelper(t, "", errors.New("nn-vision helper not found"))
	if _, _, err := ClipboardImage(context.Background()); err == nil {
		t.Error("missing helper: want an error")
	}
}

func TestDarwinCopyTextForcesUTF8(t *testing.T) {
	f := newFakeSystem(t, "darwin", map[string]string{})
	if err := CopyText(context.Background(), "\u0442\u0435\u043a\u0441\u0442"); err != nil {
		t.Fatal(err)
	}
	c := f.calls[0]
	if c.name != "pbcopy" || c.stdin != "\u0442\u0435\u043a\u0441\u0442" || !slices.Contains(c.env, "LANG=en_US.UTF-8") {
		t.Errorf("ran %+v", c)
	}

	f = newFakeSystem(t, "darwin", map[string]string{"LANG": "ru_RU.UTF-8"})
	if err := CopyText(context.Background(), "x"); err != nil {
		t.Fatal(err)
	}
	if env := f.calls[0].env; len(env) != 0 {
		t.Errorf("UTF-8 locale was overridden: %q", env)
	}
}

func TestDarwinOpenURL(t *testing.T) {
	f := newFakeSystem(t, "darwin", map[string]string{})
	if err := OpenURL(context.Background(), "obsidian://open?path=%2Fx"); err != nil {
		t.Fatal(err)
	}
	if got := f.commands(); !slices.Equal(got, []string{"open obsidian://open?path=%2Fx"}) {
		t.Errorf("ran %q", got)
	}
}

func TestDarwinScreenAccess(t *testing.T) {
	stubHelper(t, "/opt/nn-vision", nil)
	f := newFakeSystem(t, "darwin", map[string]string{})
	for _, tt := range []struct {
		out            string
		err            error
		granted, known bool
	}{
		{"granted\n", nil, true, true},
		{"denied\n", nil, false, true},
		{"", exitError(t, 1), false, false},
	} {
		f.handle = func(c call) (string, string, error) { return tt.out, "", tt.err }
		if g, k := ScreenAccess(context.Background()); g != tt.granted || k != tt.known {
			t.Errorf("%q: got %v %v", tt.out, g, k)
		}
	}

	stubHelper(t, "", errors.New("missing"))
	if g, k := ScreenAccess(context.Background()); g || k {
		t.Errorf("missing helper: got %v %v", g, k)
	}
}
