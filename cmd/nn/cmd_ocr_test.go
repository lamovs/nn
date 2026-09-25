package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lamovs/nn/internal/ocr"
)

func TestOCRSingleImagePrintsText(t *testing.T) {
	root := newTestVault(t)
	writeImage(t, root, "nn/assets/hotkeys.png", []byte("fake image bytes"))
	withFakeOCR(t, &fakeOCREngine{lines: []ocr.Line{{Text: "Cmd+Shift+4"}}})

	stdout, stderr, code := runCmd(t, "", "ocr", "nn/assets/hotkeys.png")
	if code != 0 {
		t.Fatalf("code = %d, stderr = %q", code, stderr)
	}
	if strings.TrimSpace(stdout) != "Cmd+Shift+4" {
		t.Errorf("stdout = %q, want the recognized line", stdout)
	}
}

func TestOCRReadsPathListFromStdin(t *testing.T) {
	root := newTestVault(t)
	writeImage(t, root, "nn/assets/a.png", []byte("a"))
	writeImage(t, root, "nn/assets/b.png", []byte("b"))
	withFakeOCR(t, &fakeOCREngine{lines: []ocr.Line{{Text: "hit"}}})

	stdout, stderr, code := runCmd(t, "nn/assets/a.png\nnn/assets/b.png\n", "ocr", "-")
	if code != 0 {
		t.Fatalf("code = %d, stderr = %q", code, stderr)
	}
	if strings.Count(stdout, "hit") != 2 {
		t.Errorf("stdout = %q, want two hits (one per image, blank-line separated)", stdout)
	}
}

func TestOCRJSONIncludesResult(t *testing.T) {
	root := newTestVault(t)
	writeImage(t, root, "nn/assets/a.png", []byte("a"))
	withFakeOCR(t, &fakeOCREngine{lines: []ocr.Line{{Text: "hello"}}})

	stdout, stderr, code := runCmd(t, "", "ocr", "nn/assets/a.png", "--json")
	if code != 0 {
		t.Fatalf("code = %d, stderr = %q", code, stderr)
	}
	rows := decodeJSONRows[ocrRow](t, stdout)
	if len(rows) != 1 || rows[0].Result == nil || len(rows[0].Result.Lines) != 1 {
		t.Fatalf("rows = %+v", rows)
	}
	if rows[0].Result.Lines[0].Text != "hello" {
		t.Errorf("text = %q", rows[0].Result.Lines[0].Text)
	}
}

func TestOCRMissingFileFails(t *testing.T) {
	newTestVault(t)
	withFakeOCR(t, &fakeOCREngine{})
	_, stderr, code := runCmd(t, "", "ocr", "nn/assets/nope.png")
	if code != 2 {
		t.Errorf("code = %d, want 2, stderr = %q", code, stderr)
	}
}

func TestOCRNoArgsIsMisuse(t *testing.T) {
	newTestVault(t)
	_, stderr, code := runCmd(t, "", "ocr")
	if code != 2 {
		t.Errorf("code = %d, want 2, stderr = %q", code, stderr)
	}
}

func TestOCRLangsFlagOverridesDefault(t *testing.T) {
	root := newTestVault(t)
	writeImage(t, root, "nn/assets/a.png", []byte("a"))
	engine := &fakeOCREngine{lines: []ocr.Line{{Text: "x"}}}
	withFakeOCR(t, engine)

	_, stderr, code := runCmd(t, "", "ocr", "nn/assets/a.png", "--langs", "rus+eng")
	if code != 0 {
		t.Fatalf("code = %d, stderr = %q", code, stderr)
	}
	if len(engine.lastLangs) != 2 || engine.lastLangs[0] != "rus" || engine.lastLangs[1] != "eng" {
		t.Errorf("lastLangs = %v, want [rus eng]", engine.lastLangs)
	}
}

func TestOCRReindexFillsMissingSidecars(t *testing.T) {
	root := newTestVault(t)
	writeImage(t, root, "nn/assets/a.png", []byte("a"))
	writeImage(t, root, "nn/assets/b.png", []byte("b"))
	withFakeOCR(t, &fakeOCREngine{lines: []ocr.Line{{Text: "recognized"}}})

	_, stderr, code := runCmd(t, "", "ocr", "--reindex")
	if code != 0 {
		t.Fatalf("code = %d, stderr = %q", code, stderr)
	}
	for _, rel := range []string{"nn/assets/a.png", "nn/assets/b.png"} {
		if _, err := ocr.LoadSidecar(root, rel); err != nil {
			t.Errorf("sidecar for %s: %v", rel, err)
		}
	}
}

func TestOCRDeletedImageFailsInsteadOfPrintingCache(t *testing.T) {
	root := newTestVault(t)
	rel := "nn/assets/gone.png"
	writeImage(t, root, rel, []byte("bytes"))
	withFakeOCR(t, &fakeOCREngine{lines: []ocr.Line{{Text: "cached text"}}})

	if _, stderr, code := runCmd(t, "", "ocr", rel); code != 0 {
		t.Fatalf("code = %d, stderr = %q", code, stderr)
	}
	if err := os.Remove(filepath.Join(root, filepath.FromSlash(rel))); err != nil {
		t.Fatal(err)
	}

	stdout, stderr, code := runCmd(t, "", "ocr", rel)
	if code == 0 {
		t.Errorf("code = 0, want a failure for a deleted image")
	}
	if strings.Contains(stdout, "cached text") {
		t.Errorf("stdout = %q, want no text for an image that is gone", stdout)
	}
	if !strings.Contains(stderr, "gone.png") {
		t.Errorf("stderr = %q, want it to name the image", stderr)
	}
}

func TestOCRReindexReportsAndSweepsOrphans(t *testing.T) {
	root := newTestVault(t)
	writeImage(t, root, "nn/assets/kept.png", []byte("k"))
	writeImage(t, root, "nn/assets/gone.png", []byte("g"))
	withFakeOCR(t, &fakeOCREngine{lines: []ocr.Line{{Text: "recognized"}}})

	if _, stderr, code := runCmd(t, "", "ocr", "--reindex"); code != 0 {
		t.Fatalf("code = %d, stderr = %q", code, stderr)
	}
	if err := os.Remove(filepath.Join(root, "nn", "assets", "gone.png")); err != nil {
		t.Fatal(err)
	}

	_, stderr, code := runCmd(t, "", "ocr", "--reindex")
	if code != 0 {
		t.Fatalf("code = %d, stderr = %q", code, stderr)
	}
	if !strings.Contains(stderr, "1 image(s) indexed, 0 no longer on disk skipped, 1 orphaned sidecar(s) removed") {
		t.Errorf("stderr = %q, want the run's totals", stderr)
	}
	if _, err := ocr.LoadSidecar(root, "nn/assets/gone.png"); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("orphaned sidecar survived: %v", err)
	}
	if _, err := ocr.LoadSidecar(root, "nn/assets/kept.png"); err != nil {
		t.Errorf("sidecar of a live image was swept: %v", err)
	}
}

// vaultWithLinkOut symlinks nn/assets/link.png to an image outside the vault and returns its path.
func vaultWithLinkOut(t *testing.T, root string) (secret string) {
	t.Helper()
	secret = filepath.Join(t.TempDir(), "secret.png")
	if err := os.WriteFile(secret, []byte("secret bytes"), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "nn", "assets", "link.png")
	if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(secret, link); err != nil {
		t.Skipf("symlinks unavailable here: %v", err)
	}
	return secret
}

func TestOCRSymlinkOutOfVaultIsRefused(t *testing.T) {
	root := newTestVault(t)
	vaultWithLinkOut(t, root)
	rel := "nn/assets/link.png"

	for _, args := range [][]string{{"ocr", rel}, {"ocr", rel, "--force"}} {
		engine := &fakeOCREngine{lines: []ocr.Line{{Text: "LEAKED"}}}
		withFakeOCR(t, engine)

		stdout, stderr, code := runCmd(t, "", args...)
		if code == 0 {
			t.Errorf("%v: code = 0, want a failure", args)
		}
		if strings.Contains(stdout, "LEAKED") {
			t.Errorf("%v: stdout = %q, want no text for a file outside the vault", args, stdout)
		}
		if !strings.Contains(stderr, "outside the vault") {
			t.Errorf("%v: stderr = %q, want the vault-boundary refusal", args, stderr)
		}
		if engine.calls != 0 {
			t.Errorf("%v: the engine ran %d times on a file outside the vault", args, engine.calls)
		}
		txt, js := ocr.SidecarPaths(root, rel)
		for _, p := range []string{txt, js} {
			if _, err := os.Stat(p); !errors.Is(err, os.ErrNotExist) {
				t.Errorf("%v: sidecar %s was written: %v", args, p, err)
			}
		}
	}
}

func TestOCRReindexSkipsSymlinkOutOfVault(t *testing.T) {
	root := newTestVault(t)
	writeImage(t, root, "nn/assets/good.png", []byte("g"))
	vaultWithLinkOut(t, root)
	engine := &fakeOCREngine{lines: []ocr.Line{{Text: "LEAKED"}}}
	withFakeOCR(t, engine)

	for _, args := range [][]string{{"ocr", "--reindex"}, {"ocr", "--reindex", "--force"}} {
		stdout, stderr, code := runCmd(t, "", args...)
		if code != 0 {
			t.Fatalf("%v: code = %d, want the run to carry on past the link, stderr = %q", args, code, stderr)
		}
		if !strings.Contains(stderr, "1 image(s) indexed") || !strings.Contains(stderr, "1 outside the vault skipped") {
			t.Errorf("%v: stderr = %q, want the link counted as skipped in the totals", args, stderr)
		}
		if strings.Contains(stdout, "LEAKED") {
			t.Errorf("%v: stdout = %q, want no text for a file outside the vault", args, stdout)
		}
		if _, err := ocr.LoadSidecar(root, "nn/assets/link.png"); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("%v: a sidecar was written for the link: %v", args, err)
		}
		if _, err := ocr.LoadSidecar(root, "nn/assets/good.png"); err != nil {
			t.Errorf("%v: the image inside the vault was not indexed: %v", args, err)
		}
	}
	if engine.calls != 2 {
		t.Errorf("the engine ran %d times, want one call per run for the image inside the vault", engine.calls)
	}
}

func TestOCRPathOutsideVaultIsRefused(t *testing.T) {
	root := newTestVault(t)
	outside := t.TempDir()
	abs := filepath.Join(outside, "evil.png")
	if err := os.WriteFile(abs, []byte("bytes"), 0o644); err != nil {
		t.Fatal(err)
	}
	guarded := abs + ".txt"
	if err := os.WriteFile(guarded, []byte("mine\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	rel, err := filepath.Rel(root, abs)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(rel, "..") {
		t.Fatalf("test setup: %q does not climb out of the vault", rel)
	}

	for _, ref := range []string{filepath.ToSlash(rel), abs} {
		engine := &fakeOCREngine{lines: []ocr.Line{{Text: "LEAKED"}}}
		withFakeOCR(t, engine)

		stdout, stderr, code := runCmd(t, "", "ocr", ref)
		if code == 0 {
			t.Errorf("%s: code = 0, want a failure", ref)
		}
		if !strings.Contains(stderr, "is outside the vault") {
			t.Errorf("%s: stderr = %q, want the vault-boundary refusal", ref, stderr)
		}
		if strings.Contains(stdout, "LEAKED") {
			t.Errorf("%s: stdout = %q, want no text for a file outside the vault", ref, stdout)
		}
		if engine.calls != 0 {
			t.Errorf("%s: the engine ran %d times on a file outside the vault", ref, engine.calls)
		}
		body, err := os.ReadFile(guarded)
		if err != nil || string(body) != "mine\n" {
			t.Errorf("%s: the file next to it = (%q, %v), want it untouched", ref, body, err)
		}
		entries, err := os.ReadDir(outside)
		if err != nil {
			t.Fatal(err)
		}
		if len(entries) != 2 {
			t.Errorf("%s: %d files outside the vault, want only the two it started with", ref, len(entries))
		}
	}
}

func TestOCRReindexKeepsForeignFilesUnderSidecarDir(t *testing.T) {
	root := newTestVault(t)
	writeImage(t, root, "nn/assets/hotkeys.png", []byte("h"))
	withFakeOCR(t, &fakeOCREngine{lines: []ocr.Line{{Text: "recognized"}}})

	if _, stderr, code := runCmd(t, "", "ocr", "--reindex"); code != 0 {
		t.Fatalf("code = %d, stderr = %q", code, stderr)
	}
	if err := os.Remove(filepath.Join(root, "nn", "assets", "hotkeys.png")); err != nil {
		t.Fatal(err)
	}

	foreign := map[string]string{
		"note.md":          "hand-written\n",
		"inner.txt":        "not a sidecar\n",
		"dir/inner.txt":    "not a sidecar either\n",
		"dir/deep/db.json": "{}\n",
	}
	for name, body := range foreign {
		p := filepath.Join(root, ".nn", "ocr", filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	_, stderr, code := runCmd(t, "", "ocr", "--reindex")
	if code != 0 {
		t.Fatalf("code = %d, stderr = %q", code, stderr)
	}
	if !strings.Contains(stderr, "1 orphaned sidecar(s) removed") {
		t.Errorf("stderr = %q, want exactly the one real orphan counted", stderr)
	}
	for name, want := range foreign {
		got, err := os.ReadFile(filepath.Join(root, ".nn", "ocr", filepath.FromSlash(name)))
		if err != nil || string(got) != want {
			t.Errorf("%s: (%q, %v), want it left where it is", name, got, err)
		}
	}
	if _, err := ocr.LoadSidecar(root, "nn/assets/hotkeys.png"); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the real orphaned sidecar survived: %v", err)
	}
}

func TestOCRReindexForceRerecognizesFreshSidecars(t *testing.T) {
	root := newTestVault(t)
	writeImage(t, root, "nn/assets/a.png", []byte("a"))
	engine := &fakeOCREngine{lines: []ocr.Line{{Text: "first"}}}
	withFakeOCR(t, engine)

	_, stderr, code := runCmd(t, "", "ocr", "--reindex")
	if code != 0 {
		t.Fatalf("code = %d, stderr = %q", code, stderr)
	}
	if engine.calls != 1 {
		t.Fatalf("calls = %d after first reindex, want 1", engine.calls)
	}

	_, stderr, code = runCmd(t, "", "ocr", "--reindex")
	if code != 0 {
		t.Fatalf("code = %d, stderr = %q", code, stderr)
	}
	if engine.calls != 1 {
		t.Errorf("calls = %d after a second, non-forced reindex, want still 1 (sidecar is fresh)", engine.calls)
	}

	_, stderr, code = runCmd(t, "", "ocr", "--reindex", "--force")
	if code != 0 {
		t.Fatalf("code = %d, stderr = %q", code, stderr)
	}
	if engine.calls != 2 {
		t.Errorf("calls = %d after --force, want 2", engine.calls)
	}
}

// symlinkedRoot points NN_ROOT at a symlink, as macOS's /tmp -> /private/tmp does, and returns both spellings.
func symlinkedRoot(t *testing.T) (root, realRoot string) {
	t.Helper()
	base := newTestVault(t)
	real := filepath.Join(base, "real")
	if err := os.MkdirAll(real, 0o755); err != nil {
		t.Fatal(err)
	}
	root = filepath.Join(base, "vault")
	if err := os.Symlink(real, root); err != nil {
		t.Skipf("symlinks unavailable here: %v", err)
	}
	t.Setenv("NN_ROOT", root)
	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	return root, realRoot
}

// The vault-boundary check must resolve both the root and the argument, or the macOS
// /tmp -> /private/tmp symlink makes a path plainly inside the vault look outside it.
func TestOCRAcceptsTheResolvedSpellingOfTheRoot(t *testing.T) {
	root, realRoot := symlinkedRoot(t)
	writeImage(t, root, "nn/assets/hotkeys.png", []byte("fake image bytes"))
	withFakeOCR(t, &fakeOCREngine{lines: []ocr.Line{{Text: "Cmd+Shift+4"}}})

	for _, ref := range []string{
		filepath.Join(root, "nn", "assets", "hotkeys.png"),
		filepath.Join(realRoot, "nn", "assets", "hotkeys.png"),
	} {
		stdout, stderr, code := runCmd(t, "", "ocr", ref)
		if code != 0 {
			t.Errorf("%s: code = %d, stderr = %q", ref, code, stderr)
		}
		if !strings.Contains(stdout, "Cmd+Shift+4") {
			t.Errorf("%s: stdout = %q, want the recognized line", ref, stdout)
		}
	}
	if _, err := ocr.LoadSidecar(root, "nn/assets/hotkeys.png"); err != nil {
		t.Errorf("the sidecar is not filed under the vault-relative path: %v", err)
	}
}

// The vault boundary must also hold on the way out: a symlinked directory under .nn/ocr
// must not let a sidecar write follow it outside the vault.
func TestOCRSidecarDirSymlinkWritesNothingOutsideTheVault(t *testing.T) {
	root := newTestVault(t)
	outside := t.TempDir()
	guarded := filepath.Join(outside, "real.png.txt")
	if err := os.WriteFile(guarded, []byte("mine\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	writeImage(t, root, "nn/assets/real.png", []byte("bytes"))
	link := filepath.Join(root, ".nn", "ocr", "nn", "assets")
	if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("symlinks unavailable here: %v", err)
	}

	for _, args := range [][]string{{"ocr", "nn/assets/real.png"}, {"ocr", "nn/assets/real.png", "--force"}} {
		withFakeOCR(t, &fakeOCREngine{lines: []ocr.Line{{Text: "recognized"}}})
		_, stderr, code := runCmd(t, "", args...)
		if code == 0 {
			t.Errorf("%v: code = 0, want a refusal to write outside the vault", args)
		}
		if !strings.Contains(stderr, "outside the vault") {
			t.Errorf("%v: stderr = %q, want the vault-boundary refusal", args, stderr)
		}
		body, err := os.ReadFile(guarded)
		if err != nil || string(body) != "mine\n" {
			t.Errorf("%v: the file outside the vault = (%q, %v), want it untouched", args, body, err)
		}
		entries, err := os.ReadDir(outside)
		if err != nil {
			t.Fatal(err)
		}
		if len(entries) != 1 {
			t.Errorf("%v: %d files outside the vault, want only the one it started with", args, len(entries))
		}
	}
}

// os.Stat on an image follows a symlink, so a sidecar leaked through one must be swept
// on its own: the image "exists" by Stat even though it leads out of the vault.
func TestOCRReindexSweepsSidecarOfImageLeadingOutOfVault(t *testing.T) {
	root := newTestVault(t)
	writeImage(t, root, "nn/assets/good.png", []byte("g"))
	vaultWithLinkOut(t, root)
	writeSidecar(t, root, "nn/assets/link.png", []ocr.Line{{Text: "LEAKED"}})
	withFakeOCR(t, &fakeOCREngine{lines: []ocr.Line{{Text: "recognized"}}})

	_, stderr, code := runCmd(t, "", "ocr", "--reindex")
	if code != 0 {
		t.Fatalf("code = %d, stderr = %q", code, stderr)
	}
	if !strings.Contains(stderr, "1 sidecar(s) of images outside the vault removed") {
		t.Errorf("stderr = %q, want the swept sidecar counted on its own", stderr)
	}
	if !strings.Contains(stderr, "0 orphaned sidecar(s) removed") {
		t.Errorf("stderr = %q, want the ordinary orphan count left at zero", stderr)
	}
	if _, err := ocr.LoadSidecar(root, "nn/assets/link.png"); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the leaked sidecar survived the sweep: %v", err)
	}
	if _, err := ocr.LoadSidecar(root, "nn/assets/good.png"); err != nil {
		t.Errorf("the sidecar of a live image was swept: %v", err)
	}
}
