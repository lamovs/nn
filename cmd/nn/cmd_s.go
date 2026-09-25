package main

import (
	"errors"
	"fmt"
	"io"
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
			Verb:    "s",
			Summary: "search notes and images",
			Examples: []cli.Example{
				{Cmd: "nn s docker prune", What: "lists matching lines across the vault, best matches first"},
				{Cmd: "nn s docker --since 7d --here", What: "only lines from the last week, captured from this directory or repo"},
				{Cmd: "nn s TODO -t work -n 5", What: "the 5 best matches tagged work"},
				{Cmd: "nn s docker -o", What: "opens the best match in your editor, at the matching line"},
				{Cmd: "nn s -t work --since 7d", What: "with no query at all, lists the notes the filters keep"},
			},
			Sections: []cli.HelpSection{
				{Title: "Matching", Items: []string{
					"Every word must occur somewhere in a note: title, aliases, tags, body or an image's OCR text.",
					"A query is required, unless at least one filter (-t, --since, --here, --inbox, --img) is given: filters on their own list whole notes, as \"path (title)\".",
					"A query that is a keyboard shortcut (cmd shift 4) matches shortcuts, in any spelling, instead of words.",
					"Nothing matches as typed: the query is retried in the other keyboard layout, and a note on stderr says so; search.layout_fallback = false turns this retry off.",
					fmt.Sprintf("-n's default is search.limit (default: %d).", def.Search.Limit),
				}},
			},
			SeeAlso: []string{"ls", "show", "snip"},
		},
		run: cmdS,
	})
}

type sOptions struct {
	tags          []string
	here          bool
	since         string
	inbox         bool
	img           bool
	regex         bool
	caseSensitive bool
	limit         int
	hasLimit      bool
	quiet         bool
	open          bool
}

func parseSOptions(args []string) (sOptions, []string, error) {
	var opt sOptions
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
		case "-r":
			opt.regex = true
		case "-c":
			opt.caseSensitive = true
		case "-n":
			if i+1 >= len(args) {
				return opt, nil, errors.New("-n needs a number")
			}
			i++
			n, err := strconv.Atoi(args[i])
			if err != nil || n < 1 {
				return opt, nil, fmt.Errorf("-n: %q is not a positive number", args[i])
			}
			opt.limit, opt.hasLimit = n, true
		case "-q":
			opt.quiet = true
		case "-o":
			opt.open = true
		default:
			if a != "-" && strings.HasPrefix(a, "-") {
				return opt, nil, fmt.Errorf("unknown option %q", a)
			}
			words = append(words, a)
		}
	}
	return opt, words, nil
}

func (o sOptions) hasFilter() bool {
	return len(o.tags) > 0 || o.here || o.since != "" || o.inbox || o.img
}

func parseSince(s string, now time.Time) (time.Time, error) {
	s = strings.TrimSpace(s)
	if s == "today" {
		y, m, d := now.Date()
		return time.Date(y, m, d, 0, 0, 0, 0, now.Location()), nil
	}
	if n, unit, ok := parseElapsedSuffix(s); ok {
		days := n
		if unit == 'w' {
			days *= 7
		}
		return now.AddDate(0, 0, -days), nil
	}
	if t, err := time.ParseInLocation("2006-01-02", s, now.Location()); err == nil {
		return t, nil
	}
	return time.Time{}, fmt.Errorf("--since: %q is not today, Nd, Nw or a YYYY-MM-DD date", s)
}

func parseElapsedSuffix(s string) (n int, unit byte, ok bool) {
	if len(s) < 2 {
		return 0, 0, false
	}
	unit = s[len(s)-1]
	if unit != 'd' && unit != 'w' {
		return 0, 0, false
	}
	v, err := strconv.Atoi(s[:len(s)-1])
	if err != nil || v < 0 {
		return 0, 0, false
	}
	return v, unit, true
}

func rankerFor(env *app.Env) search.Ranker {
	if env.State == nil {
		return nil
	}
	return env.State
}

const kindNote = "note"

type sRow struct {
	Path   string   `json:"path"`
	Line   int      `json:"line,omitempty"`
	Kind   string   `json:"kind"`
	Text   string   `json:"text"`
	Image  string   `json:"image,omitempty"`
	Title  string   `json:"title"`
	Score  float64  `json:"score"`
	Layout bool     `json:"layout,omitempty"`
	Ranges [][2]int `json:"ranges,omitempty"`
}

func sSpec(p cli.Palette) output.Spec[sRow] {
	return output.Spec[sRow]{
		Text: func(w io.Writer, rows []sRow) error {
			lastPath, metaShown := "", false
			for _, r := range rows {
				if r.Path != lastPath {
					lastPath, metaShown = r.Path, false
				}
				switch {
				case r.Image != "":
					fmt.Fprintf(w, "[img] %s  \"%s\"\n", r.Image, highlightRanges(r.Text, r.Ranges, p))
				case r.Line == 0:
					if metaShown {
						continue
					}
					metaShown = true
					fmt.Fprintf(w, "%s  (%s)\n", r.Path, r.Title)
				default:
					fmt.Fprintf(w, "%s:%d  %s\n", r.Path, r.Line, highlightRanges(r.Text, r.Ranges, p))
				}
			}
			return nil
		},
		Path: func(r sRow) string {
			if r.Image != "" {
				return r.Image
			}
			return r.Path
		},
		TSVHeader: []string{"path", "line", "kind", "text"},
		TSV: func(r sRow) []string {
			return []string{r.Path, strconv.Itoa(r.Line), r.Kind, r.Text}
		},
	}
}

func highlightRanges(text string, ranges [][2]int, p cli.Palette) string {
	if len(ranges) == 0 || !p.Enabled() {
		return text
	}
	var b strings.Builder
	prev := 0
	for _, r := range ranges {
		if r[0] < prev || r[1] > len(text) || r[0] > r[1] {
			continue
		}
		b.WriteString(text[prev:r[0]])
		b.WriteString(p.Accent(text[r[0]:r[1]]))
		prev = r[1]
	}
	b.WriteString(text[prev:])
	return b.String()
}

func cmdS(inv *invocation) int {
	outOpt, rest, err := output.ParseFlags(inv.args)
	if err != nil {
		return inv.misuse("%v", err)
	}
	sOpt, words, err := parseSOptions(rest)
	if err != nil {
		return inv.misuse("%v", err)
	}
	if len(words) == 0 && !sOpt.hasFilter() {
		return inv.misuse("s needs a QUERY, or at least one filter: -t, --since, --here, --inbox or --img")
	}

	env, err := app.Open()
	if err != nil {
		return inv.fail(err)
	}
	if !sOpt.hasLimit {
		sOpt.limit = env.Cfg.Search.Limit
	}

	q := search.Query{
		Text:             strings.Join(words, " "),
		Regex:            sOpt.regex,
		CaseSensitive:    sOpt.caseSensitive,
		Tags:             sOpt.tags,
		Inbox:            sOpt.inbox,
		ImagesOnly:       sOpt.img,
		Limit:            sOpt.limit,
		NoLayoutFallback: !env.Cfg.Search.LayoutFallback,
	}
	if sOpt.since != "" {
		since, err := parseSince(sOpt.since, env.Now())
		if err != nil {
			return inv.misuse("%v", err)
		}
		q.Since = since
	}
	if sOpt.here {
		where, repo := vault.Context("")
		q.Here = &search.Here{Cwd: where, Repo: repo}
	}

	docs, err := env.Docs(inv.ctx)
	if err != nil {
		return inv.fail(err)
	}
	results, err := search.Search(inv.ctx, docs, q, rankerFor(env), env.Now())
	if err != nil {
		return inv.fail(err)
	}

	if sOpt.open {
		if code, ok := inv.openBestResult(env, results); !ok {
			return code
		}
	}

	if sOpt.quiet {
		if len(results) == 0 {
			return output.ExitNotFound
		}
		return output.ExitOK
	}

	if len(results) > 0 && results[0].Layout {
		if swapped, ok := search.SwapLayout(q.Text); ok {
			fmt.Fprintf(inv.stderr, "no results for %q, showing %q\n", q.Text, swapped)
		}
	}

	rows := make([]sRow, 0, len(results))
	for _, res := range results {
		if len(res.Hits) == 0 {
			rows = append(rows, sRow{
				Path: res.Doc.Path, Kind: kindNote, Text: res.Doc.Title,
				Title: res.Doc.Title, Score: res.Score,
			})
			continue
		}
		for _, h := range res.Hits {
			rows = append(rows, sRow{
				Path: res.Doc.Path, Line: h.Line, Kind: string(h.Kind), Text: h.Text,
				Image: h.Image, Title: res.Doc.Title, Score: res.Score, Layout: res.Layout,
				Ranges: h.Ranges,
			})
		}
	}
	p := resolveColor(outOpt.Color, outOpt.ColorSet, cli.ColorMode(env.Cfg.Output.Color), inv.stdout)
	if err := output.Emit(inv.stdout, outOpt, rows, sSpec(p)); err != nil {
		return inv.fail(err)
	}
	if len(rows) == 0 {
		return output.ExitNotFound
	}
	return output.ExitOK
}

func (inv *invocation) openBestResult(env *app.Env, results []search.Result) (int, bool) {
	if len(results) == 0 {
		return output.ExitOK, true
	}
	best := results[0]
	line := 0
	for _, h := range best.Hits {
		if h.Line > 0 {
			line = h.Line
			break
		}
	}
	if err := app.Edit(inv.ctx, env, env.Vault.Abs(best.Doc.Path), line); err != nil {
		return inv.fail(err), false
	}
	env.RecordOpen(best.Doc.Path)
	return output.ExitOK, true
}
