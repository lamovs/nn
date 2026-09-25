package platform

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

type command struct {
	name         string
	args         []string
	stdin        []byte
	env          []string // KEY=VALUE entries added to the inherited environment
	discard      bool     // avoids hanging on a clipboard owner holding the selection open
	processGroup bool     // screenshot helpers get their own group so cancellation stops them too
}

// runCommand runs c to completion; tests replace it to stub external tools.
var runCommand = executeCommand

func executeCommand(ctx context.Context, c command) (stdout, stderr []byte, err error) {
	cmd := exec.CommandContext(ctx, c.name, c.args...)
	if c.processGroup {
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		cmd.Cancel = func() error {
			if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); err != nil {
				if errors.Is(err, syscall.ESRCH) {
					return os.ErrProcessDone
				}
				return err
			}
			return nil
		}
	}
	if len(c.env) > 0 {
		cmd.Env = append(os.Environ(), c.env...)
	}
	if c.stdin != nil {
		cmd.Stdin = bytes.NewReader(c.stdin)
	}
	var out, errOut bytes.Buffer
	if !c.discard {
		cmd.Stdout = &out
		cmd.Stderr = &errOut
	}
	cmd.WaitDelay = 2 * time.Second
	err = cmd.Run()
	if c.processGroup && cmd.Process != nil {
		// A screenshot tool must not leave helpers running after it exits.
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
	return out.Bytes(), errOut.Bytes(), err
}

const launchGrace = 2 * time.Second

// startCommand is not tied to ctx: cancelling nn must not kill the program
// it launched. It reports an early exit's status, nil once still running after launchGrace.
var startCommand = func(ctx context.Context, c command) error {
	cmd := exec.Command(c.name, c.args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return err
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	timer := time.NewTimer(launchGrace)
	defer timer.Stop()
	select {
	case err := <-done:
		return err
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return cancelled(ctx)
	}
}

var lookPath = exec.LookPath

func have(name string) bool {
	_, err := lookPath(name)
	return err == nil
}

func cancelled(ctx context.Context) error {
	return fmt.Errorf("%w: %w", ErrCancelled, context.Cause(ctx))
}

func commandError(ctx context.Context, name string, err error, stderr []byte) error {
	if ctx.Err() != nil {
		return cancelled(ctx)
	}
	if errors.Is(err, exec.ErrNotFound) {
		return fmt.Errorf("%s is not installed", filepath.Base(name))
	}
	if msg := strings.TrimSpace(string(stderr)); msg != "" {
		return fmt.Errorf("%s: %s", filepath.Base(name), msg)
	}
	return fmt.Errorf("%s: %w", filepath.Base(name), err)
}

// captureToFile treats anything short of an error message as cancellation,
// since tools report a dismissed selection inconsistently.
func captureToFile(ctx context.Context, c command, path string) ([]byte, error) {
	c.processGroup = true
	_, stderr, err := runCommand(ctx, c)
	if ctx.Err() != nil {
		return nil, cancelled(ctx)
	}
	if data, readErr := os.ReadFile(path); readErr == nil && len(data) > 0 {
		return data, nil
	}
	var exit *exec.ExitError
	switch {
	case err == nil:
	case !errors.As(err, &exit):
		return nil, commandError(ctx, c.name, err, stderr)
	default:
		msg := strings.TrimSpace(string(stderr))
		if msg != "" && !strings.Contains(strings.ToLower(msg), "cancel") {
			return nil, commandError(ctx, c.name, err, stderr)
		}
	}
	return nil, ErrCancelled
}

func withTempDir[T any](fn func(dir string) (T, error)) (T, error) {
	dir, err := os.MkdirTemp("", "nn-*")
	if err != nil {
		var zero T
		return zero, err
	}
	defer os.RemoveAll(dir)
	return fn(dir)
}
