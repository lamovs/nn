package main

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/lamovs/nn/internal/state"
)

func TestLsListsNotesNewestFirst(t *testing.T) {
	root := newTestVault(t)
	writeNote(t, root, "nn/a.md", "# A\n\nbody\n")
	writeNote(t, root, "nn/b.md", "# B\n\nbody\n")
	setModified(t, root, "nn/a.md", time.Now().Add(-2*time.Hour))
	setModified(t, root, "nn/b.md", time.Now())

	stdout, _, code := runCmd(t, "", "ls")
	if code != 0 {
		t.Fatalf("code = %d", code)
	}
	lines := strings.Split(strings.TrimRight(stdout, "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("lines = %v", lines)
	}
	if !strings.Contains(lines[0], "nn/b.md") || !strings.Contains(lines[1], "nn/a.md") {
		t.Errorf("stdout = %q, want b.md before a.md (newest modified first)", stdout)
	}
}

func TestLsEmptyVaultExitsOne(t *testing.T) {
	newTestVault(t)
	stdout, _, code := runCmd(t, "", "ls")
	if code != 1 || stdout != "" {
		t.Errorf("code=%d stdout=%q, want 1/empty", code, stdout)
	}
}

func TestLsEmptyJSONIsAnArray(t *testing.T) {
	newTestVault(t)
	stdout, _, code := runCmd(t, "", "ls", "--json")
	if code != 1 {
		t.Errorf("code = %d, want 1", code)
	}
	if got := strings.TrimSpace(stdout); got != "[]" {
		t.Errorf("stdout = %q, want []", got)
	}
	if rows := decodeJSONRows[lsRow](t, stdout); len(rows) != 0 {
		t.Errorf("rows = %+v, want none", rows)
	}
}

func TestLsImagesOnlyWithImg(t *testing.T) {
	root := newTestVault(t)
	writeNote(t, root, "nn/plain.md", "# Plain\n\nno image here\n")
	writeImage(t, root, "nn/assets/pic.png", []byte("fake"))
	writeNote(t, root, "nn/with-image.md", "# With image\n\n![[nn/assets/pic.png]]\n")
	writeImage(t, root, "nn/assets/loose.png", []byte("fake too"))

	stdout, _, code := runCmd(t, "", "ls", "--paths")
	if code != 0 {
		t.Fatalf("code = %d", code)
	}
	plain := strings.Fields(stdout)
	if len(plain) != 2 || !slices.Contains(plain, "nn/plain.md") || !slices.Contains(plain, "nn/with-image.md") {
		t.Errorf("ls --paths = %v, want only the two notes (no loose image)", plain)
	}

	stdout, _, code = runCmd(t, "", "ls", "--img", "--paths")
	if code != 0 {
		t.Fatalf("--img: code = %d", code)
	}
	img := strings.Fields(stdout)
	if len(img) != 2 || !slices.Contains(img, "nn/with-image.md") || !slices.Contains(img, "nn/assets/loose.png") {
		t.Errorf("ls --img --paths = %v, want the note that embeds an image plus the loose image", img)
	}
}

func TestLsRejectsZeroLimit(t *testing.T) {
	root := newTestVault(t)
	writeNote(t, root, "nn/a.md", "# A\n\nbody\n")

	stdout, stderr, code := runCmd(t, "", "ls", "-n", "0")
	if code != 2 {
		t.Errorf("code = %d, want 2", code)
	}
	if stdout != "" {
		t.Errorf("stdout = %q, want nothing listed", stdout)
	}
	if !strings.Contains(stderr, "-n") {
		t.Errorf("stderr = %q, want it to name -n", stderr)
	}
}

func TestLsJSONAndTags(t *testing.T) {
	root := newTestVault(t)
	writeNote(t, root, "nn/a.md", "---\ntags: [docker, ops]\n---\n\n# A\n\nbody\n")

	stdout, _, code := runCmd(t, "", "ls", "--json")
	if code != 0 {
		t.Fatalf("code = %d", code)
	}
	rows := decodeJSONRows[lsRow](t, stdout)
	if len(rows) != 1 || rows[0].Path != "nn/a.md" {
		t.Fatalf("rows = %+v", rows)
	}
	if len(rows[0].Tags) != 2 {
		t.Errorf("tags = %v", rows[0].Tags)
	}

	stdout, _, code = runCmd(t, "", "ls")
	if code != 0 {
		t.Fatalf("code = %d", code)
	}
	if !strings.Contains(stdout, "#docker") || !strings.Contains(stdout, "#ops") {
		t.Errorf("stdout = %q, want both tags rendered with #", stdout)
	}
}

func TestLsSinceAndInboxFilters(t *testing.T) {
	root := newTestVault(t)
	writeNote(t, root, "nn/recent.md", "# Recent\n\nbody\n")
	writeNote(t, root, "old.md", "# Old\n\nbody\n") // outside the inbox
	setModified(t, root, "old.md", time.Now().Add(-60*24*time.Hour))

	stdout, _, code := runCmd(t, "", "ls", "--inbox", "--paths")
	if code != 0 {
		t.Fatalf("code = %d", code)
	}
	if strings.TrimSpace(stdout) != "nn/recent.md" {
		t.Errorf("--inbox: stdout = %q, want only nn/recent.md", stdout)
	}

	stdout, _, code = runCmd(t, "", "ls", "--since", "7d", "--paths")
	if code != 0 {
		t.Fatalf("code = %d", code)
	}
	if strings.Contains(stdout, "old.md") {
		t.Errorf("--since 7d: stdout = %q, should exclude old.md", stdout)
	}
}

func TestLsSortTitle(t *testing.T) {
	root := newTestVault(t)
	writeNote(t, root, "nn/z.md", "# Zebra\n\nbody\n")
	writeNote(t, root, "nn/a.md", "# Apple\n\nbody\n")

	stdout, _, code := runCmd(t, "", "ls", "--sort", "title", "--paths")
	if code != 0 {
		t.Fatalf("code = %d", code)
	}
	lines := strings.Split(strings.TrimRight(stdout, "\n"), "\n")
	if len(lines) != 2 || lines[0] != "nn/a.md" || lines[1] != "nn/z.md" {
		t.Errorf("lines = %v, want [nn/a.md nn/z.md]", lines)
	}
}

func TestLsSortOpened(t *testing.T) {
	root := newTestVault(t)
	writeNote(t, root, "nn/a.md", "# A\n\nbody\n")
	writeNote(t, root, "nn/b.md", "# B\n\nbody\n")

	statePath, err := state.Path()
	if err != nil {
		t.Fatal(err)
	}
	st, err := state.LoadFile(statePath, 14*24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	st.RecordOpen("nn/b.md", time.Now())
	if err := st.Save(); err != nil {
		t.Fatal(err)
	}

	stdout, _, code := runCmd(t, "", "ls", "--sort", "opened", "--paths")
	if code != 0 {
		t.Fatalf("code = %d", code)
	}
	lines := strings.Split(strings.TrimRight(stdout, "\n"), "\n")
	if len(lines) != 2 || lines[0] != "nn/b.md" {
		t.Errorf("lines = %v, want nn/b.md (opened) first", lines)
	}
}

func TestLsSortOpenedHalfLifeFromConfig(t *testing.T) {
	root := newTestVault(t)
	writeNote(t, root, "nn/a.md", "# A\n\nbody\n")
	writeNote(t, root, "nn/b.md", "# B\n\nbody\n")

	statePath, err := state.Path()
	if err != nil {
		t.Fatal(err)
	}
	st, err := state.LoadFile(statePath, 14*24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-48 * time.Hour)
	for i := range 3 {
		st.RecordOpen("nn/a.md", old.Add(time.Duration(i)*time.Second))
	}
	st.RecordOpen("nn/b.md", time.Now())
	if err := st.Save(); err != nil {
		t.Fatal(err)
	}

	// At the default half-life, a's three 2-day-old opens outweigh b's one fresh open.
	stdout, stderr, code := runCmd(t, "", "ls", "--sort", "opened", "--paths")
	if code != 0 {
		t.Fatalf("code = %d, stderr = %q", code, stderr)
	}
	lines := strings.Split(strings.TrimRight(stdout, "\n"), "\n")
	if len(lines) != 2 || lines[0] != "nn/a.md" {
		t.Fatalf("lines = %v, want nn/a.md first at the default half-life", lines)
	}

	// A much shorter half-life decays a's opens away entirely, so b now ranks first.
	writeConfig(t, "[state]\nhalf_life = \"1m\"\n")
	stdout, stderr, code = runCmd(t, "", "ls", "--sort", "opened", "--paths")
	if code != 0 {
		t.Fatalf("code = %d, stderr = %q", code, stderr)
	}
	lines = strings.Split(strings.TrimRight(stdout, "\n"), "\n")
	if len(lines) != 2 || lines[0] != "nn/b.md" {
		t.Errorf("lines = %v, want nn/b.md first (state.half_life = 1m decays a's old opens)", lines)
	}
}

func TestLsRejectsPositionalArgs(t *testing.T) {
	newTestVault(t)
	_, stderr, code := runCmd(t, "", "ls", "docker")
	if code != 2 {
		t.Errorf("code = %d, want 2", code)
	}
	if !strings.Contains(stderr, "nn s docker") {
		t.Errorf("stderr = %q, want a hint pointing at nn s", stderr)
	}
}

func TestLsLimit(t *testing.T) {
	root := newTestVault(t)
	writeNote(t, root, "nn/a.md", "# A\n\nbody\n")
	writeNote(t, root, "nn/b.md", "# B\n\nbody\n")
	writeNote(t, root, "nn/c.md", "# C\n\nbody\n")

	stdout, _, code := runCmd(t, "", "ls", "-n", "2", "--paths")
	if code != 0 {
		t.Fatalf("code = %d", code)
	}
	if got := strings.Count(strings.TrimSpace(stdout), "\n") + 1; got != 2 {
		t.Errorf("got %d paths, want 2: %q", got, stdout)
	}
}

func TestLsSortDefaultFromConfig(t *testing.T) {
	root := newTestVault(t)
	writeConfig(t, "[ls]\nsort = \"title\"\n")
	writeNote(t, root, "nn/z.md", "# Zebra\n\nbody\n")
	writeNote(t, root, "nn/a.md", "# Apple\n\nbody\n")

	stdout, stderr, code := runCmd(t, "", "ls", "--paths")
	if code != 0 {
		t.Fatalf("code = %d, stderr = %q", code, stderr)
	}
	lines := strings.Split(strings.TrimRight(stdout, "\n"), "\n")
	if len(lines) != 2 || lines[0] != "nn/a.md" || lines[1] != "nn/z.md" {
		t.Errorf("lines = %v, want [nn/a.md nn/z.md] (ls.sort = title)", lines)
	}
}

func TestLsSortFlagBeatsConfig(t *testing.T) {
	root := newTestVault(t)
	writeConfig(t, "[ls]\nsort = \"title\"\n")
	writeNote(t, root, "nn/a.md", "# Apple\n\nbody\n")
	writeNote(t, root, "nn/z.md", "# Zebra\n\nbody\n")
	setModified(t, root, "nn/a.md", time.Now().Add(-2*time.Hour))
	setModified(t, root, "nn/z.md", time.Now())

	stdout, stderr, code := runCmd(t, "", "ls", "--sort", "modified", "--paths")
	if code != 0 {
		t.Fatalf("code = %d, stderr = %q", code, stderr)
	}
	lines := strings.Split(strings.TrimRight(stdout, "\n"), "\n")
	if len(lines) != 2 || lines[0] != "nn/z.md" || lines[1] != "nn/a.md" {
		t.Errorf("lines = %v, want [nn/z.md nn/a.md] (--sort modified beats ls.sort = title)", lines)
	}
}

func TestLsLimitDefaultFromConfig(t *testing.T) {
	root := newTestVault(t)
	writeConfig(t, "[ls]\nlimit = 1\n")
	writeNote(t, root, "nn/a.md", "# A\n\nbody\n")
	writeNote(t, root, "nn/b.md", "# B\n\nbody\n")

	stdout, stderr, code := runCmd(t, "", "ls", "--paths")
	if code != 0 {
		t.Fatalf("code = %d, stderr = %q", code, stderr)
	}
	if got := strings.Fields(stdout); len(got) != 1 {
		t.Errorf("stdout = %q, want exactly 1 path (ls.limit = 1)", stdout)
	}
}

func TestLsLimitAllCancelsConfig(t *testing.T) {
	root := newTestVault(t)
	writeConfig(t, "[ls]\nlimit = 1\n")
	writeNote(t, root, "nn/a.md", "# A\n\nbody\n")
	writeNote(t, root, "nn/b.md", "# B\n\nbody\n")

	stdout, stderr, code := runCmd(t, "", "ls", "--paths", "-n", "all")
	if code != 0 {
		t.Fatalf("code = %d, stderr = %q", code, stderr)
	}
	if got := strings.Fields(stdout); len(got) != 2 {
		t.Errorf("stdout = %q, want 2 paths (-n all cancels ls.limit = 1)", stdout)
	}
}

func TestLsLimitFlagBeatsConfig(t *testing.T) {
	root := newTestVault(t)
	writeConfig(t, "[ls]\nlimit = 1\n")
	writeNote(t, root, "nn/a.md", "# A\n\nbody\n")
	writeNote(t, root, "nn/b.md", "# B\n\nbody\n")

	stdout, stderr, code := runCmd(t, "", "ls", "-n", "2", "--paths")
	if code != 0 {
		t.Fatalf("code = %d, stderr = %q", code, stderr)
	}
	if got := strings.Fields(stdout); len(got) != 2 {
		t.Errorf("stdout = %q, want 2 paths (-n 2 beats ls.limit)", stdout)
	}
}
