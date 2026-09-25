package main

import (
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/lamovs/nn/internal/app"
	"github.com/lamovs/nn/internal/cli"
	"github.com/lamovs/nn/internal/output"
	"github.com/lamovs/nn/internal/platform"
	"github.com/lamovs/nn/internal/vault"
)

func init() {
	register(command{
		help: cli.Help{
			Verb:    "code",
			Summary: "print code blocks from a note",
			Examples: []cli.Example{
				{Cmd: "nn code docker cleanup --lang sh", What: "prints only the shell code blocks"},
				{Cmd: "nn code docker cleanup -n 2 --copy", What: "the second matching block, also copied to the clipboard"},
			},
			Sections: []cli.HelpSection{
				{Title: "Selecting a block", Items: []string{
					"Without -n, every fenced code block matching --lang (or every block, with no --lang) is printed.",
					"-n N picks the Nth such block (1-based); a number out of range is treated as nothing found.",
				}},
			},
			SeeAlso: []string{"cat", "snip"},
		},
		run: cmdCode,
	})
}

type codeOptions struct {
	hasN bool
	n    int
	lang string
	copy bool
}

func parseCodeOptions(args []string) (codeOptions, []string, error) {
	var opt codeOptions
	var words []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			words = append(words, args[i+1:]...)
			break
		}
		switch a {
		case "-n":
			if i+1 >= len(args) {
				return opt, nil, errors.New("-n needs a block number")
			}
			i++
			n, err := strconv.Atoi(args[i])
			if err != nil || n < 1 {
				return opt, nil, fmt.Errorf("-n: %q is not a positive number", args[i])
			}
			opt.n, opt.hasN = n, true
		case "--lang":
			if i+1 >= len(args) {
				return opt, nil, errors.New("--lang needs a value")
			}
			i++
			opt.lang = args[i]
		case "--copy":
			opt.copy = true
		default:
			if a != "-" && strings.HasPrefix(a, "-") {
				return opt, nil, fmt.Errorf("unknown option %q", a)
			}
			words = append(words, a)
		}
	}
	return opt, words, nil
}

type codeRow struct {
	Path      string `json:"path"`
	Lang      string `json:"lang"`
	Code      string `json:"code"`
	StartLine int    `json:"line"`
	EndLine   int    `json:"endLine"`
}

func codeSpec() output.Spec[codeRow] {
	return output.Spec[codeRow]{
		Text: func(w io.Writer, rows []codeRow) error {
			for i, r := range rows {
				if i > 0 {
					fmt.Fprintln(w)
				}
				fmt.Fprintln(w, r.Code)
			}
			return nil
		},
		Path:      func(r codeRow) string { return r.Path },
		TSVHeader: []string{"path", "line", "endLine", "lang", "code"},
		TSV: func(r codeRow) []string {
			return []string{r.Path, strconv.Itoa(r.StartLine), strconv.Itoa(r.EndLine), r.Lang, r.Code}
		},
	}
}

func cmdCode(inv *invocation) int {
	outOpt, rest, err := output.ParseFlags(inv.args)
	if err != nil {
		return inv.misuse("%v", err)
	}
	opt, words, err := parseCodeOptions(rest)
	if err != nil {
		return inv.misuse("%v", err)
	}
	ref, err := oneRefFromWords("code", inv.stdin, inv.stderr, words)
	if err != nil {
		return inv.misuse("%v", err)
	}

	env, err := app.Open()
	if err != nil {
		return inv.fail(err)
	}
	rel, err := env.Resolve(inv.ctx, ref)
	if err != nil {
		return inv.fail(err)
	}
	n, err := env.Vault.Load(rel)
	if err != nil {
		return inv.fail(err)
	}

	blocks := vault.CodeBlocks(n.Body, n.BodyStartLine)
	if opt.lang != "" {
		filtered := blocks[:0]
		for _, b := range blocks {
			if strings.EqualFold(b.Lang, opt.lang) {
				filtered = append(filtered, b)
			}
		}
		blocks = filtered
	}
	if opt.hasN {
		if opt.n > len(blocks) {
			blocks = nil
		} else {
			blocks = blocks[opt.n-1 : opt.n]
		}
	}
	rows := make([]codeRow, len(blocks))
	texts := make([]string, len(blocks))
	for i, b := range blocks {
		rows[i] = codeRow{Path: rel, Lang: b.Lang, Code: b.Code, StartLine: b.StartLine, EndLine: b.EndLine}
		texts[i] = b.Code
	}

	// Nothing found leaves the clipboard alone rather than wiping it.
	if opt.copy && len(rows) > 0 {
		if err := platform.CopyText(inv.ctx, strings.Join(texts, "\n\n")); err != nil {
			return inv.fail(err)
		}
	}

	// Emit runs even for an empty result, so --json prints "[]"; the exit code is decided afterwards.
	if err := output.Emit(inv.stdout, outOpt, rows, codeSpec()); err != nil {
		return inv.fail(err)
	}
	if len(rows) == 0 {
		return output.ExitNotFound
	}
	// Reading a note's code is reading the note: it counts for frecency.
	env.RecordOpen(rel)
	return output.ExitOK
}
