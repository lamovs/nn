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
	"github.com/lamovs/nn/internal/vault"
)

func newLastFixture(t *testing.T) filterFixture {
	t.Helper()
	f := newFilterFixture(t)
	f.cfg = strings.ReplaceAll(f.cfg, "[ai.tasks.filter]", "[ai.tasks.last]")
	writeConfig(t, f.cfg)
	f.write(t, "answer", `{"title":"Command analysis","tags":["shell"],"body":"Explanation\n"}`)
	return f
}
func lastRequest(t *testing.T, f filterFixture) lastPayload {
	t.Helper()
	request := f.read(t, "request")
	at := strings.LastIndex(request, "\n\n{")
	if at < 0 {
		t.Fatal("missing JSON")
	}
	var result lastPayload
	if err := json.Unmarshal([]byte(request[at+2:]), &result); err != nil {
		t.Fatal(err)
	}
	return result
}
func TestLastReadOnlyLiteralPayload(t *testing.T) {
	for _, supplied := range []bool{false, true} {
		t.Run(map[bool]string{false: "absent", true: "empty"}[supplied], func(t *testing.T) {
			f := newLastFixture(t)
			t.Setenv("NN_ROOT", filepath.Join(f.dir, "missing-root"))
			watched := []string{os.Getenv("NN_CONFIG"), os.Getenv("NN_ROOT"), os.Getenv("XDG_DATA_HOME"), os.Getenv("XDG_STATE_HOME"), os.Getenv("XDG_CACHE_HOME")}
			before := make([]map[string]string, len(watched))
			for i, p := range watched {
				before[i] = askTree(t, p)
			}
			command := "  printf '%s' '$(touch NEVER)'\n# literal\t\r\n "
			args := []string{"last"}
			if supplied {
				f.write(t, "PRIVATE_FILENAME", "")
				args = append(args, "--output", filepath.Join(f.dir, "PRIVATE_FILENAME"))
			}
			out, errout, code := runCmd(t, command, args...)
			if code != 0 || out != "Explanation\n" {
				t.Fatalf("code=%d out=%q err=%q", code, out, errout)
			}
			p := lastRequest(t, f)
			if p.Command != command || p.Output != "" || p.OutputProvided != supplied {
				t.Fatalf("payload=%+v", p)
			}
			if strings.Contains(f.read(t, "request"), "PRIVATE_FILENAME") || f.read(t, "calls") != "call\n" {
				t.Fatal("wrong disclosure or call count")
			}
			for i, p := range watched {
				if !reflect.DeepEqual(before[i], askTree(t, p)) {
					t.Errorf("modified %s", p)
				}
			}
		})
	}
}
func TestLastInvalidInput(t *testing.T) {
	for _, tc := range []struct {
		name, input, want string
		args              []string
	}{
		{"empty", "", "nonblank", nil}, {"blank", " \n", "nonblank", nil}, {"utf8", string([]byte{255}), "UTF-8", nil}, {"nul", "a\x00b", "NUL", nil},
		{"oversize", strings.Repeat("x", ai.MaxTextBytes+1), "exceeds", nil}, {"serialized", strings.Repeat("\n", ai.MaxTextBytes/2) + "x", "exceeds", nil},
		{"positional", "cmd", "only on stdin", []string{"instruction"}}, {"mode", "cmd", "unknown option", []string{"--ai-mode=background"}},
		{"missing output", "cmd", "needs a file", []string{"--output"}}, {"empty profile", "cmd", "needs a value", []string{"--ai="}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newLastFixture(t)
			out, stderr, code := runCmd(t, tc.input, append([]string{"last"}, tc.args...)...)
			if code != 2 || out != "" || !strings.Contains(stderr, tc.want) {
				t.Fatalf("%d %q %q", code, out, stderr)
			}
			f.uncalled(t)
		})
	}
}
func TestLastOutputValidation(t *testing.T) {
	for _, kind := range []string{"fifo", "directory", "device", "missing", "invalid", "nul", "oversize", "combined", "symlink"} {
		t.Run(kind, func(t *testing.T) {
			f := newLastFixture(t)
			path := filepath.Join(f.dir, "output")
			command := "echo safe"
			switch kind {
			case "fifo":
				if err := syscall.Mkfifo(path, 0600); err != nil {
					t.Fatal(err)
				}
			case "directory":
				if err := os.Mkdir(path, 0700); err != nil {
					t.Fatal(err)
				}
			case "device":
				path = "/dev/null"
			case "invalid":
				f.write(t, "output", string([]byte{255}))
			case "nul":
				f.write(t, "output", "a\x00")
			case "oversize":
				f.write(t, "output", strings.Repeat("x", ai.MaxTextBytes+1))
			case "combined":
				command = strings.Repeat("x", ai.MaxTextBytes/2)
				f.write(t, "output", strings.Repeat("x", ai.MaxTextBytes/2))
			case "symlink":
				f.write(t, "target", "error\n")
				if err := os.Symlink(filepath.Join(f.dir, "target"), path); err != nil {
					t.Fatal(err)
				}
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			var out, stderr bytes.Buffer
			code := runText(ctx, []string{"last", "--output", path}, strings.NewReader(command), &out, &stderr)
			if kind == "symlink" {
				if code != 0 || lastRequest(t, f).Output != "error\n" {
					t.Fatalf("%d %s", code, &stderr)
				}
				return
			}
			if code != 2 || out.Len() != 0 || ctx.Err() != nil {
				t.Fatalf("%d %q %q context=%v", code, &out, &stderr, ctx.Err())
			}
			f.uncalled(t)
		})
	}
}
func TestLastSecretsAndConsent(t *testing.T) {
	// Fixture is split so secret scanners do not flag it.
	secret := "gh" + "p_A1b2C3d4E5f6G7h8I9j0K1l2M3n4O5p6Q7r8"
	for _, tc := range []struct {
		name          string
		allow, output bool
		consent       string
		want          int
	}{
		{"command", false, false, "always", 2}, {"output", false, true, "always", 2}, {"allow", true, false, "always", 0}, {"never", true, false, "never", 2}, {"ask", true, false, "ask", 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newLastFixture(t)
			writeConfig(t, strings.Replace(f.cfg, "task-choice=\"always\"", "task-choice=\""+tc.consent+"\"", 1))
			input := secret
			args := []string{"last"}
			if tc.output {
				input = "echo harmless"
				f.write(t, "output", secret)
				args = append(args, "--output", filepath.Join(f.dir, "output"))
			}
			if tc.allow {
				args = append(args, "--allow-secret")
			}
			out, stderr, code := runCmd(t, input, args...)
			if code != tc.want || strings.Contains(stderr, secret) {
				t.Fatalf("%d %q %q", code, out, stderr)
			}
			if code != 0 {
				f.uncalled(t)
				if out != "" {
					t.Fatal("unexpected output")
				}
			}
		})
	}
}
func TestLastProfilesAndPrompt(t *testing.T) {
	for _, tc := range []struct {
		name, env, want string
		args            []string
	}{
		{"task", "", "task-choice\nbase-model\nlow\n", nil}, {"env", "env-choice", "env-choice\nbase-model\nlow\n", nil},
		{"flag", "env-choice", "flag-choice\ncustom\nhigh\n", []string{"--ai=flag-choice", "--model", "custom", "--effort=high"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newLastFixture(t)
			t.Setenv("NN_AI", tc.env)
			f.write(t, "prompt", "CUSTOM LAST PROMPT")
			writeConfig(t, strings.Replace(f.cfg, "[ai.tasks.last]\n", "[ai.tasks.last]\nprompt_file="+config.TOMLString(filepath.Join(f.dir, "prompt"))+"\n", 1))
			_, stderr, code := runCmd(t, "echo x", append([]string{"last"}, tc.args...)...)
			if code != 0 || f.read(t, "args") != tc.want || !strings.HasPrefix(f.read(t, "request"), "CUSTOM LAST PROMPT\n\n") {
				t.Fatalf("%d %s", code, stderr)
			}
		})
	}
}
func TestLastTimeoutCancellationAndBadReply(t *testing.T) {
	t.Run("stdin deadline", func(t *testing.T) {
		f := newLastFixture(t)
		r, w := io.Pipe()
		defer w.Close()
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
		defer cancel()
		var out, stderr bytes.Buffer
		if code := runText(ctx, []string{"last"}, r, &out, &stderr); code != 2 || out.Len() != 0 || !strings.Contains(stderr.String(), "timed out") {
			t.Fatalf("%d %s", code, &stderr)
		}
		f.uncalled(t)
	})
	t.Run("cancel", func(t *testing.T) {
		f := newLastFixture(t)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		var out, stderr bytes.Buffer
		if code := runText(ctx, []string{"last"}, strings.NewReader("echo x"), &out, &stderr); code != 130 || out.Len() != 0 {
			t.Fatalf("%d %s", code, &stderr)
		}
		f.uncalled(t)
	})
	t.Run("model deadline", func(t *testing.T) {
		f := newLastFixture(t)
		f.write(t, "wait", "")
		ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
		defer cancel()
		var out, stderr bytes.Buffer
		if code := runText(ctx, []string{"last"}, strings.NewReader("echo x"), &out, &stderr); code != 2 || out.Len() != 0 || !strings.Contains(stderr.String(), "timed out") {
			t.Fatalf("%d %s", code, &stderr)
		}
	})
	t.Run("bad reply", func(t *testing.T) {
		f := newLastFixture(t)
		f.write(t, "answer", `{"body":"incomplete"}`)
		out, stderr, code := runCmd(t, "echo x", "last")
		if code != 2 || out != "" {
			t.Fatalf("%d %q %q", code, out, stderr)
		}
	})
}
func TestLastSaveOneNoteAndFailure(t *testing.T) {
	for _, kind := range []string{"success", "failure", "missing root"} {
		t.Run(kind, func(t *testing.T) {
			f := newLastFixture(t)
			root := filepath.Join(f.dir, "vault")
			t.Setenv("NN_ROOT", root)
			if kind != "missing root" {
				if err := os.Mkdir(root, 0700); err != nil {
					t.Fatal(err)
				}
			}
			cfg := "[vault]\ninbox=\"inbox\"\n" + strings.Replace(f.cfg, `post_save="touch MUST_NOT_RUN"`, `post_save="printf 'hook\\n' >> \"$NN_FILTER_FAKE/hooks\""`, 1)
			writeConfig(t, cfg)
			if kind == "failure" {
				if err := os.WriteFile(filepath.Join(root, "inbox"), []byte("blocked"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			command := "echo '```'\n\n"
			rawOutput := "````\r\nraw\t\n"
			f.write(t, "output", rawOutput)
			out, stderr, code := runCmd(t, command, "last", "--save", "--output", filepath.Join(f.dir, "output"))
			if kind == "missing root" {
				if code != 2 || out != "" {
					t.Fatalf("%d %q %q", code, out, stderr)
				}
				f.uncalled(t)
				return
			}
			if out != "Explanation\n" || f.read(t, "calls") != "call\n" {
				t.Fatalf("%d %q %q", code, out, stderr)
			}
			if kind == "failure" {
				if code != 2 || !strings.Contains(stderr, "save analysis") {
					t.Fatalf("%d %q", code, stderr)
				}
				return
			}
			if code != 0 {
				t.Fatal(stderr)
			}
			files, err := filepath.Glob(filepath.Join(root, "inbox", "*.md"))
			if err != nil || len(files) != 1 {
				t.Fatalf("files %v err %v", files, err)
			}
			body, err := os.ReadFile(files[0])
			if err != nil {
				t.Fatal(err)
			}
			text := string(body)
			for _, want := range []string{"via: stdin", "#shell", lastFence(command, "sh"), lastFence(rawOutput, "text"), "Explanation"} {
				if !strings.Contains(text, want) {
					t.Errorf("missing %q in %q", want, text)
				}
			}
			if strings.Contains(text, "where:") || strings.Contains(text, "repo:") {
				t.Fatal("inferred provenance")
			}
			if f.read(t, "hooks") != "hook\n" {
				t.Fatal("hook not exactly once")
			}
		})
	}
}

func TestLastZshTransport(t *testing.T) {
	zsh, err := exec.LookPath("zsh")
	if err != nil {
		t.Skip("zsh is unavailable")
	}
	for _, kind := range []string{"ai", "legacy", "options require ai"} {
		t.Run(kind, func(t *testing.T) {
			dir := t.TempDir()
			integration := filepath.Join(dir, "nn.zsh")
			if err := os.WriteFile(integration, []byte(zshIntegration), 0600); err != nil {
				t.Fatal(err)
			}
			script := `compdef() { :; }
source "$1"
HISTFILE="$2/history"
HISTSIZE=100
SAVEHIST=0
nn() {
  builtin printf '%s\0' "$@" > "$2_ROOT/args"
  if [[ $1 == last ]]; then /bin/cat > "$2_ROOT/input"; fi
}
`
			script = strings.ReplaceAll(script, "$2_ROOT", "$FIXTURE")
			command := "printf '%s' 'literal'; touch \"$FIXTURE/NEVER\"\n# literal `noexec`; echo \"$HOME\""
			invocation := "nn-last --ai=chosen --model 'custom model' --output 'error file.log' --save"
			if kind == "ai" {
				command += "\t literal \\n\n\n"
			}
			if kind == "legacy" {
				invocation = "nn-last"
				command = "printf '%s' 'literal'; touch \"$FIXTURE/NEVER\""
			}
			if kind == "options require ai" {
				invocation = "nn-last --save"
			}
			cmd := exec.Command(zsh, "-df", "-c", script+"print -rs -- \"$3\"\n"+invocation+"\n", "test", integration, dir, command)
			if kind == "ai" {
				setup := strings.NewReplacer(`source "$1"`, `source "$FIXTURE/nn.zsh"`, `HISTFILE="$2/history"`, `HISTFILE="$FIXTURE/history"`).Replace(script)
				setup += "print -rs -- \"$SOURCE_LITERAL\"\n"
				if err := os.WriteFile(filepath.Join(dir, "setup"), []byte(setup), 0600); err != nil {
					t.Fatal(err)
				}
				cmd = exec.Command(zsh, "-dfi")
				cmd.Stdin = strings.NewReader("source \"$FIXTURE/setup\"\n" + invocation + "\nexit\n")
			}
			cmd.Env = append(os.Environ(), "HOME="+dir, "ZDOTDIR="+dir, "FIXTURE="+dir, "HISTFILE="+filepath.Join(dir, "history"), "SOURCE_LITERAL="+command)
			output, err := cmd.CombinedOutput()
			if kind == "options require ai" {
				if err == nil || !strings.Contains(string(output), "require --ai") {
					t.Fatalf("%v %s", err, output)
				}
				if _, err := os.Stat(filepath.Join(dir, "args")); !os.IsNotExist(err) {
					t.Fatal("called nn")
				}
				return
			}
			if err != nil {
				t.Fatalf("%v %s", err, output)
			}
			args, err := os.ReadFile(filepath.Join(dir, "args"))
			if err != nil {
				t.Fatal(err)
			}
			if kind == "legacy" {
				if string(args) != "add\x00"+command+"\x00--code=sh\x00" {
					t.Fatalf("args %q", args)
				}
				return
			}
			if string(args) != "last\x00--ai=chosen\x00--model\x00custom model\x00--output\x00error file.log\x00--save\x00" {
				t.Fatalf("args %q", args)
			}
			got, err := os.ReadFile(filepath.Join(dir, "input"))
			if err != nil || string(got) != command {
				t.Fatalf("input %q err %v", got, err)
			}
			if _, err := os.Stat(filepath.Join(dir, "NEVER")); !os.IsNotExist(err) {
				t.Fatal("executed command")
			}
		})
	}
}

func TestLastSaveUnclosedAnalysisFences(t *testing.T) {
	for _, fence := range []string{"```sh", "~~~~"} {
		t.Run(fence, func(t *testing.T) {
			f := newLastFixture(t)
			root := filepath.Join(f.dir, "vault")
			if err := os.Mkdir(root, 0700); err != nil {
				t.Fatal(err)
			}
			t.Setenv("NN_ROOT", root)
			answer, _ := json.Marshal(map[string]any{"title": "Analysis", "tags": []string{"shell"}, "body": "Explanation\n" + fence + "\nunclosed"})
			f.write(t, "answer", string(answer))
			command := "echo safe\n```\n#inside-command"
			output := "~~~~\n#inside-output"
			f.write(t, "output", output)
			_, stderr, code := runCmd(t, command, "last", "--save", "--output", filepath.Join(f.dir, "output"))
			if code != 0 {
				t.Fatal(stderr)
			}
			cfg, _, err := config.Load()
			if err != nil {
				t.Fatal(err)
			}
			v, err := vault.Open(cfg)
			if err != nil {
				t.Fatal(err)
			}
			notes, err := v.LoadAll(context.Background())
			if err != nil || len(notes) != 1 {
				t.Fatalf("%v %v", notes, err)
			}
			body := notes[0].Body
			masked := vault.MaskCode(body)
			if strings.Contains(masked, "#inside-command") || strings.Contains(masked, "#inside-output") || !strings.Contains(masked, "## Analysis") || !strings.Contains(masked, "#shell") {
				t.Fatalf("bad boundaries %q", masked)
			}
			if !strings.Contains(body, lastFence(command, "sh")) || !strings.Contains(body, lastFence(output, "text")) {
				t.Fatal("source bytes changed")
			}
		})
	}
}

func TestLastZshInteractivePreviousEntry(t *testing.T) {
	zsh, err := exec.LookPath("zsh")
	if err != nil {
		t.Skip("zsh unavailable")
	}
	dir := t.TempDir()
	fragment := filepath.Join(dir, "fragment")
	if err := os.WriteFile(fragment, []byte(zshIntegration), 0600); err != nil {
		t.Fatal(err)
	}
	setup := `HISTFILE="$FIXTURE/history"
HISTSIZE=100
SAVEHIST=0
PS1=''
PS2=''
compdef() { :; }
source "$FIXTURE/fragment"
nn() { builtin printf '%s\0' "$@" > "$FIXTURE/args"; /bin/cat > "$FIXTURE/input"; }
`
	if err := os.WriteFile(filepath.Join(dir, "setup"), []byte(setup), 0600); err != nil {
		t.Fatal(err)
	}
	command := ": 'literal $HOME $(touch NEVER) `date`\nsecond line'"
	shell := exec.Command(zsh, "-dfi")
	shell.Env = append(os.Environ(), "HOME="+dir, "ZDOTDIR="+dir, "HISTFILE="+filepath.Join(dir, "history"), "FIXTURE="+dir)
	shell.Stdin = strings.NewReader("source \"$FIXTURE/setup\"\n" + command + "\nnn-last --ai\nexit\n")
	combined, err := shell.CombinedOutput()
	if err != nil {
		t.Fatalf("%v %s", err, combined)
	}
	got, err := os.ReadFile(filepath.Join(dir, "input"))
	if err != nil || string(got) != command {
		t.Fatalf("history input=%q want=%q err=%v shell=%s", got, command, err, combined)
	}
}

func TestLastProfileTimeoutAndRootlessLegacy(t *testing.T) {
	t.Run("profile timeout", func(t *testing.T) {
		f := newLastFixture(t)
		writeConfig(t, strings.Replace(f.cfg, "[ai]\n", "[ai]\ntimeout=\"50ms\"\n", 1))
		r, w := io.Pipe()
		defer w.Close()
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		var out, stderr bytes.Buffer
		code := runText(ctx, []string{"last"}, r, &out, &stderr)
		if code != 2 || out.Len() != 0 || ctx.Err() != nil || !strings.Contains(stderr.String(), "timed out") {
			t.Fatalf("%d %q %q", code, &out, &stderr)
		}
		f.uncalled(t)
	})
	t.Run("legacy root", func(t *testing.T) {
		f := newLastFixture(t)
		writeConfig(t, "root=\"/obsolete\"\n"+f.cfg)
		out, stderr, code := runCmd(t, "echo x", "last")
		if code != 0 || out != "Explanation\n" {
			t.Fatalf("%d %q %q", code, out, stderr)
		}
	})
}

func TestLastSavedMetadata(t *testing.T) {
	f := newLastFixture(t)
	root := filepath.Join(f.dir, "vault")
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("NN_ROOT", root)
	if err := os.WriteFile(filepath.Join(root, "old.md"), []byte("# Existing\n#Shell\n"), 0600); err != nil {
		t.Fatal(err)
	}
	f.write(t, "answer", `{"title":"###","tags":["shell","SHELL","new-tag"],"body":"Analysis"}`)
	_, stderr, code := runCmd(t, "echo x", "last", "--save")
	if code != 0 {
		t.Fatal(stderr)
	}
	cfg, _, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	v, err := vault.Open(cfg)
	if err != nil {
		t.Fatal(err)
	}
	notes, err := v.LoadAll(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, n := range notes {
		if n.Path == "old.md" {
			continue
		}
		found = true
		if n.Title != "Command analysis" || !strings.Contains(n.Body, "#Shell #new-tag") || strings.Contains(n.Body, "#shell") {
			t.Fatalf("metadata %q", n.Body)
		}
	}
	if !found {
		t.Fatal("no new note")
	}
	if f.read(t, "calls") != "call\n" {
		t.Fatal("recursive AI call")
	}
}

func TestLastZshHistoryIsolation(t *testing.T) {
	zsh, err := exec.LookPath("zsh")
	if err != nil {
		t.Skip("zsh unavailable")
	}
	for _, kind := range []string{"ignored invocation", "imported startup", "no local", "shared history"} {
		t.Run(kind, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, "fragment"), []byte(zshIntegration), 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, "history"), []byte(": 1900000000:0;: IMPORTED-EVENT\n"), 0600); err != nil {
				t.Fatal(err)
			}
			setup := `HISTFILE="$FIXTURE/history"
HISTSIZE=100
SAVEHIST=100
PS1=''
PS2=''
compdef() { :; }
source "$FIXTURE/fragment"
nn() { builtin printf '%s\0' "$@" > "$FIXTURE/args"; /bin/cat > "$FIXTURE/input"; }
`
			invocation := "nn-last --ai"
			switch kind {
			case "ignored invocation":
				setup += "setopt HIST_IGNORE_SPACE\n"
				invocation = " nn-last --ai"
			case "imported startup", "no local":
				setup += "builtin fc -R \"$HISTFILE\"\n"
			case "shared history":
				setup += `setopt SHARE_HISTORY
count=0
precmd() {
  (( ++count ))
  if (( count == 2 )); then
    print -r -- ': 2000000000:0;: REMOTE-EVENT' >> "$HISTFILE"
  fi
}
`
			}
			if err := os.WriteFile(filepath.Join(dir, "setup"), []byte(setup), 0600); err != nil {
				t.Fatal(err)
			}
			command := ": LOCAL-EVENT"
			shell := exec.Command(zsh, "-dfi")
			shell.Stdin = strings.NewReader("source \"$FIXTURE/setup\"\n" + command + "\n\n" + invocation + "\nexit\n")
			if kind == "no local" {
				shell = exec.Command(zsh, "-dfc", setup+"\nnn-last --ai\n")
			}
			shell.Env = append(os.Environ(), "HOME="+dir, "ZDOTDIR="+dir, "HISTFILE="+filepath.Join(dir, "history"), "FIXTURE="+dir)
			combined, err := shell.CombinedOutput()
			if kind == "no local" {
				if err == nil || !strings.Contains(string(combined), "no previous local command") {
					t.Fatalf("err=%v shell=%s", err, combined)
				}
				if _, err := os.Stat(filepath.Join(dir, "input")); !os.IsNotExist(err) {
					t.Fatal("imported-only history sent")
				}
				return
			}
			if err != nil {
				t.Fatalf("%v %s", err, combined)
			}
			got, err := os.ReadFile(filepath.Join(dir, "input"))
			if err != nil || string(got) != command {
				t.Fatalf("selected=%q expected=%q err=%v shell=%s", got, command, err, combined)
			}
		})
	}
}

func TestLastZshDoesNotExportSource(t *testing.T) {
	zsh, err := exec.LookPath("zsh")
	if err != nil {
		t.Skip("zsh unavailable")
	}
	for _, tc := range []struct {
		name, options string
		fail          bool
	}{
		{"exported global", "export cmd=OLD\n", false},
		{"allexport", "setopt ALL_EXPORT\n", false},
		{"both", "export cmd=OLD\nsetopt ALL_EXPORT\n", false},
		{"typeset unset", "export cmd=OLD\nsetopt ALL_EXPORT TYPESET_TO_UNSET\n", false},
		{"restore on failure", "export cmd=OLD\nsetopt ALL_EXPORT\n", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, "fragment"), []byte(zshIntegration), 0600); err != nil {
				t.Fatal(err)
			}
			setup := `HISTFILE="$FIXTURE/history"
HISTSIZE=100
SAVEHIST=0
PS1=''
PS2=''
compdef() { :; }
source "$FIXTURE/fragment"
nn() {
  /usr/bin/env > "$FIXTURE/environment"
  /bin/cat > "$FIXTURE/input"
  if [[ $2 == --fail ]]; then return 2; fi
}
` + tc.options
			if err := os.WriteFile(filepath.Join(dir, "setup"), []byte(setup), 0600); err != nil {
				t.Fatal(err)
			}
			command := ": 'unique-command-canary\nsource second line'"
			invocation := "nn-last --ai"
			if tc.fail {
				invocation = "nn-last --fail --ai"
			}
			shell := exec.Command(zsh, "-dfi")
			shell.Env = append(os.Environ(), "HOME="+dir, "ZDOTDIR="+dir, "HISTFILE="+filepath.Join(dir, "history"), "FIXTURE="+dir)
			shell.Stdin = strings.NewReader("source \"$FIXTURE/setup\"\nbuiltin setopt > \"$FIXTURE/options.before\"\n" + command + "\n" + invocation + "\n" + `print -r -- "$? $options[allexport] ${cmd-UNSET}" > "$FIXTURE/restored"` + "\nbuiltin setopt > \"$FIXTURE/options.after\"\nexit\n")
			combined, err := shell.CombinedOutput()
			if err != nil {
				t.Fatalf("%v %s", err, combined)
			}
			input, err := os.ReadFile(filepath.Join(dir, "input"))
			if err != nil || string(input) != command {
				t.Fatalf("input %q err %v shell %s", input, err, combined)
			}
			environment, err := os.ReadFile(filepath.Join(dir, "environment"))
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(environment), "unique-command-canary") || strings.Contains(string(environment), "source second line") {
				t.Fatal("source leaked in child environment")
			}
			restored, err := os.ReadFile(filepath.Join(dir, "restored"))
			if err != nil {
				t.Fatal(err)
			}
			want := "0 "
			if tc.fail {
				want = "2 "
			}
			if strings.Contains(tc.options, "ALL_EXPORT") {
				want += "on "
			} else {
				want += "off "
			}
			if strings.Contains(tc.options, "cmd=OLD") {
				want += "OLD\n"
			} else {
				want += "UNSET\n"
			}
			if string(restored) != want {
				t.Fatalf("restored %q want %q", restored, want)
			}
			before, err := os.ReadFile(filepath.Join(dir, "options.before"))
			if err != nil {
				t.Fatal(err)
			}
			after, err := os.ReadFile(filepath.Join(dir, "options.after"))
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(before, after) {
				t.Fatalf("shell options changed: before %q after %q", before, after)
			}
		})
	}
}
