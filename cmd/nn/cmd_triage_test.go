package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/lamovs/nn/internal/ai"
	"github.com/lamovs/nn/internal/app"
	"github.com/lamovs/nn/internal/config"
	"github.com/lamovs/nn/internal/triage"
)

const triageTestAnswer = `{"action":"propose","query":"","proposals":[{"note_id":"N1","title":"","tags":["shell"],"links":[],"topic":"Shell workflows","reason":"The inbox note captures a shell workflow."}]}`
const triageTestOriginal = "Original inbox text about shell workflows.\n"

func newTriageFixture(t *testing.T) askFixture {
	t.Helper()
	f := newAskFixture(t)
	f.cfg = strings.ReplaceAll(f.cfg, "[ai.tasks.ask]", "[ai.tasks.triage]")
	f.cfg = strings.Replace(f.cfg, `post_save="touch MUST_NOT_RUN"`, `post_save=`+config.TOMLString(`printf '%s\n' "$NN_NOTE" >> "$NN_ASK_FAKE/hooks"`), 1)
	writeConfig(t, f.cfg)
	f.write(t, "answer", triageTestAnswer)
	writeNote(t, f.root, "nn/inbox.md", triageTestOriginal)
	withTriageAsker(t, &scriptedAsker{})
	return f
}

func withTriageAsker(t *testing.T, asker ai.Asker) {
	t.Helper()
	previous := triageAsker
	triageAsker = func(context.Context) ai.Asker { return asker }
	t.Cleanup(func() { triageAsker = previous })
}

type triageTestAsker struct {
	interactive bool
	ask         func(string, int) (string, error)
	questions   []string
}

func (a *triageTestAsker) Interactive() bool { return a.interactive }
func (a *triageTestAsker) Ask(question string) (string, error) {
	a.questions = append(a.questions, question)
	return a.ask(question, len(a.questions))
}

func triageUncalled(t *testing.T, f askFixture) {
	t.Helper()
	if _, err := os.Stat(filepath.Join(f.dir, "calls")); !os.IsNotExist(err) {
		t.Fatal("model unexpectedly called")
	}
}

func triageNoRecovery(t *testing.T) {
	t.Helper()
	if _, err := os.Stat(filepath.Join(os.Getenv("XDG_DATA_HOME"), "nn", "triage")); !os.IsNotExist(err) {
		t.Fatalf("unexpected recovery artifacts: %v", err)
	}
}

func triageUnchanged(t *testing.T, f askFixture, want string) {
	t.Helper()
	got, err := os.ReadFile(filepath.Join(f.root, "nn/inbox.md"))
	if err != nil || string(got) != want {
		t.Fatalf("note changed: %q err=%v", got, err)
	}
	if _, err := os.Stat(filepath.Join(f.dir, "hooks")); !os.IsNotExist(err) {
		t.Fatal("hook ran without a note change")
	}
}

func TestTriageReadOnlyPlan(t *testing.T) {
	f := newTriageFixture(t)
	watched := []string{f.root, os.Getenv("NN_CONFIG"), os.Getenv("XDG_DATA_HOME"), os.Getenv("XDG_CACHE_HOME"), os.Getenv("XDG_STATE_HOME")}
	before := make([]map[string]string, len(watched))
	for i, path := range watched {
		before[i] = askTree(t, path)
	}
	out, stderr, code := runCmd(t, "STDIN MUST NOT BE SENT", "triage")
	if code != 0 || !strings.Contains(out, "nn/inbox.md") || !strings.Contains(out, "[1] tag") || !strings.Contains(out, "#shell") {
		t.Fatalf("code=%d out=%q stderr=%q", code, out, stderr)
	}
	if f.read(t, "calls") != "1\n" || strings.Contains(f.read(t, "request-1"), "STDIN MUST NOT BE SENT") {
		t.Fatal("wrong model call or stdin disclosure")
	}
	for i, path := range watched {
		if !reflect.DeepEqual(before[i], askTree(t, path)) {
			t.Errorf("read-only triage changed %s", path)
		}
	}
	triageUnchanged(t, f, triageTestOriginal)
	triageNoRecovery(t)
}

func TestTriageRefusesUnattendedApplyBeforeModel(t *testing.T) {
	f := newTriageFixture(t)
	out, stderr, code := runCmd(t, "", "triage", "--apply")
	if code != 2 || out != "" || !strings.Contains(stderr, "interactive stdin and stderr") {
		t.Fatalf("code=%d out=%q stderr=%q", code, out, stderr)
	}
	triageUncalled(t, f)
	triageUnchanged(t, f, triageTestOriginal)
	triageNoRecovery(t)
}

func TestTriageDeclineAndInvalidSelection(t *testing.T) {
	for _, tc := range []struct {
		name    string
		answers []string
		want    int
		preview bool
	}{
		{"empty", []string{""}, 0, false},
		{"none", []string{"none"}, 0, false},
		{"unknown id", []string{"999"}, 2, false},
		{"implicit yes is not all", []string{"yes"}, 2, false},
		{"negative confirmation", []string{"all", "no"}, 0, true},
		{"empty confirmation", []string{"1", ""}, 0, true},
		{"bad confirmation", []string{"1", "maybe"}, 2, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newTriageFixture(t)
			asker := &scriptedAsker{interactive: true, answers: tc.answers}
			withTriageAsker(t, asker)
			out, stderr, code := runCmd(t, "", "triage", "--apply")
			if code != tc.want || out != "" || strings.Contains(stderr, "+#shell") != tc.preview {
				t.Fatalf("code=%d out=%q stderr=%q", code, out, stderr)
			}
			if len(asker.questions) != len(tc.answers) || f.read(t, "calls") != "1\n" {
				t.Fatal("unexpected question or model count")
			}
			triageUnchanged(t, f, triageTestOriginal)
			triageNoRecovery(t)
		})
	}
}

func TestTriageApplyShowsExactPreviewAndRunsOneHook(t *testing.T) {
	f := newTriageFixture(t)
	var out, stderr bytes.Buffer
	withTriageAsker(t, &triageTestAsker{interactive: true, ask: func(question string, round int) (string, error) {
		if round == 1 {
			if !strings.Contains(stderr.String(), "[1] tag") || out.Len() != 0 {
				t.Fatal("action selection was hidden behind redirected stdout")
			}
			return "1", nil
		}
		if round != 2 || !strings.Contains(question, "[y/N]") || !strings.Contains(stderr.String(), "--- \"nn/inbox.md\"") || !strings.Contains(stderr.String(), "+#shell") {
			t.Fatalf("confirmation did not follow exact diff: question=%q stderr=%q", question, &stderr)
		}
		triageUnchanged(t, f, triageTestOriginal)
		triageNoRecovery(t)
		return "yes", nil
	}})
	code := runText(context.Background(), []string{"triage", "--apply"}, strings.NewReader(""), &out, &stderr)
	if code != 0 || !strings.Contains(out.String(), `applied "nn/inbox.md" [1]`) || !strings.Contains(stderr.String(), "Recovery directory:") {
		t.Fatalf("code=%d out=%q stderr=%q", code, &out, &stderr)
	}
	body, err := os.ReadFile(filepath.Join(f.root, "nn/inbox.md"))
	if err != nil || !bytes.HasPrefix(body, []byte(triageTestOriginal)) || strings.Count(string(body), "#shell") != 1 {
		t.Fatalf("saved body=%q err=%v", body, err)
	}
	if f.read(t, "calls") != "1\n" || strings.Count(f.read(t, "hooks"), "\n") != 1 {
		t.Fatal("model or hook repeated")
	}
	runs, err := os.ReadDir(filepath.Join(os.Getenv("XDG_DATA_HOME"), "nn", "triage"))
	if err != nil || len(runs) != 1 {
		t.Fatalf("recovery runs=%v err=%v", runs, err)
	}
	// A replay must not re-plan or re-apply the same already-applied addition.
	withTriageAsker(t, &scriptedAsker{interactive: true})
	_, stderr2, code2 := runCmd(t, "", "triage", "--apply")
	if code2 != 0 || !strings.Contains(stderr2, "No applicable additions") || strings.Count(f.read(t, "hooks"), "\n") != 1 {
		t.Fatalf("replay code=%d stderr=%q", code2, stderr2)
	}
	runs2, _ := os.ReadDir(filepath.Join(os.Getenv("XDG_DATA_HOME"), "nn", "triage"))
	if len(runs2) != len(runs) {
		t.Fatal("no-op planning created another recovery run")
	}
}

func TestTriageStaleConfirmationPreservesNewUserBytes(t *testing.T) {
	f := newTriageFixture(t)
	newBody := triageTestOriginal + "User edit after preview.\n"
	withTriageAsker(t, &triageTestAsker{interactive: true, ask: func(_ string, round int) (string, error) {
		if round == 1 {
			return "1", nil
		}
		if err := os.WriteFile(filepath.Join(f.root, "nn/inbox.md"), []byte(newBody), 0600); err != nil {
			t.Fatal(err)
		}
		return "yes", nil
	}})
	out, stderr, code := runCmd(t, "", "triage", "--apply")
	if code != 2 || !strings.Contains(out, "conflict") || !strings.Contains(stderr, "changed since preview") {
		t.Fatalf("code=%d out=%q stderr=%q", code, out, stderr)
	}
	triageUnchanged(t, f, newBody)
	triageNoRecovery(t)
}

func TestTriageEmptyAndNoActions(t *testing.T) {
	for _, kind := range []string{"empty inbox", "no proposals", "existing additions"} {
		t.Run(kind, func(t *testing.T) {
			f := newTriageFixture(t)
			withTriageAsker(t, &scriptedAsker{interactive: true})
			switch kind {
			case "empty inbox":
				if err := os.Remove(filepath.Join(f.root, "nn/inbox.md")); err != nil {
					t.Fatal(err)
				}
			case "no proposals":
				f.write(t, "answer", `{"action":"propose","query":"","proposals":[]}`)
			case "existing additions":
				writeNote(t, f.root, "nn/inbox.md", triageTestOriginal+"\n#shell\n")
			}
			before := askTree(t, f.root)
			out, stderr, code := runCmd(t, "", "triage", "--apply")
			if code != 0 || !reflect.DeepEqual(before, askTree(t, f.root)) {
				t.Fatalf("code=%d out=%q stderr=%q", code, out, stderr)
			}
			if kind == "empty inbox" {
				triageUncalled(t, f)
				if !strings.Contains(out, "No inbox notes") {
					t.Fatal("missing empty inbox result")
				}
			} else if !strings.Contains(stderr, "No applicable additions") {
				t.Fatal("missing no-op result")
			}
			triageNoRecovery(t)
			if _, err := os.Stat(filepath.Join(f.dir, "hooks")); !os.IsNotExist(err) {
				t.Fatal("no-op ran a hook")
			}
		})
	}
}

func TestTriageConsentProfilesAndInvalidInput(t *testing.T) {
	t.Run("once covers searches but not application", func(t *testing.T) {
		f := newTriageFixture(t)
		writeConfig(t, strings.Replace(f.cfg, `task-choice="always"`, `task-choice="ask"`, 1))
		f.write(t, "first", `{"action":"search","query":"docker cache","proposals":[]}`)
		asker := &scriptedAsker{interactive: true, answers: []string{"once", "1", "no"}}
		withTriageAsker(t, asker)
		configBefore, _ := os.ReadFile(os.Getenv("NN_CONFIG"))
		out, stderr, code := runCmd(t, "", "triage", "--apply")
		if code != 0 || out != "" || f.read(t, "calls") != "2\n" || len(asker.questions) != 3 {
			t.Fatalf("code=%d out=%q stderr=%q questions=%v", code, out, stderr, asker.questions)
		}
		if !strings.Contains(asker.questions[0], "metadata") || !strings.Contains(asker.questions[0], "related") || !strings.Contains(asker.questions[1], "Select action IDs") || !strings.Contains(asker.questions[2], "[y/N]") {
			t.Fatalf("consent gates conflated: %v", asker.questions)
		}
		configAfter, _ := os.ReadFile(os.Getenv("NN_CONFIG"))
		if !bytes.Equal(configBefore, configAfter) {
			t.Fatal("once persisted engine consent")
		}
		triageUnchanged(t, f, triageTestOriginal)
		triageNoRecovery(t)
	})
	for _, mode := range []string{"ask", "never"} {
		t.Run("consent "+mode, func(t *testing.T) {
			f := newTriageFixture(t)
			writeConfig(t, strings.Replace(f.cfg, `task-choice="always"`, `task-choice="`+mode+`"`, 1))
			out, stderr, code := runCmd(t, "", "triage")
			if code != 2 || out != "" || !strings.Contains(stderr, "consent") {
				t.Fatalf("code=%d out=%q stderr=%q", code, out, stderr)
			}
			triageUncalled(t, f)
			triageNoRecovery(t)
		})
	}
	for _, option := range []string{"--yes", "--json", "--ai-mode=background", "--no-ai", "--ai="} {
		t.Run(option, func(t *testing.T) {
			f := newTriageFixture(t)
			out, stderr, code := runCmd(t, "", "triage", option)
			if code != 2 || out != "" {
				t.Fatalf("code=%d out=%q stderr=%q", code, out, stderr)
			}
			triageUncalled(t, f)
		})
	}
	t.Run("profile and prompt", func(t *testing.T) {
		f := newTriageFixture(t)
		t.Setenv("NN_AI", "env-choice")
		f.write(t, "prompt", "CUSTOM TRIAGE PROMPT")
		writeConfig(t, strings.Replace(f.cfg, "[ai.tasks.triage]\n", "[ai.tasks.triage]\nprompt_file="+config.TOMLString(filepath.Join(f.dir, "prompt"))+"\n", 1))
		_, stderr, code := runCmd(t, "", "triage", "nn/inbox.md", "--ai=flag-choice", "--model", "custom", "--effort=high")
		if code != 0 || f.read(t, "args-1") != "flag-choice\ncustom\nhigh\n" || !strings.HasPrefix(f.read(t, "request-1"), "CUSTOM TRIAGE PROMPT\n\n") {
			t.Fatalf("code=%d stderr=%q", code, stderr)
		}
	})
	t.Run("outside inbox", func(t *testing.T) {
		f := newTriageFixture(t)
		_, _, code := runCmd(t, "", "triage", "notes/docker.md")
		if code != 2 {
			t.Fatal("outside-inbox subject accepted")
		}
		triageUncalled(t, f)
	})
}

func TestTriageCancellationAndReviewDeadline(t *testing.T) {
	t.Run("model timeout", func(t *testing.T) {
		f := newTriageFixture(t)
		f.write(t, "wait", "")
		writeConfig(t, strings.Replace(f.cfg, "[ai]\n", "[ai]\ntimeout=\"50ms\"\n", 1))
		out, stderr, code := runCmd(t, "", "triage")
		if code != 2 || out != "" {
			t.Fatalf("code=%d out=%q stderr=%q", code, out, stderr)
		}
		triageNoRecovery(t)
	})
	t.Run("review outlives model deadline", func(t *testing.T) {
		f := newTriageFixture(t)
		writeConfig(t, strings.Replace(f.cfg, "[ai]\n", "[ai]\ntimeout=\"1s\"\n", 1))
		withTriageAsker(t, &triageTestAsker{interactive: true, ask: func(_ string, round int) (string, error) {
			if round == 1 {
				time.Sleep(1100 * time.Millisecond)
				return "1", nil
			}
			return "yes", nil
		}})
		_, stderr, code := runCmd(t, "", "triage", "--apply")
		if code != 0 {
			t.Fatalf("review consumed model deadline: %d %q", code, stderr)
		}
	})
	t.Run("cancel during confirmation", func(t *testing.T) {
		f := newTriageFixture(t)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		withTriageAsker(t, &triageTestAsker{interactive: true, ask: func(_ string, round int) (string, error) {
			if round == 1 {
				return "1", nil
			}
			cancel()
			return "yes", nil
		}})
		var out, stderr bytes.Buffer
		if code := runText(ctx, []string{"triage", "--apply"}, strings.NewReader(""), &out, &stderr); code != 130 {
			t.Fatalf("code=%d stderr=%q", code, &stderr)
		}
		triageUnchanged(t, f, triageTestOriginal)
		triageNoRecovery(t)
	})
}

type triageFailWriter struct{}

func (triageFailWriter) Write([]byte) (int, error) { return 0, errors.New("synthetic write failure") }

type triagePreviewFailWriter struct{ bytes.Buffer }

func (w *triagePreviewFailWriter) Write(p []byte) (int, error) {
	if bytes.Contains(p, []byte("Diff display:")) {
		return 0, errors.New("synthetic preview write failure")
	}
	return w.Buffer.Write(p)
}

func (w *triagePreviewFailWriter) WriteString(text string) (int, error) {
	return w.Write([]byte(text))
}

func TestTriageDisplayAndOutputFailure(t *testing.T) {
	t.Run("preview writer fails", func(t *testing.T) {
		f := newTriageFixture(t)
		asker := &scriptedAsker{interactive: true, answers: []string{"all", "yes"}}
		withTriageAsker(t, asker)
		if code := runText(context.Background(), []string{"triage", "--apply"}, strings.NewReader(""), io.Discard, triageFailWriter{}); code != 2 || len(asker.questions) != 0 {
			t.Fatalf("code=%d questions=%v", code, asker.questions)
		}
		triageUnchanged(t, f, triageTestOriginal)
		triageNoRecovery(t)
	})
	t.Run("report controls", func(t *testing.T) {
		var out, stderr bytes.Buffer
		inv := &invocation{stdout: &out, stderr: &stderr}
		report := triage.ApplyReport{RecoveryDir: "recovery\x1b[31m", Notes: []triage.ApplyNote{{Path: "nn/note\nspoof.md", Status: triage.StatusUncertain, Error: "receipt\x1b[2Jfailure", Changed: true}}}
		if err := writeTriageReport(inv, report, []string{"1"}, []triage.Action{{ID: "1", Path: "nn/note\nspoof.md"}}); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(out.String()+stderr.String(), "\x1b") || !strings.Contains(out.String(), `nn/note\nspoof.md`) || !strings.Contains(out.String(), "uncertain") {
			t.Fatalf("unsafe report: %q %q", &out, &stderr)
		}
	})
	t.Run("exact diff cannot be written", func(t *testing.T) {
		f := newTriageFixture(t)
		asker := &scriptedAsker{interactive: true, answers: []string{"all", "yes"}}
		withTriageAsker(t, asker)
		var stderr triagePreviewFailWriter
		if code := runText(context.Background(), []string{"triage", "--apply"}, strings.NewReader(""), io.Discard, &stderr); code != 2 || len(asker.questions) != 1 {
			t.Fatalf("code=%d questions=%v", code, asker.questions)
		}
		triageUnchanged(t, f, triageTestOriginal)
		triageNoRecovery(t)
	})
	t.Run("report fails after confirmed write", func(t *testing.T) {
		f := newTriageFixture(t)
		withTriageAsker(t, &scriptedAsker{interactive: true, answers: []string{"all", "yes"}})
		var stderr bytes.Buffer
		if code := runText(context.Background(), []string{"triage", "--apply"}, strings.NewReader(""), triageFailWriter{}, &stderr); code != 2 {
			t.Fatalf("code=%d stderr=%q", code, &stderr)
		}
		body, err := os.ReadFile(filepath.Join(f.root, "nn/inbox.md"))
		if err != nil || !strings.Contains(string(body), "#shell") || strings.Count(f.read(t, "hooks"), "\n") != 1 || !strings.Contains(stderr.String(), "Recovery directory:") {
			t.Fatalf("committed outcome lost: body=%q err=%v stderr=%q", body, err, &stderr)
		}
	})
}

func TestTriageHooksFollowKnownWrites(t *testing.T) {
	f := newTriageFixture(t)
	env, err := app.Open()
	if err != nil {
		t.Fatal(err)
	}
	report := triage.ApplyReport{Notes: []triage.ApplyNote{
		{Path: "nn/inbox.md", Status: triage.StatusApplied, Changed: true},
		{Path: "nn/second.md", Status: triage.StatusUncertain, Changed: true, Error: "note write succeeded but receipt persistence failed"},
		{Path: "nn/inbox.md", Status: triage.StatusApplied, Changed: true},
		{Path: "nn/third.md", Status: triage.StatusUnchanged},
		{Path: "nn/fourth.md", Status: triage.StatusUnattempted},
	}}
	postSaveTriage(context.Background(), env, report)
	got := strings.Split(strings.TrimSpace(f.read(t, "hooks")), "\n")
	want := []string{filepath.Join(f.root, "nn/inbox.md"), filepath.Join(f.root, "nn/second.md")}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("hooks=%v want=%v", got, want)
	}
}

func TestTriageSelectionParser(t *testing.T) {
	actions := []triage.Action{{ID: "1"}, {ID: "2"}}
	for input, want := range map[string][]string{"": nil, "none": nil, "all": {"1", "2"}, "2, 1": {"2", "1"}, "yes": {"yes"}, "all 1": {"all", "1"}} {
		if got := triageSelection(input, actions); !reflect.DeepEqual(got, want) {
			t.Error(fmt.Sprintf("input=%q got=%v want=%v", input, got, want))
		}
	}
}
