package main

import (
	"strings"
	"testing"
)

func TestSExplicitColorAutoBeatsConfigOutputColor(t *testing.T) {
	root := newTestVault(t)
	writeConfig(t, "[output]\ncolor = \"always\"\n")
	writeNote(t, root, "nn/a.md", "# A\n\ndocker prune\n")

	stdout, stderr, code := runCmd(t, "", "s", "prune", "--color=auto")
	if code != 0 {
		t.Fatalf("code = %d, stderr = %q", code, stderr)
	}
	if strings.Contains(stdout, "\x1b[") {
		t.Errorf("stdout = %q, want no color (--color=auto off a tty beats output.color = always)", stdout)
	}
}

func TestSColorFlagAlwaysBeatsConfigNever(t *testing.T) {
	root := newTestVault(t)
	writeConfig(t, "[output]\ncolor = \"never\"\n")
	writeNote(t, root, "nn/a.md", "# A\n\ndocker prune\n")

	stdout, stderr, code := runCmd(t, "", "s", "prune", "--color=always")
	if code != 0 {
		t.Fatalf("code = %d, stderr = %q", code, stderr)
	}
	if !strings.Contains(stdout, "\x1b[") {
		t.Errorf("stdout = %q, want color (--color=always beats output.color = never)", stdout)
	}
}

func TestIndexUsesConfigOutputColor(t *testing.T) {
	newTestVault(t)
	writeConfig(t, "[output]\ncolor = \"always\"\n")

	stdout, stderr, code := runCmd(t, "")
	if code != 0 {
		t.Fatalf("code = %d, stderr = %q", code, stderr)
	}
	if !strings.Contains(stdout, "\x1b[") {
		t.Errorf("stdout = %q, want color (bare index honors output.color = always)", stdout)
	}
}

func TestBareHelpFlagUsesConfigOutputColor(t *testing.T) {
	newTestVault(t)
	writeConfig(t, "[output]\ncolor = \"always\"\n")

	stdout, stderr, code := runCmd(t, "", "--help")
	if code != 0 {
		t.Fatalf("code = %d, stderr = %q", code, stderr)
	}
	if !strings.Contains(stdout, "\x1b[") {
		t.Errorf("stdout = %q, want color (nn --help honors output.color = always)", stdout)
	}
}

func TestVerbHelpFlagUsesConfigOutputColor(t *testing.T) {
	newTestVault(t)
	writeConfig(t, "[output]\ncolor = \"always\"\n")

	stdout, stderr, code := runCmd(t, "", "s", "--help")
	if code != 0 {
		t.Fatalf("code = %d, stderr = %q", code, stderr)
	}
	if !strings.Contains(stdout, "\x1b[") {
		t.Errorf("stdout = %q, want color (nn s --help honors output.color = always)", stdout)
	}
}

func TestSDefaultColorUsesConfigOutputColor(t *testing.T) {
	root := newTestVault(t)
	writeConfig(t, "[output]\ncolor = \"always\"\n")
	writeNote(t, root, "nn/a.md", "# A\n\ndocker prune\n")

	stdout, stderr, code := runCmd(t, "", "s", "prune")
	if code != 0 {
		t.Fatalf("code = %d, stderr = %q", code, stderr)
	}
	if !strings.Contains(stdout, "\x1b[") {
		t.Errorf("stdout = %q, want color (nn s honors output.color = always with no --color flag)", stdout)
	}
}

func TestHelpUsesConfigOutputColor(t *testing.T) {
	newTestVault(t)
	writeConfig(t, "[output]\ncolor = \"always\"\n")

	stdout, stderr, code := runCmd(t, "", "help", "s")
	if code != 0 {
		t.Fatalf("code = %d, stderr = %q", code, stderr)
	}
	if !strings.Contains(stdout, "\x1b[") {
		t.Errorf("stdout = %q, want color (help honors output.color = always)", stdout)
	}
}

func TestBareHelpCommandUsesConfigOutputColor(t *testing.T) {
	newTestVault(t)
	writeConfig(t, "[output]\ncolor = \"always\"\n")

	stdout, stderr, code := runCmd(t, "", "help")
	if code != 0 {
		t.Fatalf("code = %d, stderr = %q", code, stderr)
	}
	if !strings.Contains(stdout, "\x1b[") {
		t.Errorf("stdout = %q, want color (nn help honors output.color = always)", stdout)
	}
}

func TestSetupNoArgsUsesConfigOutputColor(t *testing.T) {
	newTestVault(t)
	writeConfig(t, "[output]\ncolor = \"always\"\n")

	stdout, stderr, code := runCmd(t, "", "setup")
	if code != 0 {
		t.Fatalf("code = %d, stderr = %q", code, stderr)
	}
	if !strings.Contains(stdout, "\x1b[") {
		t.Errorf("stdout = %q, want color (nn setup honors output.color = always)", stdout)
	}
}

func TestSNoColorEnvBeatsConfigOutputColor(t *testing.T) {
	t.Run("NO_COLOR beats output.color", func(t *testing.T) {
		root := newTestVault(t)
		writeConfig(t, "[output]\ncolor = \"always\"\n")
		writeNote(t, root, "nn/a.md", "# A\n\ndocker prune\n")
		t.Setenv("NO_COLOR", "1")

		stdout, stderr, code := runCmd(t, "", "s", "prune")
		if code != 0 {
			t.Fatalf("code = %d, stderr = %q", code, stderr)
		}
		if strings.Contains(stdout, "\x1b[") {
			t.Errorf("stdout = %q, want no color (NO_COLOR beats output.color = always)", stdout)
		}
	})

	t.Run("--color=always beats NO_COLOR", func(t *testing.T) {
		root := newTestVault(t)
		writeConfig(t, "[output]\ncolor = \"always\"\n")
		writeNote(t, root, "nn/a.md", "# A\n\ndocker prune\n")
		t.Setenv("NO_COLOR", "1")

		stdout, stderr, code := runCmd(t, "", "s", "prune", "--color=always")
		if code != 0 {
			t.Fatalf("code = %d, stderr = %q", code, stderr)
		}
		if !strings.Contains(stdout, "\x1b[") {
			t.Errorf("stdout = %q, want color (--color=always beats NO_COLOR)", stdout)
		}
	})
}
