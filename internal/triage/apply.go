package triage

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/lamovs/nn/internal/vault"
)

const (
	StatusApplied     = "applied"
	StatusUnchanged   = "unchanged"
	StatusConflict    = "conflict"
	StatusFailed      = "failed"
	StatusUncertain   = "uncertain"
	StatusUnattempted = "unattempted"
)

type ApplyNote struct {
	Path, Status, Error string
	Changed             bool
}
type ApplyReport struct {
	RecoveryDir string
	Notes       []ApplyNote
}
type journalNote struct {
	Path                string   `json:"path"`
	Backup              string   `json:"backup"`
	BeforeSHA256        string   `json:"before_sha256"`
	ExpectedAfterSHA256 string   `json:"expected_after_sha256"`
	Status              string   `json:"status"`
	Error               string   `json:"error,omitempty"`
	Actions             []Action `json:"actions"`
}
type journal struct {
	VaultRoot string        `json:"vault_root"`
	Version   int           `json:"version"`
	Created   time.Time     `json:"created"`
	Notes     []journalNote `json:"notes"`
}

// Apply persists private recovery data before its first checked write. It
// stops at the first failure and never rolls back another writer's changes.
func (s *Selection) Apply(ctx context.Context) (ApplyReport, error) {
	report := ApplyReport{}
	for _, n := range s.notes {
		report.Notes = append(report.Notes, ApplyNote{Path: n.source.Path, Status: StatusUnattempted})
	}
	if s.used {
		return report, errors.New("triage: selection already applied")
	}
	s.used = true
	if len(s.notes) == 0 {
		return report, nil
	}
	if err := ctx.Err(); err != nil {
		return report, err
	}
	for i, n := range s.notes {
		err := s.plan.session.v.CheckSnapshot(n.source.snapshot)
		if err == nil {
			err = s.checkTargets(ctx, n, nil)
		}
		if err != nil {
			report.Notes[i].Status = StatusFailed
			if errors.Is(err, vault.ErrSnapshotChanged) {
				report.Notes[i].Status = StatusConflict
			}
			report.Notes[i].Error = err.Error()
			return report, err
		}
	}
	root, err := filepath.Abs(s.plan.session.v.Root)
	if err != nil {
		return report, err
	}
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		return report, err
	}
	j := &journal{VaultRoot: root, Version: 1, Created: time.Now().UTC()}
	for _, n := range s.notes {
		after, err := vault.PreviewEnrichment(n.source.snapshot, n.enrichment)
		if err != nil {
			return report, err
		}
		j.Notes = append(j.Notes, journalNote{Path: n.source.Path, Backup: fmt.Sprintf("%04d.before", len(j.Notes)+1), BeforeSHA256: sum(n.source.snapshot.Bytes), ExpectedAfterSHA256: sum(after), Status: StatusUnattempted, Actions: n.actions})
	}
	dir, err := recoveryDir()
	if err != nil {
		return report, err
	}
	report.RecoveryDir = dir
	for i, n := range s.notes {
		if err := writeDurable(filepath.Join(dir, j.Notes[i].Backup), n.source.snapshot.Bytes); err != nil {
			return report, err
		}
	}
	if err := s.persist(dir, j); err != nil {
		return report, err
	}
	expected := map[string]vault.Snapshot{}
	for i, n := range s.notes {
		if err := ctx.Err(); err != nil {
			return report, err
		}
		applyErr := s.checkTargets(ctx, n, expected)
		var after vault.Snapshot
		if applyErr == nil {
			after, applyErr = s.plan.session.v.ApplyEnrichmentChecked(n.source.snapshot, n.enrichment)
		}
		if applyErr != nil {
			status := StatusFailed
			if errors.Is(applyErr, vault.ErrSnapshotChanged) {
				status = StatusConflict
			}
			report.Notes[i].Status = status
			report.Notes[i].Error = applyErr.Error()
			j.Notes[i].Status = status
			j.Notes[i].Error = applyErr.Error()
			if receiptErr := s.persist(dir, j); receiptErr != nil {
				return report, errors.Join(applyErr, fmt.Errorf("triage: receipt: %w", receiptErr))
			}
			return report, applyErr
		}
		status := StatusApplied
		if bytes.Equal(after.Bytes, n.source.snapshot.Bytes) {
			status = StatusUnchanged
		}
		expected[n.source.Path] = after
		report.Notes[i].Status = status
		report.Notes[i].Changed = status == StatusApplied
		j.Notes[i].Status = status
		if err := s.persist(dir, j); err != nil {
			report.Notes[i].Status = StatusUncertain
			report.Notes[i].Error = "note write succeeded but receipt persistence failed: " + err.Error()
			return report, fmt.Errorf("triage: %s", report.Notes[i].Error)
		}
	}
	return report, nil
}
func sum(data []byte) string { hash := sha256.Sum256(data); return hex.EncodeToString(hash[:]) }
func recoveryDir() (string, error) {
	data := os.Getenv("XDG_DATA_HOME")
	if !filepath.IsAbs(data) {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		data = filepath.Join(home, ".local", "share")
	}
	if err := os.MkdirAll(data, 0700); err != nil {
		return "", err
	}
	base := data
	for _, part := range []string{"nn", "triage"} {
		parent := base
		base = filepath.Join(base, part)
		if err := os.Mkdir(base, 0700); err != nil && !errors.Is(err, os.ErrExist) {
			return "", err
		}
		info, err := os.Lstat(base)
		if err != nil {
			return "", err
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return "", errors.New("triage: recovery parent is not a real directory")
		}
		if part == "triage" && info.Mode().Perm()&0077 != 0 {
			return "", errors.New("triage: recovery directory permissions must be private")
		}
		if err := syncDir(parent); err != nil {
			return "", err
		}
	}
	dir, err := os.MkdirTemp(base, "run-")
	if err != nil {
		return "", err
	}
	if err := syncDir(base); err != nil {
		return "", err
	}
	return dir, nil
}
func writeDurable(path string, data []byte) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	_, writeErr := f.Write(data)
	if writeErr == nil {
		writeErr = f.Sync()
	}
	return errors.Join(writeErr, f.Close())
}
func saveJournal(dir string, j *journal) error {
	data, err := json.MarshalIndent(j, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	f, err := os.CreateTemp(dir, ".manifest-")
	if err != nil {
		return err
	}
	name := f.Name()
	defer os.Remove(name)
	if _, err = f.Write(data); err == nil {
		err = f.Sync()
	}
	err = errors.Join(err, f.Close())
	if err != nil {
		return err
	}
	if err := os.Rename(name, filepath.Join(dir, "manifest.json")); err != nil {
		return err
	}
	return syncDir(dir)
}
func syncDir(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	return errors.Join(f.Sync(), f.Close())
}
