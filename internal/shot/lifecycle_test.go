package shot

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/lamovs/nn/internal/ai"
	"github.com/lamovs/nn/internal/config"
	"github.com/lamovs/nn/internal/vault"
)

type lifecycleFixture struct {
	base     string
	dir      string
	pidPath  string
	marker   string
	image    []byte
	request  ai.Request
	job      job
	approval ai.Approval
	vault    *vault.Vault
}

func newLifecycleFixture(t *testing.T, binary, engineBody string, holdBeforeWorker bool) *lifecycleFixture {
	t.Helper()
	base := t.TempDir()
	for _, name := range []string{"home", "config", "cache", "data", "state", "tmp", "vault", "bin", "shot-job"} {
		if err := os.Mkdir(filepath.Join(base, name), 0700); err != nil {
			t.Fatal(err)
		}
	}
	cfgPath := filepath.Join(base, "config", "nn.toml")
	for key, value := range map[string]string{
		"HOME": filepath.Join(base, "home"), "NN_CONFIG": cfgPath,
		"NN_ROOT": filepath.Join(base, "vault"), "NN_AI": "",
		"XDG_CONFIG_HOME": filepath.Join(base, "config"), "XDG_CACHE_HOME": filepath.Join(base, "cache"),
		"XDG_DATA_HOME": filepath.Join(base, "data"), "XDG_STATE_HOME": filepath.Join(base, "state"),
		"TMPDIR": filepath.Join(base, "tmp"), "PATH": filepath.Join(base, "bin"),
		"NN_LIFE_BASE": base,
	} {
		t.Setenv(key, value)
	}
	f := &lifecycleFixture{base: base, dir: filepath.Join(base, "shot-job"), pidPath: filepath.Join(base, "worker.pid"), marker: filepath.Join(base, "model.started"), image: realPNG(t)}
	engine := filepath.Join(base, "bin", "fake-model")
	body := "#!/bin/sh\n: > \"$NN_LIFE_BASE/model.started\"\n/bin/cat >/dev/null\n" + engineBody + "\n"
	if err := os.WriteFile(engine, []byte(body), 0700); err != nil {
		t.Fatal(err)
	}
	cfgText := "[vault]\nroot=" + config.TOMLString(filepath.Join(base, "vault")) + "\n[notify]\nenabled=false\n[ai.profiles.lifecycle]\nengine=\"command\"\ncommand=[" + config.TOMLString(engine) + ",\"{image}\"]\ntimeout=\"10s\"\n[ai.consent]\nlifecycle=\"ask\"\n"
	if err := os.WriteFile(cfgPath, []byte(cfgText), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, _, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	f.vault, err = vault.Open(cfg)
	if err != nil {
		t.Fatal(err)
	}
	note, err := f.vault.Create(vault.NewNote{Title: "Lifecycle capture", Body: "Recognized screenshot text"})
	if err != nil {
		t.Fatal(err)
	}
	call, err := ai.Resolve(cfg, "shot", ai.Overrides{AI: true, Profile: "lifecycle"})
	if err != nil {
		t.Fatal(err)
	}
	decision, approval, err := ai.Approve(&cfg, call, ai.Sends("shot"), onceAsker{}, io.Discard)
	if err != nil || decision != ai.Allowed {
		t.Fatalf("approval: %v %v", decision, err)
	}
	f.approval = approval
	f.request = ai.Request{System: "Analyze this screenshot without questions.", Text: "Recognized screenshot text", Image: f.image}
	f.job = job{Version: 1, ID: filepath.Base(f.dir), Config: cfg.Path, Root: f.vault.Root, Inbox: f.vault.Inbox, Note: note.Path, Image: "original.png", System: f.request.System, Text: f.request.Text}
	if err := os.WriteFile(filepath.Join(f.dir, f.job.Image), f.image, 0600); err != nil {
		t.Fatal(err)
	}
	wrapper := filepath.Join(base, "bin", "worker")
	launch := "#!/bin/sh\nprintf '%s' \"$$\" > \"$NN_LIFE_BASE/worker.pid\"\n"
	if holdBeforeWorker {
		launch += "while :; do :; done\n"
	} else {
		launch += "exec '" + strings.ReplaceAll(binary, "'", "'\\''") + "' \"$@\"\n"
	}
	if err := os.WriteFile(wrapper, []byte(launch), 0700); err != nil {
		t.Fatal(err)
	}
	previous := workerExecutable
	workerExecutable = func() (string, error) { return wrapper, nil }
	t.Cleanup(func() {
		workerExecutable = previous
		if data, err := os.ReadFile(f.pidPath); err == nil {
			if pid, err := strconv.Atoi(string(data)); err == nil {
				_ = syscall.Kill(pid, syscall.SIGTERM)
			}
		}
	})
	return f
}

func lifecycleWait(t *testing.T, description string, ready func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !ready() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", description)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func (f *lifecycleFixture) pid(t *testing.T) int {
	t.Helper()
	var pid int
	lifecycleWait(t, "worker pid", func() bool {
		data, err := os.ReadFile(f.pidPath)
		if err != nil {
			return false
		}
		pid, err = strconv.Atoi(string(data))
		return err == nil && pid > 0
	})
	return pid
}

func (f *lifecycleFixture) assertOriginal(t *testing.T) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(f.dir, f.job.Image))
	if err != nil || !bytes.Equal(data, f.image) {
		t.Fatalf("original capture lost or changed: %v", err)
	}
}

func (f *lifecycleFixture) start(ctx context.Context) error {
	return startWorker(ctx, f.dir, f.job, f.approval, f.request)
}

// holdAtReady runs the real private command but deliberately withholds GO.
func (f *lifecycleFixture) holdAtReady(t *testing.T, binary string) (*exec.Cmd, *os.File, <-chan error) {
	t.Helper()
	data, err := json.Marshal(f.job)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(f.dir, "job.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
	pipe := func() (*os.File, *os.File) {
		r, w, err := os.Pipe()
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { r.Close(); w.Close() })
		return r, w
	}
	grantR, grantW := pipe()
	readyR, readyW := pipe()
	releaseR, releaseW := pipe()
	command := exec.Command(binary, "__ai", f.dir)
	command.ExtraFiles = []*os.File{grantR, readyW, releaseR}
	command.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	grantR.Close()
	readyW.Close()
	releaseR.Close()
	done := make(chan error, 1)
	go func() { done <- command.Wait() }()
	t.Cleanup(func() { _ = command.Process.Kill() })
	if err := ai.WriteTransfer(grantW, f.approval, ai.TransferBinding{JobID: f.job.ID, RequestSHA256: ai.RequestDigest(f.request)}); err != nil {
		t.Fatal(err)
	}
	grantW.Close()
	if err := readyR.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatal(err)
	}
	var ack [6]byte
	if _, err := io.ReadFull(readyR, ack[:]); err != nil || string(ack[:]) != "READY\n" {
		t.Fatalf("worker acknowledgement=%q err=%v", ack, err)
	}
	readyR.Close()
	return command, releaseW, done
}

func TestBackgroundWorkerLifecycle(t *testing.T) {
	binary := filepath.Join(t.TempDir(), "nn")
	build := exec.Command("go", "build", "-o", binary, "../../cmd/nn")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build current worker: %v\n%s", err, out)
	}
	for _, how := range []string{"EOF", "SIGHUP", "invalid release"} {
		t.Run("READY without GO "+how, func(t *testing.T) {
			f := newLifecycleFixture(t, binary, "exit 91", false)
			command, release, done := f.holdAtReady(t, binary)
			time.Sleep(20 * time.Millisecond)
			if _, err := os.Stat(f.marker); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("model ran before GO: %v", err)
			}
			switch how {
			case "EOF":
				release.Close()
			case "SIGHUP":
				if err := command.Process.Signal(syscall.SIGHUP); err != nil {
					t.Fatal(err)
				}
			case "invalid release":
				if _, err := release.Write([]byte{'X'}); err != nil {
					t.Fatal(err)
				}
			}
			select {
			case err := <-done:
				if err == nil {
					t.Fatal("worker accepted an unreleased job")
				}
			case <-time.After(2 * time.Second):
				t.Fatal("worker did not stop before GO")
			}
			if _, err := os.Stat(f.marker); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("unreleased job ran a model: %v", err)
			}
			f.assertOriginal(t)
		})
	}
	t.Run("immediate completion commits without false recovery", func(t *testing.T) {
		f := newLifecycleFixture(t, binary, `printf '%s' '{"body":"Immediate answer"}'`, false)
		if err := f.start(context.Background()); err != nil {
			t.Fatalf("successful worker reported startup failure: %v", err)
		}
		lifecycleWait(t, "immediate success cache cleanup", func() bool { _, err := os.Stat(f.dir); return errors.Is(err, os.ErrNotExist) })
		note, err := f.vault.Load(f.job.Note)
		if err != nil || !strings.Contains(note.Body, "Immediate answer") || strings.Contains(note.Body, f.dir) {
			t.Fatalf("successful result has stale recovery: note=%+v err=%v", note, err)
		}
	})
	t.Run("cancel before acknowledgement retains image", func(t *testing.T) {
		f := newLifecycleFixture(t, binary, "exit 91", true)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		done := make(chan error, 1)
		go func() { done <- f.start(ctx); close(done) }()
		defer func() {
			cancel()
			select {
			case <-done:
			case <-time.After(7 * time.Second):
				t.Error("launcher did not finish during cleanup")
			}
		}()
		deadline := time.After(8 * time.Second)
	waiting:
		for {
			if data, err := os.ReadFile(f.pidPath); err == nil && len(data) > 0 {
				break
			}
			select {
			case err := <-done:
				t.Fatalf("launcher exited before cancellation fixture was ready: %v", err)
			case <-deadline:
				t.Fatal("launcher did not reach pre-ACK hold")
			case <-time.After(5 * time.Millisecond):
				continue waiting
			}
		}
		pid := f.pid(t)
		cancel()
		select {
		case err := <-done:
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("cancel before ACK: %v", err)
			}
		case <-time.After(7 * time.Second):
			t.Fatal("cancel before ACK did not terminate worker")
		}
		if err := syscall.Kill(pid, 0); !errors.Is(err, syscall.ESRCH) {
			t.Fatalf("worker survived cancellation: %v", err)
		}
		if _, err := os.Stat(f.marker); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("model started before acknowledged transfer: %v", err)
		}
		f.assertOriginal(t)
	})
	t.Run("parent cancellation after acknowledgement leaves independent worker", func(t *testing.T) {
		f := newLifecycleFixture(t, binary, `
if ( : <&3 ) 2>/dev/null || ( : >&4 ) 2>/dev/null || ( : <&5 ) 2>/dev/null; then
  printf leaked > "$NN_LIFE_BASE/fds"
  exit 92
fi
printf closed > "$NN_LIFE_BASE/fds"
while [ ! -f "$NN_LIFE_BASE/release" ]; do /bin/sleep 0.01; done
printf '%s' '{"body":"Independent background answer"}'`, false)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		if err := f.start(ctx); err != nil {
			t.Fatal(err)
		}
		cancel()
		pid := f.pid(t)
		lifecycleWait(t, "model descriptor check", func() bool {
			data, err := os.ReadFile(filepath.Join(f.base, "fds"))
			return err == nil && len(data) > 0
		})
		fds, err := os.ReadFile(filepath.Join(f.base, "fds"))
		if err != nil || string(fds) != "closed" {
			t.Fatalf("control descriptors leaked to model: %q (%v)", fds, err)
		}
		if err := syscall.Kill(pid, 0); err != nil {
			t.Fatalf("parent cancellation killed acknowledged worker: %v", err)
		}
		if err := os.WriteFile(filepath.Join(f.base, "release"), nil, 0600); err != nil {
			t.Fatal(err)
		}
		lifecycleWait(t, "successful cache cleanup", func() bool { _, err := os.Stat(f.dir); return errors.Is(err, os.ErrNotExist) })
		note, err := f.vault.Load(f.job.Note)
		if err != nil || !strings.Contains(note.Body, "Independent background answer") || !strings.Contains(note.Body, "Recognized screenshot text") {
			t.Fatalf("background answer not preserved: note=%+v err=%v", note, err)
		}
		lifecycleWait(t, "worker reaped", func() bool { return errors.Is(syscall.Kill(pid, 0), syscall.ESRCH) })
	})
	t.Run("SIGHUP stops model descendants and preserves recovery", func(t *testing.T) {
		f := newLifecycleFixture(t, binary, `
(while :; do printf child > "$NN_LIFE_BASE/heartbeat"; /bin/sleep 0.01; done) &
wait`, false)
		if err := f.start(context.Background()); err != nil {
			t.Fatal(err)
		}
		pid := f.pid(t)
		heartbeat := filepath.Join(f.base, "heartbeat")
		lifecycleWait(t, "model descendant", func() bool { _, err := os.Stat(heartbeat); return err == nil })
		if err := syscall.Kill(pid, syscall.SIGHUP); err != nil {
			t.Fatal(err)
		}
		lifecycleWait(t, "worker terminated after SIGHUP", func() bool { return errors.Is(syscall.Kill(pid, 0), syscall.ESRCH) })
		if err := os.WriteFile(heartbeat, []byte("stopped"), 0600); err != nil {
			t.Fatal(err)
		}
		time.Sleep(50 * time.Millisecond)
		data, err := os.ReadFile(heartbeat)
		if err != nil || string(data) != "stopped" {
			t.Fatalf("model descendant survived SIGHUP: %q (%v)", data, err)
		}
		f.assertOriginal(t)
		note, err := f.vault.Load(f.job.Note)
		if err != nil || !strings.Contains(note.Body, filepath.Join(f.dir, f.job.Image)) || !strings.Contains(note.Body, "Recognized screenshot text") {
			t.Fatalf("SIGHUP lost recovery: note=%+v err=%v", note, err)
		}
	})
}
