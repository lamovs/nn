package ocr

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// tesseractEngine: psm 11 (sparse text) reads hotkey chords far better than
// the uniform-block mode 6.
type tesseractEngine struct {
	langs []string
	psm   int
}

func newTesseractEngine(langs []string, psm int) *tesseractEngine {
	return &tesseractEngine{langs: langs, psm: psm}
}

func (e *tesseractEngine) Name() string { return "tesseract" }

func (e *tesseractEngine) Check(ctx context.Context) error {
	if _, err := lookPath("tesseract"); err != nil {
		return fmt.Errorf("tesseract: %w", ErrNotInstalled)
	}
	stdout, stderr, err := runCommand(ctx, command{name: "tesseract", args: []string{"--list-langs"}})
	if err != nil {
		return commandError(ctx, "tesseract", err, stderr)
	}
	// Older releases print the list to stderr.
	available := parseListLangs(append(stdout, stderr...))
	var missing []string
	for _, lang := range e.langs {
		if !containsFold(available, lang) {
			missing = append(missing, lang)
		}
	}
	if len(missing) > 0 {
		return &LangsError{Engine: e.Name(), Missing: missing, Available: available}
	}
	return nil
}

func (e *tesseractEngine) Recognize(ctx context.Context, imagePath string, langs []string) (*Result, error) {
	data, err := os.ReadFile(imagePath)
	if err != nil {
		return nil, err
	}
	langs = tesseractLangs(langs)
	if len(langs) == 0 {
		langs = e.langs
	}

	input := argPath(imagePath)
	var width, height int
	// webp, undecodable by the standard library, goes to tesseract as is.
	if prep, err := preprocess(data); err == nil {
		width, height = prep.width, prep.height
		if prep.png != nil {
			dir, err := os.MkdirTemp("", "nn-ocr-*")
			if err != nil {
				return nil, err
			}
			defer os.RemoveAll(dir)
			input = filepath.Join(dir, "input.png")
			if err := os.WriteFile(input, prep.png, 0o600); err != nil {
				return nil, err
			}
		}
	}

	var env []string
	// Avoids OpenMP threads fighting each other under Reindex's worker pool.
	if _, ok := os.LookupEnv("OMP_THREAD_LIMIT"); !ok {
		env = append(env, "OMP_THREAD_LIMIT=1")
	}
	stdout, stderr, err := runCommand(ctx, command{
		name: "tesseract",
		args: tesseractArgs(input, langs, e.psm),
		env:  env,
	})
	if err != nil {
		return nil, commandError(ctx, "tesseract", err, stderr)
	}

	lines, pageW, pageH, err := parseTSV(stdout)
	if err != nil {
		return nil, fmt.Errorf("tesseract: %w", err)
	}
	if width == 0 || height == 0 {
		width, height = pageW, pageH
	}
	return &Result{
		Engine: e.Name(),
		Langs:  langs,
		Width:  width,
		Height: height,
		SHA256: sha256Hex(data),
		Lines:  mergeRows(lines),
	}, nil
}

// tesseractArgs follows omarchy-capture-text's flags.
func tesseractArgs(input string, langs []string, psm int) []string {
	args := []string{input, "stdout"}
	if len(langs) > 0 {
		args = append(args, "-l", strings.Join(langs, "+"))
	}
	return append(args,
		"--oem", "1",
		"--psm", strconv.Itoa(psm),
		"--dpi", "300",
		"-c", "preserve_interword_spaces=1",
		"tsv",
	)
}

func parseListLangs(out []byte) []string {
	var langs []string
	sc := bufio.NewScanner(bytes.NewReader(out))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.ContainsAny(line, " \t:") {
			continue
		}
		langs = appendUnique(langs, line)
	}
	return langs
}

var tsvColumns = []string{"level", "page_num", "block_num", "par_num", "line_num", "left", "top", "width", "height", "conf", "text"}

// parseTSV boxes are normalized to the page size from the level 1 row;
// confidence is the mean word confidence scaled to 0..1.
func parseTSV(out []byte) (lines []Line, pageW, pageH int, err error) {
	type lineKey struct{ page, block, par, line int }
	type acc struct {
		words                    []string
		left, top, right, bottom int
		confSum                  float64
		confN                    int
	}

	sc := bufio.NewScanner(bytes.NewReader(out))
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	if !sc.Scan() {
		return nil, 0, 0, sc.Err()
	}
	col := map[string]int{}
	for i, name := range strings.Split(strings.TrimRight(sc.Text(), "\r"), "\t") {
		col[name] = i
	}
	for _, name := range tsvColumns {
		if _, ok := col[name]; !ok {
			return nil, 0, 0, fmt.Errorf("unexpected TSV header %q", sc.Text())
		}
	}
	textCol := col["text"]

	var order []lineKey
	groups := map[lineKey]*acc{}
	maxRight, maxBottom := 0, 0
	for sc.Scan() {
		fields := strings.SplitN(strings.TrimRight(sc.Text(), "\r"), "\t", textCol+1)
		field := func(name string) string {
			if i := col[name]; i < len(fields) {
				return strings.TrimSpace(fields[i])
			}
			return ""
		}
		num := func(name string) int {
			n, _ := strconv.Atoi(field(name))
			return n
		}
		switch num("level") {
		case 1:
			pageW, pageH = num("width"), num("height")
			continue
		case 5:
		default:
			continue
		}
		text := field("text")
		if text == "" {
			continue
		}

		left, top := num("left"), num("top")
		right, bottom := left+num("width"), top+num("height")
		maxRight, maxBottom = max(maxRight, right), max(maxBottom, bottom)
		key := lineKey{num("page_num"), num("block_num"), num("par_num"), num("line_num")}
		g := groups[key]
		if g == nil {
			g = &acc{left: left, top: top, right: right, bottom: bottom}
			groups[key] = g
			order = append(order, key)
		}
		g.words = append(g.words, text)
		g.left, g.top = min(g.left, left), min(g.top, top)
		g.right, g.bottom = max(g.right, right), max(g.bottom, bottom)
		if conf, err := strconv.ParseFloat(field("conf"), 64); err == nil && conf >= 0 {
			g.confSum += conf
			g.confN++
		}
	}
	if err := sc.Err(); err != nil {
		return nil, 0, 0, err
	}
	if pageW <= 0 || pageH <= 0 {
		pageW, pageH = maxRight, maxBottom
	}

	lines = make([]Line, 0, len(order))
	for _, key := range order {
		g := groups[key]
		var conf float64
		if g.confN > 0 {
			conf = clamp01(g.confSum / float64(g.confN) / 100)
		}
		lines = append(lines, Line{
			Text: strings.Join(g.words, " "),
			Box: Box{
				X: norm(g.left, pageW),
				Y: norm(g.top, pageH),
				W: norm(g.right-g.left, pageW),
				H: norm(g.bottom-g.top, pageH),
			},
			Confidence: conf,
		})
	}
	return lines, pageW, pageH, nil
}

func norm(v, total int) float64 {
	if total <= 0 {
		return 0
	}
	return clamp01(float64(v) / float64(total))
}

func clamp01(v float64) float64 {
	return min(max(v, 0), 1)
}
