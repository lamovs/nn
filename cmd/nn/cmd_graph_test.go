package main

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestGraphMermaidWholeVault(t *testing.T) {
	root := newTestVault(t)
	writeNote(t, root, "nn/a.md", "# A\n\n[[b]]\n")
	writeNote(t, root, "nn/b.md", "# B\n\nbody\n")

	stdout, _, code := runCmd(t, "", "graph")
	if code != 0 {
		t.Fatalf("code = %d", code)
	}
	if !strings.HasPrefix(stdout, "flowchart LR") {
		t.Errorf("stdout = %q, want a mermaid flowchart", stdout)
	}
	if !strings.Contains(stdout, "-->") {
		t.Errorf("stdout = %q, want an edge between A and B", stdout)
	}
}

func TestGraphDotFormat(t *testing.T) {
	root := newTestVault(t)
	writeNote(t, root, "nn/a.md", "# A\n\n[[b]]\n")
	writeNote(t, root, "nn/b.md", "# B\n\nbody\n")

	stdout, _, code := runCmd(t, "", "graph", "--format", "dot")
	if code != 0 {
		t.Fatalf("code = %d", code)
	}
	if !strings.HasPrefix(stdout, "digraph nn {") {
		t.Errorf("stdout = %q, want Graphviz DOT", stdout)
	}
}

func TestGraphFormatDefaultFromConfig(t *testing.T) {
	root := newTestVault(t)
	writeConfig(t, "[graph]\nformat = \"dot\"\n")
	writeNote(t, root, "nn/a.md", "# A\n\n[[b]]\n")
	writeNote(t, root, "nn/b.md", "# B\n\nbody\n")

	stdout, stderr, code := runCmd(t, "", "graph")
	if code != 0 {
		t.Fatalf("code = %d, stderr = %q", code, stderr)
	}
	if !strings.HasPrefix(stdout, "digraph nn {") {
		t.Errorf("stdout = %q, want Graphviz DOT (graph.format = dot)", stdout)
	}
}

func TestGraphFormatFlagBeatsConfig(t *testing.T) {
	root := newTestVault(t)
	writeConfig(t, "[graph]\nformat = \"dot\"\n")
	writeNote(t, root, "nn/a.md", "# A\n\n[[b]]\n")
	writeNote(t, root, "nn/b.md", "# B\n\nbody\n")

	stdout, stderr, code := runCmd(t, "", "graph", "--format", "mermaid")
	if code != 0 {
		t.Fatalf("code = %d, stderr = %q", code, stderr)
	}
	if !strings.HasPrefix(stdout, "flowchart LR") {
		t.Errorf("stdout = %q, want mermaid (--format beats graph.format = dot)", stdout)
	}
}

func TestGraphJSONFormat(t *testing.T) {
	root := newTestVault(t)
	writeNote(t, root, "nn/a.md", "# A\n\n[[b]]\n")
	writeNote(t, root, "nn/b.md", "# B\n\nbody\n")

	stdout, _, code := runCmd(t, "", "graph", "--format", "json")
	if code != 0 {
		t.Fatalf("code = %d", code)
	}
	var doc graphDoc
	if err := json.Unmarshal([]byte(stdout), &doc); err != nil {
		t.Fatalf("decode: %v\n%s", err, stdout)
	}
	if len(doc.Nodes) != 2 {
		t.Errorf("nodes = %+v, want 2", doc.Nodes)
	}
	if len(doc.Edges) != 1 || doc.Edges[0].From != "nn/a.md" || doc.Edges[0].To != "nn/b.md" {
		t.Errorf("edges = %+v", doc.Edges)
	}
}

func TestGraphDepthLimitsHops(t *testing.T) {
	root := newTestVault(t)
	writeNote(t, root, "nn/a.md", "# A\n\n[[b]]\n")
	writeNote(t, root, "nn/b.md", "# B\n\n[[c]]\n")
	writeNote(t, root, "nn/c.md", "# C\n\nbody\n")

	stdout, _, code := runCmd(t, "", "graph", "a", "--format", "json")
	if code != 0 {
		t.Fatalf("code = %d", code)
	}
	var doc graphDoc
	if err := json.Unmarshal([]byte(stdout), &doc); err != nil {
		t.Fatal(err)
	}
	if len(doc.Nodes) != 2 {
		t.Errorf("default depth 1: nodes = %+v, want A and B only", doc.Nodes)
	}

	stdout, _, code = runCmd(t, "", "graph", "a", "--depth", "2", "--format", "json")
	if code != 0 {
		t.Fatalf("code = %d", code)
	}
	if err := json.Unmarshal([]byte(stdout), &doc); err != nil {
		t.Fatal(err)
	}
	if len(doc.Nodes) != 3 {
		t.Errorf("depth 2: nodes = %+v, want all three notes", doc.Nodes)
	}
}

func TestGraphTagFilter(t *testing.T) {
	root := newTestVault(t)
	writeNote(t, root, "nn/a.md", "---\ntags: [work]\n---\n\n# A\n\n[[b]]\n")
	writeNote(t, root, "nn/b.md", "---\ntags: [home]\n---\n\n# B\n\nbody\n")

	stdout, _, code := runCmd(t, "", "graph", "-t", "work", "--format", "json")
	if code != 0 {
		t.Fatalf("code = %d", code)
	}
	var doc graphDoc
	if err := json.Unmarshal([]byte(stdout), &doc); err != nil {
		t.Fatal(err)
	}
	if len(doc.Nodes) != 1 || doc.Nodes[0].Path != "nn/a.md" {
		t.Errorf("nodes = %+v, want only nn/a.md", doc.Nodes)
	}
	if len(doc.Edges) != 0 {
		t.Errorf("edges = %+v, want none (b is filtered out)", doc.Edges)
	}
}

func TestGraphNoteNotFoundFails(t *testing.T) {
	newTestVault(t)
	_, _, code := runCmd(t, "", "graph", "nowhere-at-all")
	if code != 2 {
		t.Errorf("code = %d, want 2", code)
	}
}

func TestGraphEmptyVaultExitsOK(t *testing.T) {
	newTestVault(t)
	stdout, _, code := runCmd(t, "", "graph")
	if code != 0 {
		t.Errorf("code = %d, want 0 even for an empty vault", code)
	}
	if !strings.HasPrefix(stdout, "flowchart LR") {
		t.Errorf("stdout = %q", stdout)
	}
}
