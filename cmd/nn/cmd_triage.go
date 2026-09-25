package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"unicode"

	"github.com/lamovs/nn/internal/ai"
	"github.com/lamovs/nn/internal/app"
	"github.com/lamovs/nn/internal/cli"
	"github.com/lamovs/nn/internal/output"
	"github.com/lamovs/nn/internal/triage"
)

func init() {
	register(command{help: cli.Help{
		Verb: "triage", Summary: "review inbox metadata and link suggestions",
		Examples: []cli.Example{
			{Cmd: `nn triage`, What: "review a bounded inbox batch without changing notes"},
			{Cmd: `nn triage nn/idea.md nn/workflow.md --apply`, What: "select additions, inspect the exact diff and confirm"},
			{Cmd: `nn triage --ai=codex --effort high`, What: "use a selected profile for the read-only plan"},
		},
		Sections: []cli.HelpSection{
			{Title: "Plan and selection", Items: []string{
				"Optional NOTE arguments are exact inbox note paths. Without them, nn chooses a deterministic bounded batch and names the included notes; it does not claim to review the entire inbox.",
				"The default command prints a plan and changes no notes. --apply requires interactive stdin and stderr before model work. It creates a fresh plan, asks for individual action IDs, shows the exact combined diff on stderr, and asks for confirmation with default no.",
				"Choose all only by typing all; an empty selection or a negative confirmation changes nothing. There is no --yes, unattended apply, saved-plan reuse or model call after selection.",
			}},
			{Title: "Allowed additions", Items: []string{
				"Suggestions can add a missing title, missing trailing keyword tags, and verified links to supplied existing notes. Topics and reasons are informational. Manual titles, frontmatter, original body and paths are preserved.",
				"Notes are not moved, renamed, deleted or merged. Stale source or target snapshots stop application; no automatic rollback or retry overwrites newer edits. Confirmed application creates private backups and a receipt, with its recovery directory reported on stderr.",
			}},
			{Title: "AI and limits", Items: []string{
				"--ai[=PROFILE], --model MODEL and --effort low|medium|high|max select the triage profile. ai.tasks.triage.prompt_file replaces the built-in prompt. The command waits regardless of ai.mode.",
				"ai.context.notes, ai.context.chars and ai.context.search_rounds bound the selected inbox notes, related excerpts and additional local searches. One engine consent covers this bounded model session; applying changes requires a separate explicit confirmation.",
				"One profile timeout covers model rounds and search refinement. Time spent reviewing the proposed changes is outside that model deadline. No live OCR or image interpretation is performed.",
			}},
			{Title: "Exit status", Items: []string{
				"0: plan shown, changes applied, or application declined; 2: input, consent, model, preview, write or recovery failure; 130: cancelled. Partial application reports each note's status and the recovery directory; already completed writes cannot be rolled back automatically.",
			}},
		}, SeeAlso: []string{"ask", "ls", "tags", "setup"},
	}, run: cmdTriage})
}

type triageOptions struct {
	ai    ai.Overrides
	apply bool
}

func parseTriageOptions(args []string) (triageOptions, error) {
	var opt triageOptions
	var modelArgs []string
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--apply" {
			opt.apply = true
			continue
		}
		modelArgs = append(modelArgs, arg)
		if (arg == "--model" || arg == "--effort") && i+1 < len(args) {
			i++
			modelArgs = append(modelArgs, args[i])
		}
	}
	var err error
	opt.ai, err = parseAskOptions(modelArgs)
	return opt, err
}

var triageAsker = func(ctx context.Context) ai.Asker { return askAsker{ctx} }

func cmdTriage(inv *invocation) int {
	opt, err := parseTriageOptions(inv.refinements)
	if err != nil {
		return inv.misuse("%v", err)
	}
	fail := func(err error) int {
		if inv.interrupted() || errors.Is(err, context.Canceled) {
			fmt.Fprintln(inv.stderr, "nn: triage: cancelled")
			return output.ExitInterrupted
		}
		return inv.fail(errors.New(triageDisplay(err.Error())))
	}
	if err := inv.ctx.Err(); err != nil {
		return fail(err)
	}
	asker := triageAsker(inv.ctx)
	if opt.apply && !asker.Interactive() {
		return fail(errors.New("--apply requires interactive stdin and stderr; run without --apply to review a plan"))
	}
	env, err := app.Open()
	if err != nil {
		return fail(err)
	}
	call, err := ai.Resolve(env.Cfg, "triage", opt.ai)
	if err != nil {
		return fail(err)
	}
	session, err := triage.Prepare(inv.ctx, env.Vault, triage.Options{
		Limits: env.Cfg.AI.Context, NoLayoutFallback: !env.Cfg.Search.LayoutFallback,
		Notes: inv.data,
	})
	if err != nil {
		return fail(err)
	}
	if session.Empty() {
		if _, err := io.WriteString(inv.stdout, "No inbox notes to review.\n"); err != nil {
			return fail(err)
		}
		return output.ExitOK
	}
	prompt, err := ai.Prompt(env.Cfg, "triage")
	if err != nil {
		return fail(err)
	}
	disclosure := ai.Sends("triage") + "; " + session.ContextDescription()
	decision, approval, err := ai.Approve(&env.Cfg, call, disclosure, asker, inv.stderr)
	if err != nil {
		return fail(err)
	}
	if decision != ai.Allowed {
		return fail(errors.New("AI consent was not granted; configure it with nn setup ai"))
	}
	plan, err := session.Run(inv.ctx, approval, call, prompt, nil)
	if err != nil {
		return fail(err)
	}
	if err := inv.ctx.Err(); err != nil {
		return fail(err)
	}
	actions := plan.Actions()
	w := inv.stdout
	if opt.apply {
		w = inv.stderr
	}
	if err := writeTriagePlan(w, plan.Description(), actions); err != nil {
		return fail(err)
	}
	if !opt.apply || len(actions) == 0 {
		return output.ExitOK
	}
	answer, err := asker.Ask("Select action IDs (space/comma separated), all, or none [none]:")
	if err != nil {
		return fail(err)
	}
	if err := inv.ctx.Err(); err != nil {
		return fail(err)
	}
	ids := triageSelection(answer, actions)
	if len(ids) == 0 {
		if _, err := io.WriteString(inv.stderr, "No changes applied.\n"); err != nil {
			return fail(err)
		}
		return output.ExitOK
	}
	selection, err := plan.Select(ids)
	if err != nil {
		return fail(err)
	}
	preview, err := selection.Preview()
	if err != nil {
		return fail(err)
	}
	if err := inv.ctx.Err(); err != nil {
		return fail(err)
	}
	// stderr is the interactive surface checked before model work. A stdout
	// pipe cannot hide the exact selected changes from the confirmation prompt.
	if _, err := io.WriteString(inv.stderr, preview); err != nil {
		return fail(err)
	}
	if !strings.HasSuffix(preview, "\n") {
		if _, err := io.WriteString(inv.stderr, "\n"); err != nil {
			return fail(err)
		}
	}
	answer, err = asker.Ask("Apply exactly these selected additions? [y/N]")
	if err != nil {
		return fail(err)
	}
	if err := inv.ctx.Err(); err != nil {
		return fail(err)
	}
	switch strings.ToLower(strings.TrimSpace(answer)) {
	case "y", "yes":
	case "", "n", "no":
		if _, err := io.WriteString(inv.stderr, "No changes applied.\n"); err != nil {
			return fail(err)
		}
		return output.ExitOK
	default:
		return fail(errors.New("please answer y or n; no changes applied"))
	}
	if err := inv.ctx.Err(); err != nil {
		return fail(err)
	}
	report, applyErr := selection.Apply(inv.ctx)
	postSaveTriage(inv.ctx, env, report)
	reportErr := writeTriageReport(inv, report, ids, actions)
	if err := errors.Join(applyErr, reportErr); err != nil {
		return fail(err)
	}
	return output.ExitOK
}

func postSaveTriage(ctx context.Context, env *app.Env, report triage.ApplyReport) {
	seen := make(map[string]bool)
	for _, note := range report.Notes {
		if note.Changed && !seen[note.Path] {
			seen[note.Path] = true
			env.PostSave(ctx, note.Path, "append")
		}
	}
}

func writeTriagePlan(w io.Writer, description string, actions []triage.Action) error {
	var b strings.Builder
	b.WriteString(description)
	if !strings.HasSuffix(description, "\n") {
		b.WriteByte('\n')
	}
	if len(actions) > 0 {
		b.WriteString("\nAvailable additions:\n")
		for _, action := range actions {
			fmt.Fprintf(&b, "[%s] %s %q: %q\n", triageDisplay(action.ID), triageDisplay(action.Kind), action.Path, action.Summary)
		}
	}
	_, err := io.WriteString(w, b.String())
	return err
}

func triageSelection(answer string, actions []triage.Action) []string {
	answer = strings.TrimSpace(answer)
	switch strings.ToLower(answer) {
	case "", "none", "no", "n":
		return nil
	case "all":
		ids := make([]string, 0, len(actions))
		for _, action := range actions {
			ids = append(ids, action.ID)
		}
		return ids
	default:
		return strings.FieldsFunc(answer, func(r rune) bool { return r == ',' || unicode.IsSpace(r) })
	}
}

func writeTriageReport(inv *invocation, report triage.ApplyReport, ids []string, actions []triage.Action) error {
	var reportErr error
	if report.RecoveryDir != "" {
		_, reportErr = fmt.Fprintf(inv.stderr, "Recovery directory: %q\n", report.RecoveryDir)
	}
	selected := make(map[string]bool, len(ids))
	for _, id := range ids {
		selected[id] = true
	}
	byPath := make(map[string][]string)
	for _, action := range actions {
		if selected[action.ID] {
			byPath[action.Path] = append(byPath[action.Path], action.ID)
		}
	}
	var b strings.Builder
	for _, note := range report.Notes {
		fmt.Fprintf(&b, "%s %q [%s]", triageDisplay(note.Status), note.Path, triageDisplay(strings.Join(byPath[note.Path], ", ")))
		if note.Error != "" {
			fmt.Fprintf(&b, ": %q", note.Error)
		}
		b.WriteByte('\n')
	}
	_, err := io.WriteString(inv.stdout, b.String())
	return errors.Join(reportErr, err)
}

func triageDisplay(text string) string {
	quoted := strconv.Quote(text)
	return quoted[1 : len(quoted)-1]
}
