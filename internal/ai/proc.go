package ai

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"syscall"
	"time"
)

// maxOutput is the most an engine may print to stdout, and again to
// stderr besides what it echoes there, before its run fails.
const maxOutput = 4 << 20

// waitDelay is how long a run waits, once its process has been killed or
// has exited, for whatever still holds its output open.
const waitDelay = 2 * time.Second

var (
	errTimedOut = errors.New("timed out")
	errTooMuch  = errors.New("too much output")
)

type process struct {
	name  string // as messages call it
	path  string // absolute
	args  []string
	stdin []byte   // nil for none
	env   []string // nil for nn's own
	dir   string

	echo       int // stderr bytes allowed on top of maxOutput, for a prompt echo
	stdoutEcho int // same, for stdout

	timeoutHint string // what may be behind a timeout
}

// run kills p's whole process group on ctx cancel, timeout, or output past
// maxOutput; a child left running after p exits 0 is not killed.
func run(ctx context.Context, p process, timeout time.Duration) (stdout, stderr []byte, err error) {
	if err := validateArguments(append([]string{p.path}, p.args...)); err != nil {
		return nil, nil, err
	}
	runCtx, stop := context.WithCancelCause(ctx)
	defer stop(nil)
	runCtx, cancelTimeout := context.WithTimeoutCause(runCtx, timeout, errTimedOut)
	defer cancelTimeout()

	cmd := exec.CommandContext(runCtx, p.path, p.args...)
	cmd.Dir = p.dir
	cmd.Env = p.env
	if p.stdin != nil {
		cmd.Stdin = bytes.NewReader(p.stdin)
	}
	tooMuch := func() { stop(errTooMuch) }
	out := &capped{limit: maxOutput + p.stdoutEcho, full: tooMuch}
	errOut := &capped{limit: maxOutput + p.echo, full: tooMuch}
	cmd.Stdout, cmd.Stderr = out, errOut
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	stopped := false // settled by Cancel, which runs before Wait returns
	cmd.Cancel = func() error {
		err := killGroup(cmd.Process)
		stopped = !errors.Is(err, os.ErrProcessDone)
		return err
	}
	cmd.WaitDelay = waitDelay

	err = cmd.Run()
	switch {
	case out.over || errOut.over:
		return nil, nil, fmt.Errorf("%s printed more than %d MiB: %w", p.name, maxOutput>>20, ErrOutputLimit)
	case !stopped:
		// Ended on its own, before any kill: what it printed is whole.
		if errors.Is(err, exec.ErrWaitDelay) {
			err = nil
		}
		return out.buf.Bytes(), errOut.buf.Bytes(), err
	case errors.Is(context.Cause(runCtx), errTimedOut):
		return nil, nil, &TimeoutError{Name: p.name, Timeout: timeout, Hint: p.timeoutHint}
	}
	return nil, nil, fmt.Errorf("%s: %w", p.name, context.Cause(runCtx))
}

// killGroup kills the process group proc leads; it kills nothing once proc
// has been waited for, since the group's pid may have been reused by then.
func killGroup(proc *os.Process) error {
	if err := proc.Signal(syscall.Signal(0)); err != nil {
		return err // os.ErrProcessDone once it has been waited for
	}
	if err := syscall.Kill(-proc.Pid, syscall.SIGKILL); err != nil {
		if errors.Is(err, syscall.ESRCH) {
			return os.ErrProcessDone
		}
		return err
	}
	return nil
}

// capped keeps what one output stream prints, up to limit bytes; a write
// past that keeps nothing, calls full, and fails.
type capped struct {
	buf   bytes.Buffer
	limit int
	full  func()
	over  bool
}

func (c *capped) Write(p []byte) (int, error) {
	if c.buf.Len()+len(p) > c.limit {
		c.over = true
		c.full()
		return 0, errTooMuch
	}
	return c.buf.Write(p)
}
