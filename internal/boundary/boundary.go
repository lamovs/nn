// Package boundary checks whether a resolved path stays within an anchor
// directory; callers must use the resolved path, not the original.
package boundary

import (
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"strings"
)

// Within reports whether target is dir itself or something under it (paths
// taken as written; resolving symlinks is the caller's job).
func Within(dir, target string) bool {
	rel, err := filepath.Rel(dir, target)
	return err == nil && RelIsInside(rel)
}

// RelIsInside reports whether rel stays at or below the directory it is
// relative to.
func RelIsInside(rel string) bool {
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// Dir is dir as it resolves on disk, or as written when it does not resolve.
func Dir(dir string) string {
	if real, err := filepath.EvalSymlinks(dir); err == nil {
		return real
	}
	return dir
}

// Resolve follows raw's symlinks and reports whether the result is under
// anchor (already resolved). On error inside is false, which does not mean
// "outside" - the path could not be checked.
func Resolve(anchor, raw string) (resolved string, inside bool, err error) {
	resolved, err = Follow(raw)
	if err != nil {
		return "", false, err
	}
	return resolved, Within(anchor, resolved), nil
}

// Follow is where raw leads once its symlinks are followed, climbing to the
// deepest existing ancestor for a path not yet on disk. Only fs.ErrNotExist
// is climbed past; any other error is returned instead of a guess.
func Follow(raw string) (string, error) {
	resolved, err := filepath.EvalSymlinks(raw)
	if err == nil {
		return resolved, nil
	}
	dir, tail := filepath.Clean(raw), ""
	for {
		if !errors.Is(err, fs.ErrNotExist) {
			return "", fmt.Errorf("cannot tell where %s leads: %w", raw, err)
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return raw, nil
		}
		tail = filepath.Join(filepath.Base(dir), tail)
		if resolved, err = filepath.EvalSymlinks(parent); err == nil {
			return filepath.Join(resolved, tail), nil
		}
		dir = parent
	}
}
