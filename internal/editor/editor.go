// Package editor supports running the user's text editor; it never touches
// os/exec, which is internal/app's job.
package editor

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"unicode"
	"unicode/utf8"
)

const MaxSize = 4 << 20

// Draft is a scratch .md file created by NewDraft, for the caller to open in an editor and read back.
type Draft struct {
	Path    string
	Data    []byte
	Changed bool

	dir     string
	initial []byte
}

func (d *Draft) Discard() error { return os.RemoveAll(d.dir) }

// NewDraft creates a scratch .md file preloaded with initial, in a fresh directory only the user can read.
func NewDraft(initial []byte) (*Draft, error) {
	if len(initial) > MaxSize {
		return nil, errors.New("editor document exceeds the 4 MiB limit")
	}
	dir, err := os.MkdirTemp("", "nn-edit-")
	if err != nil {
		return nil, fmt.Errorf("create editor directory: %w", err)
	}
	d := &Draft{Path: filepath.Join(dir, "note.md"), dir: dir, initial: initial}
	if err := os.WriteFile(d.Path, initial, 0600); err != nil {
		_ = d.Discard()
		return nil, fmt.Errorf("write editor document: %w", err)
	}
	return d, nil
}

// ReadBack rejects anything but a plain, UTF-8, NUL-free file under MaxSize -
// not a symlink or device swapped in the draft's place.
func (d *Draft) ReadBack() error {
	data, err := readDraft(d.Path)
	if err != nil {
		return err
	}
	d.Data = data
	d.Changed = !bytes.Equal(d.initial, d.Data)
	return nil
}

// LineArgs returns the argument that opens program at line, or nil if it doesn't support one.
func LineArgs(program string, line int) []string {
	if line <= 0 {
		return nil
	}
	switch program {
	case "vi", "vim", "nvim", "nano", "hx", "helix", "kak":
		return []string{fmt.Sprintf("+%d", line)}
	case "micro":
		return []string{fmt.Sprintf("+%d:1", line)}
	default:
		return nil
	}
}

func readDraft(path string) ([]byte, error) {

	fd, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK|syscall.O_CLOEXEC, 0)
	if err != nil {
		return nil, fmt.Errorf("open saved editor document: %w", err)
	}
	f := os.NewFile(uintptr(fd), path)
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, fmt.Errorf("inspect saved editor document: %w", err)
	}
	if !info.Mode().IsRegular() {
		return nil, errors.New("saved editor document must be a regular file")
	}
	if info.Size() > MaxSize {
		return nil, errors.New("saved editor document exceeds the 4 MiB limit")
	}
	if err := f.Chmod(0600); err != nil {
		return nil, fmt.Errorf("protect saved editor document: %w", err)
	}
	data, err := io.ReadAll(io.LimitReader(f, MaxSize+1))
	if err != nil {
		return nil, fmt.Errorf("read saved editor document: %w", err)
	}
	if len(data) > MaxSize {
		return nil, errors.New("saved editor document exceeds the 4 MiB limit")
	}
	if !utf8.Valid(data) || bytes.IndexByte(data, 0) >= 0 {
		return nil, errors.New("saved editor document must be UTF-8 text without NUL bytes")
	}
	return data, nil
}

// Command splits an editor command line into argv like a shell would (no globbing or redirection).
func Command(value string) ([]string, error) {
	if !utf8.ValidString(value) || strings.ContainsFunc(value, func(r rune) bool { return unicode.IsControl(r) && r != '\t' }) {
		return nil, errors.New("VISUAL or EDITOR contains invalid control characters")
	}
	var args []string
	var word strings.Builder
	var quote rune
	escaped, started := false, false
	for _, r := range value {
		if escaped {
			word.WriteRune(r)
			escaped = false
			started = true
			continue
		}
		if r == '\\' && quote != '\'' {
			escaped = true
			started = true
			continue
		}
		if quote != 0 {
			if r == quote {
				quote = 0
			} else {
				word.WriteRune(r)
			}
			continue
		}
		if r == '\'' || r == '"' {
			quote = r
			started = true
			continue
		}
		if unicode.IsSpace(r) {
			if started {
				args = append(args, word.String())
				word.Reset()
				started = false
			}
			continue
		}
		word.WriteRune(r)
		started = true
	}
	if escaped || quote != 0 {
		return nil, errors.New("VISUAL or EDITOR has an unfinished quote or escape")
	}
	if started {
		args = append(args, word.String())
	}
	if len(args) == 0 || args[0] == "" {
		return nil, errors.New("set VISUAL or EDITOR to an editor command (for example: vi or code --wait)")
	}
	return args, nil
}
