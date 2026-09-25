package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"slices"

	"github.com/lamovs/nn/internal/ai"
	"github.com/lamovs/nn/internal/cli"
	"github.com/lamovs/nn/internal/config"
	"github.com/lamovs/nn/internal/output"
	"github.com/lamovs/nn/internal/platform"
	"github.com/lamovs/nn/internal/state"
	"github.com/lamovs/nn/internal/vault"
)

func init() {
	register(command{
		help: cli.Help{
			Verb:    "doctor",
			Summary: "check that nn is set up correctly",
			Examples: []cli.Example{
				{Cmd: "nn doctor", What: "reports problems with the config, vault, OCR, platform tools and AI engines"},
				{Cmd: "nn doctor --fix", What: "reports problems and creates missing vault directories"},
			},
			Sections: []cli.HelpSection{
				{Title: "Notes", Items: []string{
					"state.json.lock next to state.json is normal - it is state's advisory lock, not a problem.",
					"Over SSH without a graphical session, the screenshot and clipboard checks are informational and do not affect the exit code.",
					"obsidian.check = false skips the Obsidian registration check entirely: no row, no effect on the exit code.",
					"On Linux, shot.tool set to a specific tool (not auto) checks that tool by name instead of whichever one the session would otherwise pick.",
					"The ai rows cover ai.profile and the profile each task runs on, NN_AI included; no model is run, nothing is asked and the config is not written.",
					"The ai rows affect the exit code when consent is ask (\"not approved\": nn setup ai, or the line to add by hand for a profile name with a dot), a program is not found while consent is ask or always, a prompt_file cannot be used, shot uses a command without {image} (unless its run or consent is never), or NN_AI names an unknown profile. Consent never, an approved program found, and codex's cost per call are informational.",
				}},
			},
			SeeAlso: []string{"setup", "config"},
		},
		run: cmdDoctor,
	})
}

func cmdDoctor(inv *invocation) int {
	outOpt, rest, err := output.ParseFlags(inv.refinements)
	if err != nil {
		return inv.misuse("%v", err)
	}
	fix := false
	for _, r := range rest {
		if r != "--fix" {
			return inv.misuseWord("unknown option ", r)
		}
		fix = true
	}
	if len(inv.data) > 0 {
		return inv.misuse("doctor takes no arguments")
	}

	cfg, problems, cfgErr := config.Load()
	var checks []platform.Check
	checks = append(checks, configChecks(cfg, problems, cfgErr)...)
	if cfgErr == nil {
		v, verr := vault.Open(cfg)
		checks = append(checks, rootCheck(cfg, verr))
		if verr == nil {
			// Resolved before --fix runs, which vault.EnsureDir would refuse anyway on a bad inbox.
			real, inside, resolveErr := v.RealDir(v.Inbox)
			var fixErr error
			if fix && inside {
				fixErr = applyDoctorFix(v)
			}
			inbox := inboxCheck(v, real, inside, resolveErr)
			checks = append(checks, inbox)
			// Gated on the row, not on inside: a failed inbox row already covers both subdirectories.
			if inbox.OK {
				checks = append(checks, inboxSubdirChecks(v)...)
			}
			if fixErr != nil {
				checks = append(checks, fixCheck(fixErr))
			}
		}
		checks = append(checks, stateDirCheck())
		checks = append(checks, platformChecks(inv.ctx, cfg)...)
		checks = append(checks, aiChecks(inv.ctx, cfg)...)
	}
	checks = append(checks, shellIntegrationHint())

	hasProblems := false
	for _, c := range checks {
		if c.OK {
			continue
		}
		if linuxHeadless() && (c.Name == "screenshot" || c.Name == "clipboard") {
			continue
		}
		hasProblems = true
	}

	if err := output.Emit(inv.stdout, outOpt, doctorRows(checks), output.Spec[doctorRow]{
		Text:      writeChecks,
		TSVHeader: []string{"name", "ok", "detail"},
		TSV:       func(c doctorRow) []string { return []string{c.Name, fmt.Sprint(c.OK), c.Detail} },
	}); err != nil {
		return inv.fail(err)
	}
	if hasProblems {
		// Distinct from output's own "nothing found" exit code, which also happens to be 1.
		return 1
	}
	return output.ExitOK
}

// doctorRow restates platform.Check with lowercase json keys; Fix is never nil, so ".fix[]" works.
type doctorRow struct {
	Name   string   `json:"name"`
	OK     bool     `json:"ok"`
	Detail string   `json:"detail"`
	Fix    []string `json:"fix"`
}

func doctorRows(checks []platform.Check) []doctorRow {
	rows := make([]doctorRow, len(checks))
	for i, c := range checks {
		fix := c.Fix
		if fix == nil {
			fix = []string{}
		}
		rows[i] = doctorRow{Name: c.Name, OK: c.OK, Detail: c.Detail, Fix: fix}
	}
	return rows
}

func writeChecks(w io.Writer, checks []doctorRow) error {
	for _, c := range checks {
		mark := "ok"
		if !c.OK {
			mark = "!!"
		}
		if _, err := fmt.Fprintf(w, "[%s] %s: %s\n", mark, c.Name, c.Detail); err != nil {
			return err
		}
		for _, f := range c.Fix {
			if _, err := fmt.Fprintf(w, "      %s\n", f); err != nil {
				return err
			}
		}
	}
	return nil
}

func configChecks(cfg config.Config, problems []config.Problem, err error) []platform.Check {
	var fatal config.Problem
	isFatal := errors.As(err, &fatal)
	if err != nil && !isFatal {
		check := platform.Check{Name: "config", Detail: err.Error()}
		if pathErr := (*fs.PathError)(nil); errors.As(err, &pathErr) && cfg.Path != "" {
			fix := fmt.Sprintf("make %s readable, or point NN_CONFIG at another file", cfg.Path)
			if info, statErr := os.Stat(cfg.Path); statErr == nil && info.IsDir() {
				fix = fmt.Sprintf("%s is a directory; point NN_CONFIG at a file", cfg.Path)
			}
			check.Fix = []string{fix}
		}
		return []platform.Check{check}
	}
	head := platform.Check{Name: "config", OK: true, Detail: cfg.Path}
	if isFatal {
		head = problemCheck(fatal)
	}
	checks := []platform.Check{head}
	for _, p := range problems {
		if p.Fatal {
			continue
		}
		checks = append(checks, problemCheck(p))
	}
	return checks
}

func problemCheck(p config.Problem) platform.Check {
	c := platform.Check{Name: "config", Detail: p.Message, OK: p.Kind == config.KindReserved}
	if p.Fix != "" {
		c.Fix = []string{p.Fix}
	}
	return c
}

// aiChecks checks the profiles actually in use, without running any engine or writing to config.
func aiChecks(ctx context.Context, cfg config.Config) []platform.Check {
	var checks []platform.Check
	type use struct {
		name    string
		profile config.Profile
	}
	var uses []use
	inUse := func(name string, p config.Profile) {
		if !slices.ContainsFunc(uses, func(u use) bool { return u.name == name }) {
			uses = append(uses, use{name, p})
		}
	}
	if p, ok := cfg.AI.Profiles[cfg.AI.Profile]; ok && !commandless(p) {
		inUse(cfg.AI.Profile, p)
	}
	calls := map[string]ai.Call{}
	var refused []string
	for _, task := range config.Tasks() {
		call, err := ai.Resolve(cfg, task, ai.Overrides{})
		if err != nil {
			if resolvesToCommandless(cfg, task) {
				continue
			}
			if msg := err.Error(); !slices.Contains(refused, msg) {
				refused = append(refused, msg)
				checks = append(checks, platform.Check{Name: "ai", Detail: msg, Fix: []string{
					"name a profile that exists, or declare it under [ai.profiles]",
				}})
			}
			continue
		}
		calls[task] = call
		inUse(call.Name, call.Profile)
	}

	seen := map[string]bool{}
	for _, u := range uses {
		key := ai.ConsentKey(u.name, u.profile)
		if seen[key] {
			continue
		}
		seen[key] = true
		checks = append(checks, aiEngineCheck(cfg, key, u.profile))
		if u.profile.Engine == "codex" && ai.ConsentOf(cfg, key) != ai.ConsentNever {
			checks = append(checks, platform.Check{Name: "ai", OK: true, Detail: codexOverhead})
		}
	}

	for _, task := range config.Tasks() {
		call, ok := calls[task]
		if ok && ai.TakesImage(task) && !ai.AcceptsImage(call.Profile) && cfg.AI.Tasks[task].Run != "never" &&
			ai.ConsentOf(cfg, ai.ConsentKey(call.Name, call.Profile)) != ai.ConsentNever {
			checks = append(checks, aiImageCheck(task, call))
		}
		if err := ai.CheckPromptFile(cfg, task); err != nil {
			checks = append(checks, platform.Check{Name: "ai", Detail: err.Error(), Fix: []string{
				fmt.Sprintf("fix the file, or stop using it: nn config ai.tasks.%s.prompt_file \"\"", task),
			}})
		}
	}
	return checks
}

func commandless(p config.Profile) bool {
	return p.Engine == config.EngineCommand && len(p.Command) == 0
}

// resolvesToCommandless re-runs Resolve with a stand-in command in each commandless profile, since a refusal names none.
func resolvesToCommandless(cfg config.Config, task string) bool {
	standIn := maps.Clone(cfg.AI.Profiles)
	for name, p := range standIn {
		if commandless(p) {
			p.Command = []string{name}
			standIn[name] = p
		}
	}
	probe := cfg
	probe.AI.Profiles = standIn
	call, err := ai.Resolve(probe, task, ai.Overrides{})
	return err == nil && commandless(cfg.AI.Profiles[call.Name])
}

// aiEngineCheck is the row of one ai.consent entry in use, whose program is the one profile p runs.
func aiEngineCheck(cfg config.Config, key string, p config.Profile) platform.Check {
	c := platform.Check{Name: "ai", OK: true}
	consent := ai.ConsentOf(cfg, key)
	if consent == ai.ConsentNever {
		c.Detail = fmt.Sprintf("%s is off: %s is never", key, ai.ConsentSetting(key))
		return c
	}
	path, err := ai.LookBinary(p)
	switch {
	case err != nil:
		c.Detail = err.Error()
		if ai.Binary(p) != key {
			c.Detail = key + ": " + c.Detail
		}
		c.OK = false
		c.Fix = []string{fmt.Sprintf("install %s, or add the directory it is in to PATH", key)}
		if p.Engine == config.EngineCommand {
			c.Fix = []string{fmt.Sprintf("install %s, or fix the command of profile %s", ai.Binary(p), key)}
		}
	case consent == ai.ConsentAsk:
		c.OK = false
		c.Detail = key + " not approved"
		c.Fix = []string{"nn setup ai"}
		if hint := ai.ConsentHintFor(key, ai.ConsentAlways); !hint.Setup {
			c.Fix = []string{hint.ByHand()}
		}
	default:
		c.Detail = key + ": " + path
	}
	return c
}

// aiImageCheck is the row of task, which sends an image, on a profile that takes none.
func aiImageCheck(task string, call ai.Call) platform.Check {
	fix := []string{fmt.Sprintf("put {image} in the command of profile %s, where the path of the image goes", call.Name)}
	if call.From == "NN_AI" {
		fix = append(fix, "or set NN_AI to a profile that takes images")
	} else {
		fix = append(fix, fmt.Sprintf("or run %s on another profile: nn config ai.tasks.%s.profile claude", task, task))
	}
	fix = append(fix, fmt.Sprintf("or turn AI off for %s: nn config ai.tasks.%s.run never", task, task))
	return platform.Check{
		Name:   "ai",
		Detail: fmt.Sprintf("%s sends an image, but profile %s takes none: its command has no {image}", task, call.Name),
		Fix:    fix,
	}
}

func rootCheck(cfg config.Config, err error) platform.Check {
	if err != nil {
		return platform.Check{Name: "vault root", Detail: err.Error(), Fix: []string{
			"create the directory, or set it with nn config vault.root PATH",
		}}
	}
	return platform.Check{Name: "vault root", OK: true, Detail: cfg.Vault.Root}
}

// inboxCheck probes only the nearest existing ancestor; nn creates the inbox itself on first capture.
func inboxCheck(v *vault.Vault, real string, inside bool, resolveErr error) platform.Check {
	c := platform.Check{Name: "inbox"}
	dir := v.Abs(v.Inbox)
	if resolveErr != nil {
		c.Detail = resolveErr.Error()
		return c
	}
	if !inside {
		c.Detail = fmt.Sprintf("%s resolves to %s, outside the vault: nn can neither write there nor find what it wrote", dir, real)
		c.Fix = []string{
			"replace it with a real directory under " + v.Root,
			"or point vault.root at the vault that really holds it: nn config vault.root PATH",
		}
		return c
	}
	existing, err := writableAncestor(dir, func(target string) bool {
		_, inside, err := v.RealPath(target)
		return err == nil && inside
	})
	if err != nil {
		c.Detail = err.Error()
		c.Fix = blockedPathFix(err)
		return c
	}
	if err := probeWritable(existing); err != nil {
		c.Detail = fmt.Sprintf("%s is not writable: %v", existing, err)
		c.Fix = []string{"fix permissions on " + existing}
		return c
	}
	c.OK = true
	c.Detail = dir
	if existing != dir {
		c.Detail += " (created on first capture)"
	}
	return c
}

// inboxSubdirChecks reports assets or _templates when either leads out of the
// vault or cannot be followed although the inbox itself is fine; only
// offenders get a row. The caller runs it only after the inbox row passed.
func inboxSubdirChecks(v *vault.Vault) []platform.Check {
	var checks []platform.Check
	for _, sub := range []string{vault.AssetsDir, vault.TemplatesDir} {
		rel := path.Join(v.Inbox, sub)
		real, inside, err := v.RealDir(rel)
		switch {
		case err != nil:
			checks = append(checks, platform.Check{Name: "inbox/" + sub, Detail: err.Error()})
		case !inside:
			checks = append(checks, platform.Check{
				Name:   "inbox/" + sub,
				Detail: fmt.Sprintf("%s resolves to %s, outside the vault: nn can neither write there nor find what it wrote", v.Abs(rel), real),
				Fix:    []string{"move what is there back and replace it with a real directory under " + v.Abs(v.Inbox)},
			})
		}
	}
	return checks
}

// fixCheck folds a --fix that stopped partway into the report, like any other check.
func fixCheck(err error) platform.Check {
	return platform.Check{
		Name:   "--fix",
		Detail: "did not finish: " + err.Error(),
		Fix:    []string{"fix what it names, then re-run: nn doctor --fix"},
	}
}

func stateDirCheck() platform.Check {
	c := platform.Check{Name: "state directory"}
	p, err := state.Path()
	if err != nil {
		c.Detail = err.Error()
		return c
	}
	dir := filepath.Dir(p)
	// No vault boundary to check: the state directory lives outside every vault.
	existing, err := writableAncestor(dir, nil)
	if err != nil {
		c.Detail = err.Error()
		c.Fix = blockedPathFix(err)
		return c
	}
	if err := probeWritable(existing); err != nil {
		c.Detail = fmt.Sprintf("%s is not writable: %v", existing, err)
		return c
	}
	c.OK = true
	c.Detail = p
	return c
}

// platformChecks indirects platform.Checks so tests can substitute a canned result.
var platformChecks = platform.Checks

func shellIntegrationHint() platform.Check {
	return platform.Check{Name: "shell integration", OK: true, Detail: `run "nn setup zsh" to add completion and shortcuts`}
}

// linuxHeadless reports a Linux session with no display (SSH), where screenshot/clipboard are expected to fail.
func linuxHeadless() bool {
	return runtime.GOOS == "linux" && os.Getenv("DISPLAY") == "" && os.Getenv("WAYLAND_DISPLAY") == ""
}

// blockedPath is a writableAncestor failure carrying its own fix, since it depends on what is blocking.
type blockedPath struct {
	msg string
	fix []string
}

func (e *blockedPath) Error() string { return e.msg }

// blockedPathFix returns err's fix if it is a blockedPath, nil otherwise.
func blockedPathFix(err error) []string {
	var b *blockedPath
	if errors.As(err, &b) {
		return b.fix
	}
	return nil
}

// writableAncestor never creates a directory; Lstat catches a dangling symlink that Stat alone misreads as empty.
func writableAncestor(dir string, within func(target string) bool) (string, error) {
	d := dir
	for {
		info, err := os.Stat(d)
		if err == nil {
			if !info.IsDir() {
				return "", &blockedPath{
					msg: fmt.Sprintf("%s is not a directory", d),
					fix: []string{"remove " + d + " or move it aside"},
				}
			}
			return d, nil
		}
		if !errors.Is(err, fs.ErrNotExist) {
			return "", err
		}
		if _, lerr := os.Lstat(d); lerr == nil {
			return "", danglingLink(d, within)
		}
		parent := filepath.Dir(d)
		if parent == d {
			return "", fmt.Errorf("no existing ancestor of %s", dir)
		}
		d = parent
	}
}

// danglingLink offers to recreate the target only when within allows it, never one outside the vault boundary.
func danglingLink(d string, within func(target string) bool) error {
	target, err := os.Readlink(d)
	if err != nil {
		return &blockedPath{msg: fmt.Sprintf("%s cannot be read: %v", d, err)}
	}
	fix := []string{"remove " + d + ", and nn creates the directory on its own"}
	offer := within == nil
	if !offer {
		if abs := linkTarget(d, target); abs != "" {
			offer = within(abs)
		}
	}
	if offer {
		fix = append(fix, "or create "+target+", if that is where it should live")
	}
	return &blockedPath{
		msg: fmt.Sprintf("%s is a symlink to %s, which does not exist: nothing will create a directory at that name", d, target),
		fix: fix,
	}
}

// linkTarget resolves target relative to link's real directory, matching what the kernel follows.
func linkTarget(link, target string) string {
	if filepath.IsAbs(target) {
		return target
	}
	dir, err := filepath.EvalSymlinks(filepath.Dir(link))
	if err != nil {
		return ""
	}
	return filepath.Join(dir, target)
}

func probeWritable(dir string) error {
	f, err := os.CreateTemp(dir, ".nn-doctor-*")
	if err != nil {
		return err
	}
	name := f.Name()
	f.Close()
	return os.Remove(name)
}

// applyDoctorFix writes only through the vault's entry points and stops at
// the first refusal: EnsureDir's check-then-create leaves a window for a
// planted link, so going on past a refusal could scatter directories
// outside the vault.
func applyDoctorFix(v *vault.Vault) error {
	for _, sub := range []string{"", vault.AssetsDir, vault.TemplatesDir} {
		if _, err := v.EnsureDir(path.Join(v.Inbox, sub)); err != nil {
			return err
		}
	}
	// WriteNew refuses an existing name rather than following a symlink there.
	err := v.WriteNew(path.Join(v.Inbox, vault.TemplatesDir, "hotkey.md"), []byte(hotkeyTemplate))
	if errors.Is(err, fs.ErrExist) {
		return nil
	}
	return err
}

const hotkeyTemplate = `---
tags: [hotkey]
date: {{date}}
time: "{{time}}"
where: {{where}}
repo: {{repo}}
---
## {{title}}

- Shortcut:
- Action:
`
