package main

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

func withFakeOpenURL(t *testing.T) (opened *string) {
	t.Helper()
	var got string
	prev := openURL
	openURL = func(ctx context.Context, url string) error { got = url; return nil }
	t.Cleanup(func() { openURL = prev })
	return &got
}

func TestOpenPrintDoesNotCallOpenURL(t *testing.T) {
	root := newTestVault(t)
	writeNote(t, root, "nn/docker-cleanup.md", "body\n")
	opened := withFakeOpenURL(t)

	stdout, stderr, code := runCmd(t, "", "open", "docker-cleanup", "--print")
	if code != 0 {
		t.Fatalf("code = %d, stderr = %q", code, stderr)
	}
	want := "obsidian://open?path=" + escapedAbs(filepath.Join(root, "nn", "docker-cleanup.md"))
	if strings.TrimSpace(stdout) != want {
		t.Errorf("stdout = %q, want %q", stdout, want)
	}
	if *opened != "" {
		t.Errorf("open was called (%q) despite --print", *opened)
	}
	// Printing a URI is not opening the note: nothing to record.
	if f := noteFrecency(t, "nn/docker-cleanup.md"); f != 0 {
		t.Errorf("Frecency = %v after --print, want 0", f)
	}
}

func TestOpenCallsOpenURL(t *testing.T) {
	root := newTestVault(t)
	writeNote(t, root, "nn/docker-cleanup.md", "body\n")
	opened := withFakeOpenURL(t)

	_, stderr, code := runCmd(t, "", "open", "docker-cleanup")
	if code != 0 {
		t.Fatalf("code = %d, stderr = %q", code, stderr)
	}
	want := "obsidian://open?path=" + escapedAbs(filepath.Join(root, "nn", "docker-cleanup.md"))
	if *opened != want {
		t.Errorf("opened = %q, want %q", *opened, want)
	}
	if f := noteFrecency(t, "nn/docker-cleanup.md"); f <= 0 {
		t.Errorf("Frecency = %v, want the open to have been recorded", f)
	}
}

func TestOpenNotFoundFails(t *testing.T) {
	newTestVault(t)
	withFakeOpenURL(t)
	_, stderr, code := runCmd(t, "", "open", "nope")
	if code != 2 {
		t.Errorf("code = %d, want 2, stderr = %q", code, stderr)
	}
}

func TestOpenNoArgIsMisuse(t *testing.T) {
	newTestVault(t)
	_, stderr, code := runCmd(t, "", "open")
	if code != 2 {
		t.Errorf("code = %d, want 2, stderr = %q", code, stderr)
	}
}

// escapedAbs only escapes "/", enough for the temp paths t.TempDir() produces; not a general percent-encoder.
func escapedAbs(p string) string {
	return strings.ReplaceAll(p, "/", "%2F")
}
