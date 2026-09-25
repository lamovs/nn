package ocr

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/lamovs/nn/internal/boundary"
)

const sidecarDir = ".nn/ocr"

// ErrOutsideVault is a path, image or sidecar, that leaves the vault -
// directly or only once its symlinks are resolved.
var ErrOutsideVault = errors.New("outside the vault")

// Image is checked against the vault: Abs is Rel with its symlinks
// resolved, so what was checked is what gets read.
type Image struct {
	Rel string
	Abs string
}

// Locate refuses any ref that leaves the vault, directly or via a resolved
// symlink; a missing file is not refused, only one nn cannot resolve.
func Locate(root, ref string) (Image, error) {
	rel, err := relInVault(root, ref)
	if err != nil {
		return Image{}, err
	}
	abs, err := insideAbs(root, rel)
	if err != nil {
		return Image{}, err
	}
	return Image{Rel: rel, Abs: abs}, nil
}

func relInVault(root, ref string) (string, error) {
	ref = strings.TrimSpace(ref)
	slash := filepath.ToSlash(ref)

	var rel string
	if filepath.IsAbs(ref) || strings.HasPrefix(slash, "/") {
		var err error
		if rel, err = absRel(root, ref); err != nil {
			return "", err
		}
	} else {
		rel = path.Clean(slash)
		if rel == ".." || strings.HasPrefix(rel, "../") {
			return "", fmt.Errorf("%s is %w", ref, ErrOutsideVault)
		}
	}
	if rel == "." || rel == "" {
		return "", fmt.Errorf("%q is not an image path", ref)
	}
	return rel, nil
}

// absRel maps an absolute ref onto the vault: as written first, then with
// root and ref resolved, so the other spelling of a symlinked root
// (/tmp vs /private/tmp) still matches. Where rel itself leads is
// insideAbs's check, not this one.
func absRel(root, ref string) (string, error) {
	if rel, ok := relFrom(root, ref); ok {
		return rel, nil
	}
	realRoot, err := filepath.EvalSymlinks(root)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return "", fmt.Errorf("%s is %w", ref, ErrOutsideVault)
	case err != nil:
		return "", cannotFollow(root, err)
	}
	realRef, err := boundary.Follow(ref)
	if err != nil {
		return "", err
	}
	if rel, ok := relFrom(realRoot, realRef); ok {
		return rel, nil
	}
	return "", fmt.Errorf("%s is %w", ref, ErrOutsideVault)
}

func cannotFollow(p string, err error) error {
	return fmt.Errorf("cannot tell where %s leads: %w", p, err)
}

// insideAbs checks the resolved path, not the written one, and returns it
// for the caller to open - so a symlink cannot point the read elsewhere.
func insideAbs(root, rel string) (string, error) {
	resolved, ok, err := boundary.Resolve(boundary.Dir(root), filepath.Join(root, filepath.FromSlash(rel)))
	if err != nil {
		return "", err
	}
	if !ok {
		return "", &escapedViaLink{Rel: rel, Target: resolved}
	}
	return resolved, nil
}

// escapedViaLink is ErrOutsideVault for a path that only leaves the vault
// once resolved; Reindex skips it instead of failing the run.
type escapedViaLink struct {
	Rel    string
	Target string
}

func (e *escapedViaLink) Error() string {
	return fmt.Sprintf("%s resolves to %s, which is %s", e.Rel, e.Target, ErrOutsideVault)
}

func (e *escapedViaLink) Unwrap() error { return ErrOutsideVault }

// relFrom takes both sides as written; resolving symlinks is the caller's job.
func relFrom(dir, target string) (string, bool) {
	rel, err := filepath.Rel(dir, target)
	if err != nil || !boundary.RelIsInside(rel) {
		return "", false
	}
	return filepath.ToSlash(rel), true
}

// sidecarAnchor is <root>/.nn/ocr, resolved. Paths anchor to the resolved
// directory, not the resolved root, so a symlink below it cannot carry a write past it.
func sidecarAnchor(root string) (dir, real string) {
	dir = filepath.Join(root, filepath.FromSlash(sidecarDir))
	return dir, boundary.Dir(dir)
}

// sidecarDirOf refuses any resolve failure other than fs.ErrNotExist, so an
// EACCES on a link component is never mistaken for the directory being absent.
func sidecarDirOf(root, rel string) (dir, real string, err error) {
	anchorDir, anchor := sidecarAnchor(root)
	dir = filepath.Dir(filepath.Join(anchorDir, filepath.FromSlash(rel)))
	real, err = filepath.EvalSymlinks(dir)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return dir, dir, nil
	case err != nil:
		return "", "", fmt.Errorf("the sidecar directory of %s: %w", rel, cannotFollow(dir, err))
	case !boundary.Within(anchor, real):
		return "", "", fmt.Errorf("the sidecar directory of %s resolves to %s, which is %w", rel, real, ErrOutsideVault)
	}
	return dir, real, nil
}

// sidecarBaseForWrite checks where the directory resolves before creating
// it and again after, since os.MkdirAll follows a symlink planted between the two checks.
func sidecarBaseForWrite(root, rel string) (string, error) {
	anchorDir, _ := sidecarAnchor(root)
	dir := filepath.Dir(filepath.Join(anchorDir, filepath.FromSlash(rel)))
	anchor, err := boundary.Follow(anchorDir)
	if err != nil {
		return "", fmt.Errorf("the sidecar directory of %s: %w", rel, err)
	}
	resolved, inside, err := boundary.Resolve(anchor, dir)
	if err != nil {
		return "", fmt.Errorf("the sidecar directory of %s: %w", rel, err)
	}
	if !inside {
		return "", fmt.Errorf("the sidecar directory of %s resolves to %s, which is %w", rel, resolved, ErrOutsideVault)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	_, real, err := sidecarDirOf(root, rel)
	if err != nil {
		return "", err
	}
	return filepath.Join(real, path.Base(rel)), nil
}
