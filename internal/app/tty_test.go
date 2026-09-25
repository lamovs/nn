package app

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"syscall"
	"testing"
	"time"
)

func TestTTYFileNonPollablePremise(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("premise only applies to darwin's non-pollable /dev/tty and fifos")
	}
	fifo := filepath.Join(t.TempDir(), "premise.fifo")
	if err := syscall.Mkfifo(fifo, 0600); err != nil {
		t.Fatalf("mkfifo: %v", err)
	}
	f, err := os.OpenFile(fifo, os.O_RDWR|syscall.O_NONBLOCK, 0)
	if err != nil {
		t.Fatalf("open fifo: %v", err)
	}
	defer f.Close()

	if err := f.SetReadDeadline(time.Now().Add(time.Second)); !errors.Is(err, os.ErrNoDeadline) {
		t.Fatalf("fifo is pollable on this Go version (SetReadDeadline err=%v); tests no longer reproduce the /dev/tty EAGAIN path", err)
	}

	buf := make([]byte, 16)
	_, readErr := f.Read(buf)
	if !errors.Is(readErr, syscall.EAGAIN) {
		t.Fatalf("expected EAGAIN on empty non-blocking fifo read, got %v", readErr)
	}
}

func TestTTYFileDelayedAnswer(t *testing.T) {
	fifo := filepath.Join(t.TempDir(), "answer.fifo")
	if err := syscall.Mkfifo(fifo, 0600); err != nil {
		t.Fatalf("mkfifo: %v", err)
	}

	tty, err := openTTYFile(fifo)
	if err != nil {
		t.Fatalf("openTTYFile: %v", err)
	}
	defer tty.Close()

	go func() {
		time.Sleep(100 * time.Millisecond)
		w, werr := os.OpenFile(fifo, os.O_WRONLY, 0)
		if werr != nil {
			t.Errorf("open writer: %v", werr)
			_ = tty.Close()
			return
		}
		defer w.Close()
		_, _ = w.Write([]byte("yes\n"))
	}()

	type result struct {
		answer string
		err    error
	}
	done := make(chan result, 1)
	go func() {
		answer, err := readTTYLine(tty, "")
		done <- result{answer, err}
	}()

	select {
	case res := <-done:
		if res.err != nil {
			t.Fatalf("readTTYLine: %v", res.err)
		}
		if res.answer != "yes" {
			t.Fatalf("answer=%q", res.answer)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("readTTYLine did not return within deadline")
	}
}

func TestTTYFileCloseDuringWait(t *testing.T) {
	fifo := filepath.Join(t.TempDir(), "close.fifo")
	if err := syscall.Mkfifo(fifo, 0600); err != nil {
		t.Fatalf("mkfifo: %v", err)
	}

	tty, err := openTTYFile(fifo)
	if err != nil {
		t.Fatalf("openTTYFile: %v", err)
	}

	done := make(chan error, 1)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		_, readErr := readTTYLine(tty, "")
		done <- readErr
	}()

	time.Sleep(100 * time.Millisecond)
	select {
	case err := <-done:
		_ = tty.Close()
		t.Fatalf("readTTYLine returned before close: %v", err)
	default:
	}
	if err := tty.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	select {
	case err := <-done:
		if !errors.Is(err, os.ErrClosed) {
			t.Fatalf("expected os.ErrClosed after close, got %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("readTTYLine did not return within deadline after close")
	}
	wg.Wait()
}

func TestAskContextCancellationOverTTYFile(t *testing.T) {
	fifo := filepath.Join(t.TempDir(), "cancel.fifo")
	if err := syscall.Mkfifo(fifo, 0600); err != nil {
		t.Fatalf("mkfifo: %v", err)
	}

	previous := openTTY
	openTTY = func() (io.ReadWriteCloser, error) { return openTTYFile(fifo) }
	t.Cleanup(func() { openTTY = previous })

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		// A fifo opened O_RDWR echoes what it writes back as input, so
		// keep the prompt to a single word with no newline: AskContext
		// appends only a trailing space, and a newline in the prompt
		// would let the echoed prompt satisfy the read on its own.
		_, err := AskContext(ctx, "ready")
		done <- err
	}()

	time.Sleep(100 * time.Millisecond)
	cancel()

	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("expected context.Canceled, got %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("AskContext did not return within deadline after cancel")
	}
}
