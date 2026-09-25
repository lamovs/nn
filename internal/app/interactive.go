package app

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/lamovs/nn/internal/cli"
	"github.com/lamovs/nn/internal/platform"
)

var ErrCancelled = platform.ErrCancelled

func Interactive() bool {
	return cli.IsTerminal(os.Stdin) && cli.IsTerminal(os.Stderr)
}

var openTTY = func() (io.ReadWriteCloser, error) {
	return openTTYFile("/dev/tty")
}

// eagainRetryPause is how long ttyFile waits before retrying a read or
// write that returned EAGAIN.
const eagainRetryPause = 20 * time.Millisecond

// ttyFile wraps a nonblocking *os.File and retries on EAGAIN. On macOS
// /dev/tty is not pollable, so the runtime cannot park the goroutine on it
// and a nonblocking read returns EAGAIN immediately instead of waiting.
type ttyFile struct {
	f *os.File
}

func (t *ttyFile) Read(p []byte) (int, error) {
	for {
		n, err := t.f.Read(p)
		if n == 0 && errors.Is(err, syscall.EAGAIN) {
			time.Sleep(eagainRetryPause)
			continue
		}
		return n, err
	}
}

func (t *ttyFile) Write(p []byte) (int, error) {
	written := 0
	for written < len(p) {
		n, err := t.f.Write(p[written:])
		written += n
		if err != nil {
			if errors.Is(err, syscall.EAGAIN) {
				time.Sleep(eagainRetryPause)
				continue
			}
			return written, err
		}
	}
	return written, nil
}

func (t *ttyFile) Close() error {
	return t.f.Close()
}

func openTTYFile(path string) (io.ReadWriteCloser, error) {
	// Nonblocking so Close interrupts a pending read on cancellation.
	f, err := os.OpenFile(path, os.O_RDWR|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	return &ttyFile{f: f}, nil
}

func readTTYLine(tty io.ReadWriter, prompt string) (string, error) {
	if _, err := io.WriteString(tty, prompt); err != nil {
		return "", err
	}
	line, err := bufio.NewReader(tty).ReadString('\n')
	if err != nil && err != io.EOF {
		return "", err
	}
	return strings.TrimRight(line, "\r\n"), nil
}

// Confirm shows def as "[Y/n]" or "[y/N]"; an empty answer takes def.
func Confirm(prompt string, def bool) (bool, error) {
	tty, err := openTTY()
	if err != nil {
		return false, err
	}
	defer tty.Close()

	hint := "[y/N]"
	if def {
		hint = "[Y/n]"
	}
	answer, err := readTTYLine(tty, prompt+" "+hint+" ")
	if err != nil {
		return false, err
	}
	switch strings.ToLower(strings.TrimSpace(answer)) {
	case "":
		return def, nil
	case "y", "yes":
		return true, nil
	case "n", "no":
		return false, nil
	default:
		return false, fmt.Errorf("please answer y or n")
	}
}

func Ask(prompt string) (string, error) {
	tty, err := openTTY()
	if err != nil {
		return "", err
	}
	defer tty.Close()
	return readTTYLine(tty, prompt+" ")
}

func AskContext(ctx context.Context, prompt string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	tty, err := openTTY()
	if err != nil {
		return "", err
	}
	closed := make(chan struct{})
	stop := context.AfterFunc(ctx, func() {
		_ = tty.Close()
		close(closed)
	})
	defer func() {
		if stop() {
			_ = tty.Close()
		} else {
			<-closed
		}
	}()
	answer, err := readTTYLine(tty, prompt+" ")
	if ctx.Err() != nil {
		return "", ctx.Err()
	}
	return answer, err
}

func Choose(prompt string, items []string) (int, error) {
	if len(items) == 0 {
		return 0, errors.New("choose: no items to choose from")
	}
	if len(items) > 9 {
		return 0, errors.New("choose: more than 9 items")
	}

	tty, err := openTTY()
	if err != nil {
		return 0, err
	}
	defer tty.Close()

	var b strings.Builder
	if prompt != "" {
		fmt.Fprintln(&b, prompt)
	}
	for i, item := range items {
		fmt.Fprintf(&b, "%d) %s\n", i+1, item)
	}
	b.WriteString("> ")

	answer, err := readTTYLine(tty, b.String())
	if err != nil {
		return 0, err
	}
	answer = strings.TrimSpace(answer)
	if answer == "" {
		return 0, ErrCancelled
	}
	n, convErr := strconv.Atoi(answer)
	if convErr != nil || n < 1 || n > len(items) {
		return 0, fmt.Errorf("choose: %q is not one of 1-%d", answer, len(items))
	}
	return n - 1, nil
}
