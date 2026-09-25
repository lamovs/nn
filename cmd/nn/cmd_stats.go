package main

import (
	"fmt"
	"io"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/lamovs/nn/internal/app"
	"github.com/lamovs/nn/internal/cli"
	"github.com/lamovs/nn/internal/config"
	"github.com/lamovs/nn/internal/output"
	"github.com/lamovs/nn/internal/vault"
)

func init() {
	def := config.Default()
	register(command{
		help: cli.Help{
			Verb:    "stats",
			Summary: "summarize vault activity",
			Examples: []cli.Example{
				{Cmd: "nn stats --by month", What: "prints an ASCII bar chart of notes per month"},
				{Cmd: "nn stats --by repo", What: "notes per captured repo, most active first"},
			},
			Sections: []cli.HelpSection{
				{Title: "Buckets", Items: []string{
					fmt.Sprintf("--by's default is stats.by (default: %s).", def.Stats.By),
					"week and month use each note's frontmatter date, or its file modification time when that is unset; buckets are ordered chronologically.",
					"tag, repo and via are ordered most-used first. A note with no repo or via is counted under (none).",
				}},
			},
			SeeAlso: []string{"tags", "ls"},
		},
		run: cmdStats,
	})
}

var statsByValues = schemaEnum("stats.by")

type statsOptions struct {
	by    string
	hasBy bool
}

func parseStatsOptions(args []string) (statsOptions, error) {
	var opt statsOptions
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch a {
		case "--by":
			if i+1 >= len(args) {
				return opt, fmt.Errorf("--by needs a value (%s)", strings.Join(statsByValues, ", "))
			}
			i++
			if !slices.Contains(statsByValues, args[i]) {
				return opt, fmt.Errorf("--by: %q is not one of %s", args[i], strings.Join(statsByValues, ", "))
			}
			opt.by, opt.hasBy = args[i], true
		default:
			if strings.HasPrefix(a, "-") && a != "-" {
				return opt, fmt.Errorf("unknown option %q", a)
			}
			return opt, fmt.Errorf("nn stats takes no arguments")
		}
	}
	return opt, nil
}

type statsRow struct {
	Bucket string `json:"bucket"`
	Count  int    `json:"count"`
}

func statsSpec() output.Spec[statsRow] {
	return output.Spec[statsRow]{
		Text: func(w io.Writer, rows []statsRow) error {
			label, maxCount := 0, 0
			for _, r := range rows {
				label = max(label, cli.DisplayWidth(r.Bucket))
				maxCount = max(maxCount, r.Count)
			}
			bar := cli.Width - label - len(strconv.Itoa(maxCount)) - 4
			for _, r := range rows {
				fmt.Fprintf(w, "%-*s  %s  %d\n", label, r.Bucket, statsBar(r.Count, maxCount, bar), r.Count)
			}
			return nil
		},
		TSVHeader: []string{"bucket", "count"},
		TSV: func(r statsRow) []string {
			return []string{r.Bucket, strconv.Itoa(r.Count)}
		},
	}
}

func statsBar(count, maxCount, width int) string {
	if count <= 0 {
		return ""
	}
	if width < 1 {
		width = 1
	}
	if maxCount <= width {
		return strings.Repeat("#", count)
	}
	n := (count*width + maxCount/2) / maxCount
	return strings.Repeat("#", min(max(n, 1), width))
}

func effectiveNoteDate(n *vault.Note) time.Time {
	if !n.Date.IsZero() {
		return n.Date
	}
	return n.Modified
}

func weekStart(t time.Time) time.Time {
	weekday := int(t.Weekday())
	if weekday == 0 {
		weekday = 7
	}
	y, m, d := t.Date()
	day := time.Date(y, m, d, 0, 0, 0, 0, t.Location())
	return day.AddDate(0, 0, -(weekday - 1))
}

const noBucket = "(none)"

func statsRows(notes []*vault.Note, by string) []statsRow {
	if by == "tag" {
		tagRows := countTags(notes)
		rows := make([]statsRow, len(tagRows))
		for i, r := range tagRows {
			rows[i] = statsRow{Bucket: r.Tag, Count: r.Count}
		}
		return rows
	}

	counts := map[string]int{}
	for _, n := range notes {
		counts[statsKey(n, by)]++
	}
	rows := make([]statsRow, 0, len(counts))
	for k, c := range counts {
		rows = append(rows, statsRow{Bucket: k, Count: c})
	}
	if by == "week" || by == "month" {
		sort.Slice(rows, func(i, j int) bool { return rows[i].Bucket < rows[j].Bucket })
	} else {
		sort.Slice(rows, func(i, j int) bool {
			if rows[i].Count != rows[j].Count {
				return rows[i].Count > rows[j].Count
			}
			return rows[i].Bucket < rows[j].Bucket
		})
	}
	return rows
}

func statsKey(n *vault.Note, by string) string {
	switch by {
	case "repo":
		if n.Repo == "" {
			return noBucket
		}
		return n.Repo
	case "via":
		if n.Via == "" {
			return noBucket
		}
		return n.Via
	case "month":
		if d := effectiveNoteDate(n); !d.IsZero() {
			return d.Format("2006-01")
		}
		return noBucket
	default: // "week"
		if d := effectiveNoteDate(n); !d.IsZero() {
			return weekStart(d).Format("2006-01-02")
		}
		return noBucket
	}
}

func cmdStats(inv *invocation) int {
	outOpt, rest, err := output.ParseFlags(inv.args)
	if err != nil {
		return inv.misuse("%v", err)
	}
	opt, err := parseStatsOptions(rest)
	if err != nil {
		return inv.misuse("%v", err)
	}

	env, err := app.Open()
	if err != nil {
		return inv.fail(err)
	}
	if !opt.hasBy {
		opt.by = env.Cfg.Stats.By
	}
	notes, err := env.Notes(inv.ctx)
	if err != nil {
		return inv.fail(err)
	}

	rows := statsRows(notes, opt.by)
	if err := output.Emit(inv.stdout, outOpt, rows, statsSpec()); err != nil {
		return inv.fail(err)
	}
	return output.ExitOK
}
