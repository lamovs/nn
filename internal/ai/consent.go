package ai

import (
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"slices"
	"strings"
	"sync"

	"github.com/lamovs/nn/internal/config"
)

const (
	ConsentAsk    = "ask"
	ConsentAlways = "always"
	ConsentNever  = "never"
)

var consentValues = schemaEnum("ai.consent.KEY")

// ConsentKey: engine name, except a command profile's own name.
func ConsentKey(name string, p config.Profile) string {
	if p.Engine == config.EngineCommand {
		return name
	}
	return p.Engine
}

// ConsentOf defaults to ask when nothing valid is recorded for key.
func ConsentOf(cfg config.Config, key string) string {
	if v := cfg.AI.Consent[key]; v != "" && slices.Contains(consentValues, v) {
		return v
	}
	return ConsentAsk
}

func ConsentSetting(key string) string {
	return "ai.consent." + config.TOMLKey(key)
}

// ConsentHint is how a message tells the user to set a consent by hand.
type ConsentHint struct {
	// Command is "" unless TOML takes the key bare (letters, digits, _ and -);
	// a dotted key is one such case.
	Command string

	Line string

	// Setup is false for an empty key or one with a dot.
	Setup bool
}

func ConsentHintFor(key, value string) ConsentHint {
	quoted := config.TOMLKey(key)
	hint := ConsentHint{
		Line:  quoted + " = " + config.TOMLString(value),
		Setup: key != "" && !strings.Contains(key, "."),
	}
	if quoted == key {
		hint.Command = "nn config ai.consent." + key + " " + value
	}
	return hint
}

func (h ConsentHint) ByHand() string {
	return "set it by hand under [ai.consent]: " + h.Line
}

// SetConsent: on error nothing is written to the config file.
func SetConsent(key, value string) error {
	if !slices.Contains(consentValues, value) {
		return fmt.Errorf("ai.consent.%s = %s: want %s", config.TOMLKey(key), config.TOMLString(value), strings.Join(consentValues, ", "))
	}
	if key == "" {
		return errors.New("ai.consent: no key")
	}
	manual := func(reason string) error {
		file, err := config.Path()
		if err != nil {
			file = "the config file"
		}
		return &config.ManualEditError{
			File:    file,
			Key:     "ai.consent." + config.TOMLKey(key),
			Section: "ai.consent",
			Line:    config.TOMLKey(key) + " = " + config.TOMLString(value),
			Reason:  reason,
		}
	}
	// config.Set splits its key at every dot, so it cannot name a profile
	// with a dot in its name.
	if strings.Contains(key, ".") {
		return manual("nn config cannot write a key with a dot in its name")
	}
	err := config.Set("ai.consent."+key, value)
	var edit *config.ManualEditError
	switch {
	case err == nil:
		return nil
	case errors.As(err, &edit):
		return edit
	}
	return manual(err.Error())
}

// ConsentError is --ai given for an engine that consent is never for.
type ConsentError struct {
	Key string
}

func (e *ConsentError) Error() string {
	hint := ConsentHintFor(e.Key, ConsentAlways)
	var ways []string
	if hint.Command != "" {
		ways = append(ways, hint.Command)
	}
	if hint.Setup {
		ways = append(ways, "nn setup ai")
	}
	if hint.Command == "" {
		ways = append(ways, hint.ByHand())
	}
	return fmt.Sprintf("%s is never, so nn sends nothing to %s; to allow it: %s", ConsentSetting(e.Key), e.Key, strings.Join(ways, ", or "))
}

type Decision int

const (
	Denied Decision = iota

	// Allowed is the only Decision that comes with a usable Approval.
	Allowed

	// Unasked: ask consent with no terminal to ask on.
	Unasked
)

// Approval is leave to run one call. Only Approve and ReadTransfer mint one
// that allows anything. It is revoked by recording never for its consent
// key, even when that answer cannot be saved.
type Approval struct {
	grant *grant
}

func (a Approval) Call() (Call, error) {
	call, ok := a.allowed()
	if !ok {
		return Call{}, ErrNotApproved
	}
	return call, nil
}

// grant is immutable once given; a transferred grant also binds its one
// request.
type grant struct {
	call          Call
	requestDigest *[32]byte
}

// grants holds every live grant; an Approval allows nothing unless its grant
// is here. Recording never, or a transfer's single use, deletes it.
var grants sync.Map // *grant -> struct{}

func issue(call Call) Approval {
	return issueBound(call, nil)
}

func issueBound(call Call, digest *[32]byte) Approval {
	// Clone: the caller's own command slice must not change what runs.
	call.Profile.Command = slices.Clone(call.Profile.Command)
	g := &grant{call: call, requestDigest: digest}
	grants.Store(g, struct{}{})
	return Approval{grant: g}
}

func (a Approval) allowed() (Call, bool) {
	if a.grant == nil {
		return Call{}, false
	}
	if _, ok := grants.Load(a.grant); !ok {
		return Call{}, false
	}
	call := a.grant.call
	call.Profile.Command = slices.Clone(call.Profile.Command)
	return call, true
}

type Asker interface {
	Interactive() bool

	// Ask returns "" or io.EOF when there is no answer.
	Ask(question string) (string, error)
}

// Approve gates the run on user consent; only Allowed pairs with a usable
// Approval. cfg must not be read concurrently with Approve.
func Approve(cfg *config.Config, call Call, sends string, asker Asker, w io.Writer) (Decision, Approval, error) {
	if call.Profile.Engine == config.EngineCommand && slices.Contains(config.BuiltinProfiles(), call.Name) {
		return Denied, Approval{}, fmt.Errorf("profile %s: built-in profile names cannot use engine command", call.Name)
	}
	key := ConsentKey(call.Name, call.Profile)
	switch ConsentOf(*cfg, key) {
	case ConsentAlways:
		return Allowed, issue(call), nil
	case ConsentNever:
		if call.Explicit {
			return Denied, Approval{}, &ConsentError{Key: key}
		}
		return Denied, Approval{}, nil
	}
	if asker == nil || !asker.Interactive() {
		return Unasked, Approval{}, nil
	}

	answer, err := asker.Ask(consentQuestion(call, key, sends))
	if err != nil && !errors.Is(err, io.EOF) {
		return Denied, Approval{}, fmt.Errorf("ask for consent: %w", err)
	}
	choice := strings.ToLower(strings.TrimSpace(answer))
	switch choice {
	case "once":
		return Allowed, issue(call), nil
	case ConsentAlways, ConsentNever:
		remember(cfg, key, choice)
		if err := SetConsent(key, choice); err != nil {
			reportUnsaved(w, err)
		}
		if choice == ConsentAlways {
			return Allowed, issue(call), nil
		}
	}
	return Denied, Approval{}, nil
}

func remember(cfg *config.Config, key, value string) {
	if cfg.AI.Consent == nil {
		cfg.AI.Consent = map[string]string{}
	}
	cfg.AI.Consent[key] = value
	if value == ConsentNever {
		grants.Range(func(entry, _ any) bool {
			g := entry.(*grant)
			if ConsentKey(g.call.Name, g.call.Profile) == key {
				grants.Delete(g)
			}
			return true
		})
	}
}

func consentQuestion(call Call, key, sends string) string {
	if sends == "" {
		sends = "the request"
	}
	target := call.Profile.Engine
	if call.Profile.Engine == config.EngineCommand {
		target = fmt.Sprintf("profile %s (%s)", call.Name, filepath.Base(Binary(call.Profile)))
	}
	model := "default model"
	if call.Profile.Model != "" {
		model = "model " + call.Profile.Model
	}
	var b strings.Builder
	fmt.Fprintf(&b, "nn is about to send %s to %s, %s.\n", sends, target, model)
	if call.Profile.Engine == engineCodex {
		b.WriteString("Your ~/.codex/AGENTS.md goes with it too, as with every codex call.\n")
	}
	// Consent is per engine: always/never must say they cover every task.
	fmt.Fprintf(&b, "always: send to %s from now on, for every task, not only this one; once: this time only; never: send nothing to %s, now or later, for any task.\n", target, target)
	if ConsentHintFor(key, ConsentAlways).Setup {
		fmt.Fprintf(&b, "always and never are saved as %s; nn setup ai changes it.\n", ConsentSetting(key))
	} else {
		fmt.Fprintf(&b, "nn cannot save always or never as %s, so they hold for this call only; to keep one, %s, or %s.\n",
			ConsentSetting(key), ConsentHintFor(key, ConsentAlways).ByHand(), ConsentHintFor(key, ConsentNever).Line)
	}
	b.WriteString("Send? [always/once/never]")
	return b.String()
}

func reportUnsaved(w io.Writer, err error) {
	if w == nil {
		return
	}
	fmt.Fprintf(w, "nn: consent not saved: %v\n", err)
	var edit *config.ManualEditError
	if errors.As(err, &edit) {
		fmt.Fprintf(w, "set it by hand, under [%s]:\n%s\n", edit.Section, edit.Line)
	}
}
