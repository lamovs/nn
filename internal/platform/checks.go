package platform

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/lamovs/nn/internal/config"
	"github.com/lamovs/nn/internal/ocr"
)

// checkTimeout bounds each external program a check runs.
const checkTimeout = 10 * time.Second

var (
	defaultEngine = ocr.Default
	defaultLangs  = ocr.DefaultLangs
)

func ocrCheck(ctx context.Context, cfg config.Config, d distro) Check {
	ctx, cancel := context.WithTimeout(ctx, checkTimeout)
	defer cancel()

	e := defaultEngine(cfg)
	langs := defaultLangs(cfg)
	c := Check{Name: "OCR (" + e.Name() + ")"}
	err := e.Check(ctx)
	if err == nil {
		c.OK = true
		c.Detail = "languages " + strings.Join(langs, ", ")
		return c
	}
	c.Detail = err.Error()

	var langsErr *ocr.LangsError
	switch {
	case goos == "darwin" && errors.Is(err, ocr.ErrNotInstalled):
		// The helper check carries the fix.
	case goos == "darwin" && errors.As(err, &langsErr):
		c.Fix = []string{`set ocr.langs in the config to Vision languages, e.g. ["ru-RU", "en-US"]`}
	case errors.Is(err, ocr.ErrNotInstalled):
		tools := []string{"tesseract"}
		for _, lang := range langs {
			tools = append(tools, "tesseract-lang:"+lang)
		}
		c.Fix = d.install(tools...)
	case errors.As(err, &langsErr):
		var tools []string
		for _, lang := range langsErr.Missing {
			tools = append(tools, "tesseract-lang:"+lang)
		}
		c.Fix = d.install(tools...)
		if d.family == familyNixOS {
			c.Fix = []string{fmt.Sprintf("use (tesseract.override { enableLanguages = [ %s ]; })", nixList(langs))}
		}
	}
	return c
}

func nixList(items []string) string {
	quoted := make([]string, len(items))
	for i, s := range items {
		quoted[i] = `"` + s + `"`
	}
	return strings.Join(quoted, " ")
}

// obsidianCheck looks for root among the vaults listed in configs (each an obsidian.json).
func obsidianCheck(root string, configs []string) Check {
	c := Check{Name: "Obsidian"}
	if root == "" {
		c.Detail = "vault root is not set"
		return c
	}
	openFix := fmt.Sprintf("open Obsidian, choose \"Open folder as vault\" and pick %s", root)

	var found []string
	for _, path := range configs {
		vaults, err := obsidianVaults(path)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			c.Detail = err.Error()
			return c
		}
		found = append(found, path)
		for _, v := range vaults {
			if samePath(v, root) {
				c.OK = true
				c.Detail = "vault registered"
				return c
			}
		}
	}
	if len(found) == 0 {
		c.Detail = "Obsidian not found (no obsidian.json)"
		c.Fix = []string{"install Obsidian from https://obsidian.md", openFix}
		return c
	}
	c.Detail = root + " is not an Obsidian vault yet"
	c.Fix = []string{openFix}
	return c
}

func obsidianVaults(path string) ([]string, error) {
	src, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var doc struct {
		Vaults map[string]struct {
			Path string `json:"path"`
		} `json:"vaults"`
	}
	if err := json.Unmarshal(src, &doc); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	var paths []string
	for _, v := range doc.Vaults {
		if v.Path != "" {
			paths = append(paths, v.Path)
		}
	}
	return paths, nil
}

func samePath(a, b string) bool {
	a, b = filepath.Clean(a), filepath.Clean(b)
	if a == b {
		return true
	}
	ra, errA := filepath.EvalSymlinks(a)
	rb, errB := filepath.EvalSymlinks(b)
	return errA == nil && errB == nil && ra == rb
}

func dirExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}
