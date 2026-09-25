package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/lamovs/nn/internal/ocr"
	"github.com/lamovs/nn/internal/output"
	"github.com/lamovs/nn/internal/state"
)

// newTestVault also overrides HOME, so a bad or missing XDG_* value can never fall back to the real HOME.
func newTestVault(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("NN_ROOT", root)
	t.Setenv("NN_CONFIG", filepath.Join(t.TempDir(), "config.toml"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(t.TempDir(), "xdg-config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(t.TempDir(), "xdg-data"))
	return root
}

func writeNote(t *testing.T, root, rel, body string) {
	t.Helper()
	p := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func setModified(t *testing.T, root, rel string, at time.Time) {
	t.Helper()
	p := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.Chtimes(p, at, at); err != nil {
		t.Fatal(err)
	}
}

func writeImage(t *testing.T, root, rel string, data []byte) {
	t.Helper()
	p := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, data, 0o644); err != nil {
		t.Fatal(err)
	}
}

func writeSidecar(t *testing.T, root, imageRel string, lines []ocr.Line) {
	t.Helper()
	if err := ocr.SaveSidecar(root, imageRel, &ocr.Result{Engine: "test", Lines: lines}); err != nil {
		t.Fatal(err)
	}
}

func noteFrecency(t *testing.T, rel string) float64 {
	t.Helper()
	path, err := state.Path()
	if err != nil {
		t.Fatal(err)
	}
	st, err := state.LoadFile(path, 14*24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	return st.Frecency(rel)
}

func runCmd(t *testing.T, stdin string, args ...string) (stdout, stderr string, code int) {
	t.Helper()
	var out, errBuf bytes.Buffer
	code = runText(context.Background(), args, strings.NewReader(stdin), &out, &errBuf)
	return out.String(), errBuf.String(), code
}

func decodeJSONRows[T any](t *testing.T, s string) []T {
	t.Helper()
	var rows []T
	if err := json.Unmarshal([]byte(s), &rows); err != nil {
		t.Fatalf("decode json: %v\noutput: %s", err, s)
	}
	return rows
}

func installFakeEditor(t *testing.T) (recordFile string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("fake editor script needs /bin/sh")
	}
	dir := t.TempDir()
	recordFile = filepath.Join(dir, "record.txt")
	script := "#!/bin/sh\nprintf '%s\\n' \"$*\" > '" + recordFile + "'\n"
	// Named "vi" so editor.LineArgs recognizes it and adds a +N argument.
	scriptPath := filepath.Join(dir, "vi")
	if err := os.WriteFile(scriptPath, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("VISUAL", scriptPath)
	return recordFile
}

func TestSFindsMatchingLine(t *testing.T) {
	root := newTestVault(t)
	writeNote(t, root, "nn/docker.md", "# Docker\n\nRun docker system prune to clean up.\n")

	stdout, _, code := runCmd(t, "", "s", "prune")
	if code != 0 {
		t.Fatalf("code = %d, stderr empty? stdout=%q", code, stdout)
	}
	if !strings.Contains(stdout, "nn/docker.md:3") {
		t.Errorf("stdout = %q, want a hit on line 3", stdout)
	}
}

func TestSNoMatchExitsOne(t *testing.T) {
	newTestVault(t)
	stdout, _, code := runCmd(t, "", "s", "nonexistentword")
	if code != 1 {
		t.Errorf("code = %d, want 1", code)
	}
	if stdout != "" {
		t.Errorf("stdout = %q, want empty", stdout)
	}
}

func TestSQuietPrintsNothing(t *testing.T) {
	root := newTestVault(t)
	writeNote(t, root, "nn/docker.md", "# Docker\n\nprune it\n")
	stdout, stderr, code := runCmd(t, "", "s", "prune", "-q")
	if code != 0 || stdout != "" || stderr != "" {
		t.Errorf("code=%d stdout=%q stderr=%q, want 0/empty/empty", code, stdout, stderr)
	}
	if _, _, code := runCmd(t, "", "s", "nope", "-q"); code != 1 {
		t.Errorf("quiet miss: code = %d, want 1", code)
	}
}

func TestSJSONAndPaths(t *testing.T) {
	root := newTestVault(t)
	writeNote(t, root, "nn/docker.md", "# Docker\n\nprune things twice: prune again\n")

	stdout, _, code := runCmd(t, "", "s", "prune", "--json")
	if code != 0 {
		t.Fatalf("code = %d", code)
	}
	rows := decodeJSONRows[sRow](t, stdout)
	if len(rows) == 0 {
		t.Fatal("no rows decoded")
	}
	for _, r := range rows {
		if r.Path != "nn/docker.md" {
			t.Errorf("row path = %q", r.Path)
		}
	}

	stdout, _, code = runCmd(t, "", "s", "prune", "--paths")
	if code != 0 {
		t.Fatalf("code = %d", code)
	}
	if strings.Count(stdout, "nn/docker.md") != 1 {
		t.Errorf("--paths should list the path once even with two hits: %q", stdout)
	}
}

func TestSSinceFilter(t *testing.T) {
	root := newTestVault(t)
	writeNote(t, root, "nn/old.md", "# Old\n\nprune old stuff\n")
	setModified(t, root, "nn/old.md", time.Now().Add(-30*24*time.Hour))
	writeNote(t, root, "nn/new.md", "# New\n\nprune new stuff\n")

	stdout, _, code := runCmd(t, "", "s", "prune", "--since", "7d", "--paths")
	if code != 0 {
		t.Fatalf("code = %d", code)
	}
	if strings.Contains(stdout, "old.md") {
		t.Errorf("old note should be excluded by --since 7d: %q", stdout)
	}
	if !strings.Contains(stdout, "new.md") {
		t.Errorf("new note should be included: %q", stdout)
	}
}

func TestSTagFilter(t *testing.T) {
	root := newTestVault(t)
	writeNote(t, root, "nn/a.md", "---\ntags: [work]\n---\n\nprune this\n")
	writeNote(t, root, "nn/b.md", "---\ntags: [home]\n---\n\nprune that\n")

	stdout, _, code := runCmd(t, "", "s", "prune", "-t", "work", "--paths")
	if code != 0 {
		t.Fatalf("code = %d", code)
	}
	if strings.TrimSpace(stdout) != "nn/a.md" {
		t.Errorf("stdout = %q, want only nn/a.md", stdout)
	}
}

func TestSImageWithOCRSidecar(t *testing.T) {
	root := newTestVault(t)
	writeImage(t, root, "nn/assets/shortcut.png", []byte("not a real png, just bytes"))
	writeSidecar(t, root, "nn/assets/shortcut.png", []ocr.Line{
		{Text: "Cmd+Shift+4 takes a screenshot"},
	})

	stdout, _, code := runCmd(t, "", "s", "screenshot")
	if code != 0 {
		t.Fatalf("code = %d, stdout=%q", code, stdout)
	}
	if !strings.Contains(stdout, "[img] nn/assets/shortcut.png") {
		t.Errorf("stdout = %q, want an [img] line for the sidecar hit", stdout)
	}
	if !strings.Contains(stdout, `"Cmd+Shift+4 takes a screenshot"`) {
		t.Errorf("stdout = %q, want the OCR line quoted", stdout)
	}
}

func TestSImgPathsGivesEmbeddedImagePath(t *testing.T) {
	root := newTestVault(t)
	writeNote(t, root, "nn/gamma.md", "# Gamma\n\nCluster notes.\n\n![[nn/assets/lonely.png]]\n")
	writeImage(t, root, "nn/assets/lonely.png", []byte("not a real png, just bytes"))
	writeSidecar(t, root, "nn/assets/lonely.png", []ocr.Line{
		{Text: "kubernetes cluster dashboard"},
	})

	stdout, stderr, code := runCmd(t, "", "s", "kubernetes", "--img", "--paths")
	if code != 0 {
		t.Fatalf("code = %d, stderr = %q", code, stderr)
	}
	if got := strings.TrimSpace(stdout); got != "nn/assets/lonely.png" {
		t.Fatalf("--img --paths = %q, want the embedded image's own path", got)
	}

	withFakeOCR(t, &fakeOCREngine{lines: []ocr.Line{{Text: "recognized again"}}})
	ocrOut, ocrErr, ocrCode := runCmd(t, stdout, "ocr", "-", "--json")
	if ocrCode != 0 {
		t.Fatalf("nn ocr -: code = %d, stderr = %q", ocrCode, ocrErr)
	}
	rows := decodeJSONRows[ocrRow](t, ocrOut)
	if len(rows) != 1 {
		t.Fatalf("nn ocr - rows = %+v, want exactly one", rows)
	}
	if rows[0].Path != "nn/assets/lonely.png" {
		t.Errorf("nn ocr - worked on %q, want the image piped from nn s", rows[0].Path)
	}
	if rows[0].Error != "" {
		t.Errorf("nn ocr -: %s", rows[0].Error)
	}
	if rows[0].Result == nil || len(rows[0].Result.Lines) == 0 {
		t.Fatalf("nn ocr - returned no text: %+v", rows[0])
	}
}

func TestSImgPathsWithNulSeparator(t *testing.T) {
	root := newTestVault(t)
	writeNote(t, root, "nn/gamma.md", "# Gamma\n\nCluster notes.\n\n![[nn/assets/lonely.png]]\n")
	writeImage(t, root, "nn/assets/lonely.png", []byte("not a real png, just bytes"))
	writeSidecar(t, root, "nn/assets/lonely.png", []ocr.Line{
		{Text: "kubernetes cluster dashboard"},
	})

	stdout, stderr, code := runCmd(t, "", "s", "kubernetes", "--img", "-0")
	if code != 0 {
		t.Fatalf("code = %d, stderr = %q", code, stderr)
	}
	if stdout != "nn/assets/lonely.png\x00" {
		t.Errorf("-0 output = %q, want the image path with a NUL separator", stdout)
	}
}

func TestSImgPathsDedupesOneImagePerPath(t *testing.T) {
	root := newTestVault(t)
	writeNote(t, root, "nn/gamma.md", "# Gamma\n\nCluster notes.\n\n![[nn/assets/first.png]]\n![[nn/assets/second.png]]\n")
	writeImage(t, root, "nn/assets/first.png", []byte("first bytes"))
	writeImage(t, root, "nn/assets/second.png", []byte("second bytes"))
	writeSidecar(t, root, "nn/assets/first.png", []ocr.Line{
		{Text: "kubernetes control plane"},
		{Text: "kubernetes worker node"},
	})
	writeSidecar(t, root, "nn/assets/second.png", []ocr.Line{
		{Text: "kubernetes ingress"},
	})

	stdout, stderr, code := runCmd(t, "", "s", "kubernetes", "--img", "--paths")
	if code != 0 {
		t.Fatalf("code = %d, stderr = %q", code, stderr)
	}
	got := strings.Split(strings.TrimRight(stdout, "\n"), "\n")
	if len(got) != 2 {
		t.Fatalf("--paths = %q, want one line per image, got %d", stdout, len(got))
	}
	want := map[string]bool{"nn/assets/first.png": true, "nn/assets/second.png": true}
	for _, p := range got {
		if !want[p] {
			t.Errorf("--paths printed %q, want only the two image paths: %q", p, stdout)
		}
		delete(want, p)
	}
	if strings.Contains(stdout, "gamma.md") {
		t.Errorf("--paths = %q, want no .md path among image hits", stdout)
	}
}

func TestSPathsWithoutImgHitsStayNotePaths(t *testing.T) {
	root := newTestVault(t)
	writeNote(t, root, "nn/gamma.md", "# Gamma\n\nprune the containers\n\n![[nn/assets/lonely.png]]\n")
	writeImage(t, root, "nn/assets/lonely.png", []byte("not a real png, just bytes"))
	writeSidecar(t, root, "nn/assets/lonely.png", []ocr.Line{
		{Text: "kubernetes cluster dashboard"},
	})

	stdout, stderr, code := runCmd(t, "", "s", "prune", "--paths")
	if code != 0 {
		t.Fatalf("code = %d, stderr = %q", code, stderr)
	}
	if got := strings.TrimSpace(stdout); got != "nn/gamma.md" {
		t.Errorf("--paths = %q, want the note's own path for a body hit", got)
	}
}

func TestSMetadataHitsCollapseToOneLine(t *testing.T) {
	root := newTestVault(t)
	writeNote(t, root, "nn/docker-cleanup.md", "---\ntags: [docker]\naliases: [Docker cleanup]\n---\n\nNothing else relevant.\n")

	stdout, _, code := runCmd(t, "", "s", "docker")
	if code != 0 {
		t.Fatalf("code = %d", code)
	}
	if got := strings.Count(stdout, "nn/docker-cleanup.md"); got != 1 {
		t.Errorf("stdout = %q, want the doc listed once, got %d times", stdout, got)
	}
}

// cyrillicDocker is "docker" typed with a Cyrillic keyboard layout active, key for key.
const cyrillicDocker = "\U00000432\U00000449\U00000441\U0000043b\U00000443\U0000043a"

func TestSLayoutSwapReportsOnStderr(t *testing.T) {
	root := newTestVault(t)
	writeNote(t, root, "nn/docker.md", "# Docker\n\nRun docker system prune.\n")

	stdout, stderr, code := runCmd(t, "", "s", cyrillicDocker)
	if code != 0 {
		t.Fatalf("code = %d, stderr = %q", code, stderr)
	}
	if !strings.Contains(stdout, "nn/docker.md") {
		t.Errorf("stdout = %q, want the note found via the swapped layout", stdout)
	}
	if !strings.Contains(stderr, fmt.Sprintf("no results for %q, showing \"docker\"", cyrillicDocker)) {
		t.Errorf("stderr = %q, want the layout-swap note", stderr)
	}
	_ = root
}

func TestSUnknownOptionIsUsageError(t *testing.T) {
	newTestVault(t)
	_, stderr, code := runCmd(t, "", "s", "--bogus")
	if code != 2 {
		t.Errorf("code = %d, want 2", code)
	}
	if !strings.Contains(stderr, "--bogus") {
		t.Errorf("stderr = %q, want it to name the bad flag", stderr)
	}
}

func TestSOpenTitleOnlyHit(t *testing.T) {
	root := newTestVault(t)
	// Matches only via the alias-derived title, not a body heading, so the only Hit is Line == 0.
	writeNote(t, root, "nn/gizmo-widget.md", "---\naliases: [gizmo]\n---\n\n# Something else\n\nNothing else relevant here.\n")
	record := installFakeEditor(t)

	_, stderr, code := runCmd(t, "", "s", "gizmo", "-o")
	if code != 0 {
		t.Fatalf("code = %d, stderr = %q", code, stderr)
	}
	data, err := os.ReadFile(record)
	if err != nil {
		t.Fatalf("editor was not invoked: %v", err)
	}
	got := string(data)
	if !strings.Contains(got, filepath.Join(root, "nn", "gizmo-widget.md")) {
		t.Errorf("recorded editor args = %q, want the note's absolute path", got)
	}
	if strings.Contains(got, "+") {
		t.Errorf("recorded editor args = %q, want no +N line argument for a Line==0 hit", got)
	}
}

func TestSOpenBodyHitUsesLine(t *testing.T) {
	root := newTestVault(t)
	writeNote(t, root, "nn/docker.md", "# Docker\n\nsomething\nprune the containers\n")
	record := installFakeEditor(t)

	_, _, code := runCmd(t, "", "s", "prune", "-o")
	if code != 0 {
		t.Fatalf("code = %d", code)
	}
	data, err := os.ReadFile(record)
	if err != nil {
		t.Fatalf("editor was not invoked: %v", err)
	}
	if !strings.Contains(string(data), "+4") {
		t.Errorf("recorded editor args = %q, want a +4 line argument", string(data))
	}
}

func TestSRecordsOpenOnlyWithOpenFlag(t *testing.T) {
	root := newTestVault(t)
	writeNote(t, root, "nn/docker.md", "# Docker\n\nprune the containers\n")
	installFakeEditor(t)

	if _, stderr, code := runCmd(t, "", "s", "prune"); code != 0 {
		t.Fatalf("code = %d, stderr = %q", code, stderr)
	}
	if f := noteFrecency(t, "nn/docker.md"); f != 0 {
		t.Errorf("Frecency = %v after a plain search, want 0", f)
	}

	if _, stderr, code := runCmd(t, "", "s", "prune", "-o"); code != 0 {
		t.Fatalf("code = %d, stderr = %q", code, stderr)
	}
	if f := noteFrecency(t, "nn/docker.md"); f <= 0 {
		t.Errorf("Frecency = %v after -o, want the open to have been recorded", f)
	}
}

func TestSEmptyJSONIsAnArray(t *testing.T) {
	newTestVault(t)
	stdout, _, code := runCmd(t, "", "s", "nonexistentword", "--json")
	if code != 1 {
		t.Errorf("code = %d, want 1", code)
	}
	if got := strings.TrimSpace(stdout); got != "[]" {
		t.Errorf("stdout = %q, want []", got)
	}
}

func TestSBareIsUsageError(t *testing.T) {
	root := newTestVault(t)
	writeNote(t, root, "nn/a.md", "# A\n\nbody\n")

	stdout, stderr, code := runCmd(t, "", "s")
	if code != 2 {
		t.Errorf("code = %d, want 2", code)
	}
	if stdout != "" {
		t.Errorf("stdout = %q, want nothing", stdout)
	}
	if !strings.Contains(stderr, "QUERY") {
		t.Errorf("stderr = %q, want it to say a QUERY is needed", stderr)
	}
}

func TestSFilterOnlyListsNotes(t *testing.T) {
	root := newTestVault(t)
	writeNote(t, root, "nn/a.md", "---\ntags: [work]\n---\n\n# Work note\n\nbody\n")
	writeNote(t, root, "nn/b.md", "---\ntags: [home]\n---\n\n# Home note\n\nbody\n")

	stdout, _, code := runCmd(t, "", "s", "-t", "work")
	if code != 0 {
		t.Fatalf("code = %d, stdout = %q", code, stdout)
	}
	if strings.TrimRight(stdout, "\n") != "nn/a.md  (Work note)" {
		t.Errorf("stdout = %q, want the title-match form for the one work note", stdout)
	}

	stdout, _, code = runCmd(t, "", "s", "-t", "work", "--json")
	if code != 0 {
		t.Fatalf("--json: code = %d", code)
	}
	rows := decodeJSONRows[sRow](t, stdout)
	if len(rows) != 1 || rows[0].Path != "nn/a.md" || rows[0].Kind != kindNote {
		t.Errorf("rows = %+v, want one %q row for nn/a.md", rows, kindNote)
	}
}

func TestSFilterOnlyNoMatchExitsOne(t *testing.T) {
	root := newTestVault(t)
	writeNote(t, root, "nn/a.md", "---\ntags: [work]\n---\n\n# Work note\n\nbody\n")

	stdout, _, code := runCmd(t, "", "s", "-t", "nosuchtag")
	if code != 1 {
		t.Errorf("code = %d, want 1", code)
	}
	if stdout != "" {
		t.Errorf("stdout = %q, want nothing", stdout)
	}
}

func TestSLimitDefaultFromConfig(t *testing.T) {
	root := newTestVault(t)
	writeConfig(t, "[search]\nlimit = 1\n")
	writeNote(t, root, "nn/a.md", "# A\n\nprune\n")
	writeNote(t, root, "nn/b.md", "# B\n\nprune\n")

	stdout, stderr, code := runCmd(t, "", "s", "prune", "--paths")
	if code != 0 {
		t.Fatalf("code = %d, stderr = %q", code, stderr)
	}
	if got := strings.Fields(stdout); len(got) != 1 {
		t.Errorf("stdout = %q, want exactly 1 path (search.limit = 1)", stdout)
	}
}

func TestSLimitFlagBeatsConfig(t *testing.T) {
	root := newTestVault(t)
	writeConfig(t, "[search]\nlimit = 1\n")
	writeNote(t, root, "nn/a.md", "# A\n\nprune\n")
	writeNote(t, root, "nn/b.md", "# B\n\nprune\n")

	stdout, stderr, code := runCmd(t, "", "s", "prune", "-n", "2", "--paths")
	if code != 0 {
		t.Fatalf("code = %d, stderr = %q", code, stderr)
	}
	if got := strings.Fields(stdout); len(got) != 2 {
		t.Errorf("stdout = %q, want 2 paths (-n 2 beats search.limit)", stdout)
	}
}

func TestSLayoutFallbackDisabledByConfig(t *testing.T) {
	root := newTestVault(t)
	writeConfig(t, "[search]\nlayout_fallback = false\n")
	writeNote(t, root, "nn/docker.md", "# Docker\n\nRun docker system prune.\n")

	stdout, stderr, code := runCmd(t, "", "s", cyrillicDocker)
	if code != output.ExitNotFound {
		t.Errorf("code = %d, want %d (no fallback, nothing found)", code, output.ExitNotFound)
	}
	if strings.Contains(stderr, "no results for") {
		t.Errorf("stderr = %q, want no layout-swap note", stderr)
	}
	if stdout != "" {
		t.Errorf("stdout = %q, want nothing", stdout)
	}
}

func TestSRejectsZeroLimit(t *testing.T) {
	root := newTestVault(t)
	writeNote(t, root, "nn/docker.md", "# Docker\n\nprune it\n")

	stdout, stderr, code := runCmd(t, "", "s", "prune", "-n", "0")
	if code != 2 {
		t.Errorf("code = %d, want 2", code)
	}
	if stdout != "" {
		t.Errorf("stdout = %q, want nothing printed", stdout)
	}
	if !strings.Contains(stderr, "-n") {
		t.Errorf("stderr = %q, want it to name -n", stderr)
	}
}

func TestSTSVEscapesTabsAndNewlines(t *testing.T) {
	root := newTestVault(t)
	writeNote(t, root, "nn/tabbed.md", "# Tabbed\n\nprune\tthe\tcontainers\n")

	stdout, _, code := runCmd(t, "", "s", "prune", "--tsv")
	if code != 0 {
		t.Fatalf("code = %d", code)
	}
	lines := strings.Split(strings.TrimRight(stdout, "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("stdout = %q, want a header and exactly one record", stdout)
	}
	fields := strings.Split(lines[1], "\t")
	if len(fields) != 4 {
		t.Fatalf("record = %q has %d columns, want 4", lines[1], len(fields))
	}
	if fields[3] != `prune\tthe\tcontainers` {
		t.Errorf("text column = %q, want the tabs escaped", fields[3])
	}
}

func TestParseSince(t *testing.T) {
	now := time.Date(2026, 9, 16, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		in   string
		want time.Time
	}{
		{"today", time.Date(2026, 9, 16, 0, 0, 0, 0, time.UTC)},
		{"7d", now.AddDate(0, 0, -7)},
		{"2w", now.AddDate(0, 0, -14)},
		{"2026-09-01", time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)},
	}
	for _, c := range cases {
		got, err := parseSince(c.in, now)
		if err != nil {
			t.Errorf("parseSince(%q): %v", c.in, err)
			continue
		}
		if !got.Equal(c.want) {
			t.Errorf("parseSince(%q) = %v, want %v", c.in, got, c.want)
		}
	}
	if _, err := parseSince("nonsense", now); err == nil {
		t.Error("parseSince(\"nonsense\") should fail")
	}
}
