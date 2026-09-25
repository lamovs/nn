package ai

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"github.com/lamovs/nn/internal/config"
)

const profileEnv = "NN_AI"

var efforts = schemaEnum("ai.profiles.NAME.effort")

func schemaEnum(key string) []string {
	k, err := config.Lookup(key)
	if err != nil || len(k.Enum) == 0 {
		panic("ai: the config schema has no enum " + key)
	}
	return k.Enum
}

type Overrides struct {
	AI      bool   // --ai given, with or without a profile
	Profile string // --ai=PROFILE; implies AI

	Model  string // --model: replaces the profile's model
	Effort string // --effort: low, medium, high or max
	NoAI   bool
}

func (o Overrides) Explicit() bool { return o.AI || o.Profile != "" }

type Call struct {
	Task string

	Name string // the profile the task runs on
	From string // what chose it: --ai, NN_AI, ai.tasks.TASK.profile or ai.profile

	Profile config.Profile // Name, with --model/--effort and timeout settled

	Explicit bool // --ai given: turns consent never from a quiet no into an error
}

// Resolve never runs anything, so nn doctor can use it.
func Resolve(cfg config.Config, task string, o Overrides) (Call, error) {
	if !slices.Contains(config.Tasks(), task) {
		return Call{}, fmt.Errorf("ai: unknown task %q", task)
	}
	if o.NoAI {
		return Call{}, ErrOff
	}
	if o.Explicit() && cfg.AI.Tasks[task].Run == "never" {
		return Call{}, fmt.Errorf("ai.tasks.%s.run is never; change it to flag or always before using --ai", task)
	}
	if o.Effort != "" && !slices.Contains(efforts, o.Effort) {
		return Call{}, fmt.Errorf("--effort=%s: want %s", o.Effort, strings.Join(efforts, ", "))
	}

	c := Call{Task: task, Explicit: o.Explicit()}
	env := strings.TrimSpace(os.Getenv(profileEnv))
	taskKey := "ai.tasks." + task + ".profile"
	var named string // how an unknown profile is named in the error
	switch {
	case o.Profile != "":
		c.Name, c.From = o.Profile, "--ai"
		named = "--ai=" + o.Profile
	case env != "":
		c.Name, c.From = env, profileEnv
		named = profileEnv + "=" + env
	case cfg.AI.Tasks[task].Profile != "":
		c.Name, c.From = cfg.AI.Tasks[task].Profile, taskKey
		named = taskKey + " = " + config.TOMLString(c.Name)
	default:
		c.Name, c.From = cfg.AI.Profile, "ai.profile"
		named = "ai.profile = " + config.TOMLString(c.Name)
	}
	p, ok := cfg.AI.Profiles[c.Name]
	if !ok {
		return Call{}, fmt.Errorf("%s: no such profile (profiles: %s)", named, strings.Join(profileNames(cfg), ", "))
	}
	if p.Engine == config.EngineCommand && len(p.Command) == 0 {
		return Call{}, fmt.Errorf("profile %s: engine command needs a command", c.Name)
	}

	p.Command = slices.Clone(p.Command)
	if o.Model != "" {
		p.Model = o.Model
	}
	if o.Effort != "" {
		p.Effort = o.Effort
	}
	if p.Timeout <= 0 {
		p.Timeout = cfg.AI.Timeout
	}
	if p.Timeout <= 0 {
		p.Timeout = config.Default().AI.Timeout
	}
	c.Profile = p
	return c, nil
}

func profileNames(cfg config.Config) []string {
	builtin := config.BuiltinProfiles()
	var names, rest []string
	for _, name := range builtin {
		if _, ok := cfg.AI.Profiles[name]; ok {
			names = append(names, name)
		}
	}
	for name := range cfg.AI.Profiles {
		if !slices.Contains(builtin, name) {
			rest = append(rest, name)
		}
	}
	sort.Strings(rest)
	return append(names, rest...)
}

// engineEffort: codex calls the top effort xhigh.
func engineEffort(engine, effort string) string {
	if engine == engineCodex && effort == "max" {
		return "xhigh"
	}
	return effort
}

func Binary(p config.Profile) string {
	if p.Engine != config.EngineCommand {
		return p.Engine
	}
	if len(p.Command) == 0 {
		return ""
	}
	return p.Command[0]
}

// LookBinary: not found is ErrNotInstalled.
func LookBinary(p config.Profile) (string, error) {
	name := Binary(p)
	if name == "" {
		return "", errors.New("engine command needs a command")
	}
	path, err := exec.LookPath(name)
	switch {
	case errors.Is(err, exec.ErrNotFound), errors.Is(err, exec.ErrDot), errors.Is(err, fs.ErrNotExist):
		return "", fmt.Errorf("%s is %w", name, ErrNotInstalled)
	case err != nil:
		return "", err
	}
	// A relative path would name something else in the engine's own dir.
	return filepath.Abs(path)
}

func AcceptsImage(p config.Profile) bool {
	if p.Engine != config.EngineCommand {
		return true
	}
	return len(p.Command) > 1 && slices.ContainsFunc(p.Command[1:], func(arg string) bool {
		return strings.Contains(arg, "{image}")
	})
}

func TakesImage(task string) bool { return task == "shot" }

var taskSends = map[string]string{
	"shot":   "the screenshot, its OCR text and existing vault tags",
	"title":  "new note text or OCR, an optional source image and existing vault tag names",
	"ask":    "your question and excerpts of the notes nn finds for it",
	"filter": "your instruction and the text piped to nn ai",
	"last":   "the previous command you provide and its optional output",
	"triage": "excerpts and metadata of selected inbox notes and related notes",
	"url":    "the link, the text of the page it points to and existing vault tag names",
	"digest": "your selection and excerpts and metadata of the notes the digest covers",
}

// Sends: consent is per engine, not per task, so it must cover every task.
func Sends(task string) string { return taskSends[task] }
