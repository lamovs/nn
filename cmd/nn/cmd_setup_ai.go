package main

import (
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/lamovs/nn/internal/ai"
	"github.com/lamovs/nn/internal/app"
	"github.com/lamovs/nn/internal/config"
	"github.com/lamovs/nn/internal/output"
)

// codexOverhead notes that codex also reads ~/.codex/AGENTS.md on every call; a private CODEX_HOME would risk codex's login.
const codexOverhead = "codex adds ~8.8k input tokens of its own to every call (claude ~0.5k), and your ~/.codex/AGENTS.md goes with each one"

const setupAttempts = 3

type ttyAsker struct{}

func (ttyAsker) Interactive() bool { return app.Interactive() }

func (ttyAsker) Ask(question string) (string, error) { return app.Ask(question) }

var setupAsker ai.Asker = ttyAsker{}

type consentEntry struct {
	key     string
	profile config.Profile
}

func consentEntries(cfg config.Config) []consentEntry {
	var entries []consentEntry
	for _, name := range config.BuiltinProfiles() {
		p := cfg.AI.Profiles[name]
		entries = append(entries, consentEntry{key: ai.ConsentKey(name, p), profile: p})
	}
	var names []string
	for name, p := range cfg.AI.Profiles {
		if p.Engine == config.EngineCommand {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	for _, name := range names {
		p := cfg.AI.Profiles[name]
		entries = append(entries, consentEntry{key: ai.ConsentKey(name, p), profile: p})
	}
	return entries
}

func cmdSetupAI(inv *invocation) int {
	cfg, ok := loadConfig(inv)
	if !ok {
		return output.ExitError
	}
	interactive := setupAsker.Interactive()
	code := output.ExitOK
	unasked := false
	for i, e := range consentEntries(cfg) {
		if inv.interrupted() {
			return output.ExitInterrupted
		}
		if i > 0 {
			fmt.Fprintln(inv.stdout)
		}
		consent := ai.ConsentOf(cfg, e.key)
		path, lookErr := ai.LookBinary(e.profile)
		writeConsentEntry(inv.stdout, e, consent, path, lookErr)
		if lookErr != nil {
			continue
		}

		if !interactive {
			if consent != ai.ConsentAlways {
				fmt.Fprintf(inv.stdout, "  to allow it: %s\n", allowConsent(e.key))
				unasked = true
			}
			continue
		}

		choice, known, err := askConsent(setupAsker, e, consent)
		switch {
		case inv.interrupted():
			return output.ExitInterrupted
		case err != nil:
			return inv.fail(fmt.Errorf("ask about %s: %w", e.key, err))
		case !known:
			fmt.Fprintf(inv.stderr, "nn: %s: no answer nn knows; %s is left as %s\n", inv.verb, ai.ConsentSetting(e.key), consent)
			continue
		case choice == "":
			continue
		}

		err = ai.SetConsent(e.key, choice)
		var manual *config.ManualEditError
		switch {
		case errors.As(err, &manual):
			fmt.Fprintf(inv.stderr, "nn: %s: %v\n", inv.verb, err)
			fmt.Fprintf(inv.stderr, "set it by hand, under [%s]:\n%s\n", manual.Section, manual.Line)
			code = 1
		case err != nil:
			fmt.Fprintf(inv.stderr, "nn: %s: %v\n", inv.verb, err)
			code = 1
		default:
			fmt.Fprintf(inv.stdout, "%s = %s\n", ai.ConsentSetting(e.key), strconv.Quote(choice))
		}
	}
	if unasked {
		fmt.Fprintf(inv.stderr, "nn: %s: no terminal to ask on; allow an engine as its \"to allow it\" line says\n", inv.verb)
		return output.ExitError
	}
	return code
}

func writeConsentEntry(w io.Writer, e consentEntry, consent, path string, lookErr error) {
	fmt.Fprintln(w, e.key)
	if lookErr != nil {
		fmt.Fprintf(w, "  program: %v\n", lookErr)
	} else {
		fmt.Fprintf(w, "  program: %s\n", path)
	}
	fmt.Fprintf(w, "  consent: %s (%s)\n", consent, ai.ConsentSetting(e.key))
	fmt.Fprintln(w, "  nn sends it:")
	for _, task := range config.Tasks() {
		if ai.TakesImage(task) && !ai.AcceptsImage(e.profile) {
			continue
		}
		fmt.Fprintf(w, "    %s: %s\n", task, ai.Sends(task))
	}
	if e.profile.Engine == "codex" {
		fmt.Fprintf(w, "  note: %s\n", codexOverhead)
	}
}

func askConsent(asker ai.Asker, e consentEntry, consent string) (string, bool, error) {
	target := e.key
	if e.profile.Engine == config.EngineCommand {
		target = fmt.Sprintf("profile %s (%s)", e.key, filepath.Base(ai.Binary(e.profile)))
	}
	question := fmt.Sprintf("always: nn sends to %s without asking, for every task; never: nn sends it nothing, for any task; skip: %s stays %s.\n"+
		"Allow %s? [always/never/skip]", target, ai.ConsentSetting(e.key), consent, target)
	prompt := question
	for range setupAttempts {
		answer, err := asker.Ask(prompt)
		if err != nil && !errors.Is(err, io.EOF) {
			return "", false, err
		}
		switch choice := strings.ToLower(strings.TrimSpace(answer)); choice {
		case ai.ConsentAlways, ai.ConsentNever:
			return choice, true, nil
		case "skip", "":
			return "", true, nil
		}
		if err != nil {
			return "", false, nil
		}
		prompt = "Answer always, never or skip.\n" + question
	}
	return "", false, nil
}

func allowConsent(key string) string {
	hint := ai.ConsentHintFor(key, ai.ConsentAlways)
	switch {
	case hint.Command != "":
		return hint.Command
	case hint.Setup:
		return "nn setup ai on a terminal, or " + hint.ByHand()
	}
	return hint.ByHand()
}
