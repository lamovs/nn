package boundary

import (
	"bytes"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestWithin(t *testing.T) {
	tests := []struct {
		dir, target string
		want        bool
	}{
		{"/vault", "/vault", true},
		{"/vault", "/vault/nn/note.md", true},
		{"/vault", "/vault/../vault/nn", true},
		{"/vault", "/vault/..", false},
		{"/vault", "/elsewhere/nn", false},
		{"/vault", "/vaultish/note.md", false},
	}
	for _, tt := range tests {
		if got := Within(tt.dir, tt.target); got != tt.want {
			t.Errorf("Within(%q, %q) = %v, want %v", tt.dir, tt.target, got, tt.want)
		}
	}
}

func TestResolve(t *testing.T) {
	base := t.TempDir()
	anchor := filepath.Join(base, "vault")
	away := filepath.Join(base, "away")
	for _, dir := range []string{anchor, away, filepath.Join(anchor, "nn")} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	anchor = Dir(anchor)
	away = Dir(away)

	inside := filepath.Join(anchor, "nn", "note.md")
	if err := os.WriteFile(inside, []byte("note"), 0o644); err != nil {
		t.Fatal(err)
	}
	stranger := filepath.Join(away, "secret.md")
	if err := os.WriteFile(stranger, []byte("secret"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(stranger, filepath.Join(anchor, "nn", "link.md")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(away, filepath.Join(anchor, "linked")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(anchor, "nn"), filepath.Join(anchor, "shelf")); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name, raw, want string
		ok              bool
	}{
		{"plain file", inside, inside, true},
		{"link to a file outside", filepath.Join(anchor, "nn", "link.md"), stranger, false},
		{"file under a linked directory", filepath.Join(anchor, "linked", "secret.md"), stranger, false},
		{"missing file in a directory inside", filepath.Join(anchor, "nn", "new.md"), filepath.Join(anchor, "nn", "new.md"), true},
		{"missing file under a linked directory", filepath.Join(anchor, "linked", "new.md"), filepath.Join(away, "new.md"), false},
		{"climbing path", filepath.Join(anchor, "..", "away"), away, false},
		{"missing file two levels under a linked directory", filepath.Join(anchor, "linked", "b", "new.md"), filepath.Join(away, "b", "new.md"), false},
		{"missing file four levels under a linked directory", filepath.Join(anchor, "linked", "b", "c", "d", "new.md"), filepath.Join(away, "b", "c", "d", "new.md"), false},
		{"missing file two levels under a link that stays inside", filepath.Join(anchor, "shelf", "b", "new.md"), filepath.Join(anchor, "nn", "b", "new.md"), true},
		{"missing directory", filepath.Join(anchor, "gone", "new.md"), filepath.Join(anchor, "gone", "new.md"), true},
		{"missing directories, several deep", filepath.Join(anchor, "gone", "deeper", "still", "new.md"), filepath.Join(anchor, "gone", "deeper", "still", "new.md"), true},
	}
	for _, tt := range tests {
		got, ok, err := Resolve(anchor, tt.raw)
		if err != nil {
			t.Errorf("%s: Resolve(%q) = %v, want an answer", tt.name, tt.raw, err)
			continue
		}
		if got != tt.want || ok != tt.ok {
			t.Errorf("%s: Resolve(%q) = %q, %v; want %q, %v", tt.name, tt.raw, got, ok, tt.want, tt.ok)
		}
		if want := Within(anchor, got); ok != want {
			t.Errorf("%s: Resolve(%q) = %q, %v, but Within(anchor, %q) is %v", tt.name, tt.raw, got, ok, got, want)
		}
	}

	raw := filepath.Join("/", "nn-boundary-test-not-here", "b", "new.md")
	if got, ok, err := Resolve(anchor, raw); got != raw || ok || err != nil {
		t.Errorf("nothing above it on disk: Resolve(%q) = %q, %v, %v; want %q, false, nil", raw, got, ok, err, raw)
	}
}

func TestResolveEndsOnEverySpelling(t *testing.T) {
	anchor := Dir(t.TempDir())
	for _, raw := range []string{"", ".", "..", "/", anchor, "relative/missing.md", "../climbing.md"} {
		got, ok, err := Resolve(anchor, raw)
		if err != nil {
			t.Errorf("Resolve(%q) = %v, want an answer", raw, err)
			continue
		}
		if want := Within(anchor, got); ok != want {
			t.Errorf("Resolve(%q) = %q, %v, but Within(anchor, %q) is %v", raw, got, ok, got, want)
		}
	}
}

func TestResolveRefusesWhatItCannotFollow(t *testing.T) {
	anchor := Dir(t.TempDir())
	if err := os.WriteFile(filepath.Join(anchor, "note.md"), []byte("note"), 0o644); err != nil {
		t.Fatal(err)
	}
	loop := filepath.Join(anchor, "loop")
	if err := os.Symlink(loop, loop); err != nil {
		t.Fatal(err)
	}
	locked := filepath.Join(anchor, "locked")
	if err := os.Mkdir(locked, 0o755); err != nil {
		t.Fatal(err)
	}
	unsearchable := unsearchableDir(t, locked)

	tests := []struct{ name, raw string }{
		{"a link that loops", loop},
		{"a missing file under a link that loops", filepath.Join(loop, "new.md")},
		{"missing directories under a link that loops", filepath.Join(loop, "b", "c", "new.md")},
		{"a file standing where a directory has to be", filepath.Join(anchor, "note.md", "new.md")},
		{"deeper under a file standing where a directory has to be", filepath.Join(anchor, "note.md", "b", "new.md")},
	}
	if unsearchable {
		tests = append(tests,
			struct{ name, raw string }{"a missing file in a directory that may not be searched", filepath.Join(locked, "new.md")},
			struct{ name, raw string }{"missing directories under a directory that may not be searched", filepath.Join(locked, "b", "new.md")},
		)
	}
	for _, tt := range tests {
		got, ok, err := Resolve(anchor, tt.raw)
		if err == nil || ok || got != "" {
			t.Errorf("%s: Resolve(%q) = %q, %v, %v; want no path, false and an error", tt.name, tt.raw, got, ok, err)
			continue
		}
		if errors.Is(err, fs.ErrNotExist) {
			t.Errorf("%s: Resolve(%q) = %v, which reads as a path that is not there", tt.name, tt.raw, err)
		}
		if !strings.Contains(err.Error(), tt.raw) {
			t.Errorf("%s: Resolve(%q) = %v, want the error to name the path", tt.name, tt.raw, err)
		}
	}
}

func TestResolveRefusesALinkItCannotRead(t *testing.T) {
	base := t.TempDir()
	anchor := filepath.Join(base, "vault")
	away := filepath.Join(base, "away")
	for _, dir := range []string{anchor, away} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	anchor = Dir(anchor)
	if err := os.WriteFile(filepath.Join(away, "secret.png"), []byte("secret"), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(anchor, "a")
	if err := os.Symlink(away, link); err != nil {
		t.Fatal(err)
	}
	unreadableLink(t, link)

	for _, raw := range []string{
		link,
		filepath.Join(link, "secret.png"),
		filepath.Join(link, "new.md"),
		filepath.Join(link, "b", "nn"),
	} {
		got, ok, err := Resolve(anchor, raw)
		if err == nil || ok || got != "" {
			t.Errorf("Resolve(%q) = %q, %v, %v; want no path, false and an error", raw, got, ok, err)
			continue
		}
		if !errors.Is(err, fs.ErrPermission) {
			t.Errorf("Resolve(%q) = %v, want the permission error that stopped it", raw, err)
		}
	}
}

func unreadableLink(t *testing.T, link string) {
	t.Helper()
	if out, err := exec.Command("chmod", "-h", "000", link).CombinedOutput(); err != nil {
		msg, _, _ := bytes.Cut(bytes.TrimSpace(out), []byte("\n"))
		t.Skipf("a symlink's own permissions cannot be taken away here (chmod -h: %v: %s)", err, msg)
	}
	t.Cleanup(func() { exec.Command("chmod", "-h", "755", link).Run() })
	if _, err := os.Readlink(link); !errors.Is(err, fs.ErrPermission) {
		t.Skipf("a symlink's own permissions are not enforced here: readlink of a link with mode 000 = %v", err)
	}
}

func unsearchableDir(t *testing.T, dir string) bool {
	t.Helper()
	if err := os.Chmod(dir, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(dir, 0o755) })
	if _, err := os.Lstat(filepath.Join(dir, "probe")); !errors.Is(err, fs.ErrPermission) {
		t.Logf("a directory with mode 000 can still be searched here (Lstat under it = %v): its cases are left out", err)
		return false
	}
	return true
}

func TestDir(t *testing.T) {
	base := t.TempDir()
	real := filepath.Join(base, "real")
	if err := os.Mkdir(real, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(base, "link")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	if got, want := Dir(link), Dir(real); got != want {
		t.Errorf("Dir(link) = %q, want %q", got, want)
	}
	gone := filepath.Join(base, "gone")
	if got := Dir(gone); got != gone {
		t.Errorf("Dir of a missing directory = %q, want %q", got, gone)
	}
}
