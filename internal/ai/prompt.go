package ai

import (
	"embed"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"slices"
	"strings"

	"github.com/lamovs/nn/internal/config"
)

//go:embed prompts/*.md
var embeddedPrompts embed.FS

var builtinPrompts fs.FS = embeddedPrompts // a variable so tests can stand in their own

const maxPromptFile = 64 << 10 // claude gets the prompt as one argument

func Prompt(cfg config.Config, task string) (string, error) {
	if !slices.Contains(config.Tasks(), task) {
		return "", fmt.Errorf("ai: unknown task %q", task)
	}
	if path := cfg.AI.Tasks[task].PromptFile; path != "" {
		return readPromptFile(task, path)
	}
	data, err := fs.ReadFile(builtinPrompts, "prompts/"+task+".md")
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return "", fmt.Errorf("%s %w; set ai.tasks.%s.prompt_file", task, ErrNoPrompt, task)
	case err != nil:
		return "", err
	}
	return strings.TrimSpace(string(data)), nil
}

// CheckPromptFile reads the file and runs nothing.
func CheckPromptFile(cfg config.Config, task string) error {
	if path := cfg.AI.Tasks[task].PromptFile; path != "" {
		_, err := readPromptFile(task, path)
		return err
	}
	return nil
}

func readPromptFile(task, path string) (string, error) {
	key := "ai.tasks." + task + ".prompt_file"
	// A FIFO would block the open; only a regular file is opened.
	info, err := os.Stat(path)
	if err != nil {
		return "", fmt.Errorf("%s: %w", key, err)
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("%s: %s is not a regular file", key, path)
	}
	f, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("%s: %w", key, err)
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, maxPromptFile+1))
	if err != nil {
		return "", fmt.Errorf("%s: %w", key, err)
	}
	if len(data) > maxPromptFile {
		return "", fmt.Errorf("%s: %s is larger than %d KiB", key, path, maxPromptFile>>10)
	}
	text := strings.TrimSpace(string(data))
	if text == "" {
		return "", fmt.Errorf("%s: %s is empty", key, path)
	}
	return text, nil
}
