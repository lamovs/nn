package main

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/lamovs/nn/internal/cli"
)

func TestStatsByRepo(t *testing.T) {
	root := newTestVault(t)
	writeNote(t, root, "nn/a.md", "---\nrepo: nn\n---\n\nbody\n")
	writeNote(t, root, "nn/b.md", "---\nrepo: nn\n---\n\nbody\n")
	writeNote(t, root, "nn/c.md", "# C\n\nbody\n")

	stdout, _, code := runCmd(t, "", "stats", "--by", "repo")
	if code != 0 {
		t.Fatalf("code = %d", code)
	}
	if !strings.Contains(stdout, "nn") || !strings.Contains(stdout, "##") {
		t.Errorf("stdout = %q, want an nn bucket with a 2-# bar", stdout)
	}
	if !strings.Contains(stdout, "(none)") {
		t.Errorf("stdout = %q, want a (none) bucket for the repo-less note", stdout)
	}
}

func TestStatsByDefaultFromConfig(t *testing.T) {
	root := newTestVault(t)
	writeConfig(t, "[stats]\nby = \"repo\"\n")
	writeNote(t, root, "nn/a.md", "---\nrepo: nn\n---\n\nbody\n")
	writeNote(t, root, "nn/b.md", "# B\n\nbody\n")

	stdout, stderr, code := runCmd(t, "", "stats")
	if code != 0 {
		t.Fatalf("code = %d, stderr = %q", code, stderr)
	}
	if !strings.Contains(stdout, "nn") || !strings.Contains(stdout, "(none)") {
		t.Errorf("stdout = %q, want an nn and a (none) bucket (stats.by = repo)", stdout)
	}
}

func TestStatsByFlagBeatsConfig(t *testing.T) {
	root := newTestVault(t)
	writeConfig(t, "[stats]\nby = \"repo\"\n")
	writeNote(t, root, "nn/a.md", "---\nrepo: nn\ntags: [work]\n---\n\nbody\n")

	stdout, stderr, code := runCmd(t, "", "stats", "--by", "tag")
	if code != 0 {
		t.Fatalf("code = %d, stderr = %q", code, stderr)
	}
	if !strings.Contains(stdout, "work") {
		t.Errorf("stdout = %q, want the work tag bucket (--by beats stats.by = repo)", stdout)
	}
}

func TestStatsByWeekChronological(t *testing.T) {
	root := newTestVault(t)
	writeNote(t, root, "nn/old.md", "# Old\n\nbody\n")
	setModified(t, root, "nn/old.md", time.Now().Add(-30*24*time.Hour))
	writeNote(t, root, "nn/new.md", "# New\n\nbody\n")

	stdout, _, code := runCmd(t, "", "stats", "--by", "week", "--tsv")
	if code != 0 {
		t.Fatalf("code = %d", code)
	}
	lines := strings.Split(strings.TrimRight(stdout, "\n"), "\n")
	if len(lines) != 3 { // header + 2 buckets
		t.Fatalf("lines = %v", lines)
	}
	if lines[1] >= lines[2] {
		t.Errorf("weeks not chronological: %v", lines[1:])
	}
}

func TestStatsByTagMatchesTagsCommand(t *testing.T) {
	root := newTestVault(t)
	writeNote(t, root, "nn/a.md", "---\ntags: [docker]\n---\n\nbody\n")
	writeNote(t, root, "nn/b.md", "---\ntags: [docker]\n---\n\nbody\n")

	stdout, _, code := runCmd(t, "", "stats", "--by", "tag", "--json")
	if code != 0 {
		t.Fatalf("code = %d", code)
	}
	rows := decodeJSONRows[statsRow](t, stdout)
	if len(rows) != 1 || rows[0].Bucket != "docker" || rows[0].Count != 2 {
		t.Errorf("rows = %+v", rows)
	}
}

func TestStatsEmptyVaultExitsOK(t *testing.T) {
	newTestVault(t)
	stdout, _, code := runCmd(t, "", "stats")
	if code != 0 {
		t.Errorf("code = %d, want 0 even with nothing to show", code)
	}
	if stdout != "" {
		t.Errorf("stdout = %q, want empty", stdout)
	}
}

func TestStatsBarsFitTerminalWidth(t *testing.T) {
	rows := []statsRow{
		{Bucket: "2026-08", Count: 300},
		{Bucket: "2026-09", Count: 41},
		{Bucket: "2026-10", Count: 1},
	}
	var buf bytes.Buffer
	if err := statsSpec().Text(&buf, rows); err != nil {
		t.Fatal(err)
	}

	lines := strings.Split(strings.TrimRight(buf.String(), "\n"), "\n")
	if len(lines) != 3 {
		t.Fatalf("lines = %v", lines)
	}
	for _, line := range lines {
		if len(line) > cli.Width {
			t.Errorf("line %q is %d characters, want at most cli.Width = %d", line, len(line), cli.Width)
		}
	}
	bars := make([]int, len(lines))
	for i, line := range lines {
		bars[i] = strings.Count(line, "#")
	}
	if bars[0] <= bars[1] || bars[1] <= bars[2] {
		t.Errorf("bar lengths = %v, want them to shrink with the counts", bars)
	}
	if bars[2] < 1 {
		t.Errorf("bar lengths = %v, want the smallest non-empty bucket to keep at least one #", bars)
	}
}

func TestStatsBarsAreExactWhenTheyFit(t *testing.T) {
	var buf bytes.Buffer
	if err := statsSpec().Text(&buf, []statsRow{{Bucket: "nn", Count: 3}, {Bucket: "tt", Count: 1}}); err != nil {
		t.Fatal(err)
	}
	if got := buf.String(); !strings.Contains(got, "nn  ###  3\n") || !strings.Contains(got, "tt  #  1\n") {
		t.Errorf("output = %q, want one # per note", got)
	}
}

func TestStatsInvalidByIsUsageError(t *testing.T) {
	newTestVault(t)
	_, _, code := runCmd(t, "", "stats", "--by", "bogus")
	if code != 2 {
		t.Errorf("code = %d, want 2", code)
	}
}
