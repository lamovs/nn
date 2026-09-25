package app

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func withRunCommand(t *testing.T, fn func(ctx context.Context, name string, args []string) error) {
	t.Helper()
	old := runCommand
	runCommand = func(ctx context.Context, name string, args []string, _ io.Reader, _, _ io.Writer) error {
		return fn(ctx, name, args)
	}
	t.Cleanup(func() { runCommand = old })
}

func TestEditUsesConfiguredEditorAndLineNumber(t *testing.T) {
	env := newTestEnv(t)
	env.Cfg.Editor.Command = "vim -u NONE"

	var gotName string
	var gotArgs []string
	withRunCommand(t, func(_ context.Context, name string, args []string) error {
		gotName, gotArgs = name, args
		return nil
	})

	if err := Edit(context.Background(), env, "/tmp/note.md", 5); err != nil {
		t.Fatal(err)
	}
	if gotName != "vim" {
		t.Errorf("name = %q, want vim", gotName)
	}
	if want := []string{"-u", "NONE", "+5", "/tmp/note.md"}; !reflect.DeepEqual(gotArgs, want) {
		t.Errorf("args = %v, want %v", gotArgs, want)
	}
}

func TestEditMicroLineFormat(t *testing.T) {
	env := newTestEnv(t)
	env.Cfg.Editor.Command = "micro"

	var gotArgs []string
	withRunCommand(t, func(_ context.Context, _ string, args []string) error {
		gotArgs = args
		return nil
	})
	if err := Edit(context.Background(), env, "/tmp/note.md", 5); err != nil {
		t.Fatal(err)
	}
	if want := []string{"+5:1", "/tmp/note.md"}; !reflect.DeepEqual(gotArgs, want) {
		t.Errorf("args = %v, want %v", gotArgs, want)
	}
}

func TestEditNoLineNumberForUnknownEditor(t *testing.T) {
	env := newTestEnv(t)
	env.Cfg.Editor.Command = "code --wait"

	var gotArgs []string
	withRunCommand(t, func(_ context.Context, _ string, args []string) error {
		gotArgs = args
		return nil
	})
	if err := Edit(context.Background(), env, "/tmp/note.md", 5); err != nil {
		t.Fatal(err)
	}
	if want := []string{"--wait", "/tmp/note.md"}; !reflect.DeepEqual(gotArgs, want) {
		t.Errorf("args = %v, want %v", gotArgs, want)
	}
}

func TestEditNoLineNumberWhenLineIsZero(t *testing.T) {
	env := newTestEnv(t)
	env.Cfg.Editor.Command = "vim"

	var gotArgs []string
	withRunCommand(t, func(_ context.Context, _ string, args []string) error {
		gotArgs = args
		return nil
	})
	if err := Edit(context.Background(), env, "/tmp/note.md", 0); err != nil {
		t.Fatal(err)
	}
	if want := []string{"/tmp/note.md"}; !reflect.DeepEqual(gotArgs, want) {
		t.Errorf("args = %v, want %v", gotArgs, want)
	}
}

func TestEditVISUALBeatsEDITOR(t *testing.T) {
	env := newTestEnv(t)
	t.Setenv("VISUAL", "nano")
	t.Setenv("EDITOR", "vi")

	var gotName string
	withRunCommand(t, func(_ context.Context, name string, _ []string) error {
		gotName = name
		return nil
	})
	if err := Edit(context.Background(), env, "/tmp/note.md", 0); err != nil {
		t.Fatal(err)
	}
	if gotName != "nano" {
		t.Errorf("name = %q, want nano (VISUAL beats EDITOR)", gotName)
	}
}

func TestEditFallsBackToNvimThenVi(t *testing.T) {
	env := newTestEnv(t)
	t.Setenv("VISUAL", "")
	t.Setenv("EDITOR", "")

	dir := t.TempDir()
	writeFakeExecutable(t, filepath.Join(dir, "vi"))
	t.Setenv("PATH", dir)

	var gotName string
	withRunCommand(t, func(_ context.Context, name string, _ []string) error {
		gotName = name
		return nil
	})
	if err := Edit(context.Background(), env, "/tmp/note.md", 0); err != nil {
		t.Fatal(err)
	}
	if gotName != "vi" {
		t.Errorf("name = %q, want vi (nvim is not on PATH)", gotName)
	}

	writeFakeExecutable(t, filepath.Join(dir, "nvim"))
	if err := Edit(context.Background(), env, "/tmp/note.md", 0); err != nil {
		t.Fatal(err)
	}
	if gotName != "nvim" {
		t.Errorf("name = %q, want nvim once it is on PATH too", gotName)
	}
}

func TestEditNoEditorAvailable(t *testing.T) {
	env := newTestEnv(t)
	t.Setenv("VISUAL", "")
	t.Setenv("EDITOR", "")
	t.Setenv("PATH", t.TempDir())

	withRunCommand(t, func(context.Context, string, []string) error {
		t.Fatal("runCommand must not be called when no editor was found")
		return nil
	})
	if err := Edit(context.Background(), env, "/tmp/note.md", 0); err == nil {
		t.Fatal("expected an error when no editor is configured or on PATH")
	}
}

func writeFakeExecutable(t *testing.T, path string) {
	t.Helper()
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
}

func TestEditDraftEmptyIsCancelled(t *testing.T) {
	env := newTestEnv(t)
	env.Cfg.Editor.Command = "vim"
	withRunCommand(t, func(context.Context, string, []string) error { return nil })

	if _, err := EditDraft(context.Background(), env, ""); !errors.Is(err, ErrEmptyDraft) {
		t.Fatalf("err = %v, want ErrEmptyDraft", err)
	}
}

func TestEditDraftWhitespaceOnlyIsCancelled(t *testing.T) {
	env := newTestEnv(t)
	env.Cfg.Editor.Command = "vim"
	withRunCommand(t, func(context.Context, string, []string) error { return nil })

	if _, err := EditDraft(context.Background(), env, "  \n\t "); !errors.Is(err, ErrEmptyDraft) {
		t.Fatalf("err = %v, want ErrEmptyDraft", err)
	}
}

func TestEditDraftReturnsSavedContent(t *testing.T) {
	env := newTestEnv(t)
	env.Cfg.Editor.Command = "vim"
	var draftPath string
	withRunCommand(t, func(_ context.Context, _ string, args []string) error {
		draftPath = args[len(args)-1]
		return os.WriteFile(draftPath, []byte("hello world\n"), 0o600)
	})

	got, err := EditDraft(context.Background(), env, "")
	if err != nil {
		t.Fatal(err)
	}
	if got != "hello world\n" {
		t.Errorf("got = %q", got)
	}
	if draftPath == "" || !filepath.IsAbs(draftPath) {
		t.Errorf("draft path = %q, want an absolute scratch path", draftPath)
	}
}

func TestEditDraftEditorFailureIsAnError(t *testing.T) {
	env := newTestEnv(t)
	env.Cfg.Editor.Command = "vim"
	withRunCommand(t, func(context.Context, string, []string) error {
		return errors.New("boom")
	})

	if _, err := EditDraft(context.Background(), env, "hello"); err == nil {
		t.Fatal("expected an error when the editor process fails")
	}
}
