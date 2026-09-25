package ocr

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/lamovs/nn/internal/install"
)

// visionEngine runs "nn-vision ocr IMAGE --langs ru-RU,en-US", which prints a Result as JSON.
type visionEngine struct {
	helper    string
	helperErr error
	langs     []string
}

func newVisionEngine(langs []string) *visionEngine {
	helper, err := install.Helper()
	if err != nil {
		err = fmt.Errorf("%w: %w", ErrNotInstalled, err)
	}
	return &visionEngine{helper: helper, helperErr: err, langs: langs}
}

func (e *visionEngine) Name() string { return "vision" }

func (e *visionEngine) Check(ctx context.Context) error {
	if e.helperErr != nil {
		return e.helperErr
	}
	stdout, stderr, err := runCommand(ctx, command{name: e.helper, args: []string{"langs"}})
	if err != nil {
		return commandError(ctx, "nn-vision", err, stderr)
	}
	available := strings.Fields(string(stdout))
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

func (e *visionEngine) Recognize(ctx context.Context, imagePath string, langs []string) (*Result, error) {
	if e.helperErr != nil {
		return nil, e.helperErr
	}
	langs = visionLangs(langs)
	if len(langs) == 0 {
		langs = e.langs
	}
	sum, err := imageSHA256(imagePath)
	if err != nil {
		return nil, err
	}

	args := []string{"ocr", argPath(imagePath)}
	if len(langs) > 0 {
		args = append(args, "--langs", strings.Join(langs, ","))
	}
	stdout, stderr, err := runCommand(ctx, command{name: e.helper, args: args})
	if err != nil {
		return nil, commandError(ctx, "nn-vision", err, stderr)
	}

	var r Result
	if err := json.Unmarshal(stdout, &r); err != nil {
		return nil, fmt.Errorf("nn-vision: unreadable output: %w", err)
	}
	r.Engine = e.Name()
	if len(r.Langs) == 0 {
		r.Langs = langs
	}
	r.Lines = mergeRows(r.Lines)
	r.SHA256 = sum
	return &r, nil
}

func containsFold(list []string, s string) bool {
	for _, have := range list {
		if strings.EqualFold(have, s) {
			return true
		}
	}
	return false
}
