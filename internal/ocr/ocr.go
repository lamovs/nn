// Package ocr recognizes text in images and caches the result next to the
// vault as a sidecar file.
package ocr

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"

	"github.com/lamovs/nn/internal/boundary"
	"github.com/lamovs/nn/internal/config"
)

var ErrNotInstalled = errors.New("not installed")

type LangsError struct {
	Engine    string
	Missing   []string
	Available []string
}

func (e *LangsError) Error() string {
	msg := fmt.Sprintf("%s: no language data for %s", e.Engine, strings.Join(e.Missing, ", "))
	if len(e.Available) > 0 {
		msg += " (available: " + strings.Join(e.Available, ", ") + ")"
	}
	return msg
}

// Box is normalized to 0..1, origin top-left.
type Box struct {
	X float64 `json:"x"`
	Y float64 `json:"y"`
	W float64 `json:"w"`
	H float64 `json:"h"`
}

type Line struct {
	Text       string  `json:"text"`
	Box        Box     `json:"box"`
	Confidence float64 `json:"confidence"`
}

type Result struct {
	Engine string   `json:"engine"`
	Langs  []string `json:"langs"`
	Width  int      `json:"width"`
	Height int      `json:"height"`
	SHA256 string   `json:"sha256"`
	Lines  []Line   `json:"lines"`
}

type Engine interface {
	Name() string
	Check(ctx context.Context) error
	Recognize(ctx context.Context, imagePath string, langs []string) (*Result, error)
}

var goos = runtime.GOOS

var getenv = os.Getenv

func Default(cfg config.Config) Engine {
	langs := DefaultLangs(cfg)
	if goos == "darwin" {
		return newVisionEngine(langs)
	}
	return newTesseractEngine(langs, cfg.OCR.TesseractPSM)
}

func DefaultLangs(cfg config.Config) []string {
	langs := splitLangs(cfg.OCR.Langs...)
	if len(langs) == 0 {
		langs = splitLangs(getenv("OMARCHY_OCR_LANGS"))
	}
	if len(langs) == 0 {
		langs = []string{"rus", "eng"}
	}
	if goos == "darwin" {
		return visionLangs(langs)
	}
	return tesseractLangs(langs)
}

// Ensure recognizes again if the sidecar is stale; a deleted or out-of-vault image fails instead of answering from the cache.
func Ensure(ctx context.Context, root, imageRel string, e Engine, langs []string) (*Result, error) {
	img, sum, err := imageToRead(root, imageRel)
	if err != nil {
		return nil, err
	}

	cached, loadErr := LoadSidecar(root, img.Rel)
	var pathErr *fs.PathError
	switch {
	case loadErr == nil && cached.SHA256 == sum && sameLangs(cached.Langs, langs):
		return cached, nil
	case loadErr != nil && errors.As(loadErr, &pathErr) && !errors.Is(loadErr, fs.ErrNotExist):
		return nil, loadErr
	}
	return recognizeAndSave(ctx, root, img.Rel, img.Abs, sum, e, langs)
}

// Refresh is Ensure without the freshness check, for "nn ocr --force".
func Refresh(ctx context.Context, root, imageRel string, e Engine, langs []string) (*Result, error) {
	img, sum, err := imageToRead(root, imageRel)
	if err != nil {
		return nil, err
	}
	return recognizeAndSave(ctx, root, img.Rel, img.Abs, sum, e, langs)
}

func imageToRead(root, imageRel string) (Image, string, error) {
	img, err := Locate(root, imageRel)
	if err != nil {
		return Image{}, "", err
	}
	sum, err := imageSHA256(img.Abs)
	if err != nil {
		return Image{}, "", err
	}
	return img, sum, nil
}

// Stats: Orphans is a deleted image's leftover sidecar, OutsideOrphans one leaked outside the vault.
type Stats struct {
	Processed      int
	Skipped        int
	Outside        int
	Orphans        int
	OutsideOrphans int
}

// Reindex fills in stale sidecars (or rebuilds all if force) and sweeps orphans; a failing image does not stop the run.
func Reindex(ctx context.Context, root string, images []string, e Engine, langs []string, force bool, progress func(done, total int)) (Stats, error) {
	stats, errs := reindexImages(ctx, root, images, e, langs, force, progress)
	if err := ctx.Err(); err != nil {
		return stats, err
	}
	orphans, outsideOrphans, err := sweepOrphanSidecars(root)
	stats.Orphans, stats.OutsideOrphans = orphans, outsideOrphans
	if err != nil {
		errs = append(errs, err)
	}
	return stats, errors.Join(errs...)
}

func reindexImages(ctx context.Context, root string, images []string, e Engine, langs []string, force bool, progress func(done, total int)) (Stats, []error) {
	var stats Stats
	total := len(images)
	if progress != nil {
		progress(0, total)
	}
	if total == 0 {
		return stats, nil
	}

	type outcome struct {
		rel  string
		skip skipReason
		err  error
	}
	jobs := make(chan string)
	outcomes := make(chan outcome)

	var wg sync.WaitGroup
	for range reindexWorkers(total) {
		wg.Go(func() {
			for rel := range jobs {
				skip, err := reindexOne(ctx, root, rel, e, langs, force)
				outcomes <- outcome{rel, skip, err}
			}
		})
	}
	go func() {
		defer close(jobs)
		for _, rel := range images {
			select {
			case jobs <- rel:
			case <-ctx.Done():
				return
			}
		}
	}()
	go func() {
		wg.Wait()
		close(outcomes)
	}()

	var errs []error
	done := 0
	for o := range outcomes {
		done++
		switch {
		case o.skip == skipGone:
			stats.Skipped++
		case o.skip == skipOutside:
			stats.Outside++
		case o.err == nil:
			stats.Processed++
		}
		if o.err != nil && ctx.Err() == nil {
			errs = append(errs, fmt.Errorf("%s: %w", o.rel, o.err))
		}
		if progress != nil {
			progress(done, total)
		}
	}
	return stats, errs
}

// isImageRel matches the same extensions as vault.IsImage.
func isImageRel(rel string) bool {
	switch strings.ToLower(filepath.Ext(rel)) {
	case ".png", ".jpg", ".jpeg", ".gif", ".webp":
		return true
	}
	return false
}

// sweepOrphanSidecars checks each image with Locate, not os.Stat, so a
// symlink out of the vault is caught; it removes only files named <image>.txt or .json.
func sweepOrphanSidecars(root string) (gone, outside int, err error) {
	_, dir := sidecarAnchor(root)
	goneImages := map[string]bool{}
	outsideImages := map[string]bool{}
	var errs []error

	walkErr := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return nil
			}
			errs = append(errs, err)
			return nil
		}
		if d.IsDir() || !d.Type().IsRegular() {
			return nil
		}
		ext := filepath.Ext(p)
		if ext != ".txt" && ext != ".json" {
			return nil
		}
		rel, relErr := filepath.Rel(dir, p)
		if relErr != nil || !boundary.RelIsInside(rel) {
			return nil
		}
		imageRel := filepath.ToSlash(strings.TrimSuffix(rel, ext))
		if !isImageRel(imageRel) {
			return nil
		}
		counted := goneImages
		img, locErr := Locate(root, imageRel)
		switch {
		case errors.Is(locErr, ErrOutsideVault):
			counted = outsideImages
		case locErr != nil:
			return nil
		default:
			if _, statErr := os.Stat(img.Abs); !errors.Is(statErr, fs.ErrNotExist) {
				return nil
			}
		}
		if err := os.Remove(p); err != nil && !errors.Is(err, fs.ErrNotExist) {
			errs = append(errs, err)
			return nil
		}
		counted[imageRel] = true
		return nil
	})
	if walkErr != nil {
		errs = append(errs, walkErr)
	}
	return len(goneImages), len(outsideImages), errors.Join(errs...)
}

type skipReason int

const (
	skipNone skipReason = iota
	skipGone
	skipOutside
)

func reindexOne(ctx context.Context, root, rel string, e Engine, langs []string, force bool) (skipReason, error) {
	if ctx.Err() != nil {
		return skipNone, ctx.Err()
	}
	img, err := Locate(root, rel)
	if err != nil {
		var viaLink *escapedViaLink
		if errors.As(err, &viaLink) {
			return skipOutside, nil
		}
		return skipNone, err
	}
	if _, err := os.Stat(img.Abs); errors.Is(err, fs.ErrNotExist) {
		return skipGone, nil
	}
	if force {
		_, err = Refresh(ctx, root, img.Rel, e, langs)
	} else {
		_, err = Ensure(ctx, root, img.Rel, e, langs)
	}
	return classifySkip(img.Abs, err)
}

// classifySkip treats a since-deleted image (re-checked with Lstat) or one
// leading out of the vault as a skip; any other error fails the run.
func classifySkip(abs string, err error) (skipReason, error) {
	var viaLink *escapedViaLink
	switch {
	case err == nil:
		return skipNone, nil
	case errors.As(err, &viaLink):
		return skipOutside, nil
	case !errors.Is(err, fs.ErrNotExist):
		return skipNone, err
	}
	if _, statErr := os.Lstat(abs); errors.Is(statErr, fs.ErrNotExist) {
		return skipGone, nil
	}
	return skipNone, err
}

// reindexWorkers keeps the pool small: both engines are themselves heavy
// (Vision uses the GPU/ANE, tesseract a full core per page).
func reindexWorkers(total int) int {
	return max(1, min(runtime.GOMAXPROCS(0), 4, total))
}

func recognizeAndSave(ctx context.Context, root, imageRel, abs, sum string, e Engine, langs []string) (*Result, error) {
	r, err := e.Recognize(ctx, abs, langs)
	if err != nil {
		return nil, err
	}
	if r.SHA256 == "" {
		r.SHA256 = sum
	}
	if err := SaveSidecar(root, imageRel, r); err != nil {
		return nil, err
	}
	return r, nil
}
