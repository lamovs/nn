package ocr

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

// countingEngine returns one line naming the image and counts calls.
type countingEngine struct {
	calls atomic.Int32
	fail  string // image base name to fail on
	block chan struct{}
}

func (e *countingEngine) Name() string                    { return "fake" }
func (e *countingEngine) Check(ctx context.Context) error { return nil }

func (e *countingEngine) Recognize(ctx context.Context, imagePath string, langs []string) (*Result, error) {
	e.calls.Add(1)
	if e.block != nil {
		select {
		case <-e.block:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if filepath.Base(imagePath) == e.fail {
		return nil, errors.New("unreadable image")
	}
	return &Result{Engine: "fake", Langs: langs, Lines: []Line{{Text: filepath.Base(imagePath)}}}, nil
}

func TestEnsureCachesUntilImageChanges(t *testing.T) {
	root := t.TempDir()
	rel := "nn/assets/a.png"
	writeImage(t, root, rel, []byte("v1"))
	e := &countingEngine{}
	ctx := context.Background()

	r, err := Ensure(ctx, root, rel, e, []string{"eng"})
	if err != nil {
		t.Fatal(err)
	}
	if e.calls.Load() != 1 || r.SHA256 != sha256Hex([]byte("v1")) || r.Lines[0].Text != "a.png" {
		t.Fatalf("first Ensure: calls=%d result=%+v", e.calls.Load(), r)
	}
	if _, err := LoadSidecar(root, rel); err != nil {
		t.Fatalf("sidecar not saved: %v", err)
	}

	if _, err := Ensure(ctx, root, rel, e, []string{"eng"}); err != nil {
		t.Fatal(err)
	}
	if e.calls.Load() != 1 {
		t.Errorf("fresh sidecar was recomputed")
	}

	writeImage(t, root, rel, []byte("v2"))
	r, err = Ensure(ctx, root, rel, e, []string{"eng"})
	if err != nil {
		t.Fatal(err)
	}
	if e.calls.Load() != 2 || r.SHA256 != sha256Hex([]byte("v2")) {
		t.Errorf("changed image: calls=%d sha=%s", e.calls.Load(), r.SHA256)
	}
}

func TestEnsureRebuildsCorruptSidecar(t *testing.T) {
	root := t.TempDir()
	rel := "img/b.png"
	writeImage(t, root, rel, []byte("data"))
	_, js := SidecarPaths(root, rel)
	if err := os.MkdirAll(filepath.Dir(js), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(js, []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	e := &countingEngine{}
	if _, err := Ensure(context.Background(), root, rel, e, nil); err != nil {
		t.Fatal(err)
	}
	if e.calls.Load() != 1 {
		t.Errorf("calls = %d", e.calls.Load())
	}
}

func TestEnsureMissingImage(t *testing.T) {
	_, err := Ensure(context.Background(), t.TempDir(), "nope.png", &countingEngine{}, nil)
	if !errors.Is(err, os.ErrNotExist) {
		t.Errorf("err = %v", err)
	}
}

func TestReindex(t *testing.T) {
	root := t.TempDir()
	images := []string{"a.png", "b.png", "c.png", "d.png", "e.png"}
	for _, rel := range images {
		writeImage(t, root, rel, []byte(rel))
	}
	ctx := context.Background()
	e := &countingEngine{}
	if _, err := Ensure(ctx, root, "a.png", e, nil); err != nil {
		t.Fatal(err)
	}

	var mu sync.Mutex
	var seen [][2]int
	progress := func(done, total int) {
		mu.Lock()
		defer mu.Unlock()
		seen = append(seen, [2]int{done, total})
	}
	stats, err := Reindex(ctx, root, images, e, nil, false, progress)
	if err != nil || stats != (Stats{Processed: 5}) {
		t.Fatalf("stats=%+v err=%v", stats, err)
	}
	if got := e.calls.Load(); got != 5 {
		t.Errorf("calls = %d, want 1 + 4 stale images", got)
	}
	want := [][2]int{{0, 5}, {1, 5}, {2, 5}, {3, 5}, {4, 5}, {5, 5}}
	if !slices.Equal(seen, want) {
		t.Errorf("progress = %v", seen)
	}

	if _, err := Reindex(ctx, root, images, e, nil, true, nil); err != nil {
		t.Fatal(err)
	}
	if got := e.calls.Load(); got != 10 {
		t.Errorf("force: calls = %d", got)
	}
}

func TestReindexCollectsErrors(t *testing.T) {
	root := t.TempDir()
	images := []string{"good.png", "bad.png", "missing.png"}
	writeImage(t, root, "good.png", []byte("g"))
	writeImage(t, root, "bad.png", []byte("b"))
	e := &countingEngine{fail: "bad.png"}

	stats, err := Reindex(context.Background(), root, images, e, nil, false, nil)
	if err == nil || !strings.Contains(err.Error(), "bad.png: unreadable image") {
		t.Fatalf("err = %v", err)
	}
	if stats.Skipped != 1 || stats.Processed != 1 || strings.Contains(err.Error(), "missing.png") {
		t.Errorf("stats=%+v err=%v", stats, err)
	}
	if _, err := LoadSidecar(root, "good.png"); err != nil {
		t.Errorf("good image not indexed: %v", err)
	}
}

func TestReindexSkipsDeletedImages(t *testing.T) {
	root := t.TempDir()
	images := []string{"kept.png", "gone.png"}
	writeImage(t, root, "kept.png", []byte("k"))
	writeImage(t, root, "gone.png", []byte("g"))
	e := &countingEngine{}
	ctx := context.Background()
	if _, err := Ensure(ctx, root, "gone.png", e, nil); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(root, "gone.png")); err != nil {
		t.Fatal(err)
	}

	for _, force := range []bool{false, true} {
		before := e.calls.Load()
		wantOrphans := 0
		if !force {
			// The first pass is the one that still finds the leftover.
			wantOrphans = 1
		}
		stats, err := Reindex(ctx, root, images, e, nil, force, nil)
		if err != nil || stats != (Stats{Processed: 1, Skipped: 1, Orphans: wantOrphans}) {
			t.Fatalf("force=%v: stats=%+v err=%v", force, stats, err)
		}
		if got := e.calls.Load() - before; got != 1 {
			t.Errorf("force=%v: engine ran %d times, want only the kept image", force, got)
		}
	}
	if _, err := LoadSidecar(root, "gone.png"); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("sidecar of the deleted image survived: %v", err)
	}
	txt, _ := SidecarPaths(root, "gone.png")
	if _, err := os.Stat(txt); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("text sidecar of the deleted image survived: %v", err)
	}
	if _, err := LoadSidecar(root, "kept.png"); err != nil {
		t.Errorf("sidecar of a live image was swept: %v", err)
	}
}

func TestReindexRemovesOrphanedSidecars(t *testing.T) {
	root := t.TempDir()
	rel := "shots/deep/gone.png"
	writeImage(t, root, rel, []byte("g"))
	e := &countingEngine{}
	ctx := context.Background()
	if _, err := Ensure(ctx, root, rel, e, nil); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(root, filepath.FromSlash(rel))); err != nil {
		t.Fatal(err)
	}
	// A file under .nn/ocr that is not a sidecar must be left alone.
	stray := filepath.Join(root, ".nn", "ocr", "notes.md")
	if err := os.WriteFile(stray, []byte("hand-written\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	stats, err := Reindex(ctx, root, nil, e, nil, false, nil)
	if err != nil || stats != (Stats{Orphans: 1}) {
		t.Fatalf("stats=%+v err=%v", stats, err)
	}
	txt, js := SidecarPaths(root, rel)
	for _, p := range []string{txt, js} {
		if _, err := os.Stat(p); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("%s survived: %v", p, err)
		}
	}
	if _, err := os.Stat(stray); err != nil {
		t.Errorf("a non-sidecar file under .nn/ocr was removed: %v", err)
	}

	// Nothing orphaned left: a second run has nothing to remove.
	stats, err = Reindex(ctx, root, nil, e, nil, false, nil)
	if err != nil || stats != (Stats{}) {
		t.Fatalf("second run: stats=%+v err=%v", stats, err)
	}
}

func TestEnsureFailsOnDeletedImage(t *testing.T) {
	root := t.TempDir()
	rel := "shots/gone.png"
	writeImage(t, root, rel, []byte("v1"))
	e := &countingEngine{}
	ctx := context.Background()
	if _, err := Ensure(ctx, root, rel, e, []string{"eng"}); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(root, rel)); err != nil {
		t.Fatal(err)
	}

	stale, err := Stale(root, rel)
	if !errors.Is(err, os.ErrNotExist) || stale {
		t.Errorf("Stale = (%v, %v), want (false, a not-exist error)", stale, err)
	}
	r, err := Ensure(ctx, root, rel, e, []string{"eng"})
	if !errors.Is(err, os.ErrNotExist) || r != nil {
		t.Fatalf("Ensure = (%+v, %v), want a not-exist error", r, err)
	}
	if e.calls.Load() != 1 {
		t.Errorf("a deleted image was sent to the engine")
	}
	if _, err := LoadSidecar(root, rel); err != nil {
		t.Errorf("Ensure dropped the sidecar; only Reindex sweeps: %v", err)
	}
}

func TestEnsureRecognizesAgainForOtherLangs(t *testing.T) {
	root := t.TempDir()
	rel := "a.png"
	writeImage(t, root, rel, []byte("v1"))
	e := &countingEngine{}
	ctx := context.Background()
	if _, err := Ensure(ctx, root, rel, e, []string{"rus", "eng"}); err != nil {
		t.Fatal(err)
	}

	// Same set, other order and other engine's spelling: still cached.
	for _, langs := range [][]string{{"eng", "rus"}, {"en-US", "ru-RU"}, nil} {
		if _, err := Ensure(ctx, root, rel, e, langs); err != nil {
			t.Fatal(err)
		}
		if e.calls.Load() != 1 {
			t.Fatalf("langs %q recomputed a fresh sidecar", langs)
		}
	}

	r, err := Ensure(ctx, root, rel, e, []string{"deu"})
	if err != nil {
		t.Fatal(err)
	}
	if e.calls.Load() != 2 || !slices.Equal(r.Langs, []string{"deu"}) {
		t.Errorf("other langs: calls=%d langs=%q", e.calls.Load(), r.Langs)
	}
	cached, err := LoadSidecar(root, rel)
	if err != nil || !slices.Equal(cached.Langs, []string{"deu"}) {
		t.Errorf("sidecar langs = %q, err = %v", cached.Langs, err)
	}
}

func TestReindexSweepKeepsForeignFiles(t *testing.T) {
	root := t.TempDir()
	rel := "shots/gone.png"
	writeImage(t, root, rel, []byte("g"))
	e := &countingEngine{}
	ctx := context.Background()
	if _, err := Ensure(ctx, root, rel, e, nil); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(root, filepath.FromSlash(rel))); err != nil {
		t.Fatal(err)
	}

	foreign := map[string]string{
		"notes.md":                "hand-written\n",
		"inner.txt":               "not a sidecar\n",
		"dir/inner.txt":           "not a sidecar either\n",
		"dir/deep/data.json":      "{}\n",
		"shots/gone.png.json.txt": "not a sidecar of anything\n",
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

	stats, err := Reindex(ctx, root, nil, e, nil, false, nil)
	if err != nil || stats != (Stats{Orphans: 1}) {
		t.Fatalf("stats=%+v err=%v, want exactly the one real orphan", stats, err)
	}
	for name, want := range foreign {
		got, err := os.ReadFile(filepath.Join(root, ".nn", "ocr", filepath.FromSlash(name)))
		if err != nil || string(got) != want {
			t.Errorf("%s: (%q, %v), want it left where it is", name, got, err)
		}
	}
	txt, js := SidecarPaths(root, rel)
	for _, p := range []string{txt, js} {
		if _, err := os.Stat(p); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("the real orphan %s survived: %v", p, err)
		}
	}
}

// deletingEngine simulates an image deleted mid-run.
type deletingEngine struct{ calls atomic.Int32 }

func (e *deletingEngine) Name() string                    { return "deleting" }
func (e *deletingEngine) Check(ctx context.Context) error { return nil }

func (e *deletingEngine) Recognize(ctx context.Context, imagePath string, langs []string) (*Result, error) {
	e.calls.Add(1)
	if err := os.Remove(imagePath); err != nil {
		return nil, err
	}
	_, err := os.ReadFile(imagePath)
	return nil, err
}

func TestReindexSkipsImageDeletedMidRun(t *testing.T) {
	for _, force := range []bool{false, true} {
		t.Run(fmt.Sprintf("force=%v", force), func(t *testing.T) {
			root := t.TempDir()
			images := []string{"a.png", "b.png"}
			for _, rel := range images {
				writeImage(t, root, rel, []byte(rel))
			}
			e := &deletingEngine{}
			stats, err := Reindex(context.Background(), root, images, e, nil, force, nil)
			if err != nil {
				t.Fatalf("err = %v, want images that vanished mid-run to be skipped", err)
			}
			if stats != (Stats{Skipped: len(images)}) {
				t.Errorf("stats = %+v, want %d skipped", stats, len(images))
			}
		})
	}
}

func TestReindexKeepsNotExistErrorWhenImageIsThere(t *testing.T) {
	root := t.TempDir()
	writeImage(t, root, "a.png", []byte("a"))
	stats, err := Reindex(context.Background(), root, []string{"a.png"}, missingToolEngine{}, nil, false, nil)
	if err == nil || !strings.Contains(err.Error(), "nn-vision") {
		t.Fatalf("err = %v, want the missing program's error", err)
	}
	if stats != (Stats{}) {
		t.Errorf("stats = %+v, want nothing skipped: the image is still on disk", stats)
	}
}

type missingToolEngine struct{}

func (missingToolEngine) Name() string                    { return "missing-tool" }
func (missingToolEngine) Check(ctx context.Context) error { return nil }

func (missingToolEngine) Recognize(ctx context.Context, imagePath string, langs []string) (*Result, error) {
	return nil, &fs.PathError{Op: "fork/exec", Path: "/usr/local/bin/nn-vision", Err: fs.ErrNotExist}
}

func TestReindexCancel(t *testing.T) {
	root := t.TempDir()
	var images []string
	for _, name := range []string{"a", "b", "c", "d", "e", "f", "g", "h"} {
		rel := name + ".png"
		writeImage(t, root, rel, []byte(name))
		images = append(images, rel)
	}
	ctx, cancel := context.WithCancel(context.Background())
	e := &countingEngine{block: make(chan struct{})}
	started := make(chan struct{}, len(images))
	progress := func(done, total int) {
		if done == 0 {
			started <- struct{}{}
		}
	}
	go func() {
		<-started
		cancel()
	}()
	if _, err := Reindex(ctx, root, images, e, nil, false, progress); !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v", err)
	}
	if n := e.calls.Load(); n > int32(reindexWorkers(len(images))) {
		t.Errorf("%d images started after cancel", n)
	}
}
