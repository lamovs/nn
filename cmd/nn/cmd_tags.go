package main

import (
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"

	"github.com/lamovs/nn/internal/app"
	"github.com/lamovs/nn/internal/capture"
	"github.com/lamovs/nn/internal/cli"
	"github.com/lamovs/nn/internal/output"
	"github.com/lamovs/nn/internal/vault"
)

func init() {
	register(command{
		help: cli.Help{
			Verb:    "tags",
			Summary: "list tags by frequency",
			Examples: []cli.Example{
				{Cmd: "nn tags", What: "every tag with its note count, most used first"},
				{Cmd: "nn tags --similar", What: "lists tags with counts and near-duplicate pairs"},
				{Cmd: "nn tags --names", What: "bare tag names, one per line, for shell completion"},
			},
			SeeAlso: []string{"ls", "stats"},
		},
		run: cmdTags,
	})
}

type tagsOptions struct {
	similar bool
	names   bool
}

func parseTagsOptions(args []string) (tagsOptions, error) {
	var opt tagsOptions
	for _, a := range args {
		switch a {
		case "--similar":
			opt.similar = true
		case "--names":
			opt.names = true
		default:
			if strings.HasPrefix(a, "-") && a != "-" {
				return opt, fmt.Errorf("unknown option %q", a)
			}
			return opt, fmt.Errorf("nn tags takes no arguments")
		}
	}
	return opt, nil
}

type tagRow struct {
	Tag   string `json:"tag"`
	Count int    `json:"count"`
}

func tagsSpec(names bool) output.Spec[tagRow] {
	return output.Spec[tagRow]{
		Text: func(w io.Writer, rows []tagRow) error {
			for _, r := range rows {
				if names {
					fmt.Fprintln(w, r.Tag)
					continue
				}
				fmt.Fprintf(w, "%s  %d\n", r.Tag, r.Count)
			}
			return nil
		},
		TSVHeader: []string{"tag", "count"},
		TSV: func(r tagRow) []string {
			return []string{r.Tag, strconv.Itoa(r.Count)}
		},
	}
}

type tagSimilarRow struct {
	Tag      string `json:"tag"`
	Existing string `json:"existing"`
	Count    int    `json:"count"`
}

func tagsSimilarSpec() output.Spec[tagSimilarRow] {
	return output.Spec[tagSimilarRow]{
		Text: func(w io.Writer, rows []tagSimilarRow) error {
			for _, r := range rows {
				fmt.Fprintf(w, "%s~%s\n", r.Tag, r.Existing)
			}
			return nil
		},
		TSVHeader: []string{"tag", "existing", "count"},
		TSV: func(r tagSimilarRow) []string {
			return []string{r.Tag, r.Existing, strconv.Itoa(r.Count)}
		},
	}
}

func countTags(notes []*vault.Note) []tagRow {
	type entry struct {
		display string
		count   int
	}
	byKey := map[string]*entry{}
	var order []string
	for _, n := range notes {
		for _, t := range n.Tags {
			key := strings.ToLower(t)
			e, ok := byKey[key]
			if !ok {
				e = &entry{display: t}
				byKey[key] = e
				order = append(order, key)
			}
			e.count++
		}
	}
	rows := make([]tagRow, len(order))
	for i, key := range order {
		e := byKey[key]
		rows[i] = tagRow{Tag: e.display, Count: e.count}
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].Count != rows[j].Count {
			return rows[i].Count > rows[j].Count
		}
		return strings.ToLower(rows[i].Tag) < strings.ToLower(rows[j].Tag)
	})
	return rows
}

func cmdTags(inv *invocation) int {
	outOpt, rest, err := output.ParseFlags(inv.args)
	if err != nil {
		return inv.misuse("%v", err)
	}
	opt, err := parseTagsOptions(rest)
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
	rows := countTags(notes)

	if opt.similar {
		existing := make(map[string]int, len(rows))
		names := make([]string, len(rows))
		for i, r := range rows {
			existing[r.Tag] = r.Count
			names[i] = r.Tag
		}
		suggestions := capture.SimilarTags(existing, names)
		simRows := make([]tagSimilarRow, len(suggestions))
		for i, s := range suggestions {
			simRows[i] = tagSimilarRow{Tag: s.Tag, Existing: s.Existing, Count: s.Count}
		}
		if err := output.Emit(inv.stdout, outOpt, simRows, tagsSimilarSpec()); err != nil {
			return inv.fail(err)
		}
		if len(simRows) == 0 {
			return output.ExitNotFound
		}
		return output.ExitOK
	}

	if err := output.Emit(inv.stdout, outOpt, rows, tagsSpec(opt.names)); err != nil {
		return inv.fail(err)
	}
	if len(rows) == 0 {
		return output.ExitNotFound
	}
	return output.ExitOK
}
