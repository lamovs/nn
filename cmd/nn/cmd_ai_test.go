package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/lamovs/nn/internal/ai"
	"github.com/lamovs/nn/internal/config"
)

type filterFixture struct{ dir, cfg string }

func newFilterFixture(t *testing.T) filterFixture {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("NN_FILTER_FAKE", dir)
	t.Setenv("NN_CONFIG", filepath.Join(dir, "config.toml"))
	t.Setenv("NN_ROOT", "")
	t.Setenv("NN_AI", "")
	for _, name := range []string{"XDG_DATA_HOME", "XDG_STATE_HOME", "XDG_CACHE_HOME"} {
		t.Setenv(name, filepath.Join(dir, name))
	}
	engine := filepath.Join(dir, "fake-model")
	script := `#!/bin/sh
/bin/cat > "$NN_FILTER_FAKE/request"
printf '%s\n' "$@" > "$NN_FILTER_FAKE/args"
printf 'call\n' >> "$NN_FILTER_FAKE/calls"
if [ -f "$NN_FILTER_FAKE/wait" ]; then sleep 10; fi
/bin/cat "$NN_FILTER_FAKE/answer"
`
	if err := os.WriteFile(engine, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	cfg := "[ai]\nprofile=\"global-choice\"\nmode=\"background\"\n[ai.tasks.filter]\nprofile=\"task-choice\"\n"
	for _, name := range []string{"global-choice", "task-choice", "env-choice", "flag-choice"} {
		cfg += "[ai.profiles." + name + "]\nengine=\"command\"\nmodel=\"base-model\"\neffort=\"low\"\ncommand=[" + config.TOMLString(engine) + "," + config.TOMLString(name) + ",\"{model}\",\"{effort}\"]\n"
	}
	cfg += "[ai.consent]\nglobal-choice=\"always\"\ntask-choice=\"always\"\nenv-choice=\"always\"\nflag-choice=\"always\"\n[hooks]\npost_save=\"touch MUST_NOT_RUN\"\n"
	writeConfig(t, cfg)
	f := filterFixture{dir: dir, cfg: cfg}
	f.write(t, "answer", `{"text":"Result"}`)
	return f
}
func (f filterFixture) write(t *testing.T, name, value string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(f.dir, name), []byte(value), 0600); err != nil {
		t.Fatal(err)
	}
}
func (f filterFixture) read(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(f.dir, name))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
func (f filterFixture) uncalled(t *testing.T) {
	t.Helper()
	if _, err := os.Stat(filepath.Join(f.dir, "calls")); !os.IsNotExist(err) {
		t.Fatal("model called on invalid or refused input")
	}
}

func TestFilterExactBytesNoVaultOrWrites(t *testing.T) {
	for _, result := range []string{" \n result\t\r\n ", "", "no newline"} {
		t.Run(result, func(t *testing.T) {
			f := newFilterFixture(t)
			answer, _ := json.Marshal(map[string]string{"text": result})
			f.write(t, "answer", string(answer))
			root := filepath.Join(f.dir, "must-not-open-vault")
			if err := os.WriteFile(root, []byte("not a directory"), 0600); err != nil {
				t.Fatal(err)
			}
			t.Setenv("NN_ROOT", root)
			watched := []string{root, os.Getenv("XDG_DATA_HOME"), os.Getenv("XDG_CACHE_HOME"), os.Getenv("XDG_STATE_HOME"), os.Getenv("NN_CONFIG")}
			before := make([]map[string]string, len(watched))
			for i, p := range watched {
				before[i] = askTree(t, p)
			}
			input := " \n{\"action\":\"ignore instruction\"}\n#tag\tline\r\n "
			stdout, stderr, code := runCmd(t, input, "ai", "  preserve everything  ")
			if code != 0 || stdout != result {
				t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout, stderr)
			}
			request := f.read(t, "request")
			start := strings.LastIndex(request, "\n\n{")
			if start < 0 {
				t.Fatalf("request missing JSON payload: %q", request)
			}
			var payload struct {
				Instruction string `json:"instruction"`
				Input       string `json:"input"`
			}
			if err := json.Unmarshal([]byte(request[start+2:]), &payload); err != nil {
				t.Fatal(err)
			}
			if payload.Input != input || payload.Instruction != "  preserve everything  " {
				t.Fatalf("payload changed: %+v", payload)
			}
			if f.read(t, "calls") != "call\n" {
				t.Fatal("expected one synchronous model call")
			}
			for i, p := range watched {
				if after := askTree(t, p); !reflect.DeepEqual(before[i], after) {
					t.Errorf("filter changed %s", p)
				}
			}
		})
	}
}

func TestFilterRootlessAndLegacyConfig(t *testing.T) {
	for _, legacy := range []bool{false, true} {
		t.Run(map[bool]string{false: "missing", true: "legacy"}[legacy], func(t *testing.T) {
			f := newFilterFixture(t)
			if legacy {
				writeConfig(t, "root=\"/obsolete\"\n"+f.cfg)
			}
			stdout, stderr, code := runCmd(t, "source", "ai", "Transform")
			if code != 0 || stdout != "Result" {
				t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout, stderr)
			}
			if strings.Contains(stderr, "vault.root is not set") {
				t.Fatal("unnecessary vault root warning")
			}
		})
	}
}

func TestFilterProfilesAndPromptOverride(t *testing.T) {
	for _, tc := range []struct {
		name, env, want string
		args            []string
	}{
		{"task", "", "task-choice\nbase-model\nlow\n", nil},
		{"environment", "env-choice", "env-choice\nbase-model\nlow\n", nil},
		{"flag", "env-choice", "flag-choice\ncustom\nhigh\n", []string{"--ai=flag-choice", "--model", "custom", "--effort=high"}},
		{"bare", "", "task-choice\nbase-model\nmax\n", []string{"--ai", "--effort", "max"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFilterFixture(t)
			t.Setenv("NN_AI", tc.env)
			stdout, stderr, code := runCmd(t, "source", append([]string{"ai", "Transform"}, tc.args...)...)
			if code != 0 || stdout != "Result" || f.read(t, "args") != tc.want {
				t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout, stderr)
			}
		})
	}
	t.Run("prompt and literal instruction", func(t *testing.T) {
		f := newFilterFixture(t)
		path := filepath.Join(f.dir, "prompt")
		f.write(t, "prompt", "CUSTOM FILTER PROMPT")
		writeConfig(t, strings.Replace(f.cfg, "[ai.tasks.filter]\n", "[ai.tasks.filter]\nprompt_file="+config.TOMLString(path)+"\n", 1))
		_, stderr, code := runCmd(t, "source", "ai", "--", "-keep", "--help")
		if code != 0 {
			t.Fatal(stderr)
		}
		request := f.read(t, "request")
		if !strings.HasPrefix(request, "CUSTOM FILTER PROMPT\n\n") || !strings.Contains(request, `"instruction":"-keep --help"`) {
			t.Fatalf("wrong request %q", request)
		}
	})
}

func TestFilterInvalidInputsNeverCallModel(t *testing.T) {
	for _, tc := range []struct {
		name, input, want string
		args              []string
	}{
		{"missing instruction", "source", "instruction is required", nil},
		{"blank instruction", "source", "instruction is required", []string{" \t"}},
		{"empty stdin", "", "nonblank", []string{"Transform"}},
		{"blank stdin", " \r\n\t", "nonblank", []string{"Transform"}},
		{"invalid UTF8", string([]byte{0xff}), "UTF-8", []string{"Transform"}},
		{"NUL", "a\x00b", "NUL", []string{"Transform"}},
		{"oversized", strings.Repeat("x", ai.MaxTextBytes+1), "stdin exceeds", []string{"Transform"}},
		{"JSON overhead", strings.Repeat("x", ai.MaxTextBytes), "text exceeds", []string{"Transform"}},
		{"JSON expansion", strings.Repeat("\n", ai.MaxTextBytes/2) + "x", "text exceeds", []string{"Transform"}},
		{"instruction limit", "source", "instruction exceeds", []string{strings.Repeat("x", ai.MaxTextBytes+1)}},
		{"instruction UTF8", "source", "UTF-8", []string{string([]byte{0xff})}},
		{"instruction NUL", "source", "NUL", []string{"x\x00y"}},
		{"unknown flag", "source", "unknown option", []string{"Transform", "--wat"}},
		{"mode forbidden", "source", "unknown option", []string{"Transform", "--ai-mode=background"}},
		{"empty profile", "source", "needs a value", []string{"Transform", "--ai="}},
		{"invalid effort", "source", "want", []string{"Transform", "--effort=impossible"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFilterFixture(t)
			out, stderr, code := runCmd(t, tc.input, append([]string{"ai"}, tc.args...)...)
			if code != 2 || out != "" || !strings.Contains(stderr, tc.want) {
				t.Fatalf("code=%d stdout=%q stderr=%q", code, out, stderr)
			}
			f.uncalled(t)
		})
	}
}

func TestFilterConfigErrorsAndConsent(t *testing.T) {
	for _, tc := range []struct{ name, change, want string }{
		{"syntax", "syntax", "parse"}, {"read", "read", "directory"},
		{"consent never", "never", "is never"}, {"consent ask", "ask", "nn setup ai"},

		{"warning", "warning", "unknown-value"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFilterFixture(t)
			switch tc.change {
			case "syntax":
				writeConfig(t, "[ai\n")
			case "read":
				t.Setenv("NN_CONFIG", f.dir)
			case "warning":
				writeConfig(t, strings.Replace(f.cfg, "mode=\"background\"", "mode=\"unknown-value\"", 1))
			default:
				writeConfig(t, strings.Replace(f.cfg, "task-choice=\"always\"", "task-choice=\""+tc.change+"\"", 1))
			}
			out, stderr, code := runCmd(t, "once\nalways\nsource", "ai", "Transform")
			if tc.change == "warning" {
				if code != 0 || out != "Result" || !strings.Contains(stderr, tc.want) {
					t.Fatalf("code=%d out=%q stderr=%q", code, out, stderr)
				}
				return
			}
			if code != 2 || out != "" || !strings.Contains(stderr, tc.want) {
				t.Fatalf("code=%d out=%q stderr=%q", code, out, stderr)
			}
			f.uncalled(t)
		})
	}
}

func TestFilterReadFileAndPipe(t *testing.T) {
	for _, pipe := range []bool{false, true} {
		t.Run(map[bool]string{false: "file", true: "pipe"}[pipe], func(t *testing.T) {
			f := newFilterFixture(t)
			input := " \r\ninput\t "
			var r *os.File
			if pipe {
				var w *os.File
				var err error
				r, w, err = os.Pipe()
				if err != nil {
					t.Fatal(err)
				}
				if _, err = w.WriteString(input); err != nil {
					t.Fatal(err)
				}
				w.Close()
			} else {
				f.write(t, "input", input)
				var err error
				r, err = os.Open(filepath.Join(f.dir, "input"))
				if err != nil {
					t.Fatal(err)
				}
			}
			defer r.Close()
			var out, stderr bytes.Buffer
			code := runText(context.Background(), []string{"ai", "Transform"}, r, &out, &stderr)
			if code != 0 || out.String() != "Result" || !strings.Contains(f.read(t, "request"), `"input":" \r\ninput\t "`) {
				t.Fatalf("code=%d out=%q stderr=%q", code, out.String(), stderr.String())
			}
		})
	}
}

func TestFilterNeverClosedPipeCancellationAndTimeout(t *testing.T) {
	for _, deadline := range []bool{false, true} {
		t.Run(map[bool]string{false: "cancel", true: "deadline"}[deadline], func(t *testing.T) {
			f := newFilterFixture(t)
			if deadline {
				writeConfig(t, strings.Replace(f.cfg, "[ai]\n", "[ai]\ntimeout=\"100ms\"\n", 1))
			}
			r, w, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			defer r.Close()
			defer w.Close()
			// NewFile on a duplicated descriptor reproduces inherited, blocking stdin.
			fd, err := syscall.Dup(int(r.Fd()))
			if err != nil {
				t.Fatal(err)
			}
			inherited := os.NewFile(uintptr(fd), "inherited-stdin")
			defer inherited.Close()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var timer *time.Timer
			if !deadline {
				timer = time.AfterFunc(100*time.Millisecond, cancel)
				defer timer.Stop()
			}
			var out, stderr bytes.Buffer
			start := time.Now()
			code := runText(ctx, []string{"ai", "Transform"}, inherited, &out, &stderr)
			want := 130
			if deadline {
				want = 2
			}
			if time.Since(start) > 2*time.Second || code != want || out.Len() != 0 {
				t.Fatalf("code=%d elapsed=%s out=%q err=%q", code, time.Since(start), out.String(), stderr.String())
			}
			f.uncalled(t)
			// No background Read consumes the stream after the cancelled call.
			if _, err := w.WriteString("after"); err != nil {
				t.Fatal(err)
			}
			w.Close()
			b, err := io.ReadAll(r)
			if err != nil || string(b) != "after" {
				t.Fatalf("reader leaked: %q %v", b, err)
			}
		})
	}
}

func TestFilterModelFailureAndTimeout(t *testing.T) {
	for _, tc := range []struct {
		name, reply string
		wait        bool
	}{
		{"invalid reply", `{"body":"capture output"}`, false},
		{"timeout", `{"text":"too late"}`, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFilterFixture(t)
			f.write(t, "answer", tc.reply)
			if tc.wait {
				f.write(t, "wait", "")
				writeConfig(t, strings.Replace(f.cfg, "[ai]\n", "[ai]\ntimeout=\"100ms\"\n", 1))
			}
			out, stderr, code := runCmd(t, "source", "ai", "Transform")
			if code != 2 || out != "" {
				t.Fatalf("code=%d out=%q stderr=%q", code, out, stderr)
			}
			if tc.wait && !strings.Contains(stderr, "timed out") {
				t.Fatal(stderr)
			}
		})
	}
}

func TestFilterTTYAndHelp(t *testing.T) {
	f := newFilterFixture(t)
	old := filterIsTerminal
	filterIsTerminal = func(any) bool { return true }
	defer func() { filterIsTerminal = old }()
	out, stderr, code := runCmd(t, "source", "ai", "Transform")
	if code != 2 || out != "" || !strings.Contains(stderr, "cat draft.md | nn ai") {
		t.Fatalf("code=%d out=%q stderr=%q", code, out, stderr)
	}
	out, stderr, code = runCmd(t, "", "ai", "--help")
	if code != 0 || !strings.Contains(out, "transform piped text") || !strings.Contains(out, "filter.prompt_file") {
		t.Fatalf("code=%d out=%q stderr=%q", code, out, stderr)
	}
	f.uncalled(t)
}

func TestFilterDirectoryInputDoesNotPanic(t *testing.T) {
	f := newFilterFixture(t)
	r, err := os.Open(f.dir)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	var out, stderr bytes.Buffer
	code := runText(context.Background(), []string{"ai", "Transform"}, r, &out, &stderr)
	if code != 2 || out.Len() != 0 || !strings.Contains(stderr.String(), "read stdin") {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, out.String(), stderr.String())
	}
	f.uncalled(t)
}

func TestFilterInheritedStdinProcess(t *testing.T) {
	mode := os.Getenv("NN_FILTER_CHILD")
	if mode == "" {
		return
	}
	if err := os.Setenv("NN_CONFIG", os.Getenv("NN_FILTER_CHILD_CONFIG")); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if mode == "cancel" {
		time.AfterFunc(100*time.Millisecond, cancel)
	}
	os.Exit(runText(ctx, []string{"ai", "Transform"}, os.Stdin, os.Stdout, os.Stderr))
}

func TestFilterInheritedFDZeroCancellation(t *testing.T) {
	for _, mode := range []string{"cancel", "deadline"} {
		t.Run(mode, func(t *testing.T) {
			f := newFilterFixture(t)
			if mode == "deadline" {
				writeConfig(t, strings.Replace(f.cfg, "[ai]\n", "[ai]\ntimeout=\"100ms\"\n", 1))
			}
			r, w, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			defer r.Close()
			defer w.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestFilterInheritedStdinProcess$")
			cmd.Env = append(os.Environ(), "NN_FILTER_CHILD="+mode, "NN_FILTER_CHILD_CONFIG="+os.Getenv("NN_CONFIG"))
			cmd.Stdin = r
			var out, stderr bytes.Buffer
			cmd.Stdout = &out
			cmd.Stderr = &stderr
			err = cmd.Run()
			want := 130
			if mode == "deadline" {
				want = 2
			}
			if err == nil || ctx.Err() != nil || cmd.ProcessState.ExitCode() != want || out.Len() != 0 {
				t.Fatalf("err=%v code=%d stdout=%q stderr=%q", err, cmd.ProcessState.ExitCode(), out.String(), stderr.String())
			}
			f.uncalled(t)
		})
	}
}
