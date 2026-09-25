package platform

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
)

type call struct {
	name  string
	args  []string
	stdin string
	env   []string
	disc  bool
	group bool
}

func (c call) String() string { return strings.TrimSpace(c.name + " " + strings.Join(c.args, " ")) }

// fakeSystem stubs the environment, PATH and command runner.
type fakeSystem struct {
	t     *testing.T
	env   map[string]string
	bins  map[string]bool
	calls []call
	// handle answers a command; nil means success with no output.
	handle func(c call) (stdout, stderr string, err error)
}

func newFakeSystem(t *testing.T, os string, env map[string]string, bins ...string) *fakeSystem {
	t.Helper()
	f := &fakeSystem{t: t, env: env, bins: map[string]bool{}}
	for _, b := range bins {
		f.bins[b] = true
	}

	prevGOOS, prevEnv, prevLook, prevRun, prevStart := goos, getenv, lookPath, runCommand, startCommand
	goos = os
	getenv = func(k string) string { return f.env[k] }
	lookPath = func(name string) (string, error) {
		if f.bins[name] {
			return "/usr/bin/" + name, nil
		}
		return "", &exec.Error{Name: name, Err: exec.ErrNotFound}
	}
	runCommand = func(ctx context.Context, c command) ([]byte, []byte, error) {
		cl := call{name: c.name, args: c.args, stdin: string(c.stdin), env: c.env, disc: c.discard, group: c.processGroup}
		f.calls = append(f.calls, cl)
		if err := ctx.Err(); err != nil {
			return nil, nil, err
		}
		if f.handle == nil {
			return nil, nil, nil
		}
		stdout, stderr, err := f.handle(cl)
		return []byte(stdout), []byte(stderr), err
	}
	startCommand = func(ctx context.Context, c command) error {
		f.calls = append(f.calls, call{name: c.name, args: c.args})
		return nil
	}
	t.Cleanup(func() {
		goos, getenv, lookPath, runCommand, startCommand = prevGOOS, prevEnv, prevLook, prevRun, prevStart
	})
	return f
}

func (f *fakeSystem) commands() []string {
	var out []string
	for _, c := range f.calls {
		out = append(out, c.String())
	}
	return out
}

func exitError(t *testing.T, code int) error {
	t.Helper()
	err := exec.Command("sh", "-c", fmt.Sprintf("exit %d", code)).Run()
	if _, ok := err.(*exec.ExitError); !ok {
		t.Fatalf("sh exit %d: %v", code, err)
	}
	return err
}

// writeArg writes data to the path named by the last argument, as screenshot tools do.
func writeArg(t *testing.T, c call, data string) {
	t.Helper()
	if err := os.WriteFile(c.args[len(c.args)-1], []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
}
