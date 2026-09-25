package ai

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
	"testing/fstest"

	"github.com/lamovs/nn/internal/config"
)

func withBuiltinPrompts(t *testing.T, prompts fstest.MapFS) {
	t.Helper()
	prev := builtinPrompts
	builtinPrompts = prompts
	t.Cleanup(func() { builtinPrompts = prev })
}

func withPromptFile(cfg config.Config, task, path string) config.Config {
	tc := cfg.AI.Tasks[task]
	tc.PromptFile = path
	cfg.AI.Tasks[task] = tc
	return cfg
}

func TestPromptBuiltinAndFile(t *testing.T) {
	withBuiltinPrompts(t, fstest.MapFS{"prompts/shot.md": {Data: []byte("Describe the screenshot.\n")}})
	cfg := config.Default()

	if got, err := Prompt(cfg, "shot"); err != nil || got != "Describe the screenshot." {
		t.Errorf("built-in: %q, %v", got, err)
	}

	file := filepath.Join(t.TempDir(), "shot.md")
	if err := os.WriteFile(file, []byte("  My own prompt.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got, err := Prompt(withPromptFile(cfg, "shot", file), "shot"); err != nil || got != "My own prompt." {
		t.Errorf("prompt_file: %q, %v", got, err)
	}

	_, err := Prompt(cfg, "title")
	if !errors.Is(err, ErrNoPrompt) || !strings.Contains(err.Error(), "ai.tasks.title.prompt_file") {
		t.Errorf("no prompt: err = %v", err)
	}
	if _, err := Prompt(cfg, "README"); err == nil || errors.Is(err, ErrNoPrompt) {
		t.Errorf("unknown task: err = %v", err)
	}
}

func TestCheckPromptFile(t *testing.T) {
	dir := t.TempDir()
	write := func(name string, data []byte) string {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, data, 0o644); err != nil {
			t.Fatal(err)
		}
		return path
	}
	fifo := filepath.Join(dir, "fifo")
	if err := syscall.Mkfifo(fifo, 0o644); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, path, want string
	}{
		{"missing", filepath.Join(dir, "missing.md"), "no such file"},
		{"directory", dir, "is not a regular file"},
		{"fifo", fifo, "is not a regular file"},
		{"empty", write("empty.md", []byte(" \n\t\n")), "is empty"},
		{"too large", write("large.md", []byte(strings.Repeat("x", maxPromptFile+1))), "larger than 64 KiB"},
	} {
		cfg := withPromptFile(config.Default(), "ask", tc.path)
		err := CheckPromptFile(cfg, "ask")
		if err == nil || !strings.Contains(err.Error(), tc.want) || !strings.HasPrefix(err.Error(), "ai.tasks.ask.prompt_file: ") {
			t.Errorf("%s: err = %v, want %q", tc.name, err, tc.want)
		}
		if _, perr := Prompt(cfg, "ask"); perr == nil || perr.Error() != err.Error() {
			t.Errorf("%s: Prompt says %v, CheckPromptFile %v", tc.name, perr, err)
		}
	}

	fits := write("fits.md", []byte(strings.Repeat("x", maxPromptFile)))
	if err := CheckPromptFile(withPromptFile(config.Default(), "ask", fits), "ask"); err != nil {
		t.Errorf("a file of exactly %d bytes: %v", maxPromptFile, err)
	}
	if err := CheckPromptFile(config.Default(), "ask"); err != nil {
		t.Errorf("no prompt_file: %v", err)
	}
}

func TestEmbeddedPromptsAreTasks(t *testing.T) {
	entries, err := fs.ReadDir(embeddedPrompts, "prompts")
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		name := e.Name()
		if task, ok := strings.CutSuffix(name, ".md"); !ok || !slices.Contains(config.Tasks(), task) {
			t.Errorf("prompts/%s is not TASK.md for a task", name)
		}
	}
}
