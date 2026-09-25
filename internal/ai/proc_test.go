package ai

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// spawnerPids waits for the spawner fake to record its own pid and its
// child's.
func spawnerPids(f *fake) (child, grandchild int) {
	f.t.Helper()
	child, err1 := strconv.Atoi(f.waitForFile("child"))
	grandchild, err2 := strconv.Atoi(f.waitForFile("grandchild"))
	if err1 != nil || err2 != nil {
		f.t.Fatalf("pids: %v, %v", err1, err2)
	}
	return child, grandchild
}

func TestRunKillsTheTreeOnTimeout(t *testing.T) {
	f := newFake(t)
	call := commandCall("spawner")
	call.Profile.Timeout = 2 * time.Second

	start := time.Now()
	_, err := Run(context.Background(), issue(call), Request{Text: "T"})
	elapsed := time.Since(start)
	var timeout *TimeoutError
	if !errors.As(err, &timeout) || timeout.Name != "spawner" || timeout.Timeout != call.Profile.Timeout {
		t.Fatalf("err = %v, want a TimeoutError", err)
	}
	if elapsed > call.Profile.Timeout+waitDelay {
		t.Errorf("Run took %v: the kill did not reach every holder of the output", elapsed)
	}
	child, grandchild := spawnerPids(f)
	if !gone(child) {
		t.Errorf("the engine (pid %d) survived its timeout", child)
	}
	if !gone(grandchild) {
		t.Errorf("the process the engine started (pid %d) survived its timeout", grandchild)
	}
}

func TestRunKillsTheTreeOnCancel(t *testing.T) {
	f := newFake(t)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := Run(ctx, issue(commandCall("spawner")), Request{Text: "T"})
		done <- err
	}()
	child, grandchild := spawnerPids(f)
	cancel()

	select {
	case err := <-done:
		var timeout *TimeoutError
		if !errors.Is(err, context.Canceled) || errors.As(err, &timeout) {
			t.Errorf("err = %v, want context.Canceled", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Run did not return after the cancel")
	}
	if !gone(child) || !gone(grandchild) {
		t.Errorf("after the cancel: engine %d gone %v, its child %d gone %v", child, gone(child), grandchild, gone(grandchild))
	}
}

func TestRunEngineLeavesAProcessBehind(t *testing.T) {
	newFake(t)
	start := time.Now()
	res, err := Run(context.Background(), issue(commandCall("leaver")), Request{Text: "T"})
	if err != nil || res.Title != "Left" || res.Body != "behind" {
		t.Errorf("res = %+v, err = %v", res, err)
	}
	if elapsed := time.Since(start); elapsed > waitDelay+3*time.Second {
		t.Errorf("Run took %v", elapsed)
	}
}

func TestRunOutputLimit(t *testing.T) {
	path, err := exec.LookPath("fake-model")
	if err != nil {
		t.Fatal(err)
	}
	for _, stream := range []string{"stdout", "stderr"} {
		t.Run(stream, func(t *testing.T) {
			f := newFake(t)
			f.put(stream, strings.Repeat("x", maxOutput))
			stdout, stderr, err := run(context.Background(), process{name: "fake-model", path: path, dir: t.TempDir()}, 20*time.Second)
			if err != nil || len(stdout)+len(stderr) != maxOutput {
				t.Errorf("at the limit: %d+%d bytes, err = %v", len(stdout), len(stderr), err)
			}

			f.put(stream, strings.Repeat("x", maxOutput+1))
			f.put("sleep", "30")
			start := time.Now()
			stdout, stderr, err = run(context.Background(), process{name: "fake-model", path: path, dir: t.TempDir()}, 20*time.Second)
			if !errors.Is(err, ErrOutputLimit) || stdout != nil || stderr != nil {
				t.Errorf("over the limit: %d+%d bytes, err = %v", len(stdout), len(stderr), err)
			}
			if err != nil && !strings.Contains(err.Error(), "fake-model printed more than 4 MiB") {
				t.Errorf("message = %q", err)
			}
			if elapsed := time.Since(start); elapsed > 10*time.Second {
				t.Errorf("over the limit took %v: the engine was waited for", elapsed)
			}
		})
	}
}

func TestRunRefusesWhatItCannotSend(t *testing.T) {
	f := newFake(t)
	unknown := codexCall("", "")
	unknown.Profile.Engine = "gemini"
	untimed := codexCall("", "")
	untimed.Profile.Timeout = 0
	for _, tc := range []struct {
		name string
		call Call
		req  Request
		want string
	}{
		{"nothing to send", claudeCall("", ""), Request{System: "S"}, "nothing to send"},
		{"not an image", claudeCall("", ""), Request{Text: "T", Image: []byte("BM not a png")}, "not PNG, JPEG, GIF or WebP"},
		{"unknown engine", unknown, Request{Text: "T"}, `unknown engine "gemini"`},
		{"no timeout", untimed, Request{Text: "T"}, "no timeout"},
	} {
		if _, err := Run(context.Background(), issue(tc.call), tc.req); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: err = %v, want %q", tc.name, err, tc.want)
		}
	}
	if f.has("argv") {
		t.Error("an engine ran")
	}
}

func TestSniffImage(t *testing.T) {
	for _, tc := range []struct {
		data string
		want imageKind
	}{
		{"\x89PNG\r\n\x1a\nrest", imageKind{"image/png", "png"}},
		{"\xFF\xD8\xFF\xE0rest", imageKind{"image/jpeg", "jpg"}},
		{"GIF89arest", imageKind{"image/gif", "gif"}},
		{"GIF87arest", imageKind{"image/gif", "gif"}},
		{"RIFF\x00\x00\x00\x00WEBPVP8 ", imageKind{"image/webp", "webp"}},
	} {
		if got, ok := sniffImage([]byte(tc.data)); !ok || got != tc.want {
			t.Errorf("sniffImage(%q) = %v, %v", tc.data, got, ok)
		}
	}
	for _, data := range []string{"", "\x89PNG", "RIFF\x00\x00\x00\x00WAVE", "%PDF-1.7"} {
		if _, ok := sniffImage([]byte(data)); ok {
			t.Errorf("sniffImage(%q) took it for an image", data)
		}
	}
}

func TestExcerpt(t *testing.T) {
	for _, tc := range []struct{ name, text, want string }{
		{"plain", "error: out of memory", "error: out of memory"},
		{"control characters", "a\x1b[31mb\x00c\x7fd\u0085e", "a[31mbcde"},
		{"whitespace", " one\n\ttwo\u2028three  ", "one two three"},
		{"bidi controls", "file\u202eexe.txt\u202c \u2066x\u2069\u200e\u200f\u061c", "fileexe.txt x"},
		{"invisible formatting", "to\u200dk\u2060en\u00ad\U000e0041", "token"},
		{"letters kept", "caf\u00e9 \u0444\u0430\u0439\u043b", "caf\u00e9 \u0444\u0430\u0439\u043b"},
	} {
		if got := excerpt(tc.text); got != tc.want {
			t.Errorf("%s: excerpt(%q) = %q, want %q", tc.name, tc.text, got, tc.want)
		}
	}
	long := strings.Repeat("x", maxExcerpt+10)
	if got := excerpt(long); got != strings.Repeat("x", maxExcerpt)+"..." {
		t.Errorf("excerpt of %d characters = %q", len(long), got)
	}
}

func TestLookBinary(t *testing.T) {
	got, err := LookBinary(codexCall("", "").Profile)
	if err != nil || got != filepath.Join(testBin, "codex") {
		t.Errorf("codex: %q, %v", got, err)
	}

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "model"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)
	got, err = LookBinary(commandCall("./model").Profile)
	if want, _ := filepath.Abs("model"); err != nil || got != want || !filepath.IsAbs(got) {
		t.Errorf("./model: %q, %v, want %q", got, err, want)
	}
	if _, err := LookBinary(commandCall().Profile); err == nil {
		t.Error("a command profile with no command has a binary")
	}
}
