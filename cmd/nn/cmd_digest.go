package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/lamovs/nn/internal/ai"
	"github.com/lamovs/nn/internal/app"
	"github.com/lamovs/nn/internal/cli"
	"github.com/lamovs/nn/internal/digest"
	"github.com/lamovs/nn/internal/links"
	"github.com/lamovs/nn/internal/output"
	"github.com/lamovs/nn/internal/search"
	"github.com/lamovs/nn/internal/vault"
)

func init() {
	register(command{
		help: cli.Help{
			Verb: "digest", Summary: "summarize a selected set of notes with source links",
			Examples: []cli.Example{
				{Cmd: `nn digest --since 7d`, What: "wait for a digest of the notes from the last week"},
				{Cmd: `nn digest --since 7d --notes 30 --chars 30000 --save`, What: "cover more of a busy week and save the digest as a note"},
				{Cmd: `nn links "Project Atlas" --paths | nn digest -`, What: "digest exactly the notes a hub links to"},
			},
			Sections: []cli.HelpSection{
				{Title: "Selection", Items: []string{
					"Give TOPIC words (nn s AND semantics, at most 200 characters), or -t TAG (repeatable, at most 16), --since, --until, --inbox, --here, or a single \"-\" to read note paths from stdin exactly as nn s/ls/links/backlinks --paths print them. At least one is required, and filters combine with AND.",
					"Words may come before or after options, as in nn s. A lone \"-\" cannot be combined with other words, and only reads note paths off a pipe or file, never a terminal; put it before -- to read stdin, since after -- it is topic text, refused when a list is piped in. Notes saved by an earlier digest are left out of any selection unless listed with -.",
				}},
				{Title: "Budget and save", Items: []string{
					"--notes N (1..64) and --chars N (1000..32000) override ai.context.notes/chars for this call; config values above the caps are clamped. Notes that look like they carry a credential are skipped and disclosed; --allow-secret includes them, consent permitting.",
					"--save creates one new inbox note with the digest and verified relative links to its sources, after stdout succeeds; its path is printed to stderr.",
				}},
				{Title: "AI and limits", Items: []string{
					"--ai[=PROFILE] selects a profile; --model MODEL and --effort low|medium|high|max override it. The digest task requests AI even without --ai; there is no --no-ai.",
					"The command waits for the digest regardless of ai.mode, in exactly one model call. Configure consent beforehand with nn setup ai when the collection is read from a pipe.",
				}},
				{Title: "Exit status", Items: []string{
					"0: digest printed, and saved with --save; 1: no note matches the selection; 2: invalid input, consent, model or save failure; 130: cancelled. A save failure, or a cancel during --save, keeps the printed digest on stdout; everything else leaves stdout empty.",
				}},
			},
			SeeAlso: []string{"ask", "s", "triage", "setup"},
		},
		run: cmdDigest,
	})
}

type digestOptions struct {
	overrides   ai.Overrides
	tags        []string
	since       string
	until       string
	inbox, here bool
	notes       int
	hasNotes    bool
	chars       int
	hasChars    bool
	save        bool
	allowSecret bool
}

// hasFilter reports whether any flag that narrows the vault was given.
func (o digestOptions) hasFilter() bool {
	return len(o.tags) > 0 || o.since != "" || o.until != "" || o.inbox || o.here
}

// literalFrom is where literal TOPIC text (after a "--") begins in words, -1 if there was no "--".
func parseDigestOptions(args []string) (opt digestOptions, words []string, literalFrom int, err error) {
	literalFrom = -1
	var modelArgs []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			literalFrom = len(words)
			words = append(words, args[i+1:]...)
			break
		}
		switch a {
		case "-t":
			v, ok := flagValue(args, &i)
			if !ok {
				return opt, nil, -1, errors.New("-t needs a tag")
			}
			opt.tags = append(opt.tags, v)
		case "--since":
			v, ok := flagValue(args, &i)
			if !ok {
				return opt, nil, -1, errors.New("--since needs a value (today, Nd, Nw or YYYY-MM-DD)")
			}
			opt.since = v
		case "--until":
			v, ok := flagValue(args, &i)
			if !ok {
				return opt, nil, -1, errors.New("--until needs a YYYY-MM-DD date")
			}
			opt.until = v
		case "--inbox":
			opt.inbox = true
		case "--here":
			opt.here = true
		case "--notes":
			v, ok := flagValue(args, &i)
			if !ok {
				return opt, nil, -1, errors.New("--notes needs a number")
			}
			n, convErr := strconv.Atoi(v)
			if convErr != nil || n < 1 || n > digest.MaxNotes {
				return opt, nil, -1, fmt.Errorf("--notes: want 1..%d", digest.MaxNotes)
			}
			opt.notes, opt.hasNotes = n, true
		case "--chars":
			v, ok := flagValue(args, &i)
			if !ok {
				return opt, nil, -1, errors.New("--chars needs a number")
			}
			n, convErr := strconv.Atoi(v)
			if convErr != nil || n < digest.MinChars || n > digest.MaxChars {
				return opt, nil, -1, fmt.Errorf("--chars: want %d..%d", digest.MinChars, digest.MaxChars)
			}
			opt.chars, opt.hasChars = n, true
		case "--save":
			opt.save = true
		case "--allow-secret":
			opt.allowSecret = true
		default:
			key, _, assigned := strings.Cut(a, "=")
			if key == "--ai" || key == "--model" || key == "--effort" {
				modelArgs = append(modelArgs, a)
				if !assigned && key != "--ai" && i+1 < len(args) {
					i++
					modelArgs = append(modelArgs, args[i])
				}
				continue
			}
			if a != "-" && strings.HasPrefix(a, "-") {
				return opt, nil, -1, fmt.Errorf("unknown option %q", a)
			}
			words = append(words, a)
		}
	}
	opt.overrides, err = parseAskOptions(modelArgs)
	return opt, words, literalFrom, err
}

// digestAsker indirects askAsker so tests can supply scripted answers.
var digestAsker = func(ctx context.Context) ai.Asker { return askAsker{ctx} }

func cmdDigest(inv *invocation) int {
	opt, words, literalFrom, err := parseDigestOptions(inv.args)
	if err != nil {
		return inv.misuse("%v", err)
	}

	// A lone "-" selects the collection only when it comes before any "--".
	head := words
	if literalFrom >= 0 {
		head = words[:literalFrom]
	}
	var collection bool
	switch {
	case len(words) == 1 && len(head) == 1 && head[0] == "-":
		collection = true
	case slices.Contains(head, "-"):
		return inv.misuse(`a lone "-" cannot be combined with other words`)
	}
	if collection && filterIsTerminal(inv.stdin) {
		return inv.misuse(`"-" reads note paths from a pipe or file, not a terminal`)
	}

	var topic string
	if !collection {
		topic = strings.TrimSpace(strings.Join(words, " "))
	}
	// A "-" after "--" is TOPIC text but must not silently ignore a piped list.
	if !collection && topic == "-" && !filterIsTerminal(inv.stdin) {
		return inv.misuse(`a lone "-" after "--" is topic text; put "-" before "--" to read note paths from stdin`)
	}
	if !utf8.ValidString(topic) || utf8.RuneCountInString(topic) > 200 {
		return inv.misuse("the topic must be valid UTF-8 of at most 200 characters")
	}
	if len(opt.tags) > 16 {
		return inv.misuse("at most 16 tags")
	}
	for _, tag := range opt.tags {
		if strings.TrimSpace(tag) == "" || !utf8.ValidString(tag) || utf8.RuneCountInString(tag) > 64 {
			return inv.misuse("a tag must be nonempty valid UTF-8 of at most 64 characters")
		}
	}
	if !collection && topic == "" && !opt.hasFilter() {
		msg := "a topic, a filter (-t, --since, --until, --inbox, --here) or a list of note paths is required"
		if !filterIsTerminal(inv.stdin) {
			msg += "; use - to read note paths from stdin"
		}
		return inv.misuse("%s", msg)
	}

	fail := func(err error) int {
		if inv.interrupted() || errors.Is(err, context.Canceled) {
			fmt.Fprintln(inv.stderr, "nn: digest: cancelled")
			return output.ExitInterrupted
		}
		return inv.fail(err)
	}
	if err := inv.ctx.Err(); err != nil {
		return fail(err)
	}

	var paths []string
	if collection {
		data, err := readFilterInput(inv.ctx, inv.stdin)
		if err != nil {
			return fail(err)
		}
		refs, err := output.ReadRefs(bytes.NewReader(data))
		if err != nil {
			return fail(err)
		}
		// Non-nil even when empty: nil would tell internal/digest there is no list at all.
		paths = make([]string, 0, len(refs))
		for _, r := range refs {
			paths = append(paths, r.Path)
		}
	}

	env, err := app.Open()
	if err != nil {
		return fail(err)
	}

	call, err := ai.Resolve(env.Cfg, "digest", opt.overrides)
	if err != nil {
		return fail(err)
	}

	now := envNow(env)
	sel := digest.Selection{Tags: opt.tags, Inbox: opt.inbox, Paths: paths}
	if !collection {
		sel.Topic = topic
	}
	if opt.since != "" {
		since, err := parseSince(opt.since, now)
		if err != nil {
			return inv.misuse("%v", err)
		}
		sel.Since = since
	}
	if opt.until != "" {
		until, err := parseDigestUntil(opt.until, now)
		if err != nil {
			return inv.misuse("%v", err)
		}
		sel.Until = until
	}
	if !sel.Since.IsZero() && !sel.Until.IsZero() && !sel.Until.After(sel.Since) {
		return inv.misuse("--until %s is before --since %s", opt.until, opt.since)
	}
	if opt.here {
		where, repo := vault.Context("")
		sel.Here = &search.Here{Cwd: where, Repo: repo}
	}

	notes := opt.notes
	if !opt.hasNotes {
		notes = env.Cfg.AI.Context.Notes
	}
	if notes > digest.MaxNotes {
		notes = digest.MaxNotes
	}
	chars := opt.chars
	if !opt.hasChars {
		chars = env.Cfg.AI.Context.Chars
	}
	if chars > digest.MaxChars {
		chars = digest.MaxChars
	}

	docs, err := env.Docs(inv.ctx)
	if err != nil {
		return fail(err)
	}

	session, err := digest.Prepare(inv.ctx, env.Vault, docs, sel, digest.Options{
		Notes: notes, Chars: chars,
		NoLayoutFallback: !env.Cfg.Search.LayoutFallback,
		AllowSecret:      opt.allowSecret,
		Now:              now,
	})
	if err != nil {
		return fail(err)
	}

	// Printed even when the selection turns out empty: still worth telling the user about.
	report := session.Report()
	if n := report.SkippedNonNote; n > 0 {
		what := "paths are not notes and were"
		if n == 1 {
			what = "path is not a note and was"
		}
		fmt.Fprintf(inv.stderr, "nn: digest: %d listed %s skipped\n", n, what)
	}
	if report.Layout {
		if swapped, ok := search.SwapLayout(topic); ok {
			fmt.Fprintf(inv.stderr, "nn: digest: no results for %q, used %q\n", topic, swapped)
		} else {
			fmt.Fprintln(inv.stderr, "nn: digest: no results as typed; retried in the other keyboard layout")
		}
	}
	if session.Empty() {
		fmt.Fprintf(inv.stderr, "nn: %s: no notes match the selection\n", inv.verb)
		return output.ExitNotFound
	}

	prompt, err := ai.Prompt(env.Cfg, "digest")
	if err != nil {
		return fail(err)
	}
	if err := ai.ValidateRequest(session.Request(prompt)); err != nil {
		return fail(err)
	}
	decision, approval, err := ai.Approve(&env.Cfg, call, session.ContextDescription(), digestAsker(inv.ctx), inv.stderr)
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

	result, err := session.Run(inv.ctx, approval, call, prompt, nil)
	if err != nil {
		return fail(err)
	}
	if _, err := io.WriteString(inv.stdout, result.Text()); err != nil {
		return fail(err)
	}
	if opt.save {
		if err := saveDigest(inv.ctx, inv, env, result, now); err != nil {
			return fail(err)
		}
	}
	return output.ExitOK
}

// parseDigestUntil parses an inclusive YYYY-MM-DD date, returning the exclusive local midnight after it.
func parseDigestUntil(s string, now time.Time) (time.Time, error) {
	d, err := time.ParseInLocation("2006-01-02", strings.TrimSpace(s), now.Location())
	if err != nil {
		return time.Time{}, fmt.Errorf("--until: %q is not a YYYY-MM-DD date", s)
	}
	y, m, day := d.Date()
	return time.Date(y, m, day+1, 0, 0, 0, 0, now.Location()), nil
}

// saveDigest creates the inbox note --save asks for; notePaths lets d.Note qualify a relative wikilink.
func saveDigest(ctx context.Context, inv *invocation, env *app.Env, d *digest.Digest, now time.Time) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	notes, err := env.Notes(ctx)
	if err != nil {
		return fmt.Errorf("save digest: %w", err)
	}
	var notePaths []string
	if err := env.Vault.Walk(ctx, func(rel string) error {
		notePaths = append(notePaths, rel)
		return nil
	}); err != nil {
		return fmt.Errorf("save digest: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	graph := links.Build(notes)
	note := d.Note(graph, notePaths, env.Vault.Inbox, now)
	note.Now = now
	created, err := env.Vault.Create(note)
	if err != nil {
		return fmt.Errorf("save digest: %w", err)
	}
	env.PostSave(ctx, created.Path, "create")
	_, err = fmt.Fprintln(inv.stderr, created.Path)
	return err
}
