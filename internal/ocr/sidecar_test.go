package ocr

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeImage(t *testing.T, root, rel string, data []byte) {
	t.Helper()
	p := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, data, 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestSidecarPaths(t *testing.T) {
	root := filepath.Join(t.TempDir(), "vault")
	txt, js := SidecarPaths(root, "nn/assets/foo.png")
	wantTxt := filepath.Join(root, ".nn/ocr/nn/assets/foo.png.txt")
	wantJS := filepath.Join(root, ".nn/ocr/nn/assets/foo.png.json")
	if txt != wantTxt || js != wantJS {
		t.Errorf("got (%q, %q), want (%q, %q)", txt, js, wantTxt, wantJS)
	}
}

func TestLoadSidecarMissing(t *testing.T) {
	root := t.TempDir()
	if _, err := LoadSidecar(root, "nn/assets/foo.png"); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("err = %v, want fs.ErrNotExist", err)
	}
}

func TestSaveAndLoadSidecarRoundTrip(t *testing.T) {
	root := t.TempDir()
	rel := "nn/assets/foo.png"

	r := &Result{
		Engine: "tesseract",
		Langs:  []string{"eng"},
		Width:  100,
		Height: 50,
		SHA256: "deadbeef",
		Lines: []Line{
			{Text: "hello", Box: Box{X: 0, Y: 0, W: 0.5, H: 0.1}, Confidence: 0.9},
			{Text: "world", Box: Box{X: 0, Y: 0.1, W: 0.5, H: 0.1}, Confidence: 0.8},
		},
	}
	if err := SaveSidecar(root, rel, r); err != nil {
		t.Fatal(err)
	}

	got, err := LoadSidecar(root, rel)
	if err != nil {
		t.Fatal(err)
	}
	if got.Engine != r.Engine || got.SHA256 != r.SHA256 || len(got.Lines) != 2 {
		t.Errorf("got = %+v", got)
	}
	if got.Lines[1].Text != "world" {
		t.Errorf("Lines[1].Text = %q", got.Lines[1].Text)
	}

	txtPath, _ := SidecarPaths(root, rel)
	body, err := os.ReadFile(txtPath)
	if err != nil {
		t.Fatal(err)
	}
	if got := string(body); got != "hello\nworld\n" {
		t.Errorf(".txt = %q", got)
	}
}

func TestSaveSidecarLeavesNoTempFiles(t *testing.T) {
	root := t.TempDir()
	rel := "img.png"
	if err := SaveSidecar(root, rel, &Result{SHA256: "x"}); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(root, ".nn/ocr")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.Contains(e.Name(), ".nn-ocr-") {
			t.Errorf("leftover temp file: %s", e.Name())
		}
	}
}

func TestStaleNoSidecar(t *testing.T) {
	root := t.TempDir()
	rel := "img.png"
	writeImage(t, root, rel, []byte("image-bytes"))

	stale, err := Stale(root, rel)
	if err != nil {
		t.Fatal(err)
	}
	if !stale {
		t.Error("expected stale=true when there is no sidecar yet")
	}
}

func TestStaleMatchingSHA(t *testing.T) {
	root := t.TempDir()
	rel := "img.png"
	writeImage(t, root, rel, []byte("image-bytes"))

	sum, err := imageSHA256(filepath.Join(root, rel))
	if err != nil {
		t.Fatal(err)
	}
	if err := SaveSidecar(root, rel, &Result{SHA256: sum}); err != nil {
		t.Fatal(err)
	}

	stale, err := Stale(root, rel)
	if err != nil {
		t.Fatal(err)
	}
	if stale {
		t.Error("expected stale=false when the sha256 matches")
	}
}

func TestStaleDeletedImage(t *testing.T) {
	root := t.TempDir()
	rel := "img.png"

	stale, err := Stale(root, rel)
	if err != nil || !stale {
		t.Errorf("no image, no sidecar: (%v, %v), want (true, nil)", stale, err)
	}

	if err := SaveSidecar(root, rel, &Result{SHA256: "deadbeef"}); err != nil {
		t.Fatal(err)
	}
	stale, err = Stale(root, rel)
	if !errors.Is(err, fs.ErrNotExist) || stale {
		t.Errorf("sidecar of a deleted image: (%v, %v), want (false, a not-exist error)", stale, err)
	}
}

func TestStaleCorruptSidecar(t *testing.T) {
	root := t.TempDir()
	rel := "img.png"
	writeImage(t, root, rel, []byte("image-bytes"))
	_, js := SidecarPaths(root, rel)
	if err := os.MkdirAll(filepath.Dir(js), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(js, []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}

	stale, err := Stale(root, rel)
	if err != nil || !stale {
		t.Errorf("corrupt sidecar: (%v, %v), want (true, nil)", stale, err)
	}
}

func TestStaleChangedImage(t *testing.T) {
	root := t.TempDir()
	rel := "img.png"
	writeImage(t, root, rel, []byte("original"))

	sum, err := imageSHA256(filepath.Join(root, rel))
	if err != nil {
		t.Fatal(err)
	}
	if err := SaveSidecar(root, rel, &Result{SHA256: sum}); err != nil {
		t.Fatal(err)
	}

	writeImage(t, root, rel, []byte("changed contents"))

	stale, err := Stale(root, rel)
	if err != nil {
		t.Fatal(err)
	}
	if !stale {
		t.Error("expected stale=true after the image changed")
	}
}
