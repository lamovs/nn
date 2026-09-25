package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/lamovs/nn/internal/app"
	"github.com/lamovs/nn/internal/cli"
	"github.com/lamovs/nn/internal/output"
)

func init() {
	register(command{
		help: cli.Help{
			Verb:    "cat",
			Summary: "print a note's body",
			Examples: []cli.Example{
				{Cmd: "nn cat docker cleanup", What: "prints the body without frontmatter"},
				{Cmd: "nn cat --full docker cleanup", What: "the raw file, frontmatter included"},
				{Cmd: "nn ls --paths | nn cat -", What: "prints every listed note's body, blank-line separated"},
			},
			Sections: []cli.HelpSection{
				{Title: "Multiple notes", Items: []string{
					"The words after cat are one note reference (path, stem, alias or title), joined with spaces - the same way show, code and links read theirs.",
					"Several notes at once come from a pipe: pass \"-\" and their bodies are printed in order, separated by a blank line.",
				}},
			},
			SeeAlso: []string{"show", "code"},
		},
		run: cmdCat,
	})
}

type catOptions struct {
	full bool
}

// parseCatOptions scans args for cat's own flags, returning the leftover words.
func parseCatOptions(args []string) (catOptions, []string, error) {
	var opt catOptions
	var words []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			words = append(words, args[i+1:]...)
			break
		}
		switch a {
		case "--full":
			opt.full = true
		default:
			if a != "-" && strings.HasPrefix(a, "-") {
				return opt, nil, fmt.Errorf("unknown option %q", a)
			}
			words = append(words, a)
		}
	}
	return opt, words, nil
}

// catRefs is every path from stdin for a lone "-", or the words joined into a single reference.
func catRefs(stdin io.Reader, words []string) ([]string, error) {
	if len(words) == 1 && words[0] == "-" {
		refs, err := output.ReadRefs(stdin)
		if err != nil {
			return nil, err
		}
		out := make([]string, len(refs))
		for i, r := range refs {
			out[i] = r.Path
		}
		return out, nil
	}
	if len(words) == 0 {
		return nil, errors.New("cat needs a NOTE, or - to read paths from stdin")
	}
	return []string{strings.Join(words, " ")}, nil
}

func cmdCat(inv *invocation) int {
	opt, words, err := parseCatOptions(inv.args)
	if err != nil {
		return inv.misuse("%v", err)
	}
	refs, err := catRefs(inv.stdin, words)
	if err != nil {
		return inv.misuse("%v", err)
	}

	env, err := app.Open()
	if err != nil {
		return inv.fail(err)
	}

	for i, ref := range refs {
		rel, err := env.Resolve(inv.ctx, ref)
		if err != nil {
			return inv.fail(err)
		}
		if i > 0 {
			fmt.Fprintln(inv.stdout)
		}
		if opt.full {
			data, err := os.ReadFile(env.Vault.Abs(rel))
			if err != nil {
				return inv.fail(err)
			}
			inv.stdout.Write(data)
			if len(data) > 0 && data[len(data)-1] != '\n' {
				fmt.Fprintln(inv.stdout)
			}
			continue
		}
		n, err := env.Vault.Load(rel)
		if err != nil {
			return inv.fail(err)
		}
		io.WriteString(inv.stdout, n.Body)
		if !strings.HasSuffix(n.Body, "\n") {
			fmt.Fprintln(inv.stdout)
		}
	}
	return output.ExitOK
}
