package vault

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
)

func snapshotFixture(t *testing.T, content string) (*Vault, Snapshot) {
	t.Helper()
	v := tempVault(t)
	rel := "nn/snapshot.md"
	if err := os.WriteFile(v.Abs(rel), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := v.ReadSnapshot(rel)
	if err != nil {
		t.Fatal(err)
	}
	return v, s
}

func TestSnapshotPreviewEqualsWrite(t *testing.T) {
	for _, newline := range []string{"\n", "\r\n"} {
		t.Run(fmt.Sprintf("newline-%q", newline), func(t *testing.T) {
			original := strings.ReplaceAll("---\ntags: [Manual]\ncustom: untouched\n---\nbody #Inline  \n\n```go\nunclosed\n", "\n", newline)
			v, s := snapshotFixture(t, original)
			if string(s.Bytes) != original || s.SHA256 != sha256.Sum256([]byte(original)) || s.Note.Body != strings.SplitN(original, "---"+newline, 3)[2] {
				t.Fatalf("snapshot is inconsistent: %+v", s)
			}
			e := Enrichment{Title: "Generated title", AllowTitle: true, Body: "[[other.md]]", Tags: []string{"manual", "inline", "new"}}
			preview, err := PreviewEnrichment(s, e)
			if err != nil {
				t.Fatal(err)
			}
			if readFile(t, v.Abs(s.Path)) != original {
				t.Fatal("preview changed disk")
			}
			if !bytes.HasPrefix(preview, s.Bytes) || !strings.Contains(string(preview), "```"+newline+newline+"# Generated title") || !strings.HasSuffix(string(preview), "#new"+newline) {
				t.Fatalf("preview changed original or lost fence/tags: %q", preview)
			}
			if err := os.Chmod(v.Abs(s.Path), 0o640); err != nil {
				t.Fatal(err)
			}
			after, err := v.ApplyEnrichmentChecked(s, e)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(after.Bytes, preview) || after.SHA256 != sha256.Sum256(preview) || readFile(t, v.Abs(s.Path)) != string(preview) {
				t.Fatalf("write disagrees with preview: %q", after.Bytes)
			}
			if after.Note.Title != "Generated title" || !reflect.DeepEqual(after.Note.Tags, []string{"Manual", "Inline", "new"}) {
				t.Fatalf("parsed result = %+v", after.Note)
			}
			if info, err := os.Stat(v.Abs(s.Path)); err != nil || info.Mode().Perm() != 0o640 {
				t.Fatalf("permission inheritance: %v, %v", info, err)
			}
			if err := v.CheckSnapshot(after); err != nil {
				t.Fatal(err)
			}
			if err := v.CheckSnapshot(s); !errors.Is(err, ErrSnapshotChanged) {
				t.Fatalf("old snapshot accepted: %v", err)
			}
			noTempFiles(t, v.Root)
		})
	}
}

func TestSnapshotNoopPreservesIdentity(t *testing.T) {
	v, s := snapshotFixture(t, "---\naliases: [Manual]\ntags: [go]\n---\noriginal  \n")
	after, err := v.ApplyEnrichmentChecked(s, Enrichment{Title: "Ignored", AllowTitle: true, Tags: []string{"GO"}})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(after.Bytes, s.Bytes) || !os.SameFile(s.info, after.info) || after.Note.Title != "Manual" {
		t.Fatal("no-op changed content or identity")
	}
	if err := v.CheckSnapshot(s); err != nil {
		t.Fatal(err)
	}
	noTempFiles(t, v.Root)
}

func TestSnapshotStaleRefused(t *testing.T) {
	for _, change := range []string{"edit", "replacement", "symlink", "missing", "mutated-bytes"} {
		t.Run(change, func(t *testing.T) {
			v, s := snapshotFixture(t, "original\n")
			var outside string
			switch change {
			case "edit":
				if err := os.WriteFile(v.Abs(s.Path), []byte("manual edit\n"), 0o600); err != nil {
					t.Fatal(err)
				}
			case "replacement":
				if err := writeAtomic(v.Abs(s.Path), s.Bytes, 0o600, false); err != nil {
					t.Fatal(err)
				}
			case "symlink":
				outside = filepath.Join(t.TempDir(), "outside.md")
				if err := os.WriteFile(outside, s.Bytes, 0o600); err != nil {
					t.Fatal(err)
				}
				if err := os.Remove(v.Abs(s.Path)); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(outside, v.Abs(s.Path)); err != nil {
					t.Fatal(err)
				}
			case "missing":
				if err := os.Remove(v.Abs(s.Path)); err != nil {
					t.Fatal(err)
				}
			case "mutated-bytes":
				s.Bytes[0] = 'X'
			}
			before, beforeErr := os.ReadFile(v.Abs(s.Path))
			if err := v.CheckSnapshot(s); !errors.Is(err, ErrSnapshotChanged) {
				t.Fatalf("CheckSnapshot = %v", err)
			}
			if _, err := v.ApplyEnrichmentChecked(s, Enrichment{Tags: []string{"extra"}}); !errors.Is(err, ErrSnapshotChanged) {
				t.Fatalf("ApplyEnrichmentChecked = %v", err)
			}
			after, afterErr := os.ReadFile(v.Abs(s.Path))
			if !bytes.Equal(before, after) || (beforeErr == nil) != (afterErr == nil) {
				t.Fatal("failed write changed note")
			}
			if outside != "" && readFile(t, outside) != "original\n" {
				t.Fatal("symlink target changed")
			}
			noTempFiles(t, v.Root)
		})
	}
}

func TestSnapshotBoundaries(t *testing.T) {
	v, s := snapshotFixture(t, "original\n")
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "outside.md"), s.Bytes, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(v.Abs(s.Path), v.Abs("nn/link.md")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, v.Abs("nn/escape")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(v.Abs("nn/dir.md"), 0o700); err != nil {
		t.Fatal(err)
	}
	for _, rel := range []string{"nn/link.md", "nn/escape/outside.md", "nn/dir.md", "../outside.md", v.Abs(s.Path), "nn/../nn/snapshot.md", "nn/assets/picture.png"} {
		if _, err := v.ReadSnapshot(rel); err == nil {
			t.Errorf("ReadSnapshot accepted %q", rel)
		}
	}
	if err := os.WriteFile(v.Abs("outside-inbox.md"), s.Bytes, 0o600); err != nil {
		t.Fatal(err)
	}
	readOnly, err := v.ReadSnapshot("outside-inbox.md")
	if err != nil {
		t.Fatal(err)
	}
	if err := v.CheckSnapshot(readOnly); err != nil {
		t.Fatal(err)
	}
	if _, err := v.ApplyEnrichmentChecked(readOnly, Enrichment{Tags: []string{"extra"}}); err == nil {
		t.Fatal("applied outside inbox")
	}
	if readFile(t, v.Abs(readOnly.Path)) != string(readOnly.Bytes) {
		t.Fatal("outside-inbox note changed")
	}
	if err := os.Mkdir(v.Abs("reference"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(v.Abs("reference/note.md"), s.Bytes, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(v.Abs("reference"), v.Abs("nn/shortcut")); err != nil {
		t.Fatal(err)
	}
	shortcut, err := v.ReadSnapshot("nn/shortcut/note.md")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := v.ApplyEnrichmentChecked(shortcut, Enrichment{Tags: []string{"extra"}}); err == nil {
		t.Fatal("applied through a directory outside the physical inbox")
	}
	if err := os.Symlink(v.Abs("nn"), v.Abs("nn/inner")); err != nil {
		t.Fatal(err)
	}
	inner, err := v.ReadSnapshot("nn/inner/snapshot.md")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := v.ApplyEnrichmentChecked(inner, Enrichment{Tags: []string{"extra"}}); err != nil {
		t.Fatalf("safe inbox directory alias refused: %v", err)
	}
}

func TestSnapshotPreparedWriteRechecksBeforeRename(t *testing.T) {
	v, s := snapshotFixture(t, "original\n")
	outside := filepath.Join(t.TempDir(), "outside.md")
	if err := os.WriteFile(outside, []byte("outside\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	f, unlock, err := v.lockSnapshot(s)
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	_, err = writeSnapshotAtomic(s.target, []byte("unapproved\n"), s.info, func() error {
		if err := os.Remove(v.Abs(s.Path)); err != nil {
			return err
		}
		if err := os.Symlink(outside, v.Abs(s.Path)); err != nil {
			return err
		}
		return v.checkSnapshotLocked(s, f)
	})
	if !errors.Is(err, ErrSnapshotChanged) {
		t.Fatalf("prepared write returned %v", err)
	}
	info, err := os.Lstat(v.Abs(s.Path))
	if err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("changed target was replaced: %v, %v", info, err)
	}
	if readFile(t, outside) != "outside\n" {
		t.Fatal("outside file changed")
	}
	noTempFiles(t, v.Root)
}

func TestSnapshotConcurrentWriters(t *testing.T) {
	v, s := snapshotFixture(t, "original\n")
	const count = 12
	start := make(chan struct{})
	errs := make(chan error, count)
	var wg sync.WaitGroup
	for i := range count {
		wg.Go(func() {
			<-start
			_, err := v.ApplyEnrichmentChecked(s, Enrichment{Tags: []string{fmt.Sprintf("tag-%d", i)}})
			errs <- err
		})
	}
	close(start)
	wg.Wait()
	close(errs)
	success := 0
	for err := range errs {
		if err == nil {
			success++
		} else if !errors.Is(err, ErrSnapshotChanged) {
			t.Fatal(err)
		}
	}
	if success != 1 {
		t.Fatalf("successful writes = %d, want 1", success)
	}
	if current, err := v.Load(s.Path); err != nil || len(current.Tags) != 1 {
		t.Fatalf("current note = %+v, %v", current, err)
	}
	noTempFiles(t, v.Root)
}

func TestSnapshotConcurrentLegacyAppend(t *testing.T) {
	for range 12 {
		v, s := snapshotFixture(t, "original\n")
		start := make(chan struct{})
		checked := make(chan error, 1)
		legacy := make(chan error, 1)
		go func() {
			<-start
			_, err := v.ApplyEnrichmentChecked(s, Enrichment{Tags: []string{"extra"}})
			checked <- err
		}()
		go func() { <-start; _, err := v.Append(s.Path, "legacy", nil); legacy <- err }()
		close(start)
		checkedErr := <-checked
		if err := <-legacy; err != nil {
			t.Fatal(err)
		}
		if checkedErr != nil && !errors.Is(checkedErr, ErrSnapshotChanged) {
			t.Fatal(checkedErr)
		}
		current := readFile(t, v.Abs(s.Path))
		if !strings.Contains(current, "legacy") || !strings.HasPrefix(current, "original\n") {
			t.Fatalf("legacy append lost: %q", current)
		}
		if (checkedErr == nil) != strings.Contains(current, "#extra") {
			t.Fatalf("receipt disagrees with note: %q, %v", current, checkedErr)
		}
	}
}
