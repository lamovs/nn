package vault

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// ErrSnapshotChanged means that a preview no longer describes the current file.
var ErrSnapshotChanged = errors.New("note changed since preview")

type Snapshot struct {
	Path   string
	Bytes  []byte
	SHA256 [32]byte
	Note   *Note
	target string
	info   os.FileInfo
}

func (v *Vault) ReadSnapshot(rel string) (Snapshot, error) {
	target, _, err := v.snapshotTarget(rel)
	if err != nil {
		return Snapshot{}, err
	}
	f, unlock, err := lockNote(target)
	if err != nil {
		return Snapshot{}, err
	}
	defer unlock()
	info, err := f.Stat()
	if err != nil {
		return Snapshot{}, err
	}
	current, currentInfo, err := v.snapshotTarget(rel)
	if err != nil {
		return Snapshot{}, err
	}
	if target != current || !os.SameFile(info, currentInfo) {
		return Snapshot{}, fmt.Errorf("%s: %w", rel, ErrSnapshotChanged)
	}
	src, err := io.ReadAll(f)
	if err != nil {
		return Snapshot{}, err
	}
	s := v.makeSnapshot(rel, target, src, info)
	if err := v.checkSnapshotLocked(s, f); err != nil {
		return Snapshot{}, err
	}
	return s, nil
}

func (v *Vault) CheckSnapshot(s Snapshot) error {
	f, unlock, err := v.lockSnapshot(s)
	if err != nil {
		return err
	}
	defer unlock()
	return v.checkSnapshotLocked(s, f)
}

func PreviewEnrichment(s Snapshot, e Enrichment) ([]byte, error) {
	if sha256.Sum256(s.Bytes) != s.SHA256 {
		return nil, fmt.Errorf("%s: %w", s.Path, ErrSnapshotChanged)
	}
	addition := enrichmentAddition(string(s.Bytes), e)
	if addition == "" {
		return bytes.Clone(s.Bytes), nil
	}
	return []byte(appendMetadata(string(s.Bytes), addition)), nil
}

// ApplyEnrichmentChecked appends the previewed metadata, refusing a stale
// snapshot.
func (v *Vault) ApplyEnrichmentChecked(s Snapshot, e Enrichment) (Snapshot, error) {
	if !v.InInbox(s.Path) || strings.HasPrefix(s.Path, path.Join(v.Inbox, TemplatesDir)+"/") {
		return Snapshot{}, fmt.Errorf("%s is not an inbox note", s.Path)
	}
	f, unlock, err := v.lockSnapshot(s)
	if err != nil {
		return Snapshot{}, err
	}
	defer unlock()
	if err := v.checkSnapshotLocked(s, f); err != nil {
		return Snapshot{}, err
	}
	inbox, err := filepath.EvalSymlinks(v.Abs(v.Inbox))
	if err != nil {
		return Snapshot{}, err
	}
	relative, err := filepath.Rel(inbox, s.target)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return Snapshot{}, fmt.Errorf("%s is not physically inside the inbox", s.Path)
	}
	content, err := PreviewEnrichment(s, e)
	if err != nil {
		return Snapshot{}, err
	}
	if bytes.Equal(content, s.Bytes) {
		return v.makeSnapshot(s.Path, s.target, content, s.info), nil
	}
	currentInfo, err := f.Stat()
	if err != nil {
		return Snapshot{}, err
	}
	info, err := writeSnapshotAtomic(s.target, content, currentInfo, func() error {
		return v.checkSnapshotLocked(s, f)
	})
	if err != nil {
		return Snapshot{}, err
	}
	return v.makeSnapshot(s.Path, s.target, content, info), nil
}

func (v *Vault) makeSnapshot(rel, target string, src []byte, info os.FileInfo) Snapshot {
	src = bytes.Clone(src)
	return Snapshot{
		Path: rel, Bytes: src, SHA256: sha256.Sum256(src), target: target, info: info,
		Note: v.parseNote(rel, string(src), info.ModTime(), &lazyImages{vault: v}),
	}
}

func (v *Vault) snapshotTarget(rel string) (string, os.FileInfo, error) {
	if filepath.IsAbs(rel) || rel != cleanRel(rel) || rel == ".." || strings.HasPrefix(rel, "../") || !isNote(rel) {
		return "", nil, fmt.Errorf("invalid snapshot note path %q", rel)
	}
	dir, err := filepath.EvalSymlinks(filepath.Dir(v.Abs(rel)))
	if err != nil {
		return "", nil, err
	}
	if !v.inside(dir) {
		return "", nil, fmt.Errorf("%s: %w", rel, ErrOutsideVault)
	}
	target := filepath.Join(dir, path.Base(rel))
	info, err := os.Lstat(target)
	if err != nil {
		return "", nil, err
	}
	if !info.Mode().IsRegular() {
		return "", nil, fmt.Errorf("%s is not a regular note", rel)
	}
	return target, info, nil
}

func (v *Vault) lockSnapshot(s Snapshot) (*os.File, func(), error) {
	target, info, err := v.snapshotTarget(s.Path)
	if err != nil {
		return nil, nil, fmt.Errorf("%s: %w: %v", s.Path, ErrSnapshotChanged, err)
	}
	if s.info == nil || s.target != target || !os.SameFile(s.info, info) || sha256.Sum256(s.Bytes) != s.SHA256 {
		return nil, nil, fmt.Errorf("%s: %w", s.Path, ErrSnapshotChanged)
	}
	return lockNote(target)
}

func (v *Vault) checkSnapshotLocked(s Snapshot, f *os.File) error {
	target, info, err := v.snapshotTarget(s.Path)
	if err != nil {
		return fmt.Errorf("%s: %w: %v", s.Path, ErrSnapshotChanged, err)
	}
	openInfo, err := f.Stat()
	if err != nil {
		return err
	}
	if s.info == nil || target != s.target || !os.SameFile(s.info, info) || !os.SameFile(openInfo, info) {
		return fmt.Errorf("%s: %w", s.Path, ErrSnapshotChanged)
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return err
	}
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return err
	}
	if !bytes.Equal(h.Sum(nil), s.SHA256[:]) {
		return fmt.Errorf("%s: %w", s.Path, ErrSnapshotChanged)
	}
	return nil
}

// writeSnapshotAtomic captures identity before rename, so a concurrent
// replace right after does not read back as our own write.
func writeSnapshotAtomic(dst string, data []byte, previous os.FileInfo, check func() error) (os.FileInfo, error) {
	f, err := os.CreateTemp(filepath.Dir(dst), ".nn-*.tmp")
	if err != nil {
		return nil, err
	}
	defer os.Remove(f.Name())
	_, writeErr := f.Write(data)
	modeErr := f.Chmod(previous.Mode().Perm())
	syncErr := f.Sync()
	info, statErr := f.Stat()
	closeErr := f.Close()
	if err := errors.Join(writeErr, modeErr, syncErr, statErr, closeErr); err != nil {
		return nil, err
	}
	if err := check(); err != nil {
		return nil, err
	}
	if err := os.Rename(f.Name(), dst); err != nil {
		return nil, err
	}
	if dir, err := os.Open(filepath.Dir(dst)); err == nil {
		_ = dir.Sync()
		_ = dir.Close()
	}
	return info, nil
}
