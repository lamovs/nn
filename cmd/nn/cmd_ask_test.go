package main

import (
	"bytes"
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/lamovs/nn/internal/ai"
	"github.com/lamovs/nn/internal/config"
)

const askTestAnswer = `{"action":"answer","query":"","paragraphs":[{"text":"Use docker builder prune.","source_ids":["S1"]}],"missing":""}`

type askFixture struct {
	root, dir, cfg string
}

func newAskFixture(t *testing.T) askFixture {
	t.Helper()
	root := newTestVault(t)
	dir := t.TempDir()
	t.Setenv("NN_ASK_FAKE", dir)
	t.Setenv("NN_AI", "")
	t.Setenv("XDG_CACHE_HOME", filepath.Join(t.TempDir(), "cache"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(t.TempDir(), "state"))
	previous := askConsentAsker
	askConsentAsker = func(context.Context) ai.Asker { return &scriptedAsker{} }
	t.Cleanup(func() { askConsentAsker = previous })
	engine := filepath.Join(dir, "fake-model")
	script := `#!/bin/sh
n=0
if [ -f "$NN_ASK_FAKE/calls" ]; then read -r n < "$NN_ASK_FAKE/calls"; fi
n=$((n + 1))
printf '%s\n' "$n" > "$NN_ASK_FAKE/calls"
/bin/cat > "$NN_ASK_FAKE/request-$n"
printf '%s\n' "$@" > "$NN_ASK_FAKE/args-$n"
if [ -f "$NN_ASK_FAKE/wait" ]; then sleep 10; fi
if [ "$n" = 1 ] && [ -f "$NN_ASK_FAKE/first" ]; then
  /bin/cat "$NN_ASK_FAKE/first"
else
  /bin/cat "$NN_ASK_FAKE/answer"
fi
`
	if err := os.WriteFile(engine, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	cfg := "[ai]\nprofile=\"global-choice\"\nmode=\"background\"\n[ai.tasks.ask]\nprofile=\"task-choice\"\n"
	for _, name := range []string{"global-choice", "task-choice", "env-choice", "flag-choice"} {
		cfg += "[ai.profiles." + name + "]\nengine=\"command\"\nmodel=\"base-model\"\neffort=\"low\"\ncommand=[" + config.TOMLString(engine) + "," + config.TOMLString(name) + ",\"{model}\",\"{effort}\"]\n"
	}
	cfg += "[ai.consent]\nglobal-choice=\"always\"\ntask-choice=\"always\"\nenv-choice=\"always\"\nflag-choice=\"always\"\n[hooks]\npost_save=\"touch MUST_NOT_RUN\"\n"
	f := askFixture{root: root, dir: dir, cfg: cfg}
	f.write(t, "answer", askTestAnswer)
	writeConfig(t, cfg)
	writeNote(t, root, "notes/docker.md", "# Docker cache\n\nUse docker builder prune to clear the build cache.\n")
	writeNote(t, root, "notes/unrelated.md", "# Garden\n\nUNRELATED PRIVATE TEXT\n")
	return f
}

func (f askFixture) write(t *testing.T, name, text string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(f.dir, name), []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
}

func (f askFixture) read(t *testing.T, name string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(f.dir, name))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func askTree(t *testing.T, root string) map[string]string {
	t.Helper()
	tree := map[string]string{}
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if os.IsNotExist(err) {
			return nil
		}
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		if entry.IsDir() {
			tree[rel] = "directory"
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		tree[rel] = fmt.Sprintf("%v %d %s", info.Mode(), info.ModTime().UnixNano(), data)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return tree
}

func TestAskAnswersSynchronouslyWithoutMutations(t *testing.T) {
	f := newAskFixture(t)
	watched := []string{f.root, os.Getenv("XDG_DATA_HOME"), os.Getenv("XDG_CACHE_HOME"), os.Getenv("XDG_STATE_HOME")}
	before := make([]map[string]string, len(watched))
	for i, root := range watched {
		before[i] = askTree(t, root)
	}
	configBefore, _ := os.ReadFile(os.Getenv("NN_CONFIG"))
	stdout, stderr, code := runCmd(t, "STDIN MUST NOT BE SENT", "ask", "How", "did", "I", "clear", "Docker", "cache?")
	if code != 0 || !strings.Contains(stdout, "Use docker builder prune.") || !strings.Contains(stdout, "obsidian://open?") || !strings.Contains(stdout, "notes/docker.md") {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	request := f.read(t, "request-1")
	for _, forbidden := range []string{"UNRELATED PRIVATE TEXT", "STDIN MUST NOT BE SENT"} {
		if strings.Contains(request, forbidden) {
			t.Errorf("sent unrelated input %q", forbidden)
		}
	}
	if !strings.Contains(request, `"question":"How did I clear Docker cache?"`) || !strings.Contains(request, "docker builder prune") {
		t.Fatalf("missing question or evidence in request: %q", request)
	}
	if f.read(t, "calls") != "1\n" {
		t.Fatal("expected exactly one synchronous model call")
	}
	for i, root := range watched {
		if after := askTree(t, root); !reflect.DeepEqual(before[i], after) {
			t.Errorf("ask changed %s: before=%v after=%v", root, before[i], after)
		}
	}
	configAfter, _ := os.ReadFile(os.Getenv("NN_CONFIG"))
	if !bytes.Equal(configBefore, configAfter) {
		t.Fatal("ask changed config without consent change")
	}
}

func TestAskProfileAndOverrides(t *testing.T) {
	for _, tc := range []struct {
		name, env, want string
		args            []string
	}{
		{"task", "", "task-choice\nbase-model\nlow\n", nil},
		{"environment", "env-choice", "env-choice\nbase-model\nlow\n", nil},
		{"flag", "env-choice", "flag-choice\ncustom\nhigh\n", []string{"--ai=flag-choice", "--model", "custom", "--effort=high"}},
		{"bare ai", "", "task-choice\ncustom\nmax\n", []string{"--ai", "--model=custom", "--effort", "max"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newAskFixture(t)
			t.Setenv("NN_AI", tc.env)
			_, stderr, code := runCmd(t, "", append([]string{"ask", "Docker cache"}, tc.args...)...)
			if code != 0 || f.read(t, "args-1") != tc.want {
				t.Fatalf("code=%d args=%q stderr=%q", code, f.read(t, "args-1"), stderr)
			}
		})
	}
}

func TestAskPromptOverrideAndLiteralQuestion(t *testing.T) {
	f := newAskFixture(t)
	prompt := filepath.Join(f.dir, "prompt.md")
	if err := os.WriteFile(prompt, []byte("CUSTOM ASK PROMPT"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := strings.Replace(f.cfg, "[ai.tasks.ask]\n", "[ai.tasks.ask]\nprompt_file="+config.TOMLString(prompt)+"\n", 1)
	writeConfig(t, cfg)
	_, stderr, code := runCmd(t, "", "ask", "--", "-Docker cache", "--help")
	if code != 0 {
		t.Fatalf("code=%d stderr=%q", code, stderr)
	}
	request := f.read(t, "request-1")
	if !strings.HasPrefix(request, "CUSTOM ASK PROMPT\n\n") || !strings.Contains(request, `"question":"-Docker cache --help"`) {
		t.Fatalf("prompt override or -- lost: %q", request)
	}
}

func TestAskConsentOnceCoversFollowup(t *testing.T) {
	f := newAskFixture(t)
	writeConfig(t, strings.Replace(f.cfg, "task-choice=\"always\"", "task-choice=\"ask\"", 1))
	asker := &scriptedAsker{interactive: true, answers: []string{"once"}}
	askConsentAsker = func(context.Context) ai.Asker { return asker }
	f.write(t, "first", `{"action":"search","query":"prune","paragraphs":[],"missing":""}`)
	configBefore, _ := os.ReadFile(os.Getenv("NN_CONFIG"))
	stdout, stderr, code := runCmd(t, "", "ask", "Docker cache")
	if code != 0 || !strings.Contains(stdout, "docker builder prune") || f.read(t, "calls") != "2\n" || len(asker.questions) != 1 {
		t.Fatalf("code=%d stdout=%q stderr=%q questions=%v", code, stdout, stderr, asker.questions)
	}
	if !strings.Contains(asker.questions[0], "additional local searches") || !strings.Contains(asker.questions[0], "12000 characters") {
		t.Fatalf("consent omits followups or budget: %q", asker.questions[0])
	}
	configAfter, _ := os.ReadFile(os.Getenv("NN_CONFIG"))
	if !bytes.Equal(configBefore, configAfter) {
		t.Fatal("once consent persisted to config")
	}
}

func TestAskConsentRefusals(t *testing.T) {
	for _, tc := range []struct{ consent, want string }{{"never", "ai.consent.task-choice is never"}, {"ask", "AI needs consent"}} {
		t.Run(tc.consent, func(t *testing.T) {
			f := newAskFixture(t)
			writeConfig(t, strings.Replace(f.cfg, "task-choice=\"always\"", "task-choice=\""+tc.consent+"\"", 1))
			stdout, stderr, code := runCmd(t, "", "ask", "Docker cache")
			if code != 2 || stdout != "" || !strings.Contains(stderr, tc.want) {
				t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout, stderr)
			}
			if _, err := os.Stat(filepath.Join(f.dir, "calls")); !os.IsNotExist(err) {
				t.Fatal("model ran without consent")
			}
		})
	}
}

func TestAskLocalInsufficientSkipsConsentAndEngine(t *testing.T) {
	f := newAskFixture(t)
	writeConfig(t, f.cfg+"\n[ai.context]\nsearch_rounds=0\n")
	asker := &scriptedAsker{interactive: true, answers: []string{"once"}}
	askConsentAsker = func(context.Context) ai.Asker { return asker }
	stdout, stderr, code := runCmd(t, "", "ask", "zzunknownzz")
	if code != 1 || stdout == "" || stderr != "" || len(asker.questions) != 0 {
		t.Fatalf("code=%d stdout=%q stderr=%q questions=%v", code, stdout, stderr, asker.questions)
	}
	if _, err := os.Stat(filepath.Join(f.dir, "calls")); !os.IsNotExist(err) {
		t.Fatal("model ran for local insufficiency")
	}
}

func TestAskPartialAndInvalidAnswers(t *testing.T) {
	for _, tc := range []struct {
		name, reply string
		code        int
	}{
		{"partial", `{"action":"insufficient","query":"","paragraphs":[{"text":"Use docker builder prune.","source_ids":["S1"]}],"missing":"No date was recorded."}`, 1},
		{"unknown source", strings.Replace(askTestAnswer, "S1", "S999", 1), 2},
		{"plaintext", "Unsupported plaintext answer.", 2},
		{"raw link", strings.Replace(askTestAnswer, "Use docker builder prune.", "See https://example.com", 1), 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newAskFixture(t)
			f.write(t, "answer", tc.reply)
			stdout, stderr, code := runCmd(t, "", "ask", "Docker cache")
			if code != tc.code {
				t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout, stderr)
			}
			if tc.code == 1 {
				if !strings.Contains(stdout, "No date was recorded.") || !strings.Contains(stdout, "obsidian://open?") {
					t.Fatalf("partial answer missing explanation or sources: %q", stdout)
				}
			} else if tc.code == 2 && stdout != "" {
				t.Fatalf("invalid answer leaked to stdout: %q", stdout)
			} else if tc.code == 0 && (strings.Contains(stdout, "https://") || !strings.Contains(stdout, "obsidian://open?")) {
				t.Fatalf("model link not neutralized or source missing: %q", stdout)
			}
		})
	}
}

func TestAskInvalidFlagsAndMissingQuestion(t *testing.T) {
	for _, args := range [][]string{
		{"ask"}, {"ask", "  "}, {"ask", "Docker", "--ai-mode", "background"},
		{"ask", "Docker", "--no-ai"}, {"ask", "Docker", "--effort", "ultra"},
		{"ask", "Docker", "--model"}, {"ask", "Docker", "--ai="},
		{"ask", "Docker", "--model="}, {"ask", "Docker", "--ai=missing"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			f := newAskFixture(t)
			stdout, stderr, code := runCmd(t, "ignored input", args...)
			if code != 2 || stdout != "" || stderr == "" {
				t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout, stderr)
			}
			if _, err := os.Stat(filepath.Join(f.dir, "calls")); !os.IsNotExist(err) {
				t.Fatal("model ran for invalid input")
			}
		})
	}
}

func TestAskCancellation(t *testing.T) {
	newAskFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var stdout, stderr bytes.Buffer
	code := runText(ctx, []string{"ask", "Docker cache"}, strings.NewReader(""), &stdout, &stderr)
	if code != 130 || stdout.Len() != 0 {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
}

func TestAskHelpAndIndex(t *testing.T) {
	newTestVault(t)
	stdout, stderr, code := runCmd(t, "", "ask", "--help")
	if code != 0 || !strings.Contains(stdout, "nn ask") || !strings.Contains(stdout, "--effort") || !strings.Contains(stdout, "search_rounds") {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	stdout, _, _ = runCmd(t, "")
	if !strings.Contains(stdout, "ask") {
		t.Fatal("ask absent from command index")
	}
}

func TestAskTimeoutLeavesStdoutEmpty(t *testing.T) {
	f := newAskFixture(t)
	f.write(t, "wait", "yes")
	writeConfig(t, strings.Replace(f.cfg, "[ai.profiles.task-choice]\n", "[ai.profiles.task-choice]\ntimeout=\"50ms\"\n", 1))
	stdout, stderr, code := runCmd(t, "", "ask", "Docker cache")
	if code != 2 || stdout != "" || stderr == "" {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
}
