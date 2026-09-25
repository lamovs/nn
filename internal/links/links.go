// Package links parses Obsidian-style links and builds the vault's link graph.
package links

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"path"
	"sort"
	"strings"

	"github.com/lamovs/nn/internal/vault"
)

// ErrNotImplemented marks functionality this package only stubs out.
var ErrNotImplemented = errors.New("not implemented")

// Link is one parsed [[link]], [[link|display]], [[link#heading]], [[link^block]], embed, or [text](path.md).
type Link struct {
	Target, Heading, Block, Display string
	Embed                           bool
	Line                            int
}

// Parse returns the links in body outside code, in order; Line counts from bodyStartLine.
func Parse(body string, bodyStartLine int) []Link {
	var out []Link
	for n, line := range strings.Split(vault.MaskCode(body), "\n") {
		for i := 0; i < len(line); i++ {
			if line[i] != '[' {
				continue
			}
			embed := i > 0 && line[i-1] == '!'
			if strings.HasPrefix(line[i:], "[[") {
				end := strings.Index(line[i+2:], "]]")
				if end < 0 {
					break
				}
				l := wikiLink(line[i+2 : i+2+end])
				l.Embed, l.Line = embed, bodyStartLine+n
				out = append(out, l)
				i += 2 + end + 1
				continue
			}
			text, dest, next, ok := vault.MarkdownLinkAt(line, i)
			if !ok {
				continue
			}
			i = next - 1
			local, ok := vault.LocalTarget(dest)
			if !ok {
				continue
			}
			target, heading, _ := strings.Cut(local, "#")
			l := Link{Target: strings.TrimSpace(target), Display: text, Embed: embed, Line: bodyStartLine + n}
			l.Heading, l.Block = splitBlock(heading)
			out = append(out, l)
		}
	}
	return out
}

func wikiLink(inner string) Link {
	var l Link
	target := inner
	if pipe := strings.IndexByte(inner, '|'); pipe >= 0 {
		target, l.Display = inner[:pipe], strings.TrimSpace(inner[pipe+1:])
		// In tables the pipe is escaped as "\|".
		target = strings.TrimSuffix(target, "\\")
	}
	target, heading, hasHeading := strings.Cut(target, "#")
	if !hasHeading {
		target, l.Block, _ = strings.Cut(target, "^")
	} else {
		l.Heading, l.Block = splitBlock(heading)
	}
	l.Target = strings.TrimSpace(target)
	l.Heading = strings.TrimSpace(l.Heading)
	l.Block = strings.TrimSpace(l.Block)
	return l
}

func splitBlock(heading string) (string, string) {
	if block, ok := strings.CutPrefix(heading, "^"); ok {
		return "", block
	}
	return heading, ""
}

// Edge is a resolved or unresolved link between two notes.
type Edge struct {
	From, To, Target string
	Line             int
	Resolved         bool
}

type Graph struct {
	nodes  []string
	titles map[string]string
	edges  []Edge
	out    map[string][]Edge
	in     map[string][]Edge
	idx    *index
}

type index struct {
	paths   map[string]string   // lowercased path -> path
	stems   map[string][]string // lowercased stem -> paths
	aliases map[string][]string // lowercased alias -> paths
}

// attachments are file extensions that never become graph edges.
var attachments = map[string]bool{
	".png": true, ".jpg": true, ".jpeg": true, ".gif": true, ".webp": true, ".svg": true,
	".bmp": true, ".avif": true, ".heic": true, ".tif": true, ".tiff": true, ".ico": true,
	".pdf": true, ".mp3": true, ".wav": true, ".m4a": true, ".ogg": true, ".flac": true,
	".3gp": true, ".webm": true, ".mp4": true, ".mov": true, ".mkv": true, ".ogv": true,
	".canvas": true, ".base": true, ".zip": true, ".csv": true, ".json": true, ".txt": true,
}

// Build parses every note's body and resolves its links against notes.
func Build(notes []*vault.Note) *Graph {
	g := &Graph{
		titles: make(map[string]string, len(notes)),
		idx: &index{
			paths:   map[string]string{},
			stems:   map[string][]string{},
			aliases: map[string][]string{},
		},
	}
	for _, n := range notes {
		if n == nil {
			continue
		}
		if _, dup := g.titles[n.Path]; dup {
			continue
		}
		g.nodes = append(g.nodes, n.Path)
		g.titles[n.Path] = n.Title
		g.idx.paths[strings.ToLower(n.Path)] = n.Path
		stem := strings.ToLower(strings.TrimSuffix(path.Base(n.Path), path.Ext(n.Path)))
		g.idx.stems[stem] = append(g.idx.stems[stem], n.Path)
		for _, a := range n.Aliases {
			key := strings.ToLower(strings.TrimSpace(a))
			g.idx.aliases[key] = append(g.idx.aliases[key], n.Path)
		}
	}
	sort.Strings(g.nodes)

	var edges []Edge
	for _, n := range notes {
		if n == nil {
			continue
		}
		for _, l := range Parse(n.Body, n.BodyStartLine) {
			if l.Target == "" || attachments[strings.ToLower(path.Ext(l.Target))] {
				continue
			}
			to, ok := g.Resolve(n.Path, l.Target)
			edges = append(edges, Edge{From: n.Path, To: to, Target: l.Target, Line: l.Line, Resolved: ok})
		}
	}
	g.setEdges(edges)
	return g
}

func (g *Graph) setEdges(edges []Edge) {
	sort.SliceStable(edges, func(i, j int) bool {
		a, b := edges[i], edges[j]
		if a.From != b.From {
			return a.From < b.From
		}
		return a.Line < b.Line
	})
	g.edges = edges
	g.out = map[string][]Edge{}
	g.in = map[string][]Edge{}
	for _, e := range edges {
		g.out[e.From] = append(g.out[e.From], e)
		if e.Resolved {
			g.in[e.To] = append(g.in[e.To], e)
		}
	}
}

// Resolve looks up target the way Obsidian does: path suffix, basename or alias, ties broken by proximity.
func (g *Graph) Resolve(from, target string) (string, bool) {
	if g == nil || g.idx == nil {
		return "", false
	}
	t := strings.TrimSpace(target)
	if cut := strings.IndexAny(t, "#^|"); cut >= 0 {
		t = strings.TrimSpace(t[:cut])
	}
	t = strings.TrimLeft(strings.ReplaceAll(t, "\\", "/"), "/")
	if t == "" {
		return "", false
	}
	lower := strings.ToLower(t)
	withMD := lower
	if path.Ext(lower) != ".md" {
		withMD += ".md"
	}
	exact := func(p string) (string, bool) {
		p = path.Clean(p)
		if found, ok := g.idx.paths[p]; ok {
			return found, true
		}
		return "", false
	}

	dir := strings.ToLower(path.Dir(from))
	if strings.HasPrefix(t, "./") || strings.HasPrefix(t, "../") {
		if found, ok := exact(path.Join(dir, withMD)); ok {
			return found, true
		}
		if found, ok := exact(path.Join(dir, lower)); ok {
			return found, true
		}
		return "", false
	}
	if strings.Contains(lower, "/") {
		for _, p := range []string{withMD, lower, path.Join(dir, withMD)} {
			if found, ok := exact(p); ok {
				return found, true
			}
		}
	}

	stem := strings.TrimSuffix(path.Base(withMD), ".md")
	cands := g.idx.stems[stem]
	if strings.Contains(withMD, "/") {
		var filtered []string
		for _, c := range cands {
			if strings.HasSuffix("/"+strings.ToLower(c), "/"+path.Clean(withMD)) {
				filtered = append(filtered, c)
			}
		}
		cands = filtered
	}
	if best := vault.Closest(from, cands); best != "" {
		return best, true
	}
	if best := vault.Closest(from, g.idx.aliases[lower]); best != "" {
		return best, true
	}
	return "", false
}

// Out returns the links from path, resolved or not, in line order.
func (g *Graph) Out(path string) []Edge {
	if g == nil {
		return nil
	}
	return g.out[path]
}

// In returns the resolved links to path, ordered by source and line.
func (g *Graph) In(path string) []Edge {
	if g == nil {
		return nil
	}
	return g.in[path]
}

// Unresolved returns every link whose target matches no note.
func (g *Graph) Unresolved() []Edge {
	if g == nil {
		return nil
	}
	var out []Edge
	for _, e := range g.edges {
		if !e.Resolved {
			out = append(out, e)
		}
	}
	return out
}

// Sub returns the part of the graph within depth links of root.
func (g *Graph) Sub(root string, depth int) *Graph {
	sub := &Graph{titles: map[string]string{}, out: map[string][]Edge{}, in: map[string][]Edge{}}
	if g == nil {
		return sub
	}
	sub.idx = g.idx
	if _, ok := g.titles[root]; !ok {
		return sub
	}

	seen := map[string]bool{root: true}
	frontier := []string{root}
	for d := 0; d < depth && len(frontier) > 0; d++ {
		var next []string
		for _, p := range frontier {
			for _, e := range g.out[p] {
				if e.Resolved && !seen[e.To] {
					seen[e.To] = true
					next = append(next, e.To)
				}
			}
			for _, e := range g.in[p] {
				if !seen[e.From] {
					seen[e.From] = true
					next = append(next, e.From)
				}
			}
		}
		frontier = next
	}

	for _, p := range g.nodes {
		if seen[p] {
			sub.nodes = append(sub.nodes, p)
			sub.titles[p] = g.titles[p]
		}
	}
	var edges []Edge
	for _, e := range g.edges {
		if seen[e.From] && (!e.Resolved || seen[e.To]) {
			edges = append(edges, e)
		}
	}
	sub.setEdges(edges)
	return sub
}

// Nodes returns every note path in the graph, sorted.
func (g *Graph) Nodes() []string {
	if g == nil {
		return nil
	}
	return g.nodes
}

// Edges returns every link, resolved or not, ordered by source and line.
func (g *Graph) Edges() []Edge {
	if g == nil {
		return nil
	}
	return g.edges
}

// Title returns the title of the note at path, or "" if it is not a node.
func (g *Graph) Title(path string) string {
	if g == nil {
		return ""
	}
	return g.titles[path]
}

func (g *Graph) label(p string) string {
	if t := strings.TrimSpace(g.titles[p]); t != "" {
		return t
	}
	return p
}

func (g *Graph) renderEdges() (ids map[string]int, pairs [][2]int) {
	ids = make(map[string]int, len(g.nodes))
	for i, p := range g.nodes {
		ids[p] = i
	}
	seen := map[[2]int]bool{}
	for _, e := range g.edges {
		from, okFrom := ids[e.From]
		to, okTo := ids[e.To]
		if !e.Resolved || !okFrom || !okTo {
			continue
		}
		pair := [2]int{from, to}
		if !seen[pair] {
			seen[pair] = true
			pairs = append(pairs, pair)
		}
	}
	return ids, pairs
}

// RenderDOT writes the graph as Graphviz DOT.
func RenderDOT(w io.Writer, g *Graph) error {
	if g == nil {
		g = &Graph{}
	}
	bw := bufio.NewWriter(w)
	fmt.Fprintln(bw, "digraph nn {")
	fmt.Fprintln(bw, "  rankdir=LR;")
	fmt.Fprintln(bw, "  node [shape=box];")
	for i, p := range g.nodes {
		fmt.Fprintf(bw, "  n%d [label=\"%s\", tooltip=\"%s\"];\n", i, dotEscape(g.label(p)), dotEscape(p))
	}
	_, pairs := g.renderEdges()
	for _, pair := range pairs {
		fmt.Fprintf(bw, "  n%d -> n%d;\n", pair[0], pair[1])
	}
	fmt.Fprintln(bw, "}")
	return bw.Flush()
}

func dotEscape(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch r {
		case '\\', '"':
			b.WriteByte('\\')
			b.WriteRune(r)
		case '\n', '\r', '\t':
			b.WriteByte(' ')
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// RenderMermaid writes the graph as a Mermaid flowchart.
func RenderMermaid(w io.Writer, g *Graph) error {
	if g == nil {
		g = &Graph{}
	}
	bw := bufio.NewWriter(w)
	fmt.Fprintln(bw, "flowchart LR")
	for i, p := range g.nodes {
		fmt.Fprintf(bw, "  n%d[\"%s\"]\n", i, mermaidEscape(g.label(p)))
	}
	_, pairs := g.renderEdges()
	for _, pair := range pairs {
		fmt.Fprintf(bw, "  n%d --> n%d\n", pair[0], pair[1])
	}
	return bw.Flush()
}

func mermaidEscape(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch r {
		case '#':
			b.WriteString("#35;")
		case '"':
			b.WriteString("#34;")
		case '&':
			b.WriteString("#38;")
		case '<':
			b.WriteString("#60;")
		case '>':
			b.WriteString("#62;")
		case '`':
			b.WriteString("#96;")
		case '\n', '\r', '\t':
			b.WriteByte(' ')
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}
