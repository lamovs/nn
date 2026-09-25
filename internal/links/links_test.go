package links

import (
	"bytes"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/lamovs/nn/internal/vault"
)

func TestParse(t *testing.T) {
	body := strings.Join([]string{
		"See [[docker]] and [[Docker Notes|the notes]].",                      // 10
		"Heading [[colima#Install steps]] block [[colima#^abc123]] [[x^blk]]", // 11
		"Embed ![[diagram.png|300]] and ![[other note]]",                      // 12
		"Markdown [text](notes/docker.md#usage) ![alt](img/shot%201.png)",     // 13
		"External [site](https://example.com) [mail](mailto:a@b) [top](#top)", // 14
		"`[[in code]]` and ``[x](y.md)``",                                     // 15
		"```",                                                                 // 16
		"[[fenced]]",                                                          // 17
		"```",                                                                 // 18
		"| [[table\\|cell]] | [[#Local heading]] |",                           // 19
		"[[ spaced / path ]] [not a link] [[unclosed",                         // 20
		"[nested [brackets]](a%20b.md \"title\") [angle](<with space.md>)",    // 21
	}, "\n")
	got := Parse(body, 10)
	want := []Link{
		{Target: "docker", Line: 10},
		{Target: "Docker Notes", Display: "the notes", Line: 10},
		{Target: "colima", Heading: "Install steps", Line: 11},
		{Target: "colima", Block: "abc123", Line: 11},
		{Target: "x", Block: "blk", Line: 11},
		{Target: "diagram.png", Display: "300", Embed: true, Line: 12},
		{Target: "other note", Embed: true, Line: 12},
		{Target: "notes/docker.md", Heading: "usage", Display: "text", Line: 13},
		{Target: "img/shot 1.png", Display: "alt", Embed: true, Line: 13},
		{Target: "table", Display: "cell", Line: 19},
		{Target: "", Heading: "Local heading", Line: 19},
		{Target: "spaced / path", Line: 20},
		{Target: "a b.md", Display: "nested [brackets]", Line: 21},
		{Target: "with space.md", Display: "angle", Line: 21},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Parse =\n%s\nwant\n%s", dump(got), dump(want))
	}
}

func dump(links []Link) string {
	var b strings.Builder
	for _, l := range links {
		fmt.Fprintf(&b, "%+v\n", l)
	}
	return b.String()
}

func note(path, title string, aliases []string, body string) *vault.Note {
	return &vault.Note{Path: path, Title: title, Aliases: aliases, Body: body, BodyStartLine: 1}
}

func fixtureGraph() *Graph {
	return Build([]*vault.Note{
		note("index.md", "Index", nil, "[[docker]]\n[[colima]] [[Go Channels]]\n[[missing]] ![[pic.png]] [x](https://e.com)\n[[work/review]] [[#self]]"),
		note("notes/docker.md", "Docker", []string{"Docker notes"}, "[[colima#Install]]\n[[../index]]"),
		note("archive/docker.md", "Old docker", nil, "[[docker]]"),
		note("colima.md", "Colima", []string{"Colima"}, "[[Docker]]"),
		note("go/go-concurrency.md", "Go Concurrency", []string{"Go channels"}, ""),
		note("work/review.md", "Review", nil, "[[./sub/deep]] [[deep]]"),
		note("work/sub/deep.md", "Deep", nil, ""),
		note("Deep.md", "Deep root", nil, ""),
	})
}

func TestResolve(t *testing.T) {
	g := fixtureGraph()
	tests := []struct {
		from, target, want string
		ok                 bool
	}{
		{"index.md", "colima", "colima.md", true},
		{"index.md", "COLIMA.md", "colima.md", true},
		{"index.md", "go channels", "go/go-concurrency.md", true},
		{"index.md", "work/review", "work/review.md", true},
		{"index.md", "sub/deep", "work/sub/deep.md", true},
		{"index.md", "colima#Heading", "colima.md", true},
		{"index.md", "colima|Display", "colima.md", true},
		{"index.md", "nothing", "", false},
		{"index.md", "", "", false},
		// Two notes named docker: the one closer to from wins.
		{"notes/other.md", "docker", "notes/docker.md", true},
		{"archive/x.md", "docker", "archive/docker.md", true},
		// Equally far: fewer path segments, then lexical order.
		{"index.md", "docker", "archive/docker.md", true},
		{"work/review.md", "deep", "Deep.md", true},
		{"work/sub/other.md", "deep", "work/sub/deep.md", true},
		{"index.md", "deep", "Deep.md", true},
		{"work/review.md", "./sub/deep", "work/sub/deep.md", true},
		{"notes/docker.md", "../index", "index.md", true},
		{"index.md", "../index", "", false},
	}
	for _, tt := range tests {
		got, ok := g.Resolve(tt.from, tt.target)
		if got != tt.want || ok != tt.ok {
			t.Errorf("Resolve(%q, %q) = %q, %v; want %q, %v", tt.from, tt.target, got, ok, tt.want, tt.ok)
		}
	}
	var nilGraph *Graph
	if _, ok := nilGraph.Resolve("a.md", "b"); ok {
		t.Error("nil graph resolved a link")
	}
}

func TestGraph(t *testing.T) {
	g := fixtureGraph()

	if want := []string{"Deep.md", "archive/docker.md", "colima.md", "go/go-concurrency.md", "index.md", "notes/docker.md", "work/review.md", "work/sub/deep.md"}; !reflect.DeepEqual(g.Nodes(), want) {
		t.Fatalf("Nodes = %v", g.Nodes())
	}

	wantOut := []Edge{
		{From: "index.md", To: "archive/docker.md", Target: "docker", Line: 1, Resolved: true},
		{From: "index.md", To: "colima.md", Target: "colima", Line: 2, Resolved: true},
		{From: "index.md", To: "go/go-concurrency.md", Target: "Go Channels", Line: 2, Resolved: true},
		{From: "index.md", Target: "missing", Line: 3},
		{From: "index.md", To: "work/review.md", Target: "work/review", Line: 4, Resolved: true},
	}
	if got := g.Out("index.md"); !reflect.DeepEqual(got, wantOut) {
		t.Fatalf("Out(index) =\n%+v\nwant\n%+v", got, wantOut)
	}

	wantIn := []Edge{
		{From: "notes/docker.md", To: "colima.md", Target: "colima", Line: 1, Resolved: true},
		{From: "index.md", To: "colima.md", Target: "colima", Line: 2, Resolved: true},
	}
	gotIn := g.In("colima.md")
	if len(gotIn) != 2 || !containsEdges(gotIn, wantIn) {
		t.Fatalf("In(colima) = %+v", gotIn)
	}

	if got := g.Unresolved(); len(got) != 1 || got[0].Target != "missing" || got[0].From != "index.md" {
		t.Fatalf("Unresolved = %+v", got)
	}
	if got := g.In("nothing.md"); got != nil {
		t.Fatalf("In(nothing) = %+v", got)
	}

	sub := g.Sub("colima.md", 1)
	if want := []string{"archive/docker.md", "colima.md", "index.md", "notes/docker.md"}; !reflect.DeepEqual(sub.Nodes(), want) {
		t.Fatalf("Sub nodes = %v", sub.Nodes())
	}
	for _, e := range sub.Edges() {
		if e.Resolved && (e.To == "go/go-concurrency.md" || e.To == "work/review.md") {
			t.Fatalf("Sub kept an edge to an excluded node: %+v", e)
		}
	}
	if got := sub.Unresolved(); len(got) != 1 {
		t.Fatalf("Sub unresolved = %+v", got)
	}
	if got, ok := sub.Resolve("index.md", "go channels"); !ok || got != "go/go-concurrency.md" {
		t.Fatalf("Sub resolve = %q %v", got, ok)
	}
	if got := g.Sub("colima.md", 0).Nodes(); !reflect.DeepEqual(got, []string{"colima.md"}) {
		t.Fatalf("Sub depth 0 = %v", got)
	}
	if got := g.Sub("colima.md", 5).Nodes(); len(got) != 8 {
		t.Fatalf("Sub depth 5 = %v", got)
	}
	if got := g.Sub("nope.md", 3).Nodes(); len(got) != 0 {
		t.Fatalf("Sub of unknown root = %v", got)
	}
}

func containsEdges(got, want []Edge) bool {
	for _, w := range want {
		found := false
		for _, e := range got {
			if e == w {
				found = true
			}
		}
		if !found {
			return false
		}
	}
	return true
}

func TestRender(t *testing.T) {
	g := Build([]*vault.Note{
		note("a.md", `Quote "and" <tag> & #hash`, nil, "[[b]] [[b]] [[c]]"),
		note("b.md", "Заметка про `код`", nil, "[[a]]\n[[missing]]"),
		note("c d/c.md", "", nil, "[[c]]"),
	})

	var mermaid bytes.Buffer
	if err := RenderMermaid(&mermaid, g); err != nil {
		t.Fatal(err)
	}
	wantMermaid := "flowchart LR\n" +
		"  n0[\"Quote #34;and#34; #60;tag#62; #38; #35;hash\"]\n" +
		"  n1[\"Заметка про #96;код#96;\"]\n" +
		"  n2[\"c d/c.md\"]\n" +
		"  n0 --> n1\n" +
		"  n0 --> n2\n" +
		"  n1 --> n0\n" +
		"  n2 --> n2\n"
	if mermaid.String() != wantMermaid {
		t.Fatalf("mermaid =\n%s\nwant\n%s", mermaid.String(), wantMermaid)
	}

	var dot bytes.Buffer
	if err := RenderDOT(&dot, g); err != nil {
		t.Fatal(err)
	}
	wantDOT := "digraph nn {\n  rankdir=LR;\n  node [shape=box];\n" +
		"  n0 [label=\"Quote \\\"and\\\" <tag> & #hash\", tooltip=\"a.md\"];\n" +
		"  n1 [label=\"Заметка про `код`\", tooltip=\"b.md\"];\n" +
		"  n2 [label=\"c d/c.md\", tooltip=\"c d/c.md\"];\n" +
		"  n0 -> n1;\n  n0 -> n2;\n  n1 -> n0;\n  n2 -> n2;\n}\n"
	if dot.String() != wantDOT {
		t.Fatalf("dot =\n%s\nwant\n%s", dot.String(), wantDOT)
	}

	var empty bytes.Buffer
	if err := RenderMermaid(&empty, nil); err != nil || empty.String() != "flowchart LR\n" {
		t.Fatalf("empty mermaid = %q, %v", empty.String(), err)
	}
	empty.Reset()
	if err := RenderDOT(&empty, Build(nil)); err != nil || !strings.HasSuffix(empty.String(), "}\n") {
		t.Fatalf("empty dot = %q, %v", empty.String(), err)
	}

	if got := dotEscape(`back\slash`); got != `back\\slash` {
		t.Fatalf("dotEscape = %s", got)
	}
}
