package ocr

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
	"syscall"
)

func sidecarPaths(root, imageRel string) (txt, js string, err error) {
	rel, err := relInVault(root, imageRel)
	if err != nil {
		return "", "", err
	}
	dir, _, err := sidecarDirOf(root, rel)
	if err != nil {
		return "", "", err
	}
	base := filepath.Join(dir, path.Base(rel))
	return base + ".txt", base + ".json", nil
}

// SidecarPaths returns "", "" for a reference outside the vault; LoadSidecar and SaveSidecar report why.
func SidecarPaths(root, imageRel string) (txt, js string) {
	txt, js, err := sidecarPaths(root, imageRel)
	if err != nil {
		return "", ""
	}
	return txt, js
}

// LoadSidecar does not resolve the image itself, to keep search's per-image symlink walk off the hot path.
func LoadSidecar(root, imageRel string) (*Result, error) {
	_, jsPath, err := sidecarPaths(root, imageRel)
	if err != nil {
		return nil, err
	}
	src, err := readSidecarFile(jsPath)
	if err != nil {
		return nil, err
	}
	var r Result
	if err := json.Unmarshal(src, &r); err != nil {
		return nil, err
	}
	return &r, nil
}

var errNotRegular = errors.New("not a regular file")

// readSidecarFile opens p with O_NOFOLLOW|O_NONBLOCK, since only the
// sidecar directory is checked, not the name; anything but a regular file there is errNotRegular.
func readSidecarFile(p string) ([]byte, error) {
	f, err := os.OpenFile(p, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		if errors.Is(err, syscall.ELOOP) {
			if info, lerr := os.Lstat(p); lerr == nil && info.Mode()&fs.ModeSymlink != 0 {
				return nil, fmt.Errorf("%s is %w", p, errNotRegular)
			}
		}
		return nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%s is %w", p, errNotRegular)
	}
	return io.ReadAll(f)
}

// SaveSidecar does not resolve the image itself; the .txt file is every line's text, one per line.
func SaveSidecar(root, imageRel string, r *Result) error {
	rel, err := relInVault(root, imageRel)
	if err != nil {
		return err
	}
	base, err := sidecarBaseForWrite(root, rel)
	if err != nil {
		return err
	}

	texts := make([]string, len(r.Lines))
	for i, l := range r.Lines {
		texts[i] = l.Text
	}
	var txtBody string
	if len(texts) > 0 {
		txtBody = strings.Join(texts, "\n") + "\n"
	}

	jsBody, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}

	if err := atomicWrite(base+".txt", []byte(txtBody)); err != nil {
		return err
	}
	return atomicWrite(base+".json", jsBody)
}

// Stale fails like Ensure does for a deleted or out-of-vault image.
func Stale(root, imageRel string) (bool, error) {
	img, err := Locate(root, imageRel)
	if err != nil {
		return false, err
	}
	sidecar, loadErr := LoadSidecar(root, img.Rel)
	if loadErr != nil {
		return true, nil
	}
	sum, err := imageSHA256(img.Abs)
	if err != nil {
		return false, err
	}
	return sum != sidecar.SHA256, nil
}

func imageSHA256(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

// atomicWrite replaces path via a temp file and rename, so a symlink at
// path is overwritten rather than written through.
func atomicWrite(path string, data []byte) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".nn-ocr-*.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)

	_, writeErr := tmp.Write(data)
	syncErr := tmp.Sync()
	closeErr := tmp.Close()
	if err := errors.Join(writeErr, syncErr, closeErr); err != nil {
		return err
	}
	return os.Rename(tmpPath, path)
}
