package ocr

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

type command struct {
	name string
	args []string
	env  []string // KEY=VALUE entries added to the inherited environment
}

// runCommand: tests replace it to exercise the engines without tesseract or nn-vision installed.
var runCommand = func(ctx context.Context, c command) (stdout, stderr []byte, err error) {
	cmd := exec.CommandContext(ctx, c.name, c.args...)
	if len(c.env) > 0 {
		cmd.Env = append(os.Environ(), c.env...)
	}
	var out, errOut bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errOut
	cmd.WaitDelay = 2 * time.Second
	err = cmd.Run()
	return out.Bytes(), errOut.Bytes(), err
}

var lookPath = exec.LookPath

func commandError(ctx context.Context, name string, err error, stderr []byte) error {
	if ctxErr := ctx.Err(); ctxErr != nil {
		return ctxErr
	}
	if errors.Is(err, exec.ErrNotFound) {
		return fmt.Errorf("%s: %w", name, ErrNotInstalled)
	}
	if msg := lastLines(string(stderr), 5); msg != "" {
		return fmt.Errorf("%s: %s", name, msg)
	}
	return fmt.Errorf("%s: %w", name, err)
}

func lastLines(s string, n int) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.TrimSpace(strings.Join(lines, "\n"))
}

// argPath keeps a path that starts with "-" from being read as a flag.
func argPath(p string) string {
	if strings.HasPrefix(p, "-") {
		return "./" + p
	}
	return p
}
