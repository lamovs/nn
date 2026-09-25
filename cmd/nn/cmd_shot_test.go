package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/lamovs/nn/internal/config"
	"github.com/lamovs/nn/internal/ocr"
	"github.com/lamovs/nn/internal/platform"
)

// fakeOCREngine's bookkeeping is mutex-guarded: ocr.Reindex calls Recognize from a worker pool.
type fakeOCREngine struct {
	lines []ocr.Line
	err   error

	mu        sync.Mutex
	calls     int
	lastLangs []string
}

func (f *fakeOCREngine) Name() string                    { return "fake" }
func (f *fakeOCREngine) Check(ctx context.Context) error { return nil }
func (f *fakeOCREngine) Recognize(ctx context.Context, imagePath string, langs []string) (*ocr.Result, error) {
	f.mu.Lock()
	f.calls++
	f.lastLangs = langs
	f.mu.Unlock()
	if f.err != nil {
		return nil, f.err
	}
	return &ocr.Result{Engine: "fake", Langs: langs, Lines: f.lines}, nil
}

func withFakeScreenshot(t *testing.T, data []byte, ext string, err error) {
	t.Helper()
	prev := captureScreenshot
	captureScreenshot = func(ctx context.Context, cfg config.Config) ([]byte, string, error) { return data, ext, err }
	t.Cleanup(func() { captureScreenshot = prev })
}

// withNoScreenshotExpected fails the test if the screenshot stub is ever called.
func withNoScreenshotExpected(t *testing.T) {
	t.Helper()
	prev := captureScreenshot
	captureScreenshot = func(ctx context.Context, cfg config.Config) ([]byte, string, error) {
		t.Error("screenshot taken despite a usage error")
		return nil, "", platform.ErrCancelled
	}
	t.Cleanup(func() { captureScreenshot = prev })
}

func withFakeOCR(t *testing.T, engine *fakeOCREngine) {
	t.Helper()
	prevEngine, prevLangs := ocrEngineFor, defaultOCRLangs
	ocrEngineFor = func(config.Config) ocr.Engine { return engine }
	defaultOCRLangs = func(config.Config) []string { return []string{"eng"} }
	t.Cleanup(func() { ocrEngineFor, defaultOCRLangs = prevEngine, prevLangs })
}

func withFakeClipboardCopy(t *testing.T) (copied *string) {
	t.Helper()
	var text string
	prev := captureCopyText
	captureCopyText = func(ctx context.Context, s string) error { text = s; return nil }
	t.Cleanup(func() { captureCopyText = prev })
	return &text
}

var fakePNG = []byte("\x89PNG\r\n\x1a\nfakedata")

func TestShotCreatesNoteWithImageAndOCR(t *testing.T) {
	root := newTestVault(t)
	withFakeScreenshot(t, fakePNG, "png", nil)
	withFakeOCR(t, &fakeOCREngine{lines: []ocr.Line{{Text: "Cmd+Shift+4"}}})

	stdout, stderr, code := runCmd(t, "", "shot", "-t", "hotkeys")
	if code != 0 {
		t.Fatalf("code = %d, stderr = %q", code, stderr)
	}
	if !strings.HasPrefix(strings.TrimSpace(stdout), "+ ") {
		t.Errorf("stdout = %q, want a + prefix", stdout)
	}
	entries, err := os.ReadDir(filepath.Join(root, "nn", "assets"))
	if err != nil || len(entries) != 1 {
		t.Fatalf("expected one saved asset: %v, %v", err, entries)
	}
	sidecar, err := ocr.LoadSidecar(root, "nn/assets/"+entries[0].Name())
	if err != nil {
		t.Fatalf("no OCR sidecar was written: %v", err)
	}
	if len(sidecar.Lines) != 1 || sidecar.Lines[0].Text != "Cmd+Shift+4" {
		t.Errorf("sidecar = %+v, want the recognized line", sidecar)
	}
}

func TestShotCancelledExitsZero(t *testing.T) {
	root := newTestVault(t)
	withFakeScreenshot(t, nil, "", platform.ErrCancelled)

	_, stderr, code := runCmd(t, "", "shot")
	if code != 0 {
		t.Fatalf("code = %d, want 0, stderr = %q", code, stderr)
	}
	if !strings.Contains(stderr, "cancelled") {
		t.Errorf("stderr = %q, want a mention of cancellation", stderr)
	}
	entries, _ := os.ReadDir(filepath.Join(root, "nn"))
	if len(entries) != 0 {
		t.Errorf("a note was written despite the cancelled selection: %v", entries)
	}
}

func TestShotCopyTextPutsOCROnClipboard(t *testing.T) {
	newTestVault(t)
	withFakeScreenshot(t, fakePNG, "png", nil)
	withFakeOCR(t, &fakeOCREngine{lines: []ocr.Line{{Text: "hello"}, {Text: "world"}}})
	copied := withFakeClipboardCopy(t)

	_, stderr, code := runCmd(t, "", "shot", "--copy-text")
	if code != 0 {
		t.Fatalf("code = %d, stderr = %q", code, stderr)
	}
	if *copied != "hello\nworld" {
		t.Errorf("copied = %q, want the recognized text joined by newlines", *copied)
	}
}

func TestShotNoOCRSkipsRecognition(t *testing.T) {
	newTestVault(t)
	withFakeScreenshot(t, fakePNG, "png", nil)
	engine := &fakeOCREngine{lines: []ocr.Line{{Text: "should not run"}}}
	withFakeOCR(t, engine)

	_, stderr, code := runCmd(t, "", "shot", "--no-ocr")
	if code != 0 {
		t.Fatalf("code = %d, stderr = %q", code, stderr)
	}
	if engine.calls != 0 {
		t.Errorf("OCR engine was called %d times, want 0 with --no-ocr", engine.calls)
	}
}

func TestShotOCRDefaultOffFromConfig(t *testing.T) {
	newTestVault(t)
	writeConfig(t, "[capture]\nocr = false\n")
	withFakeScreenshot(t, fakePNG, "png", nil)
	engine := &fakeOCREngine{lines: []ocr.Line{{Text: "should not run"}}}
	withFakeOCR(t, engine)

	_, stderr, code := runCmd(t, "", "shot")
	if code != 0 {
		t.Fatalf("code = %d, stderr = %q", code, stderr)
	}
	if engine.calls != 0 {
		t.Errorf("OCR engine was called %d times, want 0 with capture.ocr = false", engine.calls)
	}
}

func TestShotOCRFlagForcesOCROverConfig(t *testing.T) {
	newTestVault(t)
	writeConfig(t, "[capture]\nocr = false\n")
	withFakeScreenshot(t, fakePNG, "png", nil)
	engine := &fakeOCREngine{lines: []ocr.Line{{Text: "hello"}}}
	withFakeOCR(t, engine)

	_, stderr, code := runCmd(t, "", "shot", "--ocr")
	if code != 0 {
		t.Fatalf("code = %d, stderr = %q", code, stderr)
	}
	if engine.calls != 1 {
		t.Errorf("OCR engine was called %d times, want 1 (--ocr beats capture.ocr = false)", engine.calls)
	}
}

func TestShotOCRFlagsCannotCombine(t *testing.T) {
	newTestVault(t)
	withNoScreenshotExpected(t)
	_, stderr, code := runCmd(t, "", "shot", "--ocr", "--no-ocr")
	if code != 2 {
		t.Errorf("code = %d, want 2, stderr = %q", code, stderr)
	}
	if !strings.Contains(stderr, "--ocr and --no-ocr cannot be combined") {
		t.Errorf("stderr = %q, want it to name the conflicting flags", stderr)
	}
}

func TestShotCopyTextForcesOCROverCaptureOCROff(t *testing.T) {
	newTestVault(t)
	writeConfig(t, "[capture]\nocr = false\n\n[shot]\ncopy_text = true\n")
	withFakeScreenshot(t, fakePNG, "png", nil)
	engine := &fakeOCREngine{lines: []ocr.Line{{Text: "hello"}}}
	withFakeOCR(t, engine)
	copied := withFakeClipboardCopy(t)

	_, stderr, code := runCmd(t, "", "shot")
	if code != 0 {
		t.Fatalf("code = %d, stderr = %q", code, stderr)
	}
	if engine.calls != 1 {
		t.Errorf("OCR engine was called %d times, want 1 (shot.copy_text needs OCR even with capture.ocr = false)", engine.calls)
	}
	if *copied != "hello" {
		t.Errorf("copied = %q, want the recognized text", *copied)
	}
}

func TestShotNoOCRBeatsCopyTextFromConfig(t *testing.T) {
	newTestVault(t)
	writeConfig(t, "[shot]\ncopy_text = true\n")
	withFakeScreenshot(t, fakePNG, "png", nil)
	engine := &fakeOCREngine{lines: []ocr.Line{{Text: "should not run"}}}
	withFakeOCR(t, engine)
	copied := withFakeClipboardCopy(t)

	_, stderr, code := runCmd(t, "", "shot", "--no-ocr")
	if code != 0 {
		t.Fatalf("code = %d, stderr = %q", code, stderr)
	}
	if engine.calls != 0 {
		t.Errorf("OCR engine was called %d times, want 0 (--no-ocr beats shot.copy_text = true)", engine.calls)
	}
	if *copied != "" {
		t.Errorf("copied = %q, want nothing copied (--no-ocr beats shot.copy_text = true)", *copied)
	}
}

func TestShotCopyTextFlagForcesOCROverCaptureOCROff(t *testing.T) {
	newTestVault(t)
	writeConfig(t, "[capture]\nocr = false\n")
	withFakeScreenshot(t, fakePNG, "png", nil)
	engine := &fakeOCREngine{lines: []ocr.Line{{Text: "hello"}}}
	withFakeOCR(t, engine)
	copied := withFakeClipboardCopy(t)

	_, stderr, code := runCmd(t, "", "shot", "--copy-text")
	if code != 0 {
		t.Fatalf("code = %d, stderr = %q", code, stderr)
	}
	if engine.calls != 1 {
		t.Errorf("OCR engine was called %d times, want 1 (--copy-text forces OCR over capture.ocr = false)", engine.calls)
	}
	if *copied != "hello" {
		t.Errorf("copied = %q, want the recognized text", *copied)
	}
}

func TestShotNoOCRAndCopyTextCannotCombine(t *testing.T) {
	newTestVault(t)
	withNoScreenshotExpected(t)
	_, stderr, code := runCmd(t, "", "shot", "--no-ocr", "--copy-text")
	if code != 2 {
		t.Errorf("code = %d, want 2, stderr = %q", code, stderr)
	}
	if !strings.Contains(stderr, "--no-ocr and --copy-text cannot be combined") {
		t.Errorf("stderr = %q, want it to name the conflicting flags", stderr)
	}
}

func TestShotCopyTextDefaultFromConfig(t *testing.T) {
	newTestVault(t)
	writeConfig(t, "[shot]\ncopy_text = true\n")
	withFakeScreenshot(t, fakePNG, "png", nil)
	withFakeOCR(t, &fakeOCREngine{lines: []ocr.Line{{Text: "hello"}}})
	copied := withFakeClipboardCopy(t)

	_, stderr, code := runCmd(t, "", "shot")
	if code != 0 {
		t.Fatalf("code = %d, stderr = %q", code, stderr)
	}
	if *copied != "hello" {
		t.Errorf("copied = %q, want the recognized text (shot.copy_text = true)", *copied)
	}
}

func TestShotNoCopyTextFlagBeatsConfig(t *testing.T) {
	newTestVault(t)
	writeConfig(t, "[shot]\ncopy_text = true\n")
	withFakeScreenshot(t, fakePNG, "png", nil)
	withFakeOCR(t, &fakeOCREngine{lines: []ocr.Line{{Text: "hello"}}})
	copied := withFakeClipboardCopy(t)

	_, stderr, code := runCmd(t, "", "shot", "--no-copy-text")
	if code != 0 {
		t.Fatalf("code = %d, stderr = %q", code, stderr)
	}
	if *copied != "" {
		t.Errorf("copied = %q, want nothing copied (--no-copy-text beats shot.copy_text = true)", *copied)
	}
}

func TestShotCopyTextFlagsCannotCombine(t *testing.T) {
	newTestVault(t)
	withNoScreenshotExpected(t)
	_, stderr, code := runCmd(t, "", "shot", "--copy-text", "--no-copy-text")
	if code != 2 {
		t.Errorf("code = %d, want 2, stderr = %q", code, stderr)
	}
	if !strings.Contains(stderr, "--copy-text and --no-copy-text cannot be combined") {
		t.Errorf("stderr = %q, want it to name the conflicting flags", stderr)
	}
}

func TestShotOCRFailureIsOnlyAWarning(t *testing.T) {
	root := newTestVault(t)
	withFakeScreenshot(t, fakePNG, "png", nil)
	withFakeOCR(t, &fakeOCREngine{err: errors.New("tesseract not installed")})

	_, stderr, code := runCmd(t, "", "shot")
	if code != 0 {
		t.Fatalf("code = %d, want 0 even though OCR failed, stderr = %q", code, stderr)
	}
	if !strings.Contains(stderr, "warning") {
		t.Errorf("stderr = %q, want an OCR warning", stderr)
	}
	entries, _ := os.ReadDir(filepath.Join(root, "nn", "assets"))
	if len(entries) != 1 {
		t.Errorf("the note's image should still be saved despite the OCR failure: %v", entries)
	}
}

func TestShotRejectsPositionalWords(t *testing.T) {
	newTestVault(t)
	withNoScreenshotExpected(t)
	_, stderr, code := runCmd(t, "", "shot", "some", "words")
	if code != 2 {
		t.Errorf("code = %d, want 2, stderr = %q", code, stderr)
	}
}

func TestShotSimilarTagWarningGoesToTheCommandsStderr(t *testing.T) {
	root := newTestVault(t)
	writeNote(t, root, "nn/channels.md", "---\ntags: [golang]\ndate: 2026-01-01\naliases: [Channels]\n---\n\nbuffered and unbuffered\n")
	withFakeScreenshot(t, fakePNG, "png", nil)
	withFakeOCR(t, &fakeOCREngine{})

	_, stderr, code := runCmd(t, "", "shot", "-t", "go")
	if code != 0 {
		t.Fatalf("code = %d, stderr = %q", code, stderr)
	}
	if !strings.Contains(stderr, `nn: shot: tag "go" is similar to existing "golang"`) {
		t.Errorf("stderr = %q, want the similar-tag warning on the command's own stderr, named for the command that wrote it", stderr)
	}
}

func TestShotTitleFlag(t *testing.T) {
	root := newTestVault(t)
	withFakeScreenshot(t, fakePNG, "png", nil)
	withFakeOCR(t, &fakeOCREngine{})

	_, stderr, code := runCmd(t, "", "shot", "--title", "error dialog")
	if code != 0 {
		t.Fatalf("code = %d, stderr = %q", code, stderr)
	}
	entries, err := os.ReadDir(filepath.Join(root, "nn"))
	if err != nil || len(entries) != 2 { // the note plus the assets directory
		t.Fatalf("ReadDir: %v, %v", err, entries)
	}
	data, err := os.ReadFile(filepath.Join(root, "nn", "error-dialog.md"))
	if err != nil {
		t.Fatalf("expected a note named after --title: %v", err)
	}
	if !strings.Contains(string(data), "via: shot") {
		t.Errorf("note = %q, want via: shot", string(data))
	}
}
