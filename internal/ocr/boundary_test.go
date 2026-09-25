package ocr

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func vaultWithNeighbour(t *testing.T) (root, outside, escapeRel string) {
	t.Helper()
	base := t.TempDir()
	root = filepath.Join(base, "vault")
	outside = filepath.Join(base, "outside")
	for _, d := range []string{root, outside} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(outside, "evil.png"), []byte("bytes"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outside, "evil.png.txt"), []byte("mine\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return root, outside, "../outside/evil.png"
}

func assertNeighbourUntouched(t *testing.T, outside string) {
	t.Helper()
	entries, err := os.ReadDir(outside)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	if len(names) != 2 {
		t.Errorf("files outside the vault = %v, want only the two it started with", names)
	}
	body, err := os.ReadFile(filepath.Join(outside, "evil.png.txt"))
	if err != nil || string(body) != "mine\n" {
		t.Errorf("file outside the vault = (%q, %v), want it untouched", body, err)
	}
}

func TestSidecarPathsRefusesEscape(t *testing.T) {
	for _, rel := range []string{"..", "../outside/evil.png", "a/../../b.png", "/../../etc/passwd.png", "."} {
		txt, js := SidecarPaths("/vault", rel)
		if txt != "" || js != "" {
			t.Errorf("SidecarPaths(%q) = (%q, %q), want empty paths", rel, txt, js)
		}
	}
}

func TestSidecarEntryPointsRefuseEscape(t *testing.T) {
	root, outside, escape := vaultWithNeighbour(t)
	e := &countingEngine{}
	ctx := context.Background()

	for _, rel := range []string{escape, "../../../outside/evil.png"} {
		if _, err := Ensure(ctx, root, rel, e, nil); !errors.Is(err, ErrOutsideVault) {
			t.Errorf("Ensure(%q) = %v, want ErrOutsideVault", rel, err)
		}
		if _, err := Refresh(ctx, root, rel, e, nil); !errors.Is(err, ErrOutsideVault) {
			t.Errorf("Refresh(%q) = %v, want ErrOutsideVault", rel, err)
		}
		if err := SaveSidecar(root, rel, &Result{SHA256: "x"}); !errors.Is(err, ErrOutsideVault) {
			t.Errorf("SaveSidecar(%q) = %v, want ErrOutsideVault", rel, err)
		}
		if _, err := LoadSidecar(root, rel); !errors.Is(err, ErrOutsideVault) {
			t.Errorf("LoadSidecar(%q) = %v, want ErrOutsideVault", rel, err)
		}
		if stale, err := Stale(root, rel); !errors.Is(err, ErrOutsideVault) || stale {
			t.Errorf("Stale(%q) = (%v, %v), want (false, ErrOutsideVault)", rel, stale, err)
		}
	}
	if n := e.calls.Load(); n != 0 {
		t.Errorf("engine ran %d times for an image outside the vault", n)
	}
	assertNeighbourUntouched(t, outside)
}

func TestReindexRefusesEscape(t *testing.T) {
	for _, force := range []bool{false, true} {
		root, outside, escape := vaultWithNeighbour(t)
		e := &countingEngine{}
		stats, err := Reindex(context.Background(), root, []string{escape}, e, nil, force, nil)
		if !errors.Is(err, ErrOutsideVault) {
			t.Errorf("force=%v: err = %v, want ErrOutsideVault", force, err)
		}
		if stats != (Stats{}) {
			t.Errorf("force=%v: stats = %+v, want nothing indexed or skipped", force, stats)
		}
		if n := e.calls.Load(); n != 0 {
			t.Errorf("force=%v: engine ran %d times for an image outside the vault", force, n)
		}
		assertNeighbourUntouched(t, outside)
	}
}

// symlink skips the test if symlinks cannot be made here.
func symlink(t *testing.T, root, rel, target string) {
	t.Helper()
	p := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, p); err != nil {
		t.Skipf("symlinks unavailable here: %v", err)
	}
}

func TestSidecarRefusesSymlinkOutOfVault(t *testing.T) {
	for _, tc := range []struct {
		name    string
		linkRel string
		rel     string
		dirLink bool
	}{
		{name: "file link", linkRel: "nn/assets/link.png", rel: "nn/assets/link.png"},
		{name: "directory link", linkRel: "linked", rel: "linked/evil.png", dirLink: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root, outside, _ := vaultWithNeighbour(t)
			target := filepath.Join(outside, "evil.png")
			if tc.dirLink {
				target = outside
			}
			symlink(t, root, tc.linkRel, target)
			e := &countingEngine{}
			ctx := context.Background()

			if _, err := Ensure(ctx, root, tc.rel, e, nil); !errors.Is(err, ErrOutsideVault) {
				t.Errorf("Ensure(%q) = %v, want ErrOutsideVault", tc.rel, err)
			}
			if _, err := Refresh(ctx, root, tc.rel, e, nil); !errors.Is(err, ErrOutsideVault) {
				t.Errorf("Refresh(%q) = %v, want ErrOutsideVault", tc.rel, err)
			}
			if n := e.calls.Load(); n != 0 {
				t.Errorf("engine ran %d times for a file outside the vault", n)
			}
			txt, js := SidecarPaths(root, tc.rel)
			for _, p := range []string{txt, js} {
				if _, err := os.Stat(p); !errors.Is(err, os.ErrNotExist) {
					t.Errorf("sidecar %q was written for a file outside the vault: %v", p, err)
				}
			}

			if err := SaveSidecar(root, tc.rel, &Result{SHA256: "x"}); err != nil {
				t.Fatal(err)
			}
			if _, err := Ensure(ctx, root, tc.rel, e, nil); !errors.Is(err, ErrOutsideVault) {
				t.Errorf("Ensure with a sidecar present = %v, want ErrOutsideVault", err)
			}
			if stale, err := Stale(root, tc.rel); !errors.Is(err, ErrOutsideVault) || stale {
				t.Errorf("Stale = (%v, %v), want (false, ErrOutsideVault)", stale, err)
			}
			assertNeighbourUntouched(t, outside)
		})
	}
}

// The engine sees the resolved path; the sidecar keeps the link's own name.
func TestSidecarFollowsSymlinkInsideVault(t *testing.T) {
	root := t.TempDir()
	writeImage(t, root, filepath.Join("nn", "assets", "real.png"), []byte("bytes"))
	symlink(t, root, "shots/link.png", filepath.Join("..", "nn", "assets", "real.png"))
	e := &countingEngine{}

	r, err := Ensure(context.Background(), root, "shots/link.png", e, nil)
	if err != nil {
		t.Fatalf("Ensure through a symlink inside the vault: %v", err)
	}
	if len(r.Lines) != 1 || r.Lines[0].Text != "real.png" {
		t.Errorf("recognized %+v, want the resolved image (real.png) to be what was opened", r.Lines)
	}
	inside := filepath.Join(root, ".nn", "ocr") + string(filepath.Separator)
	txt, js := SidecarPaths(root, "shots/link.png")
	for _, p := range []string{txt, js} {
		if !strings.HasPrefix(p, inside) {
			t.Errorf("sidecar path %q is not under the vault's .nn/ocr", p)
		}
		if _, err := os.Stat(p); err != nil {
			t.Errorf("sidecar %q was not written: %v", p, err)
		}
	}
	if _, err := Ensure(context.Background(), root, "shots/../nn/assets/real.png", e, nil); err != nil {
		t.Errorf("a path that climbs within the vault: %v", err)
	}
}

func TestReindexSkipsSymlinkOutOfVault(t *testing.T) {
	for _, force := range []bool{false, true} {
		root, outside, _ := vaultWithNeighbour(t)
		writeImage(t, root, "good.png", []byte("g"))
		symlink(t, root, "link.png", filepath.Join(outside, "evil.png"))
		e := &countingEngine{}

		stats, err := Reindex(context.Background(), root, []string{"good.png", "link.png"}, e, nil, force, nil)
		if err != nil {
			t.Errorf("force=%v: err = %v, want the run to carry on past the link", force, err)
		}
		if stats != (Stats{Processed: 1, Outside: 1}) {
			t.Errorf("force=%v: stats = %+v, want 1 indexed and 1 outside", force, stats)
		}
		if n := e.calls.Load(); n != 1 {
			t.Errorf("force=%v: engine ran %d times, want only the image inside the vault", force, n)
		}
		if _, err := LoadSidecar(root, "link.png"); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("force=%v: a sidecar was written for the link: %v", force, err)
		}
		if _, err := LoadSidecar(root, "good.png"); err != nil {
			t.Errorf("force=%v: the image inside the vault was not indexed: %v", force, err)
		}
		assertNeighbourUntouched(t, outside)
	}
}

func TestSidecarRefusesAbsolutePath(t *testing.T) {
	root, outside, _ := vaultWithNeighbour(t)
	e := &countingEngine{}
	ctx := context.Background()

	for _, rel := range []string{filepath.Join(outside, "evil.png"), "/etc/passwd.png"} {
		if _, err := Ensure(ctx, root, rel, e, nil); !errors.Is(err, ErrOutsideVault) {
			t.Errorf("Ensure(%q) = %v, want ErrOutsideVault", rel, err)
		}
		if _, err := Refresh(ctx, root, rel, e, nil); !errors.Is(err, ErrOutsideVault) {
			t.Errorf("Refresh(%q) = %v, want ErrOutsideVault", rel, err)
		}
		if _, err := LoadSidecar(root, rel); !errors.Is(err, ErrOutsideVault) {
			t.Errorf("LoadSidecar(%q) = %v, want ErrOutsideVault", rel, err)
		}
		if err := SaveSidecar(root, rel, &Result{SHA256: "x"}); !errors.Is(err, ErrOutsideVault) {
			t.Errorf("SaveSidecar(%q) = %v, want ErrOutsideVault", rel, err)
		}
		if stale, err := Stale(root, rel); !errors.Is(err, ErrOutsideVault) || stale {
			t.Errorf("Stale(%q) = (%v, %v), want (false, ErrOutsideVault)", rel, stale, err)
		}
		if txt, js := SidecarPaths(root, rel); txt != "" || js != "" {
			t.Errorf("SidecarPaths(%q) = (%q, %q), want empty paths", rel, txt, js)
		}
	}
	if n := e.calls.Load(); n != 0 {
		t.Errorf("engine ran %d times for an absolute path", n)
	}
	if _, err := os.Stat(filepath.Join(root, ".nn", "ocr", "etc")); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("an absolute path was rerooted into the vault's .nn/ocr: %v", err)
	}
	assertNeighbourUntouched(t, outside)
}

func boundaryVault(t *testing.T) (root, outside, realRoot string) {
	t.Helper()
	base := t.TempDir()
	dir := filepath.Join(base, "real")
	outside = filepath.Join(base, "outside")
	for _, d := range []string{dir, outside} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(outside, "evil.png"), []byte("bytes"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outside, "evil.png.txt"), []byte("mine\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	writeImage(t, dir, filepath.Join("nn", "assets", "good.png"), []byte("good"))
	symlink(t, dir, "linkfile.png", filepath.Join(outside, "evil.png"))
	symlink(t, dir, "linkdir", outside)
	symlink(t, dir, "inside/link.png", filepath.Join("..", "nn", "assets", "good.png"))

	root = filepath.Join(base, "vault")
	if err := os.Symlink(dir, root); err != nil {
		t.Skipf("symlinks unavailable here: %v", err)
	}
	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	return root, outside, realRoot
}

// climbOut survives being joined onto the vault root and cleaned.
func climbOut(target string) string {
	up := strings.Count(filepath.ToSlash(filepath.Clean(target)), "/") + 1
	return "/" + strings.Repeat("../", up) + strings.TrimPrefix(filepath.ToSlash(target), "/")
}

// viaLink cases are invisible to SidecarPaths and LoadSidecar until resolved.
func TestBoundaryRefusesEveryWayOut(t *testing.T) {
	for _, tc := range []struct {
		name    string
		ref     func(root, outside, realRoot string) string
		viaLink bool
	}{
		{name: "relative climbing out", ref: func(root, outside, realRoot string) string {
			return "../outside/evil.png"
		}},
		{name: "relative that only cleans out", ref: func(root, outside, realRoot string) string {
			return "a/../../outside/evil.png"
		}},
		{name: "deep relative onto a sidecar's own name", ref: func(root, outside, realRoot string) string {
			return "../../../outside/evil.png"
		}},
		{name: "absolute outside the vault", ref: func(root, outside, realRoot string) string {
			return filepath.Join(outside, "evil.png")
		}},
		{name: "absolute elsewhere entirely", ref: func(root, outside, realRoot string) string {
			return "/etc/passwd.png"
		}},
		{name: "absolute climbing back out of the root", ref: func(root, outside, realRoot string) string {
			return climbOut(filepath.Join(outside, "evil.png"))
		}},
		{name: "symlinked file leading out", viaLink: true, ref: func(root, outside, realRoot string) string {
			return "linkfile.png"
		}},
		{name: "symlinked directory leading out", viaLink: true, ref: func(root, outside, realRoot string) string {
			return "linkdir/evil.png"
		}},
		{name: "absolute through a symlinked directory leading out", viaLink: true, ref: func(root, outside, realRoot string) string {
			return filepath.Join(root, "linkdir", "evil.png")
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root, outside, realRoot := boundaryVault(t)
			ref := tc.ref(root, outside, realRoot)
			e := &countingEngine{}
			ctx := context.Background()

			if img, err := Locate(root, ref); !errors.Is(err, ErrOutsideVault) {
				t.Errorf("Locate(%q) = (%+v, %v), want ErrOutsideVault", ref, img, err)
			}
			if _, err := Ensure(ctx, root, ref, e, nil); !errors.Is(err, ErrOutsideVault) {
				t.Errorf("Ensure(%q) = %v, want ErrOutsideVault", ref, err)
			}
			if _, err := Refresh(ctx, root, ref, e, nil); !errors.Is(err, ErrOutsideVault) {
				t.Errorf("Refresh(%q) = %v, want ErrOutsideVault", ref, err)
			}
			if stale, err := Stale(root, ref); !errors.Is(err, ErrOutsideVault) || stale {
				t.Errorf("Stale(%q) = (%v, %v), want (false, ErrOutsideVault)", ref, stale, err)
			}
			if n := e.calls.Load(); n != 0 {
				t.Errorf("the engine ran %d times for %q", n, ref)
			}

			txt, js := SidecarPaths(root, ref)
			if tc.viaLink {
				for _, p := range []string{txt, js} {
					if _, err := os.Stat(p); !errors.Is(err, fs.ErrNotExist) {
						t.Errorf("a sidecar was written at %q: %v", p, err)
					}
				}
			} else {
				if txt != "" || js != "" {
					t.Errorf("SidecarPaths(%q) = (%q, %q), want empty paths", ref, txt, js)
				}
				if err := SaveSidecar(root, ref, &Result{SHA256: "x"}); !errors.Is(err, ErrOutsideVault) {
					t.Errorf("SaveSidecar(%q) = %v, want ErrOutsideVault", ref, err)
				}
				if _, err := LoadSidecar(root, ref); !errors.Is(err, ErrOutsideVault) {
					t.Errorf("LoadSidecar(%q) = %v, want ErrOutsideVault", ref, err)
				}
			}
			assertNeighbourUntouched(t, outside)
		})
	}
}

// The last two cases are the macOS /tmp -> /private/tmp case.
func TestBoundaryAcceptsWhatStaysInside(t *testing.T) {
	for _, tc := range []struct {
		name string
		ref  func(root, realRoot string) string
		rel  string
	}{
		{"vault-relative", func(root, realRoot string) string {
			return "nn/assets/good.png"
		}, "nn/assets/good.png"},
		{"relative climbing within the vault", func(root, realRoot string) string {
			return "inside/../nn/assets/good.png"
		}, "nn/assets/good.png"},
		{"symlink staying inside the vault", func(root, realRoot string) string {
			return "inside/link.png"
		}, "inside/link.png"},
		{"absolute under the root as given", func(root, realRoot string) string {
			return filepath.Join(root, "nn", "assets", "good.png")
		}, "nn/assets/good.png"},
		{"absolute under the root resolved", func(root, realRoot string) string {
			return filepath.Join(realRoot, "nn", "assets", "good.png")
		}, "nn/assets/good.png"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root, _, realRoot := boundaryVault(t)
			ref := tc.ref(root, realRoot)
			wantAbs := filepath.Join(realRoot, "nn", "assets", "good.png")

			img, err := Locate(root, ref)
			if err != nil {
				t.Fatalf("Locate(%q) = %v, want the image inside the vault", ref, err)
			}
			if img.Rel != tc.rel {
				t.Errorf("Locate(%q).Rel = %q, want %q", ref, img.Rel, tc.rel)
			}
			if img.Abs != wantAbs {
				t.Errorf("Locate(%q).Abs = %q, want the resolved image %q", ref, img.Abs, wantAbs)
			}

			e := &countingEngine{}
			r, err := Ensure(context.Background(), root, ref, e, nil)
			if err != nil {
				t.Fatalf("Ensure(%q) = %v", ref, err)
			}
			if len(r.Lines) != 1 || r.Lines[0].Text != "good.png" {
				t.Errorf("recognized %+v, want the resolved image to be what was opened", r.Lines)
			}
			anchor := filepath.Join(realRoot, ".nn", "ocr") + string(filepath.Separator)
			txt, js := SidecarPaths(root, ref)
			for _, p := range []string{txt, js} {
				resolved, err := filepath.EvalSymlinks(p)
				if err != nil {
					t.Fatalf("sidecar %q was not written: %v", p, err)
				}
				if !strings.HasPrefix(resolved, anchor) {
					t.Errorf("sidecar %q is not under the vault's .nn/ocr", resolved)
				}
			}
			if _, err := LoadSidecar(root, tc.rel); err != nil {
				t.Errorf("the sidecar is not filed under %q: %v", tc.rel, err)
			}
		})
	}
}

func TestBoundaryRefusesNonImagePaths(t *testing.T) {
	root, _, realRoot := boundaryVault(t)
	for _, ref := range []string{"", "  ", ".", "./", root, realRoot} {
		img, err := Locate(root, ref)
		if err == nil {
			t.Errorf("Locate(%q) = %+v, want an error", ref, img)
			continue
		}
		if !strings.Contains(err.Error(), "not an image path") {
			t.Errorf("Locate(%q) = %v, want it refused as not an image path", ref, err)
		}
	}
}

func TestSidecarWriteRefusesSymlinkedDirectoryUnderTheCache(t *testing.T) {
	root, outside, _ := vaultWithNeighbour(t)
	writeImage(t, root, filepath.Join("nn", "assets", "evil.png"), []byte("bytes"))
	symlink(t, root, ".nn/ocr/nn/assets", outside)
	rel := "nn/assets/evil.png"

	if err := SaveSidecar(root, rel, &Result{SHA256: "x"}); !errors.Is(err, ErrOutsideVault) {
		t.Errorf("SaveSidecar = %v, want ErrOutsideVault", err)
	}
	e := &countingEngine{}
	if _, err := Ensure(context.Background(), root, rel, e, nil); !errors.Is(err, ErrOutsideVault) {
		t.Errorf("Ensure = %v, want ErrOutsideVault", err)
	}
	if _, err := Refresh(context.Background(), root, rel, e, nil); !errors.Is(err, ErrOutsideVault) {
		t.Errorf("Refresh = %v, want ErrOutsideVault", err)
	}
	if txt, js := SidecarPaths(root, rel); txt != "" || js != "" {
		t.Errorf("SidecarPaths = (%q, %q), want empty paths", txt, js)
	}
	if _, err := LoadSidecar(root, rel); !errors.Is(err, ErrOutsideVault) {
		t.Errorf("LoadSidecar = %v, want ErrOutsideVault", err)
	}
	assertNeighbourUntouched(t, outside)
}

func TestSidecarWriteRefusesBeforeCreatingThroughALink(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	writeImage(t, root, filepath.Join("nn", "assets", "a.png"), []byte("bytes"))
	symlink(t, root, ".nn/ocr/nn", outside)
	rel := "nn/assets/a.png"

	if err := SaveSidecar(root, rel, &Result{SHA256: "x"}); !errors.Is(err, ErrOutsideVault) {
		t.Errorf("SaveSidecar = %v, want ErrOutsideVault", err)
	}
	if _, err := Ensure(context.Background(), root, rel, &countingEngine{}, nil); !errors.Is(err, ErrOutsideVault) {
		t.Errorf("Ensure = %v, want ErrOutsideVault", err)
	}
	entries, err := os.ReadDir(outside)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		names := make([]string, len(entries))
		for i, e := range entries {
			names[i] = e.Name()
		}
		t.Errorf("outside the vault holds %v, want nothing: the sidecar directory is refused before it is created", names)
	}
}

func TestSidecarWriteFollowsACacheMovedWithItsParent(t *testing.T) {
	root := t.TempDir()
	elsewhere := t.TempDir()
	writeImage(t, root, filepath.Join("nn", "assets", "real.png"), []byte("bytes"))
	symlink(t, root, ".nn", elsewhere)
	rel := "nn/assets/real.png"

	if err := SaveSidecar(root, rel, &Result{SHA256: "x"}); err != nil {
		t.Fatalf("SaveSidecar with .nn moved off: %v", err)
	}
	for _, name := range []string{"real.png.txt", "real.png.json"} {
		p := filepath.Join(elsewhere, "ocr", "nn", "assets", name)
		if _, err := os.Stat(p); err != nil {
			t.Errorf("%s was not written where the cache was moved to: %v", p, err)
		}
	}
}

func TestLocateRefusesAPathItCannotFollow(t *testing.T) {
	for _, tc := range []struct {
		name       string
		linkRel    string
		ref        string
		target     func(root, outside string) string
		unreadable bool
	}{
		{
			name: "a link that loops", linkRel: "loop", ref: "loop/evil.png",
			target: func(root, outside string) string { return filepath.Join(root, "loop") },
		},
		{
			name: "a file link it cannot read", linkRel: "nn/assets/link.png", ref: "nn/assets/link.png", unreadable: true,
			target: func(root, outside string) string { return filepath.Join(outside, "evil.png") },
		},
		{
			name: "a directory link it cannot read", linkRel: "linked", ref: "linked/evil.png", unreadable: true,
			target: func(root, outside string) string { return outside },
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root, outside, _ := vaultWithNeighbour(t)
			symlink(t, root, tc.linkRel, tc.target(root, outside))
			if tc.unreadable {
				unreadableLink(t, filepath.Join(root, filepath.FromSlash(tc.linkRel)))
			}
			e := &countingEngine{}
			ctx := context.Background()

			img, err := Locate(root, tc.ref)
			if err == nil {
				t.Fatalf("Locate(%q) = %+v, want a refusal", tc.ref, img)
			}
			if !strings.Contains(err.Error(), filepath.Join(root, filepath.FromSlash(tc.ref))) {
				t.Errorf("Locate(%q) = %v, want the error to name the path", tc.ref, err)
			}
			_, ensureErr := Ensure(ctx, root, tc.ref, e, nil)
			_, refreshErr := Refresh(ctx, root, tc.ref, e, nil)
			stale, staleErr := Stale(root, tc.ref)
			for _, got := range []struct {
				call string
				err  error
			}{{"Locate", err}, {"Ensure", ensureErr}, {"Refresh", refreshErr}, {"Stale", staleErr}} {
				if got.err == nil || errors.Is(got.err, ErrOutsideVault) {
					t.Errorf("%s(%q) = %v, want a refusal that does not call it outside the vault", got.call, tc.ref, got.err)
				}
				if tc.unreadable && !errors.Is(got.err, fs.ErrPermission) {
					t.Errorf("%s(%q) = %v, want the permission error that stopped it", got.call, tc.ref, got.err)
				}
			}
			if stale {
				t.Errorf("Stale(%q) = true, want false alongside the refusal", tc.ref)
			}
			if n := e.calls.Load(); n != 0 {
				t.Errorf("the engine ran %d times for %q", n, tc.ref)
			}
			txt, js := SidecarPaths(root, tc.ref)
			for _, p := range []string{txt, js} {
				if _, err := os.Stat(p); !errors.Is(err, fs.ErrNotExist) {
					t.Errorf("a sidecar was written at %q: %v", p, err)
				}
			}
			assertNeighbourUntouched(t, outside)
		})
	}
}

func TestLocateRefusesAnAbsolutePathItCannotFollow(t *testing.T) {
	for _, tc := range []struct {
		name       string
		unreadable bool
		// lay wires the case's link into the shared vault/outside fixture.
		lay func(t *testing.T, base string) (root, ref, link, named string)
	}{
		{
			name: "a reference through a link that loops",
			lay: func(t *testing.T, base string) (string, string, string, string) {
				link := filepath.Join(base, "outside", "loop")
				symlink(t, base, "outside/loop", link)
				ref := filepath.Join(link, "evil.png")
				return filepath.Join(base, "vault"), ref, link, ref
			},
		},
		{
			name: "a reference through a link it cannot read", unreadable: true,
			lay: func(t *testing.T, base string) (string, string, string, string) {
				link := filepath.Join(base, "outside", "into")
				symlink(t, base, "outside/into", filepath.Join(base, "vault"))
				ref := filepath.Join(link, "good.png")
				return filepath.Join(base, "vault"), ref, link, ref
			},
		},
		{
			name: "a root that loops",
			lay: func(t *testing.T, base string) (string, string, string, string) {
				root := filepath.Join(base, "loop")
				symlink(t, base, "loop", root)
				return root, filepath.Join(base, "outside", "evil.png"), root, root
			},
		},
		{
			name: "a root it cannot read", unreadable: true,
			lay: func(t *testing.T, base string) (string, string, string, string) {
				root := filepath.Join(base, "link")
				symlink(t, base, "link", filepath.Join(base, "vault"))
				return root, filepath.Join(base, "vault", "good.png"), root, root
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			base := t.TempDir()
			writeImage(t, filepath.Join(base, "vault"), "good.png", []byte("good"))
			writeImage(t, filepath.Join(base, "outside"), "evil.png", []byte("bytes"))
			root, ref, link, named := tc.lay(t, base)
			if tc.unreadable {
				unreadableLink(t, link)
			}
			e := &countingEngine{}
			ctx := context.Background()

			img, err := Locate(root, ref)
			if err == nil {
				t.Fatalf("Locate(%q) = %+v, want a refusal", ref, img)
			}
			if want := "cannot tell where " + named + " leads"; !strings.Contains(err.Error(), want) {
				t.Errorf("Locate(%q) = %v, want a refusal saying %q", ref, err, want)
			}
			_, ensureErr := Ensure(ctx, root, ref, e, nil)
			_, refreshErr := Refresh(ctx, root, ref, e, nil)
			stale, staleErr := Stale(root, ref)
			_, loadErr := LoadSidecar(root, ref)
			saveErr := SaveSidecar(root, ref, &Result{SHA256: "x"})
			for _, got := range []struct {
				call string
				err  error
			}{{"Locate", err}, {"Ensure", ensureErr}, {"Refresh", refreshErr}, {"Stale", staleErr}, {"LoadSidecar", loadErr}, {"SaveSidecar", saveErr}} {
				if got.err == nil || errors.Is(got.err, ErrOutsideVault) {
					t.Errorf("%s(%q) = %v, want a refusal that does not call it outside the vault", got.call, ref, got.err)
				}
				if tc.unreadable && !errors.Is(got.err, fs.ErrPermission) {
					t.Errorf("%s(%q) = %v, want the permission error that stopped it", got.call, ref, got.err)
				}
			}
			if stale {
				t.Errorf("Stale(%q) = true, want false alongside the refusal", ref)
			}
			if n := e.calls.Load(); n != 0 {
				t.Errorf("the engine ran %d times for %q", n, ref)
			}
			if txt, js := SidecarPaths(root, ref); txt != "" || js != "" {
				t.Errorf("SidecarPaths(%q) = (%q, %q), want empty paths", ref, txt, js)
			}
		})
	}
}

func TestLocateRefusesALinkTooLongToFollow(t *testing.T) {
	root := t.TempDir()
	far := linkTooLongToFollow(t, t.TempDir())
	writeImage(t, far, "evil.png", []byte("bytes"))
	symlink(t, root, "linked", far)
	ref := "linked/evil.png"
	e := &countingEngine{}
	ctx := context.Background()

	img, err := Locate(root, ref)
	if err == nil {
		t.Fatalf("Locate(%q) = %+v, want a refusal", ref, img)
	}
	if want := "cannot tell where " + filepath.Join(root, "linked", "evil.png") + " leads"; !strings.Contains(err.Error(), want) {
		t.Errorf("Locate(%q) = %v, want a refusal saying %q", ref, err, want)
	}
	_, ensureErr := Ensure(ctx, root, ref, e, nil)
	_, refreshErr := Refresh(ctx, root, ref, e, nil)
	stale, staleErr := Stale(root, ref)
	for _, got := range []struct {
		call string
		err  error
	}{{"Locate", err}, {"Ensure", ensureErr}, {"Refresh", refreshErr}, {"Stale", staleErr}} {
		if !errors.Is(got.err, syscall.ENAMETOOLONG) || errors.Is(got.err, ErrOutsideVault) {
			t.Errorf("%s(%q) = %v, want the refusal that says what stopped it, not one calling it outside the vault", got.call, ref, got.err)
		}
	}
	if stale {
		t.Errorf("Stale(%q) = true, want false alongside the refusal", ref)
	}
	if n := e.calls.Load(); n != 0 {
		t.Errorf("the engine ran %d times for %q", n, ref)
	}
	txt, js := SidecarPaths(root, ref)
	for _, p := range []string{txt, js} {
		if _, err := os.Stat(p); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("a sidecar was written at %q: %v", p, err)
		}
	}
	entries, err := os.ReadDir(far)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "evil.png" {
		t.Errorf("the far end of the link holds %v, want only the image it started with", entries)
	}
}

func TestLocateTakesAMissingImageThroughALinkedSpellingOfTheRoot(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "vault")
	writeImage(t, root, filepath.Join("nn", "here.png"), []byte("bytes"))
	link := filepath.Join(base, "alias")
	symlink(t, base, "alias", root)

	for _, tc := range []struct{ ref, rel string }{
		{filepath.Join(link, "nn", "here.png"), "nn/here.png"},
		{filepath.Join(link, "nn", "missing.png"), "nn/missing.png"},
		{filepath.Join(link, "gone", "deeper", "missing.png"), "gone/deeper/missing.png"},
	} {
		if img, err := Locate(root, tc.ref); err != nil || img.Rel != tc.rel {
			t.Errorf("Locate(%q) = %+v, %v; want %q", tc.ref, img, err, tc.rel)
		}
	}
	ref := filepath.Join(link, "nn", "missing.png")
	e := &countingEngine{}
	if _, err := Ensure(context.Background(), root, ref, e, nil); !errors.Is(err, fs.ErrNotExist) || errors.Is(err, ErrOutsideVault) {
		t.Errorf("Ensure(%q) = %v, want the missing image reported as missing", ref, err)
	}
	if n := e.calls.Load(); n != 0 {
		t.Errorf("the engine ran %d times for a missing image", n)
	}
}

func TestSidecarWriteRefusesAPathItCannotFollow(t *testing.T) {
	for _, tc := range []struct {
		name       string
		target     func(root, outside string) string
		unreadable bool
	}{
		{name: "a link that loops", target: func(root, outside string) string { return filepath.Join(root, ".nn", "ocr", "nn") }},
		{name: "a link it cannot read", target: func(root, outside string) string { return outside }, unreadable: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			outside := t.TempDir()
			writeImage(t, root, filepath.Join("nn", "assets", "a.png"), []byte("bytes"))
			link := filepath.Join(root, ".nn", "ocr", "nn")
			symlink(t, root, ".nn/ocr/nn", tc.target(root, outside))
			if tc.unreadable {
				unreadableLink(t, link)
			}

			err := SaveSidecar(root, "nn/assets/a.png", &Result{SHA256: "x"})
			if err == nil || errors.Is(err, ErrOutsideVault) {
				t.Errorf("SaveSidecar = %v, want a refusal that does not call it outside the vault", err)
			}
			if tc.unreadable && !errors.Is(err, fs.ErrPermission) {
				t.Errorf("SaveSidecar = %v, want the permission error that stopped it", err)
			}
			entries, err := os.ReadDir(outside)
			if err != nil {
				t.Fatal(err)
			}
			if len(entries) != 0 {
				names := make([]string, len(entries))
				for i, e := range entries {
					names[i] = e.Name()
				}
				t.Errorf("outside the vault holds %v, want nothing", names)
			}
		})
	}
}

func TestSidecarWriteRefusesALinkTooLongToFollow(t *testing.T) {
	root := t.TempDir()
	far := linkTooLongToFollow(t, t.TempDir())
	writeImage(t, root, filepath.Join("nn", "assets", "a.png"), []byte("bytes"))
	symlink(t, root, ".nn/ocr/nn", far)
	rel := "nn/assets/a.png"
	ctx := context.Background()

	saveErr := SaveSidecar(root, rel, &Result{SHA256: "x"})
	_, ensureErr := Ensure(ctx, root, rel, &countingEngine{}, nil)
	_, refreshErr := Refresh(ctx, root, rel, &countingEngine{}, nil)
	for _, got := range []struct {
		call string
		err  error
	}{{"SaveSidecar", saveErr}, {"Ensure", ensureErr}, {"Refresh", refreshErr}} {
		if !errors.Is(got.err, syscall.ENAMETOOLONG) || errors.Is(got.err, ErrOutsideVault) {
			t.Errorf("%s = %v, want the refusal that says what stopped it, not one calling it outside the vault", got.call, got.err)
		}
	}
	if want := "cannot tell where " + filepath.Join(root, ".nn", "ocr", "nn", "assets") + " leads"; saveErr == nil || !strings.Contains(saveErr.Error(), want) {
		t.Errorf("SaveSidecar = %v, want a refusal saying %q", saveErr, want)
	}
	entries, err := os.ReadDir(far)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		names := make([]string, len(entries))
		for i, e := range entries {
			names[i] = e.Name()
		}
		t.Errorf("the far end of the link holds %v, want nothing: the sidecar directory is refused before it is created", names)
	}
}

// The link is the sidecar directory for one image, an ancestor for the other.
func TestSidecarReadRefusesAPathItCannotFollow(t *testing.T) {
	for _, tc := range []struct {
		name       string
		target     func(root, outside string) string
		unreadable bool
	}{
		{name: "a link that loops", target: func(root, outside string) string { return filepath.Join(root, ".nn", "ocr", "nn") }},
		{name: "a link it cannot read", target: func(root, outside string) string { return outside }, unreadable: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			outside := t.TempDir()
			rels := []string{"nn/a.png", "nn/assets/a.png"}
			for _, rel := range rels {
				writeImage(t, root, filepath.FromSlash(rel), []byte("bytes"))
				sum, err := imageSHA256(filepath.Join(root, filepath.FromSlash(rel)))
				if err != nil {
					t.Fatal(err)
				}
				stranger, err := json.Marshal(&Result{SHA256: sum, Lines: []Line{{Text: "STRANGER"}}})
				if err != nil {
					t.Fatal(err)
				}
				far := filepath.Join(outside, filepath.FromSlash(strings.TrimPrefix(rel, "nn/"))+".json")
				if err := os.MkdirAll(filepath.Dir(far), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(far, stranger, 0o644); err != nil {
					t.Fatal(err)
				}
			}
			symlink(t, root, ".nn/ocr/nn", tc.target(root, outside))
			if tc.unreadable {
				unreadableLink(t, filepath.Join(root, ".nn", "ocr", "nn"))
			}

			e := &countingEngine{}
			for _, rel := range rels {
				loaded, loadErr := LoadSidecar(root, rel)
				ensured, ensureErr := Ensure(context.Background(), root, rel, e, nil)
				for _, got := range []struct {
					call string
					r    *Result
					err  error
				}{{"LoadSidecar", loaded, loadErr}, {"Ensure", ensured, ensureErr}} {
					if got.r != nil {
						t.Errorf("%s(%q) answered %+v, want nothing read through the link", got.call, rel, got.r.Lines)
					}
					if got.err == nil || errors.Is(got.err, ErrOutsideVault) {
						t.Errorf("%s(%q) = %v, want a refusal that does not call it outside the vault", got.call, rel, got.err)
					}
					if tc.unreadable && !errors.Is(got.err, fs.ErrPermission) {
						t.Errorf("%s(%q) = %v, want the permission error that stopped it", got.call, rel, got.err)
					}
				}
				dir := filepath.Dir(filepath.Join(root, ".nn", "ocr", filepath.FromSlash(rel)))
				if want := "cannot tell where " + dir + " leads"; loadErr == nil || !strings.Contains(loadErr.Error(), want) {
					t.Errorf("LoadSidecar(%q) = %v, want a refusal saying %q", rel, loadErr, want)
				}
				if txt, js := SidecarPaths(root, rel); txt != "" || js != "" {
					t.Errorf("SidecarPaths(%q) = (%q, %q), want empty paths", rel, txt, js)
				}
			}
		})
	}
}

func TestSidecarReadDoesNotFollowALinkedSidecarFile(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	rel := "nn/assets/a.png"
	writeImage(t, root, filepath.FromSlash(rel), []byte("bytes"))
	sum, err := imageSHA256(filepath.Join(root, filepath.FromSlash(rel)))
	if err != nil {
		t.Fatal(err)
	}
	stranger, err := json.Marshal(&Result{SHA256: sum, Lines: []Line{{Text: "STRANGER"}}})
	if err != nil {
		t.Fatal(err)
	}
	far := map[string][]byte{
		filepath.Join(outside, "s.json"): stranger,
		filepath.Join(outside, "s.txt"):  []byte("STRANGER\n"),
	}
	before := map[string]os.FileInfo{}
	for p, body := range far {
		if err := os.WriteFile(p, body, 0o644); err != nil {
			t.Fatal(err)
		}
		info, err := os.Stat(p)
		if err != nil {
			t.Fatal(err)
		}
		before[p] = info
	}
	symlink(t, root, ".nn/ocr/nn/assets/a.png.json", filepath.Join(outside, "s.json"))
	symlink(t, root, ".nn/ocr/nn/assets/a.png.txt", filepath.Join(outside, "s.txt"))

	if got, err := LoadSidecar(root, rel); got != nil || err == nil {
		t.Errorf("LoadSidecar = %+v, %v; want nothing read through the link", got, err)
	}
	if stale, err := Stale(root, rel); !stale || err != nil {
		t.Errorf("Stale = %v, %v; want true: the linked sidecar is not the image's own", stale, err)
	}

	e := &countingEngine{}
	got, err := Ensure(context.Background(), root, rel, e, nil)
	if err != nil {
		t.Fatalf("Ensure = %v, want the image recognized again", err)
	}
	if n := e.calls.Load(); n != 1 || len(got.Lines) != 1 || got.Lines[0].Text != "a.png" {
		t.Errorf("Ensure = %+v after %d engine runs, want the image's own text from one run", got.Lines, n)
	}
	for _, name := range []string{"a.png.json", "a.png.txt"} {
		p := filepath.Join(root, ".nn", "ocr", "nn", "assets", name)
		if info, err := os.Lstat(p); err != nil {
			t.Errorf("%s after Ensure: %v", name, err)
		} else if !info.Mode().IsRegular() {
			t.Errorf("%s after Ensure has mode %v, want the link replaced by a regular file", name, info.Mode())
		}
	}
	if got, err := LoadSidecar(root, rel); err != nil || len(got.Lines) != 1 || got.Lines[0].Text != "a.png" {
		t.Errorf("LoadSidecar after Ensure = %+v, %v; want the sidecar Ensure wrote", got, err)
	}
	for p, body := range far {
		info, err := os.Stat(p)
		if err != nil || !os.SameFile(before[p], info) {
			t.Errorf("%s after Ensure: Stat err = %v, same file = %v; want the very file the link pointed at", p, err, err == nil && os.SameFile(before[p], info))
			continue
		}
		if data, err := os.ReadFile(p); err != nil || !bytes.Equal(data, body) {
			t.Errorf("%s after Ensure = %q, %v; want it untouched", p, data, err)
		}
	}
}

// A FIFO at a sidecar's name must not block LoadSidecar waiting for a writer.
func TestSidecarReadDoesNotBlockOnAFIFO(t *testing.T) {
	root := t.TempDir()
	rel := "nn/assets/a.png"
	writeImage(t, root, filepath.FromSlash(rel), []byte("bytes"))
	dir := filepath.Join(root, ".nn", "ocr", "nn", "assets")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	fifo := filepath.Join(dir, "a.png.json")
	if err := syscall.Mkfifo(fifo, 0o644); err != nil {
		t.Skipf("FIFOs unavailable here: %v", err)
	}

	type loaded struct {
		r   *Result
		err error
	}
	done := make(chan loaded, 1)
	go func() {
		r, err := LoadSidecar(root, rel)
		done <- loaded{r, err}
	}()
	select {
	case got := <-done:
		if got.r != nil || got.err == nil {
			t.Errorf("LoadSidecar = %+v, %v; want a FIFO at the sidecar's name refused", got.r, got.err)
		}
	case <-time.After(5 * time.Second):
		// Let the stuck read go, so that it does not outlive the test.
		if w, err := os.OpenFile(fifo, os.O_WRONLY|syscall.O_NONBLOCK, 0); err == nil {
			w.Close()
		}
		t.Fatal("LoadSidecar is still waiting on a FIFO at the sidecar's name")
	}

	e := &countingEngine{}
	if _, err := Ensure(context.Background(), root, rel, e, nil); err != nil {
		t.Fatalf("Ensure = %v, want the image recognized and the FIFO replaced", err)
	}
	if info, err := os.Lstat(fifo); err != nil {
		t.Errorf("the sidecar after Ensure: %v", err)
	} else if !info.Mode().IsRegular() {
		t.Errorf("the sidecar after Ensure has mode %v, want a regular file", info.Mode())
	}
}

// 60 hops exceed the kernel's link limit but not filepath.EvalSymlinks's (255).
func TestEnsureDoesNotRebuildASidecarItCannotOpen(t *testing.T) {
	root := t.TempDir()
	rel := "nn/assets/a.png"
	writeImage(t, root, filepath.FromSlash(rel), []byte("bytes"))
	real := filepath.Join(root, ".nn", "ocr", "real")
	if err := os.MkdirAll(real, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(real, "a.png.json"), []byte(`{"lines":[{"text":"cached"}]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	hop := real
	for i := range 60 {
		next := filepath.Join(root, ".nn", "ocr", fmt.Sprintf("hop%d", i))
		if err := os.Symlink(hop, next); err != nil {
			t.Skipf("symlinks unavailable here: %v", err)
		}
		hop = next
	}
	symlink(t, root, ".nn/ocr/nn/assets", hop)
	if _, err := os.Stat(filepath.Join(root, ".nn", "ocr", "nn", "assets")); !errors.Is(err, syscall.ELOOP) {
		t.Skipf("the kernel follows 61 links in a row here (Stat = %v): there is no such path to test", err)
	}

	e := &countingEngine{}
	_, loadErr := LoadSidecar(root, rel)
	_, ensureErr := Ensure(context.Background(), root, rel, e, nil)
	for _, got := range []struct {
		call string
		err  error
	}{{"LoadSidecar", loadErr}, {"Ensure", ensureErr}} {
		if !errors.Is(got.err, syscall.ELOOP) {
			t.Errorf("%s = %v, want the error the open stopped with", got.call, got.err)
		}
	}
	if n := e.calls.Load(); n != 0 {
		t.Errorf("the engine ran %d times for a sidecar nn could not open", n)
	}
}

// Skipped on Linux: root reads any file regardless of mode.
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

// linkTooLongToFollow overruns PATH_MAX for filepath.EvalSymlinks, not the kernel.
func linkTooLongToFollow(t *testing.T, dir string) string {
	t.Helper()
	seg := strings.Repeat("d", 200)
	rel := filepath.Join(seg, seg, seg)
	// os.Root keeps each step's path from growing with the chain.
	r, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	far := dir
	for i := 1; i <= 10; i++ {
		name := fmt.Sprintf("l%d", i)
		if err := r.MkdirAll(rel, 0o755); err != nil {
			r.Close()
			t.Fatal(err)
		}
		if err := r.Symlink(rel, name); err != nil {
			r.Close()
			t.Skipf("symlinks unavailable here: %v", err)
		}
		next, err := r.OpenRoot(name)
		r.Close()
		if err != nil {
			t.Fatal(err)
		}
		r, far = next, filepath.Join(far, name)
	}
	r.Close()
	if info, err := os.Stat(far); err != nil || !info.IsDir() {
		t.Skipf("the kernel does not walk a chain of links spelling out a path longer than PATH_MAX here: Stat = %v, %v", info, err)
	}
	if _, err := filepath.EvalSymlinks(far); !errors.Is(err, syscall.ENAMETOOLONG) {
		t.Fatalf("filepath.EvalSymlinks(%s) = %v, want ENAMETOOLONG: the chain is no longer a link nn cannot follow", far, err)
	}
	return far
}

// The anchor is the resolved .nn/ocr, not the resolved root.
func TestSidecarWriteFollowsARelocatedCache(t *testing.T) {
	root := t.TempDir()
	elsewhere := t.TempDir()
	writeImage(t, root, filepath.Join("nn", "assets", "real.png"), []byte("bytes"))
	symlink(t, root, ".nn/ocr", elsewhere)
	rel := "nn/assets/real.png"

	e := &countingEngine{}
	if _, err := Ensure(context.Background(), root, rel, e, nil); err != nil {
		t.Fatalf("Ensure with a relocated cache: %v", err)
	}
	got, err := LoadSidecar(root, rel)
	if err != nil {
		t.Fatalf("LoadSidecar with a relocated cache: %v", err)
	}
	if len(got.Lines) != 1 || got.Lines[0].Text != "real.png" {
		t.Errorf("sidecar = %+v, want the recognized image", got)
	}
	for _, name := range []string{"real.png.txt", "real.png.json"} {
		p := filepath.Join(elsewhere, "nn", "assets", name)
		if _, err := os.Stat(p); err != nil {
			t.Errorf("%s was not written where the cache was moved to: %v", p, err)
		}
	}
}

func TestReindexSweepsSidecarsOfImagesLeadingOutOfVault(t *testing.T) {
	root, outside, _ := vaultWithNeighbour(t)
	writeImage(t, root, "good.png", []byte("g"))
	rel := "nn/assets/link.png"

	if err := SaveSidecar(root, rel, &Result{SHA256: "x", Lines: []Line{{Text: "LEAKED"}}}); err != nil {
		t.Fatal(err)
	}
	symlink(t, root, rel, filepath.Join(outside, "evil.png"))
	if _, err := LoadSidecar(root, rel); err != nil {
		t.Fatalf("test setup: the leaked sidecar is not there: %v", err)
	}

	e := &countingEngine{}
	stats, err := Reindex(context.Background(), root, []string{"good.png", rel}, e, nil, false, nil)
	if err != nil {
		t.Fatalf("err = %v, want the run to carry on past the link", err)
	}
	if stats != (Stats{Processed: 1, Outside: 1, OutsideOrphans: 1}) {
		t.Errorf("stats = %+v, want 1 indexed, 1 skipped outside and 1 of its sidecars swept", stats)
	}
	if _, err := LoadSidecar(root, rel); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("the leaked sidecar survived the sweep: %v", err)
	}
	txt, _ := SidecarPaths(root, rel)
	if _, err := os.Stat(txt); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("the leaked .txt sidecar survived the sweep: %v", err)
	}
	if _, err := LoadSidecar(root, "good.png"); err != nil {
		t.Errorf("the sidecar of a live image was swept: %v", err)
	}
	assertNeighbourUntouched(t, outside)
}
