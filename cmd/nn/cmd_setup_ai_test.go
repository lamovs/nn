package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lamovs/nn/internal/ai"
	"github.com/lamovs/nn/internal/config"
)

func loadedConsent(t *testing.T, key string) string {
	t.Helper()
	cfg, _, err := config.Load()
	if err != nil {
		t.Fatalf("config after nn setup ai: %v", err)
	}
	return cfg.AI.Consent[key]
}

func assertNoConfig(t *testing.T) {
	t.Helper()
	if _, err := os.Stat(os.Getenv("NN_CONFIG")); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("config file: %v, want none written", err)
	}
}

const sendsLines = "  nn sends it:\n" +
	shotLine +
	"    title: new note text or OCR, an optional source image and existing vault tag names\n" +
	"    ask: your question and excerpts of the notes nn finds for it\n" +
	"    filter: your instruction and the text piped to nn ai\n" +
	"    last: the previous command you provide and its optional output\n" +
	"    triage: excerpts and metadata of selected inbox notes and related notes\n" +
	"    url: the link, the text of the page it points to and existing vault tag names\n" +
	"    digest: your selection and excerpts and metadata of the notes the digest covers\n"

const shotLine = "    shot: the screenshot, its OCR text and existing vault tags\n"

func entryBlock(key, program, consent string) string {
	return entryBlockAs(key, "ai.consent."+key, program, consent)
}

func entryBlockAs(key, setting, program, consent string) string {
	return consentBlock(key, setting, program, consent, key == "claude" || key == "codex")
}

func consentBlock(key, setting, program, consent string, image bool) string {
	sends := sendsLines
	if !image {
		sends = strings.Replace(sends, shotLine, "", 1)
	}
	block := key + "\n  program: " + program + "\n  consent: " + consent + " (" + setting + ")\n" + sends
	if key == "codex" {
		block += "  note: " + codexOverhead + "\n"
	}
	return block
}

func TestSetupAISaysWhatEachTaskSends(t *testing.T) {
	var lines []string
	for _, task := range config.Tasks() {
		lines = append(lines, "    "+task+": "+ai.Sends(task)+"\n")
	}
	if want := "  nn sends it:\n" + strings.Join(lines, ""); sendsLines != want {
		t.Fatalf("sendsLines:\n%s\nwant, from ai.Sends:\n%s", sendsLines, want)
	}

	newTestVault(t)
	writeConfig(t, "[ai.profiles.seeing]\nengine = \"command\"\ncommand = [\"fake-model\", \"--image={image}\"]\n\n"+
		"[ai.profiles.blind]\nengine = \"command\"\ncommand = [\"fake-model\"]\n")
	dir := fakeEngines(t, "fake-model")
	withAsker(t, &scriptedAsker{interactive: true, answers: []string{"skip", "skip"}})

	stdout, stderr, code := runCmd(t, "", "setup", "ai")
	if code != 0 {
		t.Errorf("code = %d, want 0, stderr = %q", code, stderr)
	}
	model := filepath.Join(dir, "fake-model")
	blocks := []string{
		entryBlock("claude", "claude is not installed", "ask"),
		entryBlock("codex", "codex is not installed", "ask"),
		consentBlock("blind", "ai.consent.blind", model, "ask", false),
		consentBlock("seeing", "ai.consent.seeing", model, "ask", true),
	}
	if want := strings.Join(blocks, "\n"); stdout != want {
		t.Errorf("stdout:\n%s\nwant:\n%s", stdout, want)
	}
	if n := strings.Count(stdout, shotLine); n != 3 {
		t.Errorf("shot is listed %d times, want 3: not for blind", n)
	}
}

func TestSetupAIAnswers(t *testing.T) {
	for _, tc := range []struct {
		name    string
		answers []string
		saved   string // "" for nothing written
	}{
		{"always", []string{"always"}, "always"},
		{"never", []string{"never"}, "never"},
		{"any case and spacing", []string{"  Always \r"}, "always"},
		{"skip", []string{"skip"}, ""},
		{"empty", []string{""}, ""},
		{"no answer", nil, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			newTestVault(t)
			dir := fakeEngines(t, "claude")
			asker := &scriptedAsker{interactive: true, answers: tc.answers}
			withAsker(t, asker)

			stdout, stderr, code := runCmd(t, "", "setup", "ai")
			if code != 0 {
				t.Errorf("code = %d, want 0, stderr = %q", code, stderr)
			}
			if stderr != "" {
				t.Errorf("stderr = %q", stderr)
			}
			for _, want := range []string{
				entryBlock("claude", filepath.Join(dir, "claude"), "ask"),
				"\n" + entryBlock("codex", "codex is not installed", "ask"),
			} {
				if !strings.Contains(stdout, want) {
					t.Errorf("stdout lacks %q:\n%s", want, stdout)
				}
			}
			if len(asker.questions) != 1 {
				t.Fatalf("asked %q, want one question, about claude", asker.questions)
			}
			q := asker.questions[0]
			for _, want := range []string{"always: nn sends to claude without asking, for every task;", "never: nn sends it nothing, for any task;", "skip: ai.consent.claude stays ask", "Allow claude? [always/never/skip]"} {
				if !strings.Contains(q, want) {
					t.Errorf("question %q lacks %q", q, want)
				}
			}

			if tc.saved == "" {
				assertNoConfig(t)
				if strings.Contains(stdout, " = ") {
					t.Errorf("stdout reports a write:\n%s", stdout)
				}
				return
			}
			if got := loadedConsent(t, "claude"); got != tc.saved {
				t.Errorf("ai.consent.claude = %q, want %q", got, tc.saved)
			}
			if want := "ai.consent.claude = \"" + tc.saved + "\"\n"; !strings.Contains(stdout, want) {
				t.Errorf("stdout lacks %q:\n%s", want, stdout)
			}
		})
	}
}

func TestSetupAIUnknownAnswers(t *testing.T) {
	for _, tc := range []struct {
		name      string
		answers   []string
		questions int
		saved     string
		stderr    string
	}{
		{"three unknown", []string{"yes", "always please", "a"}, 3, "", "nn: setup: no answer nn knows; ai.consent.claude is left as ask\n"},
		{"unknown, then never", []string{"y", "never"}, 2, "never", ""},
		{"unknown, then no more input", []string{"allow"}, 2, "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			newTestVault(t)
			fakeEngines(t, "claude")
			asker := &scriptedAsker{interactive: true, answers: tc.answers}
			withAsker(t, asker)

			_, stderr, code := runCmd(t, "", "setup", "ai")
			if code != 0 {
				t.Errorf("code = %d, want 0", code)
			}
			if stderr != tc.stderr {
				t.Errorf("stderr = %q, want %q", stderr, tc.stderr)
			}
			if len(asker.questions) != tc.questions {
				t.Fatalf("asked %d times, want %d: %q", len(asker.questions), tc.questions, asker.questions)
			}
			for i, q := range asker.questions[1:] {
				if !strings.HasPrefix(q, "Answer always, never or skip.\n") || !strings.HasSuffix(q, asker.questions[0]) {
					t.Errorf("question %d = %q, want the first one after a reminder of the answers", i+2, q)
				}
			}
			if tc.saved == "" {
				assertNoConfig(t)
				return
			}
			if got := loadedConsent(t, "claude"); got != tc.saved {
				t.Errorf("ai.consent.claude = %q, want %q", got, tc.saved)
			}
		})
	}
}

type eofAsker struct {
	answer    string
	questions []string
}

func (a *eofAsker) Interactive() bool { return true }

func (a *eofAsker) Ask(question string) (string, error) {
	a.questions = append(a.questions, question)
	if len(a.questions) == 1 {
		return a.answer, io.EOF
	}
	return "always", nil
}

func TestSetupAIAnswerAtEndOfInput(t *testing.T) {
	t.Run("known", func(t *testing.T) {
		newTestVault(t)
		fakeEngines(t, "claude")
		asker := &eofAsker{answer: "never"}
		withAsker(t, asker)

		_, stderr, code := runCmd(t, "", "setup", "ai")
		if code != 0 || stderr != "" || len(asker.questions) != 1 {
			t.Errorf("code = %d, stderr = %q, questions = %d", code, stderr, len(asker.questions))
		}
		if got := loadedConsent(t, "claude"); got != "never" {
			t.Errorf("ai.consent.claude = %q, want never", got)
		}
	})
	t.Run("unknown", func(t *testing.T) {
		newTestVault(t)
		fakeEngines(t, "claude")
		asker := &eofAsker{answer: "yes"}
		withAsker(t, asker)

		_, stderr, code := runCmd(t, "", "setup", "ai")
		if code != 0 || len(asker.questions) != 1 {
			t.Errorf("code = %d, questions = %d", code, len(asker.questions))
		}
		if want := "nn: setup: no answer nn knows; ai.consent.claude is left as ask\n"; stderr != want {
			t.Errorf("stderr = %q, want %q", stderr, want)
		}
		assertNoConfig(t)
	})
}

func TestSetupAINothingFound(t *testing.T) {
	newTestVault(t)
	asker := &scriptedAsker{interactive: true, answers: []string{"always", "always"}}
	withAsker(t, asker)

	stdout, stderr, code := runCmd(t, "", "setup", "ai")
	if code != 0 {
		t.Errorf("code = %d, want 0, stderr = %q", code, stderr)
	}
	if len(asker.questions) != 0 {
		t.Errorf("asked %q, want nothing", asker.questions)
	}
	want := entryBlock("claude", "claude is not installed", "ask") + "\n" + entryBlock("codex", "codex is not installed", "ask")
	if stdout != want {
		t.Errorf("stdout = %q, want %q", stdout, want)
	}
	assertNoConfig(t)
}

func TestSetupAICommandProfiles(t *testing.T) {
	newTestVault(t)
	writeConfig(t, "[ai.profiles.zeta]\nengine = \"command\"\ncommand = [\"fake-model\", \"{prompt}\"]\n\n"+
		"[ai.profiles.alpha]\nengine = \"command\"\ncommand = [\"gone-model\"]\n\n"+
		"[ai.profiles.fast]\nengine = \"claude\"\nmodel = \"haiku\"\n")
	dir := fakeEngines(t, "codex", "fake-model")
	asker := &scriptedAsker{interactive: true, answers: []string{"skip", "never"}}
	withAsker(t, asker)

	stdout, stderr, code := runCmd(t, "", "setup", "ai")
	if code != 0 {
		t.Errorf("code = %d, want 0, stderr = %q", code, stderr)
	}
	blocks := []string{
		entryBlock("claude", "claude is not installed", "ask"),
		entryBlock("codex", filepath.Join(dir, "codex"), "ask"),
		entryBlock("alpha", "gone-model is not installed", "ask"),
		entryBlock("zeta", filepath.Join(dir, "fake-model"), "ask") + "ai.consent.zeta = \"never\"\n",
	}
	if want := strings.Join(blocks, "\n"); stdout != want {
		t.Errorf("stdout:\n%s\nwant:\n%s", stdout, want)
	}
	if len(asker.questions) != 2 || !strings.Contains(asker.questions[0], "Allow codex?") ||
		!strings.Contains(asker.questions[1], "Allow profile zeta (fake-model)? [always/never/skip]") {
		t.Errorf("questions = %q", asker.questions)
	}
	if got := loadedConsent(t, "zeta"); got != "never" {
		t.Errorf("ai.consent.zeta = %q, want never", got)
	}
	if got := loadedConsent(t, "codex"); got != "ask" {
		t.Errorf("ai.consent.codex = %q, want ask", got)
	}
}

func TestSetupAICommandProfileWithoutCommand(t *testing.T) {
	newTestVault(t)
	const body = "[ai.profiles.broken]\nengine = \"command\"\n"
	path := writeConfig(t, body)
	asker := &scriptedAsker{interactive: true, answers: []string{"always"}}
	withAsker(t, asker)

	stdout, stderr, code := runCmd(t, "", "setup", "ai")
	if code != 0 {
		t.Errorf("code = %d, want 0", code)
	}
	if want := "\n" + entryBlock("broken", "engine command needs a command", "ask"); !strings.HasSuffix(stdout, want) {
		t.Errorf("stdout:\n%s\nwant it to end with:\n%s", stdout, want)
	}
	if !strings.Contains(stderr, "nn: warning: config: ") || len(asker.questions) != 0 {
		t.Errorf("stderr = %q, questions = %q", stderr, asker.questions)
	}
	if data, err := os.ReadFile(path); err != nil || string(data) != body {
		t.Errorf("config = %q, %v; want it untouched", data, err)
	}
}

func TestSetupAIReportOffersNoCommand(t *testing.T) {
	newTestVault(t)
	writeConfig(t, "[ai.profiles.local]\nengine = \"command\"\ncommand = [\"fake-model\"]\n\n"+
		"[ai.profiles.\"a.b\"]\nengine = \"command\"\ncommand = [\"fake-model\"]\n\n"+
		"[ai.profiles.\"my box\"]\nengine = \"command\"\ncommand = [\"fake-model\"]\n")
	fakeEngines(t, "claude", "fake-model")
	asker := &scriptedAsker{interactive: true, answers: []string{"skip", "skip", "skip", "skip"}}
	withAsker(t, asker)

	stdout, stderr, code := runCmd(t, "", "setup", "ai")
	if code != 0 {
		t.Errorf("code = %d, want 0, stderr = %q", code, stderr)
	}
	wants := []string{
		"skip: ai.consent.claude stays ask",
		`skip: ai.consent."a.b" stays ask`,
		"skip: ai.consent.local stays ask",
		`skip: ai.consent."my box" stays ask`,
	}
	if len(asker.questions) != len(wants) {
		t.Fatalf("questions = %q", asker.questions)
	}
	for i, want := range wants {
		if !strings.Contains(asker.questions[i], want) {
			t.Errorf("question %d = %q, want it to contain %q", i+1, asker.questions[i], want)
		}
	}
	if all := stdout + stderr + strings.Join(asker.questions, "\n"); strings.Contains(all, "nn config") {
		t.Errorf("an nn config command is offered:\n%s", all)
	}
}

func TestSetupAIUnwritableConsent(t *testing.T) {
	t.Run("a profile name with a dot", func(t *testing.T) {
		newTestVault(t)
		writeConfig(t, "[ai.profiles.\"a.b\"]\nengine = \"command\"\ncommand = [\"fake-model\", \"{prompt}\"]\n\n"+
			"[ai.profiles.local]\nengine = \"command\"\ncommand = [\"fake-model\"]\n")
		fakeEngines(t, "fake-model")
		asker := &scriptedAsker{interactive: true, answers: []string{"always", "always"}}
		withAsker(t, asker)

		stdout, stderr, code := runCmd(t, "", "setup", "ai")
		if code != 1 {
			t.Errorf("code = %d, want 1", code)
		}
		if len(asker.questions) != 2 || !strings.Contains(asker.questions[0], "skip: ai.consent.\"a.b\" stays ask") {
			t.Fatalf("questions = %q", asker.questions)
		}
		if !strings.HasPrefix(stderr, "nn: setup: ") || !strings.HasSuffix(stderr, "set it by hand, under [ai.consent]:\n\"a.b\" = \"always\"\n") {
			t.Errorf("stderr = %q", stderr)
		}
		if !strings.Contains(stdout, "ai.consent.local = \"always\"\n") || strings.Contains(stdout, "ai.consent.\"a.b\" = ") {
			t.Errorf("stdout:\n%s", stdout)
		}
		if got := loadedConsent(t, "local"); got != "always" {
			t.Errorf("ai.consent.local = %q, want always", got)
		}
		if got := loadedConsent(t, "a.b"); got != "ask" {
			t.Errorf("ai.consent.\"a.b\" = %q, want ask", got)
		}
	})

	t.Run("a dotted key in the file", func(t *testing.T) {
		newTestVault(t)
		path := writeConfig(t, "ai.consent.claude = \"ask\"\n")
		fakeEngines(t, "claude")
		withAsker(t, &scriptedAsker{interactive: true, answers: []string{"never"}})

		_, stderr, code := runCmd(t, "", "setup", "ai")
		if code != 1 {
			t.Errorf("code = %d, want 1", code)
		}
		if !strings.HasSuffix(stderr, "set it by hand, under [ai.consent]:\nclaude = \"never\"\n") {
			t.Errorf("stderr = %q", stderr)
		}
		if data, err := os.ReadFile(path); err != nil || string(data) != "ai.consent.claude = \"ask\"\n" {
			t.Errorf("config = %q, %v; want it untouched", data, err)
		}
	})
}

func TestSetupAIWithoutTerminal(t *testing.T) {
	t.Run("programs found", func(t *testing.T) {
		newTestVault(t)
		body := "[ai.profiles.local]\nengine = \"command\"\ncommand = [\"fake-model\"]\n\n" +
			"[ai.profiles.\"a.b\"]\nengine = \"command\"\ncommand = [\"fake-model\"]\n\n" +
			"[ai.profiles.\"my box\"]\nengine = \"command\"\ncommand = [\"fake-model\"]\n\n" +
			"[ai.consent]\ncodex = \"always\"\n"
		path := writeConfig(t, body)
		dir := fakeEngines(t, "claude", "codex", "fake-model")
		asker := &scriptedAsker{interactive: false, answers: []string{"always", "always", "always", "always", "always"}}
		withAsker(t, asker)

		stdout, stderr, code := runCmd(t, "", "setup", "ai")
		if code != 2 {
			t.Errorf("code = %d, want 2", code)
		}
		if len(asker.questions) != 0 {
			t.Errorf("asked %q with no terminal", asker.questions)
		}
		model := filepath.Join(dir, "fake-model")
		blocks := []string{
			entryBlock("claude", filepath.Join(dir, "claude"), "ask") + "  to allow it: nn config ai.consent.claude always\n",
			entryBlock("codex", filepath.Join(dir, "codex"), "always"),
			entryBlockAs("a.b", `ai.consent."a.b"`, model, "ask") +
				"  to allow it: set it by hand under [ai.consent]: \"a.b\" = \"always\"\n",
			entryBlock("local", model, "ask") + "  to allow it: nn config ai.consent.local always\n",
			entryBlockAs("my box", `ai.consent."my box"`, model, "ask") +
				"  to allow it: nn setup ai on a terminal, or set it by hand under [ai.consent]: \"my box\" = \"always\"\n",
		}
		if want := strings.Join(blocks, "\n"); stdout != want {
			t.Errorf("stdout:\n%s\nwant:\n%s", stdout, want)
		}
		if want := "nn: setup: no terminal to ask on; allow an engine as its \"to allow it\" line says\n"; stderr != want {
			t.Errorf("stderr = %q, want %q", stderr, want)
		}
		if data, err := os.ReadFile(path); err != nil || string(data) != body {
			t.Errorf("config = %q, %v; want it untouched", data, err)
		}

		var commands int
		for _, line := range strings.Split(stdout, "\n") {
			command, ok := strings.CutPrefix(line, "  to allow it: nn config ")
			if !ok {
				continue
			}
			commands++
			words := strings.Fields(command)
			if _, stderr, code := runCmd(t, "", append([]string{"config"}, words...)...); code != 0 {
				t.Errorf("nn config %s: code %d, stderr %q", command, code, stderr)
			}
		}
		if commands != 2 {
			t.Errorf("%d nn config commands offered, want 2", commands)
		}
		for _, key := range []string{"claude", "local"} {
			if got := loadedConsent(t, key); got != "always" {
				t.Errorf("after the offered command, ai.consent.%s = %q, want always", key, got)
			}
		}
	})

	t.Run("nothing to ask about", func(t *testing.T) {
		newTestVault(t)
		writeConfig(t, "[ai.consent]\nclaude = \"always\"\n")
		fakeEngines(t, "claude")
		withAsker(t, &scriptedAsker{interactive: false})

		stdout, stderr, code := runCmd(t, "", "setup", "ai")
		if code != 0 || stderr != "" || strings.Contains(stdout, "to allow it") {
			t.Errorf("code = %d, stderr = %q, stdout:\n%s", code, stderr, stdout)
		}
	})
}

func TestSetupAIBeforeVaultRoot(t *testing.T) {
	newTestVault(t)
	t.Setenv("NN_ROOT", "")
	fakeEngines(t, "claude")
	withAsker(t, &scriptedAsker{interactive: true, answers: []string{"always"}})

	stdout, stderr, code := runCmd(t, "", "setup", "ai")
	if code != 0 {
		t.Errorf("code = %d, want 0, stderr = %q", code, stderr)
	}
	if !strings.Contains(stderr, "nn: warning: config: ") || !strings.Contains(stderr, "vault.root") {
		t.Errorf("stderr = %q, want the missing vault.root as a warning", stderr)
	}
	if !strings.Contains(stdout, "ai.consent.claude = \"always\"\n") {
		t.Errorf("stdout:\n%s", stdout)
	}
	data, err := os.ReadFile(os.Getenv("NN_CONFIG"))
	if err != nil || string(data) != "[ai.consent]\nclaude = \"always\"\n" {
		t.Errorf("config = %q, %v", data, err)
	}
}

func TestSetupAIConfigThatDoesNotParse(t *testing.T) {
	newTestVault(t)
	writeConfig(t, "[ai\n")
	fakeEngines(t, "claude")
	asker := &scriptedAsker{interactive: true, answers: []string{"always"}}
	withAsker(t, asker)

	stdout, stderr, code := runCmd(t, "", "setup", "ai")
	if code != 2 || stdout != "" || !strings.HasPrefix(stderr, "nn: setup: ") || len(asker.questions) != 0 {
		t.Errorf("code = %d, stdout = %q, stderr = %q, questions = %q", code, stdout, stderr, asker.questions)
	}
}

func TestSetupAIAskError(t *testing.T) {
	newTestVault(t)
	fakeEngines(t, "claude", "codex")
	asker := &scriptedAsker{interactive: true, err: errors.New("open /dev/tty: device not configured")}
	withAsker(t, asker)

	_, stderr, code := runCmd(t, "", "setup", "ai")
	if code != 2 {
		t.Errorf("code = %d, want 2", code)
	}
	if want := "nn: setup: ask about claude: open /dev/tty: device not configured\n"; stderr != want {
		t.Errorf("stderr = %q, want %q", stderr, want)
	}
	if len(asker.questions) != 1 {
		t.Errorf("asked %d times, want 1", len(asker.questions))
	}
	assertNoConfig(t)
}

type interruptingAsker struct {
	*scriptedAsker
	cancel context.CancelFunc
}

func (a interruptingAsker) Ask(question string) (string, error) {
	a.cancel()
	return a.scriptedAsker.Ask(question)
}

func TestSetupAIInterrupted(t *testing.T) {
	t.Run("before the first question", func(t *testing.T) {
		newTestVault(t)
		fakeEngines(t, "claude")
		asker := &scriptedAsker{interactive: true, answers: []string{"always"}}
		withAsker(t, asker)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		var stdout, stderr bytes.Buffer
		if code := runText(ctx, []string{"setup", "ai"}, strings.NewReader(""), &stdout, &stderr); code != 130 {
			t.Errorf("code = %d, want 130", code)
		}
		if len(asker.questions) != 0 {
			t.Errorf("asked %q", asker.questions)
		}
		assertNoConfig(t)
	})

	t.Run("while answering", func(t *testing.T) {
		newTestVault(t)
		fakeEngines(t, "claude", "codex")
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		asker := interruptingAsker{&scriptedAsker{interactive: true, answers: []string{"always", "always"}}, cancel}
		withAsker(t, asker)

		var stdout, stderr bytes.Buffer
		if code := runText(ctx, []string{"setup", "ai"}, strings.NewReader(""), &stdout, &stderr); code != 130 {
			t.Errorf("code = %d, want 130", code)
		}
		if len(asker.questions) != 1 {
			t.Errorf("asked %q, want only the first question", asker.questions)
		}
		assertNoConfig(t)
	})
}

func TestSetupAIHelpAndUsage(t *testing.T) {
	stdout, _, code := runCmd(t, "", "help", "setup")
	if code != 0 || !strings.Contains(stdout, "nn setup ai") {
		t.Errorf("code = %d, help:\n%s", code, stdout)
	}
	for _, args := range [][]string{{"setup", "ai", "extra"}, {"setup", "ai", "--json"}} {
		if _, stderr, code := runCmd(t, "", args...); code != 2 {
			t.Errorf("%q: code = %d, want 2, stderr = %q", args, code, stderr)
		}
	}
}
