package vault

import (
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/lamovs/nn/internal/boundary"
)

// ErrOutsideVault reports a write landing outside the vault: a symlinked
// directory or a ".." path. A symlinked note file is followed on purpose;
// a symlinked directory is not, since it carries every note under it out.
var ErrOutsideVault = errors.New("outside the vault")

// inside: the resolved root is the only anchor. A symlinked inbox or assets
// is refused rather than trusted: WalkDir does not follow it, so ls and s
// could never find what nn wrote there.
func (v *Vault) inside(abs string) bool {
	return boundary.Within(v.anchor(), abs)
}

// anchor is the vault root as it resolves on disk, shared so checks cannot
// drift apart.
func (v *Vault) anchor() string {
	return boundary.Dir(v.Root)
}

// RealDir reports where rel leads once resolved, without creating it; a
// directory not yet on disk is judged by its deepest existing ancestor.
func (v *Vault) RealDir(rel string) (real string, inside bool, err error) {
	return boundary.Resolve(v.anchor(), v.Abs(cleanRel(rel)))
}

func (v *Vault) RealPath(abs string) (real string, inside bool, err error) {
	return boundary.Resolve(v.anchor(), filepath.Clean(abs))
}

// EnsureDir creates the directory for rel and returns it resolved. It is
// checked before creation and after, so a symlink planted mid-call is
// still caught.
func (v *Vault) EnsureDir(rel string) (string, error) {
	clean := cleanRel(rel)
	if clean == ".." || strings.HasPrefix(clean, "../") {
		return "", fmt.Errorf("%s is %w", rel, ErrOutsideVault)
	}
	dir := v.Abs(clean)
	real, inside, err := boundary.Resolve(v.anchor(), dir)
	if err != nil {
		return "", err
	}
	if !inside {
		return "", fmt.Errorf("%s resolves to %s, which is %w", clean, real, ErrOutsideVault)
	}
	if err := os.MkdirAll(dir, dirMode); err != nil {
		return "", err
	}
	real, err = filepath.EvalSymlinks(dir)
	if err != nil {
		return "", err
	}
	if !v.inside(real) {
		return "", fmt.Errorf("%s resolves to %s, which is %w", clean, real, ErrOutsideVault)
	}
	return real, nil
}

// WriteNew writes rel, which must not exist: the directory is checked as
// EnsureDir does, and the name is claimed by the write itself, not a prior
// stat, which would follow a symlink.
func (v *Vault) WriteNew(rel string, data []byte) error {
	clean := cleanRel(rel)
	base := path.Base(clean)
	if base == "." || base == ".." {
		return fmt.Errorf("%q is not a file path", rel)
	}
	dir, err := v.EnsureDir(path.Dir(clean))
	if err != nil {
		return err
	}
	return writeAtomic(filepath.Join(dir, base), data, createMode(), true)
}

// noteForWrite resolves the note Append rewrites: the directory must
// resolve inside the vault; a symlinked note file is followed on purpose.
func (v *Vault) noteForWrite(rel string) (string, error) {
	clean := cleanRel(rel)
	if clean == ".." || strings.HasPrefix(clean, "../") {
		return "", fmt.Errorf("%s is %w", rel, ErrOutsideVault)
	}
	dir, err := filepath.EvalSymlinks(filepath.Dir(v.Abs(clean)))
	if err != nil {
		return "", err
	}
	if !v.inside(dir) {
		return "", fmt.Errorf("the directory of %s resolves to %s, which is %w", clean, dir, ErrOutsideVault)
	}
	return filepath.EvalSymlinks(filepath.Join(dir, path.Base(clean)))
}
