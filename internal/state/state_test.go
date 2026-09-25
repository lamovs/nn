package state

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

const testHalfLife = 14 * 24 * time.Hour

func tempStatePath(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("XDG_DATA_HOME", dir)
	return filepath.Join(dir, "nn", "state.json")
}

func TestPathUsesXDGDataHome(t *testing.T) {
	want := tempStatePath(t)
	got, err := Path()
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Errorf("Path() = %q, want %q", got, want)
	}
}

func TestPathIgnoresRelativeXDGDataHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_DATA_HOME", "relative/dir")
	got, err := Path()
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(home, ".local", "share", "nn", "state.json"); got != want {
		t.Errorf("Path() = %q, want %q", got, want)
	}
}

func TestLoadMissingFileIsEmpty(t *testing.T) {
	tempStatePath(t)
	s, err := Load(testHalfLife)
	if err != nil {
		t.Fatal(err)
	}
	if f := s.Frecency("nn/a.md"); f != 0 {
		t.Errorf("Frecency = %v, want 0", f)
	}
}

func TestLoadCorruptFileIsEmpty(t *testing.T) {
	path := tempStatePath(t)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	s, err := Load(testHalfLife)
	if err != nil {
		t.Fatalf("Load(testHalfLife) error = %v, want nil", err)
	}
	if s == nil {
		t.Fatal("Load(testHalfLife) returned nil State")
	}
	now := time.Date(2026, 9, 16, 12, 0, 0, 0, time.UTC)
	s.RecordOpen("nn/a.md", now)
	if err := s.Save(); err != nil {
		t.Fatal(err)
	}
	assertValidJSON(t, path)
	again, err := Load(testHalfLife)
	if err != nil {
		t.Fatal(err)
	}
	if f := again.FrecencyAt("nn/a.md", now); f != 1 {
		t.Errorf("FrecencyAt after repair = %v, want 1", f)
	}
}

func TestRecordSaveLoadRoundTrip(t *testing.T) {
	path := tempStatePath(t)
	now := time.Date(2026, 9, 16, 12, 0, 0, 0, time.UTC)
	s, err := Load(testHalfLife)
	if err != nil {
		t.Fatal(err)
	}
	s.RecordOpen("nn/a.md", now)
	s.RecordOpen("nn/a.md", now.Add(-14*24*time.Hour))
	s.RecordOpen("nn/b.md", now.Add(-28*24*time.Hour))
	if err := s.Save(); err != nil {
		t.Fatal(err)
	}
	assertValidJSON(t, path)

	loaded, err := Load(testHalfLife)
	if err != nil {
		t.Fatal(err)
	}
	if got := loaded.FrecencyAt("nn/a.md", now); !near(got, 1.5) {
		t.Errorf("a frecency = %v, want 1.5", got)
	}
	if got := loaded.FrecencyAt("nn/b.md", now); !near(got, 0.25) {
		t.Errorf("b frecency = %v, want 0.25", got)
	}
	if got := loaded.FrecencyAt("nn/missing.md", now); got != 0 {
		t.Errorf("missing frecency = %v, want 0", got)
	}
}

func TestFrecencyFutureOpenCountsFully(t *testing.T) {
	tempStatePath(t)
	s, _ := Load(testHalfLife)
	now := time.Date(2026, 9, 16, 12, 0, 0, 0, time.UTC)
	s.RecordOpen("nn/a.md", now.Add(time.Hour))
	if got := s.FrecencyAt("nn/a.md", now); got != 1 {
		t.Errorf("FrecencyAt = %v, want 1", got)
	}
}

func TestFrecencyAtNonPositiveHalfLifeCountsOpens(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	now := time.Date(2026, 9, 16, 12, 0, 0, 0, time.UTC)
	for _, halfLife := range []time.Duration{0, -time.Hour} {
		s, err := LoadFile(path, halfLife)
		if err != nil {
			t.Fatal(err)
		}
		s.RecordOpen("nn/a.md", now)
		s.RecordOpen("nn/a.md", now.Add(-30*24*time.Hour))
		s.RecordOpen("nn/a.md", now.Add(-365*24*time.Hour))
		got := s.FrecencyAt("nn/a.md", now)
		if math.IsNaN(got) || math.IsInf(got, 0) {
			t.Fatalf("halfLife = %v: FrecencyAt = %v, want a finite number", halfLife, got)
		}
		if got != 3 {
			t.Errorf("halfLife = %v: FrecencyAt = %v, want 3 (each open counts once, undecayed)", halfLife, got)
		}
	}
}

func TestRecordOpenCapsAtTwenty(t *testing.T) {
	tempStatePath(t)
	s, _ := Load(testHalfLife)
	now := time.Date(2026, 9, 16, 12, 0, 0, 0, time.UTC)
	for i := range 10 {
		s.RecordOpen("nn/a.md", now.Add(-time.Duration(1000+i)*24*time.Hour))
	}
	for i := range 20 {
		s.RecordOpen("nn/a.md", now.Add(-time.Duration(i)*time.Second))
	}
	got := s.FrecencyAt("nn/a.md", now)
	if got > 20 || got < 19.99 {
		t.Errorf("FrecencyAt = %v, want just under 20 (only the newest 20 kept)", got)
	}
	s.mu.RLock()
	n := len(s.opens["nn/a.md"])
	s.mu.RUnlock()
	if n != maxOpens {
		t.Errorf("kept %d opens, want %d", n, maxOpens)
	}
}

func TestRecordOpenIgnoresDuplicates(t *testing.T) {
	tempStatePath(t)
	s, _ := Load(testHalfLife)
	now := time.Date(2026, 9, 16, 12, 0, 0, 0, time.UTC)
	s.RecordOpen("nn/a.md", now)
	s.RecordOpen("nn/a.md", now.In(time.FixedZone("X", 3600)))
	if got := s.FrecencyAt("nn/a.md", now); got != 1 {
		t.Errorf("FrecencyAt = %v, want 1", got)
	}
}

func TestSaveWithoutChangesDoesNotWrite(t *testing.T) {
	path := tempStatePath(t)
	s, _ := Load(testHalfLife)
	if err := s.Save(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("state file exists after no-op Save: %v", err)
	}
}

func TestSaveMergesOtherWriter(t *testing.T) {
	tempStatePath(t)
	now := time.Date(2026, 9, 16, 12, 0, 0, 0, time.UTC)
	a, _ := Load(testHalfLife)
	b, _ := Load(testHalfLife)
	a.RecordOpen("nn/a.md", now)
	b.RecordOpen("nn/b.md", now)
	if err := a.Save(); err != nil {
		t.Fatal(err)
	}
	if err := b.Save(); err != nil {
		t.Fatal(err)
	}
	c, _ := Load(testHalfLife)
	if got := c.FrecencyAt("nn/a.md", now); got != 1 {
		t.Errorf("a frecency = %v, want 1 (lost by the second writer)", got)
	}
	if got := c.FrecencyAt("nn/b.md", now); got != 1 {
		t.Errorf("b frecency = %v, want 1", got)
	}
}

func TestConcurrentSavesKeepFileValid(t *testing.T) {
	path := tempStatePath(t)
	now := time.Date(2026, 9, 16, 12, 0, 0, 0, time.UTC)
	const rounds = 40

	stop := make(chan struct{})
	readerDone := make(chan error)
	go func() {
		for {
			select {
			case <-stop:
				readerDone <- nil
				return
			default:
			}
			src, err := os.ReadFile(path)
			if os.IsNotExist(err) {
				continue
			}
			if err != nil {
				readerDone <- err
				return
			}
			if !json.Valid(src) {
				readerDone <- fmt.Errorf("invalid JSON on disk: %q", src)
				return
			}
		}
	}()

	var wg sync.WaitGroup
	errs := make(chan error, 2*rounds)
	for w := range 2 {
		wg.Go(func() {
			s, err := Load(testHalfLife)
			if err != nil {
				errs <- err
				return
			}
			for i := range rounds {
				s.RecordOpen(fmt.Sprintf("nn/w%d-%d.md", w, i), now.Add(time.Duration(i)*time.Second))
				if err := s.Save(); err != nil {
					errs <- err
				}
			}
		})
	}
	wg.Wait()
	close(stop)
	if err := <-readerDone; err != nil {
		t.Fatal(err)
	}
	close(errs)
	for err := range errs {
		t.Error(err)
	}

	assertValidJSON(t, path)
	final, err := Load(testHalfLife)
	if err != nil {
		t.Fatal(err)
	}
	for w := range 2 {
		for i := range rounds {
			p := fmt.Sprintf("nn/w%d-%d.md", w, i)
			if final.FrecencyAt(p, now.Add(time.Hour)) == 0 {
				t.Errorf("open of %s was lost", p)
			}
		}
	}
	matches, _ := filepath.Glob(filepath.Join(filepath.Dir(path), ".nn-state-*.tmp"))
	if len(matches) != 0 {
		t.Errorf("temp files left behind: %v", matches)
	}
}

func assertValidJSON(t *testing.T, path string) {
	t.Helper()
	src, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var f fileFormat
	if err := json.Unmarshal(src, &f); err != nil {
		t.Fatalf("state file is not valid JSON: %v\n%s", err, src)
	}
	if f.Version != fileVer {
		t.Errorf("version = %d, want %d", f.Version, fileVer)
	}
}

func near(a, b float64) bool {
	return math.Abs(a-b) < 1e-9
}
