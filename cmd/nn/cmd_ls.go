package main

import (
	"errors"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/lamovs/nn/internal/app"
	"github.com/lamovs/nn/internal/cli"
	"github.com/lamovs/nn/internal/config"
	"github.com/lamovs/nn/internal/output"
	"github.com/lamovs/nn/internal/search"
	"github.com/lamovs/nn/internal/vault"
)

func init() {
	def := config.Default()
	register(command{
		help: cli.Help{
			Verb:    "ls",
			Summary: "list notes",
			Examples: []cli.Example{
				{Cmd: "nn ls --since 7d", What: "notes modified in the last week, newest first"},
				{Cmd: "nn ls --sort opened -n 10", What: "the 10 notes opened most often or most recently"},
				{Cmd: "nn ls -t docker --here", What: "notes tagged docker, captured from this directory or repo"},
			},
			Sections: []cli.HelpSection{
				{Title: "Filters and sorting", Items: []string{
					"Filters are the same as nn s's, minus a search query: -t, --since, --here, --inbox and --img.",
					fmt.Sprintf("--sort's default is ls.sort (default: %s); opened orders by frecency (internal/state): a mix of how often and how recently a note was opened.", def.Ls.Sort),
					fmt.Sprintf("-n's default is ls.limit (default: %d, all); -n all cancels a non-zero ls.limit back to no limit.", def.Ls.Limit),
					"--img keeps only entries that have an image: a note that embeds one is listed under its own path, an image lying on its own under the image's path.",
					"Without --img the listing is notes only; images lying on their own are left out.",
				}},
			},
			SeeAlso: []string{"s", "show", "tags"},
		},
		run: cmdLs,
	})
}

type lsOptions struct {
	tags     []string
	here     bool
	since    string
	inbox    bool
	img      bool
	sort     string
	hasSort  bool
	limit    int
	hasLimit bool
}

var lsSortValues = schemaEnum("ls.sort")

func parseLsOptions(args []string) (lsOptions, []string, error) {
	var opt lsOptions
	var words []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			words = append(words, args[i+1:]...)
			break
		}
		switch a {
		case "-t":
			if i+1 >= len(args) {
				return opt, nil, errors.New("-t needs a tag")
			}
			i++
			opt.tags = append(opt.tags, args[i])
		case "--here":
			opt.here = true
		case "--since":
			if i+1 >= len(args) {
				return opt, nil, errors.New("--since needs a value (today, Nd, Nw or YYYY-MM-DD)")
			}
			i++
			opt.since = args[i]
		case "--inbox":
			opt.inbox = true
		case "--img":
			opt.img = true
		case "--sort":
			if i+1 >= len(args) {
				return opt, nil, fmt.Errorf("--sort needs a value (%s)", strings.Join(lsSortValues, ", "))
			}
			i++
			if !isLsSortValue(args[i]) {
				return opt, nil, fmt.Errorf("--sort: %q is not one of %s", args[i], strings.Join(lsSortValues, ", "))
			}
			opt.sort, opt.hasSort = args[i], true
		case "-n":
			if i+1 >= len(args) {
				return opt, nil, errors.New("-n needs a number")
			}
			i++
			if args[i] == "all" {
				opt.limit, opt.hasLimit = 0, true
				continue
			}
			n, err := strconv.Atoi(args[i])
			if err != nil || n < 1 {
				return opt, nil, fmt.Errorf("-n: %q is not \"all\" or a positive number", args[i])
			}
			opt.limit, opt.hasLimit = n, true
		default:
			if a != "-" && strings.HasPrefix(a, "-") {
				return opt, nil, fmt.Errorf("unknown option %q", a)
			}
			words = append(words, a)
		}
	}
	return opt, words, nil
}

func isLsSortValue(s string) bool {
	for _, v := range lsSortValues {
		if s == v {
			return true
		}
	}
	return false
}

func effectiveDocDate(d *search.Doc) time.Time {
	if !d.Date.IsZero() {
		return d.Date
	}
	return d.Modified
}

func sortDocs(docs []*search.Doc, by string, env *app.Env) {
	var less func(a, b *search.Doc) bool
	switch by {
	case "date":
		less = func(a, b *search.Doc) bool {
			da, db := effectiveDocDate(a), effectiveDocDate(b)
			if !da.Equal(db) {
				return da.After(db)
			}
			return a.Path < b.Path
		}
	case "opened":
		less = func(a, b *search.Doc) bool {
			fa, fb := frecencyFor(env, a.Path), frecencyFor(env, b.Path)
			if fa != fb {
				return fa > fb
			}
			return a.Path < b.Path
		}
	case "title":
		less = func(a, b *search.Doc) bool {
			ta, tb := strings.ToLower(a.Title), strings.ToLower(b.Title)
			if ta != tb {
				return ta < tb
			}
			return a.Path < b.Path
		}
	default: // "modified"
		less = func(a, b *search.Doc) bool {
			if !a.Modified.Equal(b.Modified) {
				return a.Modified.After(b.Modified)
			}
			return a.Path < b.Path
		}
	}
	sort.Slice(docs, func(i, j int) bool { return less(docs[i], docs[j]) })
}

func frecencyFor(env *app.Env, path string) float64 {
	if env.State == nil {
		return 0
	}
	return env.State.Frecency(path)
}

type lsRow struct {
	Path  string   `json:"path"`
	Title string   `json:"title"`
	Date  string   `json:"date"`
	Tags  []string `json:"tags"`
}

func lsSpec() output.Spec[lsRow] {
	return output.Spec[lsRow]{
		Text: func(w io.Writer, rows []lsRow) error {
			for _, r := range rows {
				line := r.Date + "  " + r.Path + "  " + r.Title
				for _, tg := range r.Tags {
					line += " #" + tg
				}
				if _, err := fmt.Fprintln(w, line); err != nil {
					return err
				}
			}
			return nil
		},
		Path:      func(r lsRow) string { return r.Path },
		TSVHeader: []string{"date", "path", "title", "tags"},
		TSV: func(r lsRow) []string {
			return []string{r.Date, r.Path, r.Title, strings.Join(r.Tags, ",")}
		},
	}
}

func cmdLs(inv *invocation) int {
	outOpt, rest, err := output.ParseFlags(inv.args)
	if err != nil {
		return inv.misuse("%v", err)
	}
	lsOpt, words, err := parseLsOptions(rest)
	if err != nil {
		return inv.misuse("%v", err)
	}
	if len(words) > 0 {
		return inv.misuse("nn ls takes no arguments; try: nn s %s", strings.Join(words, " "))
	}

	env, err := app.Open()
	if err != nil {
		return inv.fail(err)
	}
	if !lsOpt.hasSort {
		lsOpt.sort = env.Cfg.Ls.Sort
	}
	if !lsOpt.hasLimit {
		lsOpt.limit = env.Cfg.Ls.Limit
	}

	q := search.Query{Tags: lsOpt.tags, Inbox: lsOpt.inbox, ImagesOnly: lsOpt.img}
	if lsOpt.since != "" {
		since, err := parseSince(lsOpt.since, env.Now())
		if err != nil {
			return inv.misuse("%v", err)
		}
		q.Since = since
	}
	if lsOpt.here {
		where, repo := vault.Context("")
		q.Here = &search.Here{Cwd: where, Repo: repo}
	}

	docs, err := env.Docs(inv.ctx)
	if err != nil {
		return inv.fail(err)
	}
	results, err := search.Search(inv.ctx, docs, q, nil, env.Now())
	if err != nil {
		return inv.fail(err)
	}

	list := make([]*search.Doc, 0, len(results))
	for _, r := range results {
		if !lsOpt.img && vault.IsImage(r.Doc.Path) {
			continue
		}
		list = append(list, r.Doc)
	}
	sortDocs(list, lsOpt.sort, env)
	if lsOpt.limit > 0 && len(list) > lsOpt.limit {
		list = list[:lsOpt.limit]
	}

	rows := make([]lsRow, len(list))
	for i, d := range list {
		date := effectiveDocDate(d)
		if !date.IsZero() {
			rows[i].Date = date.Format("2006-01-02")
		}
		rows[i].Path, rows[i].Title, rows[i].Tags = d.Path, d.Title, d.Tags
	}
	if err := output.Emit(inv.stdout, outOpt, rows, lsSpec()); err != nil {
		return inv.fail(err)
	}
	if len(rows) == 0 {
		return output.ExitNotFound
	}
	return output.ExitOK
}
