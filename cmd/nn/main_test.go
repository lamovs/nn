package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/lamovs/nn/internal/ai"
	"github.com/lamovs/nn/internal/config"
)

// TestMain points HOME, the XDG_* variables and PATH at a throwaway sandbox before any test
// runs, so no test - with or without its own newTestVault - can read or write the developer's
// real ~/.config/nn, or reach a real claude, codex or notification tool through PATH. It clears
// NO_COLOR, TERM and NN_AI too, and checks afterwards that the real config and lock dirs gained nothing.
func TestMain(m *testing.M) {
	code, err := runIsolated(m)
	if err != nil {
		fmt.Fprintln(os.Stderr, "nn: cmd/nn TestMain:", err)
		os.Exit(1)
	}
	os.Exit(code)
}

var realTools = []string{"sh", "zsh", "touch", "sleep"}

var guardedTools = []string{"claude", "codex", "osascript", "notify-send", "screencapture"}

var testBin string

func runIsolated(m *testing.M) (int, error) {
	// macOS screen capture uses an absolute path, bypassing PATH isolation; the stub fails closed.
	captureScreenshot = func(context.Context, config.Config) ([]byte, string, error) {
		return nil, "", fmt.Errorf("screenshot requires a test stub")
	}
	captureClipboardImage = func(context.Context) ([]byte, string, error) {
		return nil, "", fmt.Errorf("clipboard image requires a test stub")
	}
	captureClipboardText = func(context.Context) ([]byte, error) { return nil, fmt.Errorf("clipboard text requires a test stub") }
	realHome, err := os.UserHomeDir()
	if err != nil {
		return 0, err
	}
	watched := []string{
		filepath.Join(realHome, ".config", "nn"),
		filepath.Join(realHome, ".local", "share", "nn", "locks"),
	}
	before := make([]string, len(watched))
	for i, dir := range watched {
		before[i] = listing(dir)
	}

	links := map[string]string{}
	for _, name := range realTools {
		path, err := exec.LookPath(name)
		if err != nil {
			if name == "zsh" {
				continue
			}
			return 0, err
		}
		links[name] = path
	}

	dir, err := os.MkdirTemp("", "nn-cmd-test-*")
	if err != nil {
		return 0, err
	}
	defer os.RemoveAll(dir)
	testBin = filepath.Join(dir, "bin")
	if err := os.Mkdir(testBin, 0o755); err != nil {
		return 0, err
	}
	for name, target := range links {
		if err := os.Symlink(target, filepath.Join(testBin, name)); err != nil {
			return 0, err
		}
	}
	for key, value := range map[string]string{
		"PATH":            testBin,
		"HOME":            dir,
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
	for _, key := range []string{"NO_COLOR", "TERM", "NN_AI"} {
		if err := os.Unsetenv(key); err != nil {
			return 0, err
		}
	}
	setupAsker = &scriptedAsker{}

	code := m.Run()
	for i, dir := range watched {
		if after := listing(dir); after != before[i] {
			return 0, fmt.Errorf("the tests changed %s:\nbefore: %s\nafter: %s", dir, before[i], after)
		}
	}
	return code, nil
}

func TestScreenshotRequiresStub(t *testing.T) {
	_, _, err := captureScreenshot(context.Background(), config.Config{})
	if err == nil || err.Error() != "screenshot requires a test stub" {
		t.Fatalf("screenshot default must be a fail-closed stub: %v", err)
	}
}

func listing(dir string) string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "absent"
	}
	var b strings.Builder
	for _, e := range entries {
		info, err := e.Info()
		if err != nil {
			continue
		}
		fmt.Fprintf(&b, "%s %d %d; ", e.Name(), info.Size(), info.ModTime().UnixNano())
	}
	return b.String()
}

func TestNoRealEnginesInPath(t *testing.T) {
	if got := os.Getenv("PATH"); got != testBin {
		t.Fatalf("PATH = %q, want only %q", got, testBin)
	}
	for _, name := range guardedTools {
		if path, err := exec.LookPath(name); err == nil {
			t.Errorf("%s is reachable through PATH at %s", name, path)
		}
	}
	if _, tty := setupAsker.(ttyAsker); tty || setupAsker.Interactive() {
		t.Errorf("setupAsker = %#v, want a script with no terminal", setupAsker)
	}
}

func fakeEngines(t *testing.T, names ...string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the stand-in engines are shell scripts")
	}
	dir := t.TempDir()
	const script = "#!/bin/sh\n: > \"$0.ran\"\necho \"nn test: $0 is a stand-in and must not run\" >&2\nexit 97\n"
	for _, name := range names {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Cleanup(func() {
		for _, name := range names {
			if _, err := os.Stat(filepath.Join(dir, name+".ran")); err == nil {
				t.Errorf("the stand-in %s was run", name)
			}
		}
	})
	return dir
}

type scriptedAsker struct {
	interactive bool
	answers     []string
	err         error
	questions   []string
}

func (a *scriptedAsker) Interactive() bool { return a.interactive }

func (a *scriptedAsker) Ask(question string) (string, error) {
	a.questions = append(a.questions, question)
	switch {
	case a.err != nil:
		return "", a.err
	case len(a.questions) > len(a.answers):
		return "", io.EOF
	}
	return a.answers[len(a.questions)-1], nil
}

func withAsker(t *testing.T, asker ai.Asker) {
	t.Helper()
	prev := setupAsker
	setupAsker = asker
	t.Cleanup(func() { setupAsker = prev })
}
