package app

import (
	"context"
	"errors"
	"io"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type promptTTY struct {
	io.Reader
	closeFn func() error
	writes  chan struct{}
	closed  atomic.Int32
}

func (t *promptTTY) Write(p []byte) (int, error) {
	if t.writes != nil {
		select {
		case t.writes <- struct{}{}:
		default:
		}
	}
	return len(p), nil
}

func (t *promptTTY) Close() error {
	t.closed.Add(1)
	if t.closeFn != nil {
		return t.closeFn()
	}
	return nil
}

func usePromptTTY(t *testing.T, tty io.ReadWriteCloser) {
	t.Helper()
	previous := openTTY
	openTTY = func() (io.ReadWriteCloser, error) { return tty, nil }
	t.Cleanup(func() { openTTY = previous })
}

func TestAskContextAnswer(t *testing.T) {
	tty := &promptTTY{Reader: strings.NewReader("once\r\n")}
	usePromptTTY(t, tty)
	answer, err := AskContext(context.Background(), "Allow?")
	if answer != "once" || err != nil || tty.closed.Load() != 1 {
		t.Fatalf("answer=%q err=%v closes=%d", answer, err, tty.closed.Load())
	}
}

func TestAskContextCancellationClosesReader(t *testing.T) {
	r, w := io.Pipe()
	defer w.Close()
	tty := &promptTTY{Reader: r, closeFn: r.Close, writes: make(chan struct{}, 1)}
	usePromptTTY(t, tty)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := AskContext(ctx, "Allow?")
		done <- err
	}()
	select {
	case <-tty.writes:
	case <-time.After(time.Second):
		t.Fatal("prompt not written")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) || tty.closed.Load() != 1 {
			t.Fatalf("err=%v closes=%d", err, tty.closed.Load())
		}
	case <-time.After(time.Second):
		t.Fatal("cancelled prompt still blocked")
	}
}

func TestAskContextCancelledBeforeOpen(t *testing.T) {
	previous := openTTY
	openTTY = func() (io.ReadWriteCloser, error) {
		t.Fatal("cancelled prompt opened terminal")
		return nil, nil
	}
	t.Cleanup(func() { openTTY = previous })
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := AskContext(ctx, "Allow?"); !errors.Is(err, context.Canceled) {
		t.Fatalf("err=%v", err)
	}
}
