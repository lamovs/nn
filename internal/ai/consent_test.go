package ai

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/BurntSushi/toml"

	"github.com/lamovs/nn/internal/config"
)

// scriptedAsker records what it was asked and answers from a script.
type scriptedAsker struct {
	interactive bool
	answer      string
	err         error
	questions   []string
}

func (a *scriptedAsker) Interactive() bool { return a.interactive }

func (a *scriptedAsker) Ask(question string) (string, error) {
	a.questions = append(a.questions, question)
	return a.answer, a.err
}

func consentConfig(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.toml")
	if body != "" {
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("NN_CONFIG", path)
	return path
}

// savedConsent is ai.consent.KEY as the config file at path holds it.
func savedConsent(t *testing.T, path, key string) string {
	t.Helper()
	src, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return ""
	}
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		AI struct {
			Consent map[string]string `toml:"consent"`
		} `toml:"ai"`
	}
	if _, err := toml.Decode(string(src), &doc); err != nil {
		t.Fatalf("config after the answer does not parse: %v\n%s", err, src)
	}
	return doc.AI.Consent[key]
}

func withConsent(cfg config.Config, key, value string) config.Config {
	cfg.AI.Consent[key] = value
	return cfg
}

func TestApproveRecordedConsent(t *testing.T) {
	asker := &scriptedAsker{interactive: true, answer: "always"}

	cfg := withConsent(testConfig(), "claude", ConsentAlways)
	d, approval, err := Approve(&cfg, claudeCall("", ""), "the screenshot", asker, io.Discard)
	if _, ok := approval.allowed(); d != Allowed || !ok || err != nil {
		t.Errorf("always: %v, approval %v, %v", d, ok, err)
	}

	cfg = withConsent(testConfig(), "codex", ConsentNever)
	d, approval, err = Approve(&cfg, codexCall("", ""), "the screenshot", asker, io.Discard)
	if d != Denied || approval != (Approval{}) || err != nil {
		t.Errorf("never: %v, %v, %v, want a quiet Denied", d, approval, err)
	}

	explicit := codexCall("", "")
	explicit.Explicit = true
	d, approval, err = Approve(&cfg, explicit, "the screenshot", asker, io.Discard)
	var consent *ConsentError
	if d != Denied || approval != (Approval{}) || !errors.As(err, &consent) || consent.Key != "codex" {
		t.Fatalf("never with --ai: %v, %v, %v", d, approval, err)
	}
	for _, want := range []string{"ai.consent.codex is never", "nn config ai.consent.codex always", "nn setup ai"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("message %q lacks %q", err, want)
		}
	}
	if len(asker.questions) != 0 {
		t.Errorf("asked %q, want no question", asker.questions)
	}
}

func TestApproveWithoutTerminal(t *testing.T) {
	asker := &scriptedAsker{interactive: false, answer: "always"}
	cfg := testConfig()
	d, approval, err := Approve(&cfg, claudeCall("", ""), "the screenshot", asker, io.Discard)
	if d != Unasked || approval != (Approval{}) || err != nil || len(asker.questions) != 0 {
		t.Errorf("no terminal: %v, %v, %v, asked %q", d, approval, err, asker.questions)
	}
	if d, approval, err := Approve(&cfg, claudeCall("", ""), "the screenshot", nil, io.Discard); d != Unasked || approval != (Approval{}) || err != nil {
		t.Errorf("no asker: %v, %v, %v", d, approval, err)
	}
}

func TestApproveAsks(t *testing.T) {
	for _, tc := range []struct {
		name, answer string
		err          error
		want         Decision
		saved        string
	}{
		{"once", "once", nil, Allowed, ""},
		{"always", " Always\n", nil, Allowed, "always"},
		{"never", "never", nil, Denied, "never"},
		{"yes is not an answer", "yes", nil, Denied, ""},
		{"a letter is not an answer", "a", nil, Denied, ""},
		{"empty", "", nil, Denied, ""},
		{"end of input", "", io.EOF, Denied, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := consentConfig(t, "[vault]\nroot = \"/notes\"\n")
			asker := &scriptedAsker{interactive: true, answer: tc.answer, err: tc.err}
			var stderr bytes.Buffer
			cfg := testConfig()
			call := claudeCall("sonnet", "")
			d, approval, err := Approve(&cfg, call, "the screenshot", asker, &stderr)
			if d != tc.want || err != nil {
				t.Errorf("decision = %v, err = %v; want %v", d, err, tc.want)
			}
			if _, ok := approval.allowed(); ok != (tc.want == Allowed) {
				t.Errorf("approval given: %v, with decision %v", ok, d)
			}
			if got := savedConsent(t, path, "claude"); got != tc.saved {
				t.Errorf("saved %q, want %q", got, tc.saved)
			}
			if stderr.Len() != 0 {
				t.Errorf("stderr = %q", stderr.String())
			}
			if len(asker.questions) != 1 {
				t.Fatalf("asked %d times", len(asker.questions))
			}
			q := asker.questions[0]
			for _, want := range []string{
				"the screenshot", "claude", "model sonnet",
				"always: send to claude from now on, for every task, not only this one;",
				"once: this time only;",
				"never: send nothing to claude, now or later, for any task.",
				"always and never are saved as ai.consent.claude; nn setup ai changes it",
			} {
				if !strings.Contains(q, want) {
					t.Errorf("question %q lacks %q", q, want)
				}
			}
			if strings.Contains(q, "AGENTS.md") {
				t.Errorf("question %q names codex's AGENTS.md for claude", q)
			}
		})
	}
}

func TestApproveQuestion(t *testing.T) {
	consentConfig(t, "")
	ask := func(call Call) string {
		t.Helper()
		asker := &scriptedAsker{interactive: true, answer: "no"}
		cfg := testConfig()
		if d, _, err := Approve(&cfg, call, "the note", asker, io.Discard); d != Denied || err != nil || len(asker.questions) != 1 {
			t.Fatalf("decision = %v, err = %v, questions = %q", d, err, asker.questions)
		}
		return asker.questions[0]
	}

	q := ask(codexCall("", ""))
	for _, want := range []string{"send the note to codex, default model.\n", "~/.codex/AGENTS.md goes with it too", "saved as ai.consent.codex; nn setup ai changes it"} {
		if !strings.Contains(q, want) {
			t.Errorf("codex question %q lacks %q", q, want)
		}
	}

	spaced := commandCall("fake-model")
	spaced.Name = "my box"
	if q := ask(spaced); !strings.Contains(q, `saved as ai.consent."my box"; nn setup ai changes it`) || strings.Contains(q, "AGENTS.md") {
		t.Errorf("question for my box = %q", q)
	}

	dotted := commandCall("fake-model")
	dotted.Name = "a.b"
	q = ask(dotted)
	want := `nn cannot save always or never as ai.consent."a.b", so they hold for this call only; ` +
		`to keep one, set it by hand under [ai.consent]: "a.b" = "always", or "a.b" = "never".` + "\n"
	if !strings.Contains(q, want) || strings.Contains(q, "nn setup ai") || !strings.HasSuffix(q, "Send? [always/once/never]") {
		t.Errorf("question for a.b = %q, want it to hold %q and no nn setup ai", q, want)
	}
}

func TestApproveAskErrorIsANo(t *testing.T) {
	path := consentConfig(t, "")
	asker := &scriptedAsker{interactive: true, err: errors.New("open /dev/tty: device not configured")}
	cfg := testConfig()
	d, approval, err := Approve(&cfg, claudeCall("", ""), "the note", asker, io.Discard)
	if d != Denied || approval != (Approval{}) || err == nil || !strings.Contains(err.Error(), "device not configured") {
		t.Errorf("decision = %v, approval = %v, err = %v", d, approval, err)
	}
	if savedConsent(t, path, "claude") != "" || cfg.AI.Consent["claude"] != ConsentAsk {
		t.Error("kept an answer nobody gave")
	}
}

func TestApproveCommandProfile(t *testing.T) {
	path := consentConfig(t, "[ai.profiles.local]\nengine = \"command\"\ncommand = [\"llm\", \"{prompt}\"]\n")
	cfg := withConsent(testConfig(), "claude", ConsentAlways)
	call, err := Resolve(cfg, "ask", Overrides{Profile: "local"})
	if err != nil {
		t.Fatal(err)
	}
	asker := &scriptedAsker{interactive: true, answer: "never"}
	if d, _, err := Approve(&cfg, call, "excerpts of the vault", asker, io.Discard); d != Denied || err != nil {
		t.Errorf("decision = %v, err = %v", d, err)
	}
	if got := savedConsent(t, path, "local"); got != "never" {
		t.Errorf("saved %q under local", got)
	}
	if q := asker.questions[0]; !strings.Contains(q, "profile local (llm)") || !strings.Contains(q, "ai.consent.local") || !strings.Contains(q, "default model") {
		t.Errorf("question = %q", q)
	}
}

func TestApproveUnsavedAnswerStillCounts(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setup func(t *testing.T) string
	}{
		{"set declines", func(t *testing.T) string {
			return consentConfig(t, "ai.consent.claude = \"ask\"\n")
		}},
		{"write fails", func(t *testing.T) string {
			file := filepath.Join(t.TempDir(), "file")
			if err := os.WriteFile(file, nil, 0o644); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(file, "config.toml")
			t.Setenv("NN_CONFIG", path)
			return path
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := tc.setup(t)
			before, _ := os.ReadFile(path)
			var stderr bytes.Buffer
			asker := &scriptedAsker{interactive: true, answer: "always"}
			cfg := testConfig()
			d, approval, err := Approve(&cfg, claudeCall("", ""), "the screenshot", asker, &stderr)
			if _, ok := approval.allowed(); d != Allowed || !ok || err != nil {
				t.Errorf("decision = %v, approval %v, err = %v; want Allowed for this call", d, ok, err)
			}
			if got := cfg.AI.Consent["claude"]; got != ConsentAlways {
				t.Errorf("the unsaved answer is not kept for this call: consent %q", got)
			}
			if after, _ := os.ReadFile(path); !bytes.Equal(before, after) {
				t.Errorf("the config changed:\n%s", after)
			}
			out := stderr.String()
			if !strings.Contains(out, "nn: consent not saved:") || !strings.HasSuffix(out, "set it by hand, under [ai.consent]:\nclaude = \"always\"\n") {
				t.Errorf("stderr = %q", out)
			}

			asker.answer = "never"
			stderr.Reset()
			cfg = testConfig()
			if d, _, _ := Approve(&cfg, claudeCall("", ""), "the screenshot", asker, &stderr); d != Denied || !strings.Contains(stderr.String(), "claude = \"never\"") {
				t.Errorf("never unsaved: %v, stderr = %q", d, stderr.String())
			}
		})
	}
}

func TestApproveRemembersTheAnswer(t *testing.T) {
	for _, tc := range []struct {
		answer string
		first  Decision
		again  Decision
		asked  int
	}{
		{"always", Allowed, Allowed, 1},
		{"never", Denied, Denied, 1},
		{"once", Allowed, Allowed, 2},
	} {
		t.Run(tc.answer, func(t *testing.T) {
			consentConfig(t, "")
			asker := &scriptedAsker{interactive: true, answer: tc.answer}
			cfg := testConfig()
			if d, _, err := Approve(&cfg, codexCall("", ""), "the note", asker, io.Discard); d != tc.first || err != nil {
				t.Fatalf("first: %v, %v; want %v", d, err, tc.first)
			}
			asker.answer = "no"
			if tc.answer == "once" {
				asker.answer = "once"
			}
			d, approval, err := Approve(&cfg, codexCall("", ""), "the note", asker, io.Discard)
			if _, ok := approval.allowed(); d != tc.again || ok != (tc.again == Allowed) || err != nil {
				t.Errorf("again: %v, approval %v, %v; want %v", d, ok, err, tc.again)
			}
			if len(asker.questions) != tc.asked {
				t.Errorf("asked %d times, want %d: %q", len(asker.questions), tc.asked, asker.questions)
			}
			if tc.answer == ConsentNever {
				explicit := codexCall("", "")
				explicit.Explicit = true
				var consent *ConsentError
				if _, _, err := Approve(&cfg, explicit, "the note", asker, io.Discard); !errors.As(err, &consent) || len(asker.questions) != 1 {
					t.Errorf("never, then --ai: err = %v, questions = %d", err, len(asker.questions))
				}
			}
		})
	}

	t.Run("a config with no consent map", func(t *testing.T) {
		consentConfig(t, "")
		cfg := testConfig()
		cfg.AI.Consent = nil
		asker := &scriptedAsker{interactive: true, answer: "always"}
		if d, _, err := Approve(&cfg, claudeCall("", ""), "the note", asker, io.Discard); d != Allowed || err != nil || cfg.AI.Consent["claude"] != ConsentAlways {
			t.Errorf("decision = %v, err = %v, consent = %v", d, err, cfg.AI.Consent)
		}
	})
}

func TestRunNeedsAnApproval(t *testing.T) {
	f := newFake(t)
	f.put("stdout", claudeOK)
	consentConfig(t, "")

	denied := withConsent(testConfig(), "claude", ConsentNever)
	_, fromDenied, _ := Approve(&denied, claudeCall("", ""), "the note", nil, io.Discard)
	unasked := testConfig()
	_, fromUnasked, _ := Approve(&unasked, claudeCall("", ""), "the note", nil, io.Discard)
	for name, approval := range map[string]Approval{
		"zero":         {},
		"from Denied":  fromDenied,
		"from Unasked": fromUnasked,
		"made up":      {grant: &grant{call: claudeCall("", "")}},
	} {
		_, err := Run(context.Background(), approval, Request{Text: "T"})
		if !errors.Is(err, ErrNotApproved) {
			t.Errorf("%s: err = %v, want ErrNotApproved", name, err)
		}
	}
	if f.has("argv") {
		t.Fatal("an engine ran without an approval")
	}

	allowed := withConsent(testConfig(), "claude", ConsentAlways)
	d, approval, err := Approve(&allowed, claudeCall("", ""), "the note", nil, io.Discard)
	if d != Allowed || err != nil {
		t.Fatalf("always: %v, %v", d, err)
	}
	for range 2 {
		if res, err := Run(context.Background(), approval, Request{Text: "T"}); err != nil || res.Title != "Fix vet shadow" {
			t.Errorf("with the approval: %+v, %v", res, err)
		}
	}
}

func TestApprovalKeepsTheCallItAllowed(t *testing.T) {
	f := newFake(t)
	f.put("stdout", "answer\n")
	consentConfig(t, "")
	cfg := withConsent(testConfig(), "local", ConsentAlways)
	call := commandCall("fake-model", "--first")
	_, approval, err := Approve(&cfg, call, "the note", nil, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	call.Profile.Command[0], call.Profile.Command[1] = "no-such-model", "--changed"

	res, err := Run(context.Background(), approval, Request{Text: "T"})
	if err != nil || res.Body != "answer" {
		t.Fatalf("res = %+v, err = %v", res, err)
	}
	if got := f.argv(); !slices.Equal(got, []string{"--first"}) {
		t.Errorf("argv = %q, want the command as approved", got)
	}
}

func TestNeverRevokesApprovals(t *testing.T) {
	for _, saveFails := range []bool{false, true} {
		name := "saved"
		if saveFails {
			name = "save fails"
		}
		t.Run(name, func(t *testing.T) {
			f := newFake(t)
			path := consentConfig(t, "")
			if saveFails {
				if err := os.Mkdir(path, 0o700); err != nil {
					t.Fatal(err)
				}
			}
			cfg := testConfig()
			asker := &scriptedAsker{interactive: true, answer: "once"}
			approve := func(call Call) Approval {
				t.Helper()
				d, approval, err := Approve(&cfg, call, "the note", asker, io.Discard)
				if _, ok := approval.allowed(); d != Allowed || !ok || err != nil {
					t.Fatalf("approve %s: decision %v, valid %v, err %v", call.Name, d, ok, err)
				}
				return approval
			}
			first := approve(codexCall("", ""))
			alias := codexCall("", "")
			alias.Name = "deep"
			second := approve(alias)
			unrelated := approve(claudeCall("", ""))
			asker.answer = ConsentNever
			var stderr bytes.Buffer
			if d, approval, err := Approve(&cfg, alias, "the note", asker, &stderr); d != Denied || approval != (Approval{}) || err != nil {
				t.Fatalf("never: decision %v, approval %v, err %v", d, approval, err)
			}
			if got := ConsentOf(cfg, "codex"); got != ConsentNever {
				t.Errorf("consent = %q, want never", got)
			}
			if saveFails {
				if !strings.Contains(stderr.String(), "consent not saved") {
					t.Errorf("save failure not reported: %q", stderr.String())
				}
			} else if got := savedConsent(t, path, "codex"); got != ConsentNever {
				t.Errorf("saved consent = %q, want never", got)
			}
			for _, approval := range []Approval{first, second} {
				if _, err := Run(context.Background(), approval, Request{Text: "T"}); !errors.Is(err, ErrNotApproved) {
					t.Errorf("revoked approval: err = %v, want ErrNotApproved", err)
				}
			}
			if f.has("argv") {
				t.Fatal("a revoked approval started an engine")
			}
			if _, ok := unrelated.allowed(); !ok {
				t.Error("never for codex revoked claude's approval")
			}
			remember(&cfg, "codex", ConsentAlways)
			approve(codexCall("", ""))
			for _, approval := range []Approval{first, second} {
				if _, ok := approval.allowed(); ok {
					t.Error("a later always revived an old approval")
				}
			}
		})
	}
}

func TestApproveReservedCommandNames(t *testing.T) {
	for _, name := range append(config.BuiltinProfiles(), "local") {
		t.Run(name, func(t *testing.T) {
			cfg := withConsent(testConfig(), name, ConsentAlways)
			call := commandCall("fake-model", "{prompt}")
			call.Name = name
			asker := &scriptedAsker{interactive: true, answer: "once"}
			d, approval, err := Approve(&cfg, call, "the note", asker, io.Discard)
			if name == "local" {
				if _, ok := approval.allowed(); d != Allowed || !ok || err != nil {
					t.Errorf("ordinary command: decision %v, valid %v, err %v", d, ok, err)
				}
			} else if d != Denied || approval != (Approval{}) || err == nil || !strings.Contains(err.Error(), "built-in profile names cannot use engine command") {
				t.Errorf("reserved command: decision %v, approval %v, err %v", d, approval, err)
			}
			if len(asker.questions) != 0 {
				t.Errorf("unexpected consent question: %q", asker.questions)
			}
		})
	}
}

func TestSetConsent(t *testing.T) {
	path := consentConfig(t, "[ai.profiles.\"my box\"]\nengine = \"command\"\ncommand = [\"llm\"]\n")
	if err := SetConsent("my box", ConsentAlways); err != nil {
		t.Fatal(err)
	}
	if got := savedConsent(t, path, "my box"); got != "always" {
		t.Errorf("saved %q", got)
	}
	if err := SetConsent("claude", "sometimes"); err == nil || !strings.Contains(err.Error(), "want ask, always, never") {
		t.Errorf("bad value: %v", err)
	}
	if err := SetConsent("", ConsentAlways); err == nil {
		t.Error("empty key accepted")
	}

	err := SetConsent(`my.box "quoted"`, ConsentNever)
	var edit *config.ManualEditError
	if !errors.As(err, &edit) || edit.Section != "ai.consent" {
		t.Fatalf("dotted name: err = %v", err)
	}
	var doc map[string]string
	if _, err := toml.Decode(edit.Line, &doc); err != nil || doc[`my.box "quoted"`] != "never" || len(doc) != 1 {
		t.Errorf("line %q reads back as %v, %v", edit.Line, doc, err)
	}
	if got := savedConsent(t, path, `my.box "quoted"`); got != "" {
		t.Errorf("the dotted name was written anyway: %q", got)
	}
}

func TestConsentOf(t *testing.T) {
	cfg := testConfig()
	cfg.AI.Consent["codex"] = ConsentNever
	cfg.AI.Consent["local"] = "maybe"
	for key, want := range map[string]string{"claude": ConsentAsk, "codex": ConsentNever, "local": ConsentAsk, "unknown": ConsentAsk} {
		if got := ConsentOf(cfg, key); got != want {
			t.Errorf("ConsentOf(%s) = %q, want %q", key, got, want)
		}
	}
}

func TestConsentSetting(t *testing.T) {
	for key, want := range map[string]string{
		"claude": "ai.consent.claude",
		"my-box": "ai.consent.my-box",
		"my box": `ai.consent."my box"`,
		"a.b":    `ai.consent."a.b"`,
		`q"uote`: `ai.consent."q\"uote"`,
	} {
		if got := ConsentSetting(key); got != want {
			t.Errorf("ConsentSetting(%q) = %s, want %s", key, got, want)
		}
	}
}

func TestConsentHintFor(t *testing.T) {
	for _, tc := range []struct {
		key, command string
		setup        bool
	}{
		{"local", "nn config ai.consent.local always", true},
		{"my-box_2", "nn config ai.consent.my-box_2 always", true},
		{"my box", "", true},
		{`q"uote`, "", true},
		{"a.b", "", false},
		{"", "", false},
	} {
		hint := ConsentHintFor(tc.key, ConsentAlways)
		if hint.Command != tc.command || hint.Setup != tc.setup {
			t.Errorf("ConsentHintFor(%q) = %+v, want command %q, setup %v", tc.key, hint, tc.command, tc.setup)
		}
		var doc struct {
			AI struct {
				Consent map[string]string `toml:"consent"`
			} `toml:"ai"`
		}
		if _, err := toml.Decode("[ai.consent]\n"+hint.Line+"\n", &doc); err != nil || len(doc.AI.Consent) != 1 || doc.AI.Consent[tc.key] != ConsentAlways {
			t.Errorf("line %q for %q reads back as %v, %v", hint.Line, tc.key, doc.AI.Consent, err)
		}
		if want := "set it by hand under [ai.consent]: " + hint.Line; hint.ByHand() != want {
			t.Errorf("ByHand() = %q, want %q", hint.ByHand(), want)
		}
	}
}

func TestConsentHintCommandIsTaken(t *testing.T) {
	path := consentConfig(t, "[ai.profiles.my-box_2]\nengine = \"command\"\ncommand = [\"llm\"]\n")
	for _, key := range []string{"codex", "my-box_2"} {
		for _, value := range []string{ConsentNever, ConsentAlways} {
			words := strings.Fields(ConsentHintFor(key, value).Command)
			if len(words) != 4 || words[0] != "nn" || words[1] != "config" {
				t.Fatalf("command for %s = %q", key, words)
			}
			if err := config.Set(words[2], words[3]); err != nil {
				t.Fatalf("config.Set(%q, %q): %v", words[2], words[3], err)
			}
			if got := savedConsent(t, path, key); got != value {
				t.Errorf("after %q: ai.consent.%s = %q", words, key, got)
			}
		}
	}
}

func TestConsentErrorSaysHowToAllow(t *testing.T) {
	for key, want := range map[string]string{
		"local":  "ai.consent.local is never, so nn sends nothing to local; to allow it: nn config ai.consent.local always, or nn setup ai",
		"my box": `ai.consent."my box" is never, so nn sends nothing to my box; to allow it: nn setup ai, or set it by hand under [ai.consent]: "my box" = "always"`,
		"a.b":    `ai.consent."a.b" is never, so nn sends nothing to a.b; to allow it: set it by hand under [ai.consent]: "a.b" = "always"`,
	} {
		if got := (&ConsentError{Key: key}).Error(); got != want {
			t.Errorf("ConsentError{%q}:\ngot  %s\nwant %s", key, got, want)
		}
	}
}
