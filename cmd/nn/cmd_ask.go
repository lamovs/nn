package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/lamovs/nn/internal/ai"
	"github.com/lamovs/nn/internal/app"
	"github.com/lamovs/nn/internal/ask"
	"github.com/lamovs/nn/internal/cli"
	"github.com/lamovs/nn/internal/links"
	"github.com/lamovs/nn/internal/output"
)

func init() {
	register(command{
		help: cli.Help{
			Verb: "ask", Summary: "answer a question using excerpts of your notes",
			Examples: []cli.Example{
				{Cmd: `nn ask "How did I clear the Docker build cache?"`, What: "wait for an answer with links to supporting notes"},
				{Cmd: `nn ask "What did I decide about backups?" --save`, What: "also keep the answer as an inbox note linking its sources"},
				{Cmd: `nn ask "What did I decide about backups?" --ai=codex --effort high`, What: "use a selected profile and reasoning effort"},
				{Cmd: `nn ask -- -cache options`, What: "treat words starting with a dash as question text"},
			},
			Sections: []cli.HelpSection{
				{Title: "Question and answer", Items: []string{
					"Give the question as command-line words, before any option. nn searches note titles, bodies, tags and saved OCR locally, then sends bounded excerpts to the model. Matching is lexical, not semantic. Notes saved by nn ask --save or nn digest --save are never used as sources.",
					"The answer is printed to stdout with verified source IDs and Obsidian links. Missing evidence is reported explicitly. Without --save, notes, links and open history are not changed.",
					"--save then creates one new inbox note titled with the question: the answer with the same source numbers and a Sources list of verified relative wikilinks, via: ask, no tags. Its path is printed to stderr. An answer without enough evidence is not saved.",
					"The command waits for the answer regardless of ai.mode. It does not read question text from stdin or start a background task.",
				}},
				{Title: "AI and limits", Items: []string{
					"--ai[=PROFILE] selects a profile; --model MODEL and --effort low|medium|high|max override it. The ask task requests AI even without --ai, so consent=never is an explicit error.",
					"ai.context.notes and ai.context.chars bound all excerpts for the question. ai.context.search_rounds allows a bounded number of additional local searches requested by the model. One profile timeout covers the full answering session; the save after the answer is not covered.",
					"Consent is requested once for the question and all bounded follow-up excerpts. Configure consent with nn setup ai when running without a terminal. ai.tasks.ask.prompt_file can replace the built-in prompt.",
				}},
				{Title: "Exit status", Items: []string{
					"0: an answer with sources, saved with --save; 1: insufficient evidence, possibly with a supported partial answer, never saved; 2: invalid input, refusal, model or save failure; 130: cancelled. A save failure, or a cancel during --save, keeps the printed answer on stdout; other failures leave stdout empty.",
				}},
			},
			SeeAlso: []string{"s", "show", "setup"},
		},
		run: cmdAsk,
	})
}

func parseAskOptions(args []string) (ai.Overrides, error) {
	opt := ai.Overrides{AI: true}
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--ai" {
			continue
		}
		key, value, hasValue := strings.Cut(a, "=")
		if key != "--ai" && key != "--model" && key != "--effort" {
			return opt, fmt.Errorf("unknown option %q", a)
		}
		if !hasValue {
			var ok bool
			value, ok = flagValue(args, &i)
			if !ok {
				return opt, fmt.Errorf("%s needs a value", key)
			}
		}
		if strings.TrimSpace(value) == "" {
			return opt, fmt.Errorf("%s needs a value", key)
		}
		switch key {
		case "--ai":
			opt.Profile = value
		case "--model":
			opt.Model = value
		case "--effort":
			opt.Effort = value
		}
	}
	return opt, nil
}

type askOptions struct {
	ai   ai.Overrides
	save bool
}

// parseAskArgs follows parseLastOptions, minus --output and --allow-secret:
// --save sets save; a dash argument (and, for --model/--effort without "=",
// its value) goes to parseAskOptions; any other word is the question
// misplaced after an option, reported once the model options are checked
// so an unknown option is still reported first. A first stray word right
// after a bare --ai is reported as a misplaced profile.
func parseAskArgs(args []string) (askOptions, error) {
	opt := askOptions{}
	var modelArgs []string
	var stray string
	strayAfterAI := false
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "--save":
			opt.save = true
		case arg != "-" && strings.HasPrefix(arg, "-"):
			modelArgs = append(modelArgs, arg)
			key, _, assigned := strings.Cut(arg, "=")
			if (key == "--model" || key == "--effort") && !assigned && i+1 < len(args) {
				i++
				modelArgs = append(modelArgs, args[i])
			} else if key == "--ai" && !assigned && stray == "" && i+1 < len(args) {
				next := args[i+1]
				strayAfterAI = next == "-" || !strings.HasPrefix(next, "-")
			}
		default:
			if stray == "" {
				stray = arg
			}
		}
	}
	var err error
	opt.ai, err = parseAskOptions(modelArgs)
	if err != nil {
		return opt, err
	}
	if strayAfterAI {
		return opt, fmt.Errorf("--ai takes its profile as --ai=PROFILE")
	}
	if stray != "" {
		return opt, fmt.Errorf("%q is not an option; the question goes before options: nn ask QUESTION... [--save]", stray)
	}
	return opt, nil
}

type askAsker struct{ ctx context.Context }

func (askAsker) Interactive() bool                     { return app.Interactive() }
func (a askAsker) Ask(question string) (string, error) { return app.AskContext(a.ctx, question) }

var askConsentAsker = func(ctx context.Context) ai.Asker { return askAsker{ctx} }

func cmdAsk(inv *invocation) int {
	opt, err := parseAskArgs(inv.refinements)
	if err != nil {
		return inv.misuse("%v", err)
	}
	question := strings.TrimSpace(strings.Join(inv.data, " "))
	if question == "" {
		return inv.misuse("a question is required")
	}
	fail := func(err error) int {
		if inv.interrupted() || errors.Is(err, context.Canceled) {
			fmt.Fprintln(inv.stderr, "nn: ask: cancelled")
			return output.ExitInterrupted
		}
		return inv.fail(err)
	}
	if inv.interrupted() {
		return fail(inv.ctx.Err())
	}
	env, err := app.Open()
	if err != nil {
		return fail(err)
	}
	call, err := ai.Resolve(env.Cfg, "ask", opt.ai)
	if err != nil {
		return fail(err)
	}
	session, err := ask.Prepare(inv.ctx, env.Vault, ask.Options{
		Limits: env.Cfg.AI.Context, NoLayoutFallback: !env.Cfg.Search.LayoutFallback,
	}, question)
	if err != nil {
		return fail(err)
	}
	if text, local := session.LocalAnswer(); local {
		if _, err := io.WriteString(inv.stdout, text); err != nil {
			return fail(err)
		}
		if opt.save {
			fmt.Fprintln(inv.stderr, "nn: ask: answer not saved: not enough evidence")
		}
		return output.ExitNotFound
	}
	prompt, err := ai.Prompt(env.Cfg, "ask")
	if err != nil {
		return fail(err)
	}
	decision, approval, err := ai.Approve(&env.Cfg, call, session.ContextDescription(), askConsentAsker(inv.ctx), inv.stderr)
	if err != nil {
		return fail(err)
	}
	if decision != ai.Allowed {
		if decision == ai.Unasked {
			key := ai.ConsentKey(call.Name, call.Profile)
			hint := ai.ConsentHintFor(key, ai.ConsentAlways)
			if hint.Command != "" {
				return fail(fmt.Errorf("AI needs consent; use nn setup ai or %s", hint.Command))
			}
			return fail(fmt.Errorf("AI needs consent; %s", hint.ByHand()))
		}
		return fail(errors.New("AI consent was not granted"))
	}
	text, err := session.Run(inv.ctx, approval, call, prompt, nil)
	if err != nil && !errors.Is(err, ask.ErrInsufficient) {
		return fail(err)
	}
	if _, writeErr := io.WriteString(inv.stdout, text); writeErr != nil {
		return fail(writeErr)
	}
	if errors.Is(err, ask.ErrInsufficient) {
		if opt.save {
			fmt.Fprintln(inv.stderr, "nn: ask: answer not saved: not enough evidence")
		}
		return output.ExitNotFound
	}
	if opt.save {
		if err := saveAnswer(inv.ctx, inv, env, session); err != nil {
			return fail(err)
		}
	}
	return output.ExitOK
}

// saveAnswer mirrors saveDigest (cmd_digest.go): ask's timeout is scoped
// inside Session.Run, so the save runs on inv.ctx, without a profile timeout.
func saveAnswer(ctx context.Context, inv *invocation, env *app.Env, s *ask.Session) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	notes, err := env.Notes(ctx)
	if err != nil {
		return fmt.Errorf("save answer: %w", err)
	}
	var notePaths []string
	if err := env.Vault.Walk(ctx, func(rel string) error {
		notePaths = append(notePaths, rel)
		return nil
	}); err != nil {
		return fmt.Errorf("save answer: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	graph := links.Build(notes)
	note, err := s.Note(graph, notePaths, env.Vault.Inbox)
	if err != nil {
		return fmt.Errorf("save answer: %w", err)
	}
	note.Now = envNow(env)
	created, err := env.Vault.Create(note)
	if err != nil {
		return fmt.Errorf("save answer: %w", err)
	}
	env.PostSave(ctx, created.Path, "create")
	_, err = fmt.Fprintln(inv.stderr, created.Path)
	return err
}
