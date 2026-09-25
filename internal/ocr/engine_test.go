package ocr

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/color"
	"image/png"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/lamovs/nn/internal/config"
)

func stubRun(t *testing.T, fn func(c command) (stdout, stderr []byte, err error)) *[]command {
	t.Helper()
	var calls []command
	prev := runCommand
	runCommand = func(ctx context.Context, c command) ([]byte, []byte, error) {
		calls = append(calls, c)
		return fn(c)
	}
	t.Cleanup(func() { runCommand = prev })
	return &calls
}

func stubLookPath(t *testing.T, found bool) {
	t.Helper()
	prev := lookPath
	lookPath = func(file string) (string, error) {
		if found {
			return "/usr/bin/" + file, nil
		}
		return "", &exec.Error{Name: file, Err: exec.ErrNotFound}
	}
	t.Cleanup(func() { lookPath = prev })
}

func stubPlatform(t *testing.T, os string, env map[string]string) {
	t.Helper()
	prevGOOS, prevEnv := goos, getenv
	goos = os
	getenv = func(k string) string { return env[k] }
	t.Cleanup(func() { goos, getenv = prevGOOS, prevEnv })
}

func writePNG(t *testing.T, path string, img image.Image) {
	t.Helper()
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
}

func filled(w, h int, c color.Color) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := range h {
		for x := range w {
			img.Set(x, y, c)
		}
	}
	return img
}

func TestDefaultLangs(t *testing.T) {
	tests := []struct {
		name string
		goos string
		cfg  []string
		env  string
		want []string
	}{
		{"darwin default", "darwin", nil, "", []string{"ru-RU", "en-US"}},
		{"linux default", "linux", nil, "", []string{"rus", "eng"}},
		{"linux omarchy env", "linux", nil, "eng+rus", []string{"eng", "rus"}},
		{"config wins over env", "linux", []string{"deu"}, "eng", []string{"deu"}},
		{"config vision codes on linux", "linux", []string{"ru-RU", "en-US"}, "", []string{"rus", "eng"}},
		{"config tesseract codes on darwin", "darwin", []string{"rus+eng"}, "", []string{"ru-RU", "en-US"}},
		{"short codes", "darwin", []string{"uk", "en"}, "", []string{"uk-UA", "en-US"}},
		{"unknown passes through", "linux", []string{"chi_sim", "foo"}, "", []string{"chi_sim", "foo"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stubPlatform(t, tt.goos, map[string]string{"OMARCHY_OCR_LANGS": tt.env})
			got := DefaultLangs(config.Config{OCR: config.OCR{Langs: tt.cfg}})
			if !slices.Equal(got, tt.want) {
				t.Errorf("DefaultLangs = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestDefaultPicksEngineByPlatform(t *testing.T) {
	stubPlatform(t, "linux", nil)
	if name := Default(config.Config{}).Name(); name != "tesseract" {
		t.Errorf("linux engine = %q", name)
	}
	stubPlatform(t, "darwin", nil)
	t.Setenv("NN_VISION_HELPER", "/nonexistent/nn-vision")
	if name := Default(config.Config{}).Name(); name != "vision" {
		t.Errorf("darwin engine = %q", name)
	}
}

func TestDefaultUsesConfiguredPSM(t *testing.T) {
	stubPlatform(t, "linux", nil)
	eng, ok := Default(config.Config{OCR: config.OCR{TesseractPSM: 6}}).(*tesseractEngine)
	if !ok {
		t.Fatalf("engine = %T, want *tesseractEngine", eng)
	}
	if eng.psm != 6 {
		t.Errorf("psm = %d, want 6", eng.psm)
	}
}

func TestParseTSV(t *testing.T) {
	src, err := os.ReadFile(filepath.Join("testdata", "hotkeys.tsv"))
	if err != nil {
		t.Fatal(err)
	}
	lines, w, h, err := parseTSV(src)
	if err != nil {
		t.Fatal(err)
	}
	if w != 1520 || h != 848 {
		t.Errorf("page = %dx%d", w, h)
	}
	want := []string{
		"\u0413\u043e\u0440\u044f\u0447\u0438\u0435 \u043a\u043b\u0430\u0432\u0438\u0448\u0438 / Hotkeys",
		"Cmd+Shift+4 \u0421\u043d\u0438\u043c\u043e\u043a \u043e\u0431\u043b\u0430\u0441\u0442\u0438 \u044d\u043a\u0440\u0430\u043d\u0430",
		"Cmd+Space Spotlight search",
		"Ctrl+Alt+T",
	}
	var got []string
	for _, l := range lines {
		got = append(got, l.Text)
	}
	if !slices.Equal(got, want) {
		t.Fatalf("lines = %q, want %q", got, want)
	}

	title := lines[0]
	if title.Box.X != 48.0/1520 || title.Box.Y != 30.0/848 || title.Box.W != 530.0/1520 || title.Box.H != 48.0/848 {
		t.Errorf("title box = %+v", title.Box)
	}
	if c := title.Confidence; c < 0.937 || c > 0.938 {
		t.Errorf("title confidence = %v, want mean of word confidences", c)
	}
	// The blank word with conf -1 is ignored, not averaged in.
	if c := lines[2].Confidence; c < 0.906 || c > 0.907 {
		t.Errorf("line 3 confidence = %v", c)
	}
	if c := lines[3].Confidence; c != 0 {
		t.Errorf("zero-confidence word gave %v", c)
	}
}

func TestParseTSVRejectsUnknownHeader(t *testing.T) {
	if _, _, _, err := parseTSV([]byte("not\ta\ttsv\n")); err == nil {
		t.Error("want an error for a foreign header")
	}
	lines, _, _, err := parseTSV(nil)
	if err != nil || len(lines) != 0 {
		t.Errorf("empty output: lines=%v err=%v", lines, err)
	}
}

func TestParseListLangs(t *testing.T) {
	out := "List of available languages in \"/usr/share/tessdata/\" (3):\neng\nosd\nrus\n"
	if got := parseListLangs([]byte(out)); !slices.Equal(got, []string{"eng", "osd", "rus"}) {
		t.Errorf("got %q", got)
	}
}

func TestTesseractCheck(t *testing.T) {
	e := newTesseractEngine([]string{"rus", "eng"}, 11)

	stubLookPath(t, false)
	if err := e.Check(context.Background()); !errors.Is(err, ErrNotInstalled) {
		t.Errorf("missing binary: err = %v", err)
	}

	stubLookPath(t, true)
	stubRun(t, func(c command) ([]byte, []byte, error) {
		return []byte("List of available languages in \"/usr/share/tessdata/\" (2):\neng\nosd\n"), nil, nil
	})
	var langsErr *LangsError
	if err := e.Check(context.Background()); !errors.As(err, &langsErr) || !slices.Equal(langsErr.Missing, []string{"rus"}) {
		t.Errorf("missing rus: err = %v", err)
	}

	stubRun(t, func(c command) ([]byte, []byte, error) {
		return nil, []byte("List of available languages (3):\neng\nosd\nrus\n"), nil
	})
	if err := e.Check(context.Background()); err != nil {
		t.Errorf("list on stderr: err = %v", err)
	}
}

func TestTesseractRecognizePreprocessesAndParses(t *testing.T) {
	dir := t.TempDir()
	imgPath := filepath.Join(dir, "shot.png")
	writePNG(t, imgPath, filled(760, 424, color.Black))
	tsv, err := os.ReadFile(filepath.Join("testdata", "hotkeys.tsv"))
	if err != nil {
		t.Fatal(err)
	}

	var input string
	var inputSize image.Point
	calls := stubRun(t, func(c command) ([]byte, []byte, error) {
		input = c.args[0]
		f, err := os.Open(input)
		if err != nil {
			t.Fatalf("tesseract input: %v", err)
		}
		defer f.Close()
		cfg, _, err := image.DecodeConfig(f)
		if err != nil {
			t.Fatal(err)
		}
		inputSize = image.Pt(cfg.Width, cfg.Height)
		return tsv, nil, nil
	})

	e := newTesseractEngine([]string{"rus", "eng"}, 11)
	r, err := e.Recognize(context.Background(), imgPath, nil)
	if err != nil {
		t.Fatal(err)
	}

	c := (*calls)[0]
	if c.name != "tesseract" {
		t.Errorf("ran %q", c.name)
	}
	wantArgs := []string{"stdout", "-l", "rus+eng", "--oem", "1", "--psm", strconv.Itoa(11), "--dpi", "300", "-c", "preserve_interword_spaces=1", "tsv"}
	if !slices.Equal(c.args[1:], wantArgs) {
		t.Errorf("args = %q", c.args)
	}
	if input == imgPath {
		t.Error("a small dark image went to tesseract unprocessed")
	}
	if inputSize != image.Pt(1520, 848) {
		t.Errorf("preprocessed size = %v, want doubled", inputSize)
	}
	if _, err := os.Stat(input); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("temp input left behind: %v", err)
	}
	if r.Engine != "tesseract" || r.Width != 760 || r.Height != 424 || len(r.Lines) != 4 {
		t.Errorf("result = %+v", r)
	}
	if !slices.Equal(r.Langs, []string{"rus", "eng"}) || len(r.SHA256) != 64 {
		t.Errorf("langs=%q sha=%q", r.Langs, r.SHA256)
	}

	// Explicit languages are converted to tesseract codes.
	if _, err := e.Recognize(context.Background(), imgPath, []string{"en-US"}); err != nil {
		t.Fatal(err)
	}
	if args := (*calls)[1].args; !slices.Contains(args, "eng") || slices.Contains(args, "rus+eng") {
		t.Errorf("explicit langs args = %q", args)
	}
}

func TestTesseractRecognizeReportsStderr(t *testing.T) {
	imgPath := filepath.Join(t.TempDir(), "x.png")
	writePNG(t, imgPath, filled(10, 10, color.White))
	stubRun(t, func(c command) ([]byte, []byte, error) {
		return nil, []byte("Error opening data file /usr/share/tessdata/rus.traineddata\nFailed loading language 'rus'\n"), errors.New("exit status 1")
	})
	_, err := newTesseractEngine([]string{"rus"}, 11).Recognize(context.Background(), imgPath, nil)
	if err == nil || !strings.Contains(err.Error(), "Failed loading language 'rus'") {
		t.Errorf("err = %v", err)
	}
}

func TestVisionRecognize(t *testing.T) {
	imgPath := filepath.Join(t.TempDir(), "-shot.png")
	writePNG(t, imgPath, filled(10, 10, color.White))
	calls := stubRun(t, func(c command) ([]byte, []byte, error) {
		return []byte(`{"engine":"vision","langs":["ru-RU","en-US"],"width":760,"height":424,` +
			`"lines":[{"text":"Cmd+Shift+4","box":{"x":0.03,"y":0.15,"w":0.2,"h":0.05},"confidence":1}]}`), nil, nil
	})

	e := &visionEngine{helper: "/opt/nn-vision", langs: []string{"ru-RU", "en-US"}}
	r, err := e.Recognize(context.Background(), imgPath, []string{"rus", "eng"})
	if err != nil {
		t.Fatal(err)
	}
	c := (*calls)[0]
	if c.name != "/opt/nn-vision" || !slices.Equal(c.args, []string{"ocr", imgPath, "--langs", "ru-RU,en-US"}) {
		t.Errorf("ran %q %q", c.name, c.args)
	}
	if r.Engine != "vision" || r.Width != 760 || len(r.Lines) != 1 || len(r.SHA256) != 64 {
		t.Errorf("result = %+v", r)
	}
	if b := r.Lines[0].Box; b.Y != 0.15 || b.W != 0.2 {
		t.Errorf("box = %+v", b)
	}
}

func synthLine(text string, x, y, w, h, conf float64) Line {
	return Line{Text: text, Box: Box{X: x, Y: y, W: w, H: h}, Confidence: conf}
}

func near(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

func TestMergeRows(t *testing.T) {
	in := []Line{
		synthLine("Hotkeys", 0.03, 0.05, 0.38, 0.05, 0.5),
		synthLine("Cmd+Shift+4", 0.03, 0.156, 0.15, 0.042, 1),
		synthLine("Cmd+Space", 0.03, 0.235, 0.12, 0.049, 0.8),
		synthLine("", 0.20, 0.238, 0.02, 0.040, 0),
		synthLine("Snimok", 0.39, 0.16, 0.27, 0.047, 0.6),
		synthLine("Spotlight search", 0.39, 0.241, 0.18, 0.047, 0.6),
	}
	got := mergeRows(in)

	want := []string{"Hotkeys", "Cmd+Shift+4 Snimok", "Cmd+Space Spotlight search"}
	if !slices.Equal(lineTexts(&Result{Lines: got}), want) {
		t.Fatalf("lines = %q, want %q", lineTexts(&Result{Lines: got}), want)
	}
	if b := got[1].Box; !near(b.X, 0.03) || !near(b.Y, 0.156) || !near(b.W, 0.63) || !near(b.H, 0.051) {
		t.Errorf("merged box = %+v, want the union of both parts", b)
	}
	if c := got[1].Confidence; !near(c, 0.8) {
		t.Errorf("merged confidence = %v, want the mean of 1 and 0.6", c)
	}
	if c := got[2].Confidence; !near(c, (0.8+0+0.6)/3) {
		t.Errorf("blank part confidence = %v", c)
	}
}

func TestMergeRowsKeepsSeparateRows(t *testing.T) {
	in := []Line{
		synthLine("upper", 0.0, 0.10, 0.2, 0.04, 1),
		synthLine("lower", 0.3, 0.13, 0.2, 0.04, 1),
	}
	got := mergeRows(in)
	if len(got) != 2 || got[0].Text != "upper" || got[1].Text != "lower" {
		t.Errorf("rows = %q", lineTexts(&Result{Lines: got}))
	}
	one := []Line{synthLine(" spaced ", 0, 0, 1, 1, 1)}
	if got := mergeRows(one); !slices.Equal(got, one) {
		t.Errorf("single line = %+v", got)
	}
}

func TestVisionRecognizeMergesRows(t *testing.T) {
	imgPath := filepath.Join(t.TempDir(), "table.png")
	writePNG(t, imgPath, filled(10, 10, color.White))
	stubRun(t, func(c command) ([]byte, []byte, error) {
		return []byte(`{"engine":"vision","langs":["en-US"],"width":760,"height":424,"lines":[` +
			`{"text":"Cmd+Space","box":{"x":0.03,"y":0.235,"w":0.12,"h":0.049},"confidence":1},` +
			`{"text":"Spotlight search","box":{"x":0.39,"y":0.241,"w":0.18,"h":0.047},"confidence":1}]}`), nil, nil
	})

	e := &visionEngine{helper: "/opt/nn-vision", langs: []string{"en-US"}}
	r, err := e.Recognize(context.Background(), imgPath, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Lines) != 1 || r.Lines[0].Text != "Cmd+Space Spotlight search" {
		t.Errorf("lines = %q", lineTexts(r))
	}
}

func TestTesseractRecognizeMergesRows(t *testing.T) {
	imgPath := filepath.Join(t.TempDir(), "table.png")
	writePNG(t, imgPath, filled(10, 10, color.White))
	tsv := strings.Join([]string{
		"level\tpage_num\tblock_num\tpar_num\tline_num\tword_num\tleft\ttop\twidth\theight\tconf\ttext",
		"1\t1\t0\t0\t0\t0\t0\t0\t1000\t1000\t-1\t",
		"5\t1\t2\t1\t1\t1\t400\t205\t300\t40\t90\tSpotlight",
		"5\t1\t1\t1\t1\t1\t30\t200\t150\t40\t90\tCmd+Space",
		"5\t1\t3\t1\t1\t1\t30\t400\t150\t40\t90\tCmd+Shift+4",
	}, "\n") + "\n"
	stubRun(t, func(c command) ([]byte, []byte, error) { return []byte(tsv), nil, nil })

	r, err := newTesseractEngine([]string{"eng"}, 11).Recognize(context.Background(), imgPath, nil)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"Cmd+Space Spotlight", "Cmd+Shift+4"}
	if !slices.Equal(lineTexts(r), want) {
		t.Errorf("lines = %q, want %q", lineTexts(r), want)
	}
}

func TestVisionHelperMissing(t *testing.T) {
	e := &visionEngine{helperErr: errors.Join(ErrNotInstalled, errors.New("nn-vision helper not found"))}
	if err := e.Check(context.Background()); !errors.Is(err, ErrNotInstalled) {
		t.Errorf("Check err = %v", err)
	}
	if _, err := e.Recognize(context.Background(), "x.png", nil); !errors.Is(err, ErrNotInstalled) {
		t.Errorf("Recognize err = %v", err)
	}
}

func TestVisionCheckLanguages(t *testing.T) {
	stubRun(t, func(c command) ([]byte, []byte, error) {
		if !slices.Equal(c.args, []string{"langs"}) {
			t.Errorf("args = %q", c.args)
		}
		return []byte("en-US\nfr-FR\nru-RU\n"), nil, nil
	})
	if err := (&visionEngine{helper: "h", langs: []string{"ru-RU", "en-US"}}).Check(context.Background()); err != nil {
		t.Errorf("err = %v", err)
	}
	var langsErr *LangsError
	err := (&visionEngine{helper: "h", langs: []string{"xx-XX"}}).Check(context.Background())
	if !errors.As(err, &langsErr) || langsErr.Engine != "vision" {
		t.Errorf("err = %v", err)
	}
}

func TestCommandErrorPrefersContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := commandError(ctx, "tesseract", errors.New("signal: killed"), nil); !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v", err)
	}
	err := commandError(context.Background(), "tesseract", &exec.Error{Name: "tesseract", Err: exec.ErrNotFound}, nil)
	if !errors.Is(err, ErrNotInstalled) {
		t.Errorf("err = %v", err)
	}
}
