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
	"github.com/lamovs/nn/internal/output"
)

func init() {
	register(command{
		help: cli.Help{
			Verb: "ask", Summary: "answer a question using excerpts of your notes",
			Examples: []cli.Example{
				{Cmd: `nn ask "How did I clear the Docker build cache?"`, What: "wait for an answer with links to supporting notes"},
				{Cmd: `nn ask "What did I decide about backups?" --ai=codex --effort high`, What: "use a selected profile and reasoning effort"},
				{Cmd: `nn ask -- -cache options`, What: "treat words starting with a dash as question text"},
			},
			Sections: []cli.HelpSection{
				{Title: "Question and answer", Items: []string{
					"Give the question as command-line words. nn searches note titles, bodies, tags and saved OCR locally, then sends bounded excerpts to the model. Matching is lexical, not semantic.",
					"The answer is printed to stdout with verified source IDs and Obsidian links. Notes, links and open history are not changed. Missing evidence is reported explicitly.",
					"The command waits for the answer regardless of ai.mode. It does not read question text from stdin or start a background task.",
				}},
				{Title: "AI and limits", Items: []string{
					"--ai[=PROFILE] selects a profile; --model MODEL and --effort low|medium|high|max override it. The ask task requests AI even without --ai, so consent=never is an explicit error.",
					"ai.context.notes and ai.context.chars bound all excerpts for the question. ai.context.search_rounds allows a bounded number of additional local searches requested by the model. One profile timeout covers the full answering session.",
					"Consent is requested once for the question and all bounded follow-up excerpts. Configure consent with nn setup ai when running without a terminal. ai.tasks.ask.prompt_file can replace the built-in prompt.",
				}},
				{Title: "Exit status", Items: []string{
					"0: an answer with sources; 1: insufficient evidence, possibly with a supported partial answer; 2: invalid input, refusal or model failure; 130: cancelled. Failures leave stdout empty.",
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

type askAsker struct{ ctx context.Context }

func (askAsker) Interactive() bool                     { return app.Interactive() }
func (a askAsker) Ask(question string) (string, error) { return app.AskContext(a.ctx, question) }

var askConsentAsker = func(ctx context.Context) ai.Asker { return askAsker{ctx} }

func cmdAsk(inv *invocation) int {
	opt, err := parseAskOptions(inv.refinements)
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
	call, err := ai.Resolve(env.Cfg, "ask", opt)
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
		return output.ExitNotFound
	}
	return output.ExitOK
}
