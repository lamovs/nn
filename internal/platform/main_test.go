package platform

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// guardedTools would show something on the desktop or reach an AI service.
var guardedTools = []string{"osascript", "notify-send", "claude", "codex", "screencapture", "grim", "slurp", "spectacle", "gnome-screenshot", "maim", "import", "tesseract", "nn-vision"}

// TestMain keeps tests off the real desktop and home directory.
func TestMain(m *testing.M) {
	code, err := runIsolated(m)
	if err != nil {
		fmt.Fprintln(os.Stderr, "nn: internal/platform TestMain:", err)
		os.Exit(1)
	}
	os.Exit(code)
}

func runIsolated(m *testing.M) (int, error) {
	// Fail closed unless a test replaces the runner with its own fake.
	runCommand = func(_ context.Context, c command) ([]byte, []byte, error) {
		return nil, nil, fmt.Errorf("external command requires a test stub: %s", c.name)
	}
	startCommand = func(_ context.Context, c command) error {
		return fmt.Errorf("external launcher requires a test stub: %s", c.name)
	}
	sh, err := exec.LookPath("sh")
	if err != nil {
		return 0, err
	}
	dir, err := os.MkdirTemp("", "nn-platform-test-*")
	if err != nil {
		return 0, err
	}
	defer os.RemoveAll(dir)

	bin := filepath.Join(dir, "bin")
	home := filepath.Join(dir, "home")
	for _, d := range []string{bin, home} {
		if err := os.Mkdir(d, 0o755); err != nil {
			return 0, err
		}
	}
	if err := os.Symlink(sh, filepath.Join(bin, "sh")); err != nil {
		return 0, err
	}
	for key, value := range map[string]string{
		"PATH":            bin,
		"HOME":            home,
		"NN_CONFIG":       filepath.Join(dir, "config.toml"),
		"XDG_CONFIG_HOME": filepath.Join(dir, "xdg-config"),
		"XDG_DATA_HOME":   filepath.Join(dir, "xdg-data"),
		"XDG_CACHE_HOME":  filepath.Join(dir, "xdg-cache"),
		"XDG_STATE_HOME":  filepath.Join(dir, "xdg-state"),
	} {
		if err := os.Setenv(key, value); err != nil {
			return 0, err
		}
	}
	return m.Run(), nil
}

func TestAbsoluteScreencaptureRequiresStub(t *testing.T) {
	_, _, err := runCommand(context.Background(), command{name: screencapture})
	if err == nil {
		t.Fatal("absolute screencapture must be blocked without an explicit stub")
	}
}

func TestNoRealToolsInPath(t *testing.T) {
	for _, name := range guardedTools {
		if path, err := exec.LookPath(name); err == nil {
			t.Errorf("%s is reachable through PATH at %s", name, path)
		}
	}
}
