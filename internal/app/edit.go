package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/lamovs/nn/internal/config"
	"github.com/lamovs/nn/internal/editor"
)

var ErrEmptyDraft = errors.New("draft is empty")

var runCommand = func(ctx context.Context, name string, args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = stdin, stdout, stderr
	return cmd.Run()
}

var lookPath = exec.LookPath

// resolveEditor: cfg.Editor.Command, $VISUAL, $EDITOR, then nvim or vi.
func resolveEditor(cfg config.Config) ([]string, error) {
	for _, v := range []string{cfg.Editor.Command, os.Getenv("VISUAL"), os.Getenv("EDITOR")} {
		if strings.TrimSpace(v) == "" {
			continue
		}
		return editor.Command(v)
	}
	for _, name := range []string{"nvim", "vi"} {
		if _, err := lookPath(name); err == nil {
			return []string{name}, nil
		}
	}
	return nil, errors.New(`no editor found; set VISUAL, EDITOR, or editor.command in config (for example: vi or code --wait)`)
}

// Edit opens path at line if the editor takes one; line <= 0 opens without.
func Edit(ctx context.Context, env *Env, path string, line int) error {
	args, err := resolveEditor(env.Cfg)
	if err != nil {
		return err
	}
	full := append(append([]string{}, args...), editor.LineArgs(filepath.Base(args[0]), line)...)
	full = append(full, path)
	if err := runCommand(ctx, full[0], full[1:], os.Stdin, os.Stdout, os.Stderr); err != nil {
		return fmt.Errorf("editor failed: %w", err)
	}
	return nil
}

// EditDraft opens a scratch .md file preloaded with initial; see ErrEmptyDraft.
func EditDraft(ctx context.Context, env *Env, initial string) (string, error) {
	args, err := resolveEditor(env.Cfg)
	if err != nil {
		return "", err
	}

	draft, err := editor.NewDraft([]byte(initial))
	if err != nil {
		return "", err
	}
	defer draft.Discard()

	full := append(append([]string{}, args...), draft.Path)
	if err := runCommand(ctx, full[0], full[1:], os.Stdin, os.Stdout, os.Stderr); err != nil {
		return "", fmt.Errorf("editor failed: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if err := draft.ReadBack(); err != nil {
		return "", err
	}
	if strings.TrimSpace(string(draft.Data)) == "" {
		return "", ErrEmptyDraft
	}
	return string(draft.Data), nil
}
