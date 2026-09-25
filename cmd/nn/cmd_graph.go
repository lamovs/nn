package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"strconv"
	"strings"

	"github.com/lamovs/nn/internal/app"
	"github.com/lamovs/nn/internal/cli"
	"github.com/lamovs/nn/internal/config"
	"github.com/lamovs/nn/internal/links"
	"github.com/lamovs/nn/internal/output"
	"github.com/lamovs/nn/internal/vault"
)

func init() {
	def := config.Default()
	register(command{
		help: cli.Help{
			Verb:    "graph",
			Summary: "render the link graph",
			Examples: []cli.Example{
				{Cmd: "nn graph docker cleanup --depth 2", What: "prints a two-hop mermaid graph around the note"},
				{Cmd: "nn graph --format dot -t docker", What: "the whole vault's docker-tagged notes, as Graphviz DOT"},
			},
			Sections: []cli.HelpSection{
				{Title: "Scope", Items: []string{
					"With a NOTE, --depth (default 1) bounds how many hops out the graph reaches; without one, the whole vault is graphed and --depth does not apply.",
					"-t restricts the graph to notes carrying that tag: links leaving the tagged set are simply not drawn, the same as any other unresolved link.",
					fmt.Sprintf("--format's default is graph.format (default: %s).", def.Graph.Format),
				}},
			},
			SeeAlso: []string{"links", "backlinks"},
		},
		run: cmdGraph,
	})
}

var graphFormats = schemaEnum("graph.format")

type graphOptions struct {
	hasDepth  bool
	depth     int
	format    string
	hasFormat bool
	tags      []string
}

func parseGraphOptions(args []string) (graphOptions, []string, error) {
	var opt graphOptions
	var words []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			words = append(words, args[i+1:]...)
			break
		}
		switch a {
		case "--depth":
			if i+1 >= len(args) {
				return opt, nil, errors.New("--depth needs a number")
			}
			i++
			n, err := strconv.Atoi(args[i])
			if err != nil || n < 1 {
				return opt, nil, fmt.Errorf("--depth: %q is not a positive number", args[i])
			}
			opt.depth, opt.hasDepth = n, true
		case "--format":
			if i+1 >= len(args) {
				return opt, nil, fmt.Errorf("--format needs a value (%s)", strings.Join(graphFormats, ", "))
			}
			i++
			if !slices.Contains(graphFormats, args[i]) {
				return opt, nil, fmt.Errorf("--format: %q is not one of %s", args[i], strings.Join(graphFormats, ", "))
			}
			opt.format, opt.hasFormat = args[i], true
		case "-t":
			if i+1 >= len(args) {
				return opt, nil, errors.New("-t needs a tag")
			}
			i++
			opt.tags = append(opt.tags, args[i])
		default:
			if a != "-" && strings.HasPrefix(a, "-") {
				return opt, nil, fmt.Errorf("unknown option %q", a)
			}
			words = append(words, a)
		}
	}
	return opt, words, nil
}

func filterNotesByTags(notes []*vault.Note, tags []string) []*vault.Note {
	want := make(map[string]bool, len(tags))
	for _, t := range tags {
		want[strings.ToLower(strings.TrimPrefix(strings.TrimSpace(t), "#"))] = true
	}
	var out []*vault.Note
	for _, n := range notes {
		for _, t := range n.Tags {
			if want[strings.ToLower(t)] {
				out = append(out, n)
				break
			}
		}
	}
	return out
}

type graphNode struct {
	Path  string `json:"path"`
	Title string `json:"title"`
}

type graphEdge struct {
	From string `json:"from"`
	To   string `json:"to"`
}

type graphDoc struct {
	Nodes []graphNode `json:"nodes"`
	Edges []graphEdge `json:"edges"`
}

func renderGraph(w io.Writer, format string, g *links.Graph) error {
	switch format {
	case "dot":
		return links.RenderDOT(w, g)
	case "json":
		return renderGraphJSON(w, g)
	default:
		return links.RenderMermaid(w, g)
	}
}

func renderGraphJSON(w io.Writer, g *links.Graph) error {
	doc := graphDoc{Nodes: []graphNode{}, Edges: []graphEdge{}}
	for _, p := range g.Nodes() {
		doc.Nodes = append(doc.Nodes, graphNode{Path: p, Title: g.Title(p)})
	}
	seen := map[[2]string]bool{}
	for _, e := range g.Edges() {
		if !e.Resolved {
			continue
		}
		key := [2]string{e.From, e.To}
		if seen[key] {
			continue
		}
		seen[key] = true
		doc.Edges = append(doc.Edges, graphEdge{From: e.From, To: e.To})
	}
	return json.NewEncoder(w).Encode(doc)
}

func cmdGraph(inv *invocation) int {
	opt, words, err := parseGraphOptions(inv.args)
	if err != nil {
		return inv.misuse("%v", err)
	}

	env, err := app.Open()
	if err != nil {
		return inv.fail(err)
	}
	if !opt.hasFormat {
		opt.format = env.Cfg.Graph.Format
	}
	notes, err := env.Notes(inv.ctx)
	if err != nil {
		return inv.fail(err)
	}

	hasNote := len(words) > 0
	var rel string
	if hasNote {
		rel, err = env.Resolve(inv.ctx, strings.Join(words, " "))
		if err != nil {
			return inv.fail(err)
		}
	}

	filtered := notes
	if len(opt.tags) > 0 {
		filtered = filterNotesByTags(notes, opt.tags)
	}
	g := links.Build(filtered)

	if hasNote {
		depth := 1
		if opt.hasDepth {
			depth = opt.depth
		}
		g = g.Sub(rel, depth)
	}

	if err := renderGraph(inv.stdout, opt.format, g); err != nil {
		return inv.fail(err)
	}
	return output.ExitOK
}
