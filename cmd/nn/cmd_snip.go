package main

import (
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/lamovs/nn/internal/app"
	"github.com/lamovs/nn/internal/cli"
	"github.com/lamovs/nn/internal/output"
	"github.com/lamovs/nn/internal/search"
	"github.com/lamovs/nn/internal/snippet"
	"github.com/lamovs/nn/internal/vault"
)

func init() {
	register(command{
		help: cli.Help{
			Verb:    "snip",
			Summary: "print a code block from a matching note",
			Examples: []cli.Example{
				{Cmd: "nn snip docker prune", What: "prints the best-matching snippet, filling in {{placeholders}}"},
				{Cmd: "nn snip docker prune --set container=web", What: "answers one placeholder without a prompt"},
				{Cmd: "nn snip -t docker --list", What: "lists every code block tagged docker instead of picking one"},
				{Cmd: "nn snip docker prune --raw", What: "prints the block unfilled, placeholders and all"},
			},
			Sections: []cli.HelpSection{
				{Title: "Placeholders", Items: []string{
					"{{name}} and {{name:default}} are asked for on /dev/tty when interactive.",
					"Without a terminal, missing placeholders (no --set and no default) exit 2 and list what is missing.",
				}},
			},
			SeeAlso: []string{"code", "s"},
		},
		run: cmdSnip,
	})
}

type snipOptions struct {
	tags []string
	set  map[string]string
	copy bool
	raw  bool
	list bool
}

func parseSnipOptions(args []string) (snipOptions, error) {
	opt := snipOptions{set: map[string]string{}}
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "-t":
			v, ok := flagValue(args, &i)
			if !ok {
				return opt, fmt.Errorf("-t needs a tag")
			}
			opt.tags = append(opt.tags, v)
		case "--set":
			v, ok := flagValue(args, &i)
			if !ok {
				return opt, fmt.Errorf("--set needs name=value")
			}
			name, value, hasEq := strings.Cut(v, "=")
			if !hasEq || name == "" {
				return opt, fmt.Errorf("--set wants name=value, got %q", v)
			}
			opt.set[name] = value
		case "--copy":
			opt.copy = true
		case "--raw":
			opt.raw = true
		case "--list":
			opt.list = true
		default:
			return opt, unknownOption{args[i]}
		}
	}
	return opt, nil
}

type snipCandidate struct {
	notePath  string
	noteTitle string
	block     vault.CodeBlock
	rank      int
}

func cmdSnip(inv *invocation) int {
	outOpt, rest, err := output.ParseFlags(inv.refinements)
	if err != nil {
		return inv.misuse("%v", err)
	}
	opt, serr := parseSnipOptions(rest)
	if serr != nil {
		var unknown unknownOption
		if errors.As(serr, &unknown) {
			return inv.misuseWord("unknown option ", unknown.opt)
		}
		return inv.misuse("%v", serr)
	}

	env, err := app.Open()
	if err != nil {
		return inv.fail(err)
	}

	query := strings.TrimSpace(strings.Join(inv.data, " "))
	candidates, err := snipCandidates(inv, env, query, opt.tags)
	if err != nil {
		return inv.fail(err)
	}

	if opt.list {
		return emitSnipList(inv, outOpt, candidates)
	}

	if len(candidates) == 0 {
		fmt.Fprintf(inv.stderr, "nn: snip: no results for %q\n", query)
		return output.ExitNotFound
	}

	chosen, err := chooseSnip(inv, candidates)
	if err != nil {
		return inv.fail(err)
	}

	code := chosen.block.Code
	if !opt.raw {
		code, err = fillPlaceholders(inv, code, opt.set)
		if err != nil {
			return failMissingPlaceholders(inv, err)
		}
	}

	if opt.copy {
		if err := captureCopyText(inv.ctx, code); err != nil {
			fmt.Fprintf(inv.stderr, "nn: warning: copy text: %v\n", err)
		}
	}
	fmt.Fprintln(inv.stdout, code)
	env.RecordOpen(chosen.notePath)
	return output.ExitOK
}

func snipCandidates(inv *invocation, env *app.Env, query string, tags []string) ([]snipCandidate, error) {
	docs, err := env.Docs(inv.ctx)
	if err != nil {
		return nil, err
	}
	var ranker search.Ranker
	if env.State != nil {
		ranker = env.State
	}
	q := search.Query{Text: query, Tags: tags, NoLayoutFallback: !env.Cfg.Search.LayoutFallback}
	results, err := search.Search(inv.ctx, docs, q, ranker, envNow(env))
	if err != nil {
		return nil, err
	}

	notes, err := env.Notes(inv.ctx)
	if err != nil {
		return nil, err
	}
	byPath := make(map[string]*vault.Note, len(notes))
	for _, n := range notes {
		byPath[n.Path] = n
	}

	words := strings.Fields(strings.ToLower(query))
	var out []snipCandidate
	for rank, res := range results {
		note, ok := byPath[res.Doc.Path]
		if !ok {
			continue
		}
		wholeNote := query == "" || matchedByName(res.Hits)
		for _, block := range vault.CodeBlocks(note.Body, note.BodyStartLine) {
			if !wholeNote && !containsAllWords(block.Code, words) {
				continue
			}
			out = append(out, snipCandidate{notePath: note.Path, noteTitle: note.Title, block: block, rank: rank})
		}
	}
	return out, nil
}

func matchedByName(hits []search.Hit) bool {
	for _, h := range hits {
		if h.Kind == search.KindTitle || h.Kind == search.KindAlias {
			return true
		}
	}
	return false
}

func containsAllWords(code string, words []string) bool {
	lower := strings.ToLower(code)
	for _, w := range words {
		if !strings.Contains(lower, w) {
			return false
		}
	}
	return true
}

func chooseSnip(inv *invocation, candidates []snipCandidate) (snipCandidate, error) {
	if len(candidates) == 1 {
		return candidates[0], nil
	}
	if !app.Interactive() {
		fmt.Fprintf(inv.stderr, "nn: snip: %d matches; using %s (%s)\n", len(candidates), candidates[0].notePath, candidates[0].noteTitle)
		return candidates[0], nil
	}
	shown := candidates
	if len(shown) > 9 {
		shown = shown[:9]
	}
	items := make([]string, len(shown))
	for i, c := range shown {
		items[i] = fmt.Sprintf("%s (%s) - %s", c.notePath, c.block.Lang, firstLine(c.block.Code))
	}
	idx, err := app.Choose("multiple snippets match:", items)
	if err != nil {
		return snipCandidate{}, err
	}
	return shown[idx], nil
}

func firstLine(s string) string {
	line, _, _ := strings.Cut(s, "\n")
	return strings.TrimSpace(line)
}

func fillPlaceholders(inv *invocation, code string, set map[string]string) (string, error) {
	placeholders := snippet.Find(code)
	if len(placeholders) == 0 {
		return code, nil
	}
	values := map[string]string{}
	for k, v := range set {
		values[k] = v
	}
	if app.Interactive() {
		for _, ph := range placeholders {
			if _, ok := values[ph.Name]; ok {
				continue
			}
			prompt := ph.Name + ": "
			if ph.HasDefault {
				prompt = fmt.Sprintf("%s [%s]: ", ph.Name, ph.Default)
			}
			answer, err := app.Ask(prompt)
			if err != nil {
				return "", err
			}
			if answer != "" {
				values[ph.Name] = answer
			}
		}
	}
	if missing := snippet.Missing(code, values); len(missing) > 0 {
		return "", missingPlaceholdersError{names: missing}
	}
	return snippet.Render(code, values), nil
}

type missingPlaceholdersError struct{ names []string }

func (e missingPlaceholdersError) Error() string {
	return "missing placeholders: " + strings.Join(e.names, ", ")
}

func failMissingPlaceholders(inv *invocation, err error) int {
	var missing missingPlaceholdersError
	if errors.As(err, &missing) {
		fmt.Fprintf(inv.stderr, "nn: snip: %v\n", missing)
		return output.ExitError
	}
	return inv.fail(err)
}

type snipListRow struct {
	Path      string `json:"path"`
	Lang      string `json:"lang"`
	StartLine int    `json:"start_line"`
	EndLine   int    `json:"end_line"`
	Preview   string `json:"preview"`
}

func emitSnipList(inv *invocation, outOpt output.Options, candidates []snipCandidate) int {
	rows := make([]snipListRow, len(candidates))
	for i, c := range candidates {
		rows[i] = snipListRow{Path: c.notePath, Lang: c.block.Lang, StartLine: c.block.StartLine, EndLine: c.block.EndLine, Preview: firstLine(c.block.Code)}
	}
	if len(rows) == 0 {
		fmt.Fprintln(inv.stderr, "nn: snip: no results")
	}
	err := output.Emit(inv.stdout, outOpt, rows, output.Spec[snipListRow]{
		Text: func(w io.Writer, rows []snipListRow) error {
			for _, r := range rows {
				if _, err := fmt.Fprintf(w, "%s:%d  %s  %s\n", r.Path, r.StartLine, r.Lang, r.Preview); err != nil {
					return err
				}
			}
			return nil
		},
		Path:      func(r snipListRow) string { return r.Path },
		TSVHeader: []string{"path", "lang", "start_line", "end_line", "preview"},
		TSV: func(r snipListRow) []string {
			return []string{r.Path, r.Lang, fmt.Sprint(r.StartLine), fmt.Sprint(r.EndLine), r.Preview}
		},
	})
	if err != nil {
		return inv.fail(err)
	}
	if len(rows) == 0 {
		return output.ExitNotFound
	}
	return output.ExitOK
}
