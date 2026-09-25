package main

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/lamovs/nn/internal/ai"
	"github.com/lamovs/nn/internal/app"
	"github.com/lamovs/nn/internal/capture"
	"github.com/lamovs/nn/internal/cli"
	"github.com/lamovs/nn/internal/noteai"
	"github.com/lamovs/nn/internal/output"
	"github.com/lamovs/nn/internal/vault"
	"github.com/lamovs/nn/internal/webpage"
)

func init() {
	register(command{
		help: cli.Help{
			Verb:    "url",
			Summary: "fetch a web link and save it as a note with an AI summary",
			Examples: []cli.Example{
				{Cmd: "nn url", What: "reads a link from the clipboard, fetches it and saves a summarized note"},
				{Cmd: "nn url https://go.dev/blog/pgo --ai-mode wait", What: "waits for the finished note with a generated title, summary and #tags"},
				{Cmd: "nn url http://localhost:3000/docs --allow-private --no-ai", What: "saves the link note without fetching a summary"},
			},
			Sections: []cli.HelpSection{
				{Title: "Source", Items: []string{
					"One LINK argument, or with none, the clipboard: it must be a single bare http or https link, trimmed of surrounding whitespace and otherwise unchanged. Stdin is never read.",
				}},
				{Title: "AI summary", Items: []string{
					"AI is implicit, since the verb exists to summarize the page; --no-ai saves the link note without one. --ai=PROFILE, --model and --effort override the profile; consent is required (nn setup ai).",
					"--ai-mode background (default) saves the link immediately and appends the summary later; wait waits for the finished note; auto waits on an interactive terminal.",
					"--allow-secret is required both for a link that looks like it carries a credential and for page text that looks like it does; neither is sent without it.",
				}},
				{Title: "Fetching", Items: []string{
					"Only http and https, one 30s deadline, at most 5 redirects, no cookies, no Authorization, no environment proxies. Private and loopback addresses are refused unless --allow-private.",
					"A fetch, status or content failure after a valid link still saves a link note (Source plus a reason line) with no summary; exit 0.",
				}},
				{Title: "Exit status", Items: []string{"0: note saved, with or without a summary; 2: invalid input, a sensitive link or clipboard refused before any network, or a save failure; 130: cancelled before a note exists."}},
			},
			SeeAlso: []string{"add", "shot"},
		},
		run: cmdURL,
	})
}

type urlOptions struct {
	overrides    ai.Overrides
	aiMode       string
	tags         []string
	title        string
	allowSecret  bool
	allowPrivate bool
}

func parseURLOptions(args []string) (urlOptions, error) {
	var opt urlOptions
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "-t":
			v, ok := flagValue(args, &i)
			if !ok {
				return opt, fmt.Errorf("-t needs a tag")
			}
			opt.tags = append(opt.tags, v)
		case "--title":
			v, ok := flagValue(args, &i)
			if !ok {
				return opt, fmt.Errorf("--title needs a value")
			}
			opt.title = v
		case "--ai":
			opt.overrides.AI = true
		case "--no-ai":
			opt.overrides.NoAI = true
		case "--model", "--effort", "--ai-mode":
			key := args[i]
			value, ok := flagValue(args, &i)
			if !ok || value == "" {
				return opt, fmt.Errorf("%s needs a value", key)
			}
			switch key {
			case "--model":
				opt.overrides.Model = value
			case "--effort":
				opt.overrides.Effort = value
			case "--ai-mode":
				opt.aiMode = value
			}
		case "--allow-secret":
			opt.allowSecret = true
		case "--allow-private":
			opt.allowPrivate = true
		default:
			key, value, hasValue := strings.Cut(args[i], "=")
			if !hasValue {
				return opt, unknownOption{args[i]}
			}
			if value == "" {
				return opt, fmt.Errorf("%s needs a value", key)
			}
			switch key {
			case "--ai":
				opt.overrides.AI = true
				opt.overrides.Profile = value
			case "--model":
				opt.overrides.Model = value
			case "--effort":
				opt.overrides.Effort = value
			case "--ai-mode":
				opt.aiMode = value
			default:
				return opt, unknownOption{args[i]}
			}
		}
	}
	if opt.aiMode != "" && opt.aiMode != "auto" && opt.aiMode != "wait" && opt.aiMode != "background" {
		return opt, errors.New("--ai-mode: want auto, wait or background")
	}
	return opt, nil
}

// errClipboardLink is the one message used for every rejection reason: the clipboard's content is never echoed.
var errClipboardLink = errors.New("clipboard does not contain a single http or https link")

func cmdURL(inv *invocation) int {
	outOpt, rest, err := output.ParseFlags(inv.refinements)
	if err != nil {
		return inv.misuse("%v", err)
	}
	opt, err := parseURLOptions(rest)
	if err != nil {
		var unknown unknownOption
		if errors.As(err, &unknown) {
			return inv.misuseWord("unknown option ", unknown.opt)
		}
		return inv.misuse("%v", err)
	}
	if len(inv.data) > 1 {
		return inv.misuse("url takes at most one link")
	}

	fromClipboard := len(inv.data) == 0
	var linkText string
	if fromClipboard {
		raw, err := captureClipboardText(inv.ctx)
		if err != nil {
			return inv.fail(fmt.Errorf("read clipboard: %w", err))
		}
		linkText = string(raw)
	} else {
		linkText = inv.data[0]
	}

	link, err := webpage.ParseLink(linkText)
	if err != nil {
		if fromClipboard {
			return inv.fail(errClipboardLink)
		}
		return inv.fail(err)
	}

	if kinds := link.Sensitive(); len(kinds) > 0 && !opt.allowSecret {
		return inv.fail(fmt.Errorf("link may contain a credential (%s); --allow-secret is required to send it", strings.Join(kinds, ", ")))
	}

	env, err := app.Open()
	if err != nil {
		return inv.fail(err)
	}

	plan, err := noteai.Prepare(&env.Cfg, "url", opt.overrides, opt.aiMode, shotAsker{inv.ctx}, inv.stderr)
	if err != nil {
		return inv.fail(err)
	}

	where, repo := vault.Context("")
	in := noteai.URLInput{
		Link:     link.Raw,
		Host:     link.Host(),
		LinkPath: link.URL.EscapedPath(),
		Title:    opt.title,
		Tags:     opt.tags,
		Where:    where,
		Repo:     repo,
	}

	fetcher := &webpage.Fetcher{UserAgent: "nn/" + version, AllowPrivate: opt.allowPrivate}
	page, ferr := fetcher.Fetch(inv.ctx, link)
	var fe *webpage.FetchError
	switch {
	case ferr == nil:
		in.PageTitle, in.Description, in.Text, in.Truncated = page.Title, page.Description, page.Text, page.Truncated
	case errors.As(ferr, &fe):
		in.NotFetched = fe.Reason
	case inv.interrupted() || errors.Is(ferr, context.Canceled):
		fmt.Fprintln(inv.stderr, "nn: url: cancelled")
		return output.ExitInterrupted
	default:
		return inv.fail(ferr)
	}

	if plan != nil && !opt.allowSecret && len(capture.FindSecrets(in.PageTitle+"\n"+in.Description+"\n"+in.Text)) > 0 {
		fmt.Fprintln(inv.stderr, "nn: url: AI skipped: page text may contain credentials; --allow-secret is required to send it")
		plan = nil
	}

	note, err := noteai.SaveURL(inv.ctx, env, plan, in, inv.stderr)
	if err != nil {
		return inv.fail(err)
	}

	return emitAddResult(inv, outOpt, note.Path, "create")
}
