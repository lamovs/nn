package main

import (
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/lamovs/nn/internal/app"
	"github.com/lamovs/nn/internal/cli"
	"github.com/lamovs/nn/internal/links"
	"github.com/lamovs/nn/internal/output"
)

func init() {
	register(command{
		help: cli.Help{
			Verb:    "links",
			Summary: "list a note's outgoing links",
			Examples: []cli.Example{
				{Cmd: "nn links docker cleanup", What: "prints resolved and unresolved [[links]]"},
				{Cmd: "nn links --unresolved", What: "every broken link in the whole vault"},
			},
			SeeAlso: []string{"backlinks", "graph"},
		},
		run: cmdLinks,
	})
}

type linksOptions struct {
	unresolved bool
}

func parseLinksOptions(args []string) (linksOptions, []string, error) {
	var opt linksOptions
	var words []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			words = append(words, args[i+1:]...)
			break
		}
		switch a {
		case "--unresolved":
			opt.unresolved = true
		default:
			if a != "-" && strings.HasPrefix(a, "-") {
				return opt, nil, fmt.Errorf("unknown option %q", a)
			}
			words = append(words, a)
		}
	}
	return opt, words, nil
}

type linksRow struct {
	From     string `json:"from"`
	Target   string `json:"target"`
	To       string `json:"to,omitempty"`
	Resolved bool   `json:"resolved"`
	Line     int    `json:"line"`
}

func linksSpec(showFrom bool) output.Spec[linksRow] {
	return output.Spec[linksRow]{
		Text: func(w io.Writer, rows []linksRow) error {
			for _, r := range rows {
				prefix := ""
				if showFrom {
					prefix = r.From + "  "
				}
				if r.Resolved {
					fmt.Fprintf(w, "%s-> %s\n", prefix, r.To)
				} else {
					fmt.Fprintf(w, "%s-> %s (unresolved)\n", prefix, r.Target)
				}
			}
			return nil
		},
		Path: func(r linksRow) string {
			if r.Resolved {
				return r.To
			}
			return ""
		},
		TSVHeader: []string{"from", "target", "to", "resolved", "line"},
		TSV: func(r linksRow) []string {
			return []string{r.From, r.Target, r.To, strconv.FormatBool(r.Resolved), strconv.Itoa(r.Line)}
		},
	}
}

func cmdLinks(inv *invocation) int {
	outOpt, rest, err := output.ParseFlags(inv.args)
	if err != nil {
		return inv.misuse("%v", err)
	}
	opt, words, err := parseLinksOptions(rest)
	if err != nil {
		return inv.misuse("%v", err)
	}

	env, err := app.Open()
	if err != nil {
		return inv.fail(err)
	}
	notes, err := env.Notes(inv.ctx)
	if err != nil {
		return inv.fail(err)
	}
	g := links.Build(notes)

	var edges []links.Edge
	showFrom := false
	if len(words) == 0 {
		if !opt.unresolved {
			return inv.misuse("links needs a NOTE, or --unresolved for the whole vault")
		}
		edges = g.Unresolved()
		showFrom = true
	} else {
		rel, err := env.Resolve(inv.ctx, strings.Join(words, " "))
		if err != nil {
			return inv.fail(err)
		}
		edges = g.Out(rel)
		if opt.unresolved {
			filtered := edges[:0]
			for _, e := range edges {
				if !e.Resolved {
					filtered = append(filtered, e)
				}
			}
			edges = filtered
		}
	}

	rows := make([]linksRow, len(edges))
	for i, e := range edges {
		rows[i] = linksRow{From: e.From, Target: e.Target, To: e.To, Resolved: e.Resolved, Line: e.Line}
	}
	if err := output.Emit(inv.stdout, outOpt, rows, linksSpec(showFrom)); err != nil {
		return inv.fail(err)
	}
	if len(rows) == 0 {
		return output.ExitNotFound
	}
	return output.ExitOK
}
