// Package state tracks note opens for frecency ranking. It is shared across
// nn processes, so Save merges concurrent writes instead of overwriting them.
package state

import (
	"encoding/json"
	"errors"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"time"
)

const (
	dirName  = "nn"
	fileName = "state.json"

	maxOpens = 20

	lockWait = 2 * time.Second
	lockPoll = 10 * time.Millisecond
	fileVer  = 1
)

type State struct {
	path     string
	halfLife time.Duration

	mu    sync.RWMutex
	opens map[string][]time.Time // oldest first, at most maxOpens
	dirty bool
}

type fileFormat struct {
	Version int                    `json:"version"`
	Opens   map[string][]time.Time `json:"opens"`
}

func Path() (string, error) {
	if dir := os.Getenv("XDG_DATA_HOME"); filepath.IsAbs(dir) {
		return filepath.Join(dir, dirName, fileName), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".local", "share", dirName, fileName), nil
}

// Load: halfLife must be > 0, Frecency's decay rate (state.half_life).
func Load(halfLife time.Duration) (*State, error) {
	path, err := Path()
	if err != nil {
		return &State{opens: map[string][]time.Time{}, halfLife: halfLife}, err
	}
	return LoadFile(path, halfLife)
}

// LoadFile treats a missing or unparsable file as an empty State, not an error.
func LoadFile(path string, halfLife time.Duration) (*State, error) {
	s := &State{path: path, halfLife: halfLife, opens: map[string][]time.Time{}}
	opens, err := readFile(path)
	if err != nil {
		return s, err
	}
	s.opens = opens
	return s, nil
}

func (s *State) RecordOpen(path string, at time.Time) {
	if path == "" || at.IsZero() {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.opens[path] = addOpens(s.opens[path], at.UTC().Round(0))
	s.dirty = true
}

// Frecency sums 0.5^(age/halfLife) over path's last 20 opens, age from now.
func (s *State) Frecency(path string) float64 {
	return s.FrecencyAt(path, time.Now())
}

// FrecencyAt is Frecency with an explicit now. If halfLife <= 0, opens count
// undecayed instead of dividing by zero.
func (s *State) FrecencyAt(path string, now time.Time) float64 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	opens := s.opens[path]
	if s.halfLife <= 0 {
		return float64(len(opens))
	}
	var sum float64
	for _, at := range opens {
		age := now.Sub(at)
		if age < 0 {
			age = 0
		}
		sum += math.Pow(0.5, float64(age)/float64(s.halfLife))
	}
	return sum
}

// Save writes atomically under an advisory lock, merging opens saved by
// other processes since Load.
func (s *State) Save() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.dirty {
		return nil
	}
	if s.path == "" {
		return errors.New("state: no file path")
	}
	dir := filepath.Dir(s.path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}

	unlock := acquire(s.path + ".lock")
	defer unlock()

	onDisk, err := readFile(s.path)
	if err != nil {
		return err
	}
	for path, ats := range onDisk {
		merged := s.opens[path]
		for _, at := range ats {
			merged = addOpens(merged, at)
		}
		s.opens[path] = merged
	}

	data, err := json.Marshal(fileFormat{Version: fileVer, Opens: s.opens})
	if err != nil {
		return err
	}
	if err := atomicWrite(s.path, data); err != nil {
		return err
	}
	s.dirty = false
	return nil
}

// readFile treats a missing or unparsable file as empty, not an error.
func readFile(path string) (map[string][]time.Time, error) {
	opens := map[string][]time.Time{}
	src, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return opens, nil
	}
	if err != nil {
		return opens, err
	}
	var f fileFormat
	if err := json.Unmarshal(src, &f); err != nil {
		return opens, nil
	}
	for path, ats := range f.Opens {
		if path == "" {
			continue
		}
		var clean []time.Time
		for _, at := range ats {
			if !at.IsZero() {
				clean = addOpens(clean, at.UTC())
			}
		}
		if len(clean) > 0 {
			opens[path] = clean
		}
	}
	return opens, nil
}

// addOpens keeps ats sorted, deduplicated, and capped at maxOpens.
func addOpens(ats []time.Time, at time.Time) []time.Time {
	i, found := slices.BinarySearchFunc(ats, at, func(a, b time.Time) int { return a.Compare(b) })
	if found {
		return ats
	}
	ats = slices.Insert(ats, i, at)
	if len(ats) > maxOpens {
		ats = slices.Delete(ats, 0, len(ats)-maxOpens)
	}
	return ats
}

// acquire proceeds unlocked on failure: the atomic rename keeps the file
// intact, at worst losing one process's opens.
func acquire(path string) func() {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return func() {}
	}
	deadline := time.Now().Add(lockWait)
	for {
		err := lockFile(f)
		if err == nil {
			return func() {
				_ = unlockFile(f)
				_ = f.Close()
			}
		}
		if !errors.Is(err, errLocked) || time.Now().After(deadline) {
			_ = f.Close()
			return func() {}
		}
		time.Sleep(lockPoll)
	}
}

func atomicWrite(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".nn-state-*.tmp")
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
