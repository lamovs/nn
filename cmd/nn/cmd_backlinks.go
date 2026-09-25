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
			Verb:    "backlinks",
			Summary: "list notes that link to a note",
			Examples: []cli.Example{
				{Cmd: "nn backlinks docker cleanup", What: "prints every note referencing it, with line numbers"},
			},
			SeeAlso: []string{"links", "graph"},
		},
		run: cmdBacklinks,
	})
}

type backlinksRow struct {
	From string `json:"from"`
	Line int    `json:"line"`
}

func backlinksSpec() output.Spec[backlinksRow] {
	return output.Spec[backlinksRow]{
		Text: func(w io.Writer, rows []backlinksRow) error {
			for _, r := range rows {
				fmt.Fprintf(w, "<- %s:%d\n", r.From, r.Line)
			}
			return nil
		},
		Path:      func(r backlinksRow) string { return r.From },
		TSVHeader: []string{"from", "line"},
		TSV: func(r backlinksRow) []string {
			return []string{r.From, strconv.Itoa(r.Line)}
		},
	}
}

func cmdBacklinks(inv *invocation) int {
	outOpt, rest, err := output.ParseFlags(inv.args)
	if err != nil {
		return inv.misuse("%v", err)
	}
	var words []string
	for i := 0; i < len(rest); i++ {
		a := rest[i]
		// A bare "--" ends flag scanning, so a note titled with a leading dash stays reachable.
		if a == "--" {
			words = append(words, rest[i+1:]...)
			break
		}
		if a != "-" && strings.HasPrefix(a, "-") {
			return inv.misuse("unknown option %q", a)
		}
		words = append(words, a)
	}
	if len(words) == 0 {
		return inv.misuse("backlinks needs a NOTE")
	}

	env, err := app.Open()
	if err != nil {
		return inv.fail(err)
	}
	rel, err := env.Resolve(inv.ctx, strings.Join(words, " "))
	if err != nil {
		return inv.fail(err)
	}
	notes, err := env.Notes(inv.ctx)
	if err != nil {
		return inv.fail(err)
	}
	g := links.Build(notes)
	edges := g.In(rel)

	rows := make([]backlinksRow, len(edges))
	for i, e := range edges {
		rows[i] = backlinksRow{From: e.From, Line: e.Line}
	}
	// Emit runs even when nothing links here, so --json prints "[]"; the exit code is decided afterwards.
	if err := output.Emit(inv.stdout, outOpt, rows, backlinksSpec()); err != nil {
		return inv.fail(err)
	}
	if len(rows) == 0 {
		return output.ExitNotFound
	}
	return output.ExitOK
}
