package ocr

import (
	"context"
	"image"
	"image/color"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/lamovs/nn/internal/config"
	"github.com/lamovs/nn/internal/install"
)

// hotkeyTable is text in testdata/hotkeys.png; Mac modifier glyphs are left out.
var hotkeyTable = []string{
	"Hotkeys",
	"Cmd+Shift+4",
	"Cmd+Space",
	"Ctrl+Alt+T",
	"Command-Option-Esc",
	"Shift+Enter",
	"Ctrl+Shift+F12",
	"\u0421\u043d\u0438\u043c\u043e\u043a \u043e\u0431\u043b\u0430\u0441\u0442\u0438 \u044d\u043a\u0440\u0430\u043d\u0430",
	"Spotlight search",
	"Open terminal",
	"\u0417\u0430\u043f\u0438\u0441\u044c \u044d\u043a\u0440\u0430\u043d\u0430",
	"\u0417\u0430\u0431\u043b\u043e\u043a\u0438\u0440\u043e\u0432\u0430\u0442\u044c \u044d\u043a\u0440\u0430\u043d",
	"Force quit applications",
	"\u041f\u0435\u0440\u0435\u043a\u043b\u044e\u0447\u0438\u0442\u044c \u043f\u0440\u0438\u043b\u043e\u0436\u0435\u043d\u0438\u0435",
	"\u041d\u043e\u0432\u0430\u044f \u0441\u0442\u0440\u043e\u043a\u0430 \u0432 \u0447\u0430\u0442\u0435",
	"Move one word left or right",
	"\u041f\u043e\u043a\u0430\u0437\u0430\u0442\u044c \u0440\u0430\u0431\u043e\u0447\u0438\u0439 \u0441\u0442\u043e\u043b",
}

// hotkeyRows pairs a chord with its action, expected as one merged line.
var hotkeyRows = [][2]string{
	{"Cmd+Shift+4", "Снимок области экрана"},
	{"Cmd+Space", "Spotlight search"},
	{"Ctrl+Alt+T", "Open terminal"},
	{"Command-Option-Esc", "Force quit applications"},
	{"Shift+Enter", "Новая строка в чате"},
	{"Ctrl+Shift+F12", "Показать рабочий стол"},
}

// tableRecall is the share of hotkeyTable entries found in some recognized line.
func tableRecall(r *Result) float64 {
	found := 0
	for _, want := range hotkeyTable {
		for _, l := range r.Lines {
			if strings.Contains(l.Text, want) {
				found++
				break
			}
		}
	}
	return float64(found) / float64(len(hotkeyTable))
}

func TestVisionHelperOnHotkeyTable(t *testing.T) {
	if runtime.GOOS != "darwin" || testing.Short() {
		t.Skip("needs macOS and the nn-vision helper")
	}
	if _, err := install.Helper(); err != nil {
		t.Skipf("nn-vision helper not built: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	e := newVisionEngine([]string{"ru-RU", "en-US"})
	if err := e.Check(ctx); err != nil {
		t.Fatal(err)
	}
	path, err := filepath.Abs(filepath.Join("testdata", "hotkeys.png"))
	if err != nil {
		t.Fatal(err)
	}
	r, err := e.Recognize(ctx, path, nil)
	if err != nil {
		t.Fatal(err)
	}
	if r.Width != 760 || r.Height != 424 {
		t.Errorf("size = %dx%d", r.Width, r.Height)
	}
	if recall := tableRecall(r); recall < 0.9 {
		t.Errorf("recall = %.2f, lines = %q", recall, lineTexts(r))
	}
	checkRowsMerged(t, r)
	for _, l := range r.Lines {
		b := l.Box
		if b.X < 0 || b.Y < 0 || b.X+b.W > 1.001 || b.Y+b.H > 1.001 {
			t.Errorf("box out of range for %q: %+v", l.Text, b)
		}
		// Boxes use a top-left origin: the title is drawn at the top.
		if strings.Contains(l.Text, "Hotkeys") && b.Y > 0.2 {
			t.Errorf("title box y = %v, want near the top", b.Y)
		}
	}
}

// TestTesseractPSMOnHotkeyTable fails if the default psm reads clearly worse than the best of 3, 6 and 11.
func TestTesseractPSMOnHotkeyTable(t *testing.T) {
	if _, err := exec.LookPath("tesseract"); err != nil || testing.Short() {
		t.Skip("tesseract is not installed")
	}
	langs := []string{"rus", "eng"}
	if err := newTesseractEngine(langs, 11).Check(context.Background()); err != nil {
		t.Skipf("tesseract is not usable: %v", err)
	}

	light, err := filepath.Abs(filepath.Join("testdata", "hotkeys.png"))
	if err != nil {
		t.Fatal(err)
	}
	dark := filepath.Join(t.TempDir(), "hotkeys-dark.png")
	writePNG(t, dark, invertImage(t, light))

	defaultPSM := config.Default().OCR.TesseractPSM
	scores := map[int]float64{}
	for _, psm := range []int{3, 6, 11} {
		e := &tesseractEngine{langs: langs, psm: psm}
		for _, path := range []string{light, dark} {
			ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
			r, err := e.Recognize(ctx, path, nil)
			cancel()
			if err != nil {
				t.Fatalf("psm %d: %v", psm, err)
			}
			recall := tableRecall(r)
			scores[psm] += recall / 2
			t.Logf("psm %d %s: recall %.2f, lines %q", psm, filepath.Base(path), recall, lineTexts(r))
			if psm == defaultPSM {
				checkRowsMerged(t, r)
			}
		}
	}
	best := 0.0
	for _, s := range scores {
		best = max(best, s)
	}
	if scores[defaultPSM] < best-0.1 {
		t.Errorf("default psm %d scores %.2f, best is %.2f: %v", defaultPSM, scores[defaultPSM], best, scores)
	}
}

// checkRowsMerged asserts a chord and its action end up in one recognized line.
func checkRowsMerged(t *testing.T, r *Result) {
	t.Helper()
	for _, row := range hotkeyRows {
		chord, action := row[0], row[1]
		if !hasLineWith(r, chord) || !hasLineWith(r, action) {
			continue // a cell was misread; recall covers that
		}
		if !hasLineWith(r, chord+" "+action) {
			t.Errorf("%q and %q are in different lines: %q", chord, action, lineTexts(r))
		}
	}
}

func invertImage(t *testing.T, path string) image.Image {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	src, _, err := image.Decode(f)
	if err != nil {
		t.Fatal(err)
	}
	b := src.Bounds()
	dst := image.NewRGBA(b)
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			r, g, bl, _ := src.At(x, y).RGBA()
			dst.Set(x, y, color.RGBA{255 - uint8(r>>8), 255 - uint8(g>>8), 255 - uint8(bl>>8), 255})
		}
	}
	return dst
}

func hasLineWith(r *Result, s string) bool {
	for _, l := range r.Lines {
		if strings.Contains(l.Text, s) {
			return true
		}
	}
	return false
}

func lineTexts(r *Result) []string {
	var out []string
	for _, l := range r.Lines {
		out = append(out, l.Text)
	}
	return out
}
