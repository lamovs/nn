package install

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func stubExecutable(t *testing.T, path string) {
	t.Helper()
	writeFile(t, path)
	prev := executable
	executable = func() (string, error) { return path, nil }
	t.Cleanup(func() { executable = prev })
}

func writeFile(t *testing.T, path string) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func mkdir(t *testing.T, path string) string {
	t.Helper()
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

// resolved evaluates symlinks the same way Resources does.
func resolved(t *testing.T, path string) string {
	t.Helper()
	p, err := filepath.EvalSymlinks(path)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestResourcesLayouts(t *testing.T) {
	tests := []struct {
		name  string
		share string // share/nn directory to create, relative to the temp root
	}{
		{"next to the binary", filepath.Join("bin", "share", "nn")},
		{"one level above, as release archives lay it out", filepath.Join("share", "nn")},
		{"no bundled resources", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			var want string
			if tt.share != "" {
				want = resolved(t, mkdir(t, filepath.Join(root, tt.share)))
			}
			stubExecutable(t, filepath.Join(root, "bin", "nn"))

			got, ok := Resources()
			if got != want || ok != (want != "") {
				t.Errorf("Resources() = (%q, %v), want (%q, %v)", got, ok, want, want != "")
			}
		})
	}
}

func TestResourcesPrefersTheDirectoryWithTheHelper(t *testing.T) {
	root := t.TempDir()
	mkdir(t, filepath.Join(root, "bin", "share", "nn"))
	above := mkdir(t, filepath.Join(root, "share", "nn"))
	writeFile(t, filepath.Join(above, "nn-vision"))
	stubExecutable(t, filepath.Join(root, "bin", "nn"))

	got, ok := Resources()
	if want := resolved(t, above); !ok || got != want {
		t.Errorf("Resources() = (%q, %v), want %q", got, ok, want)
	}
}

func TestHelperFromEnv(t *testing.T) {
	dir := t.TempDir()
	missing := filepath.Join(dir, "typo", "nn-vision")
	t.Setenv("NN_VISION_HELPER", missing)
	_, err := Helper()
	if err == nil || !strings.Contains(err.Error(), "NN_VISION_HELPER") || !strings.Contains(err.Error(), missing) {
		t.Errorf("missing helper: err = %v", err)
	}

	t.Setenv("NN_VISION_HELPER", dir)
	if _, err := Helper(); err == nil {
		t.Error("a directory was accepted as the helper")
	}

	want := writeFile(t, filepath.Join(dir, "nn-vision"))
	t.Setenv("NN_VISION_HELPER", want)
	if got, err := Helper(); err != nil || got != want {
		t.Errorf("Helper() = (%q, %v), want %q", got, err, want)
	}
}

func TestHelperFromResources(t *testing.T) {
	root := t.TempDir()
	t.Setenv("NN_VISION_HELPER", "")
	t.Setenv("XDG_DATA_HOME", filepath.Join(root, "data"))
	share := mkdir(t, filepath.Join(root, "share", "nn"))
	writeFile(t, filepath.Join(share, "nn-vision"))
	stubExecutable(t, filepath.Join(root, "bin", "nn"))

	got, err := Helper()
	if want := filepath.Join(resolved(t, share), "nn-vision"); err != nil || got != want {
		t.Errorf("Helper() = (%q, %v), want %q", got, err, want)
	}
}

// Plants a file where a trimpath-style path would resolve "../.." to.
func TestDevHelperFromRejectsNonAbsolutePath(t *testing.T) {
	const trimmedFile = "github.com/lamovs/nn/internal/install/install.go"

	root := t.TempDir()
	writeFile(t, filepath.Join(root, "github.com", "lamovs", "nn", "dist", "helper", "nn-vision"))
	t.Chdir(root)

	if p, ok := devHelperFrom(trimmedFile); ok {
		t.Errorf("devHelperFrom(%q) = (%q, true), want (\"\", false)", trimmedFile, p)
	}
}

func TestHelperFromXDGDataHome(t *testing.T) {
	root := t.TempDir()
	t.Setenv("NN_VISION_HELPER", "")
	t.Setenv("XDG_DATA_HOME", filepath.Join(root, "data"))
	want := writeFile(t, filepath.Join(root, "data", "nn", "nn-vision"))
	stubExecutable(t, filepath.Join(root, "bin", "nn"))

	if got, err := Helper(); err != nil || got != want {
		t.Errorf("Helper() = (%q, %v), want %q", got, err, want)
	}
}
