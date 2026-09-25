package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lamovs/nn/internal/capture"
	"github.com/lamovs/nn/internal/config"
	"github.com/lamovs/nn/internal/ocr"
	"github.com/lamovs/nn/internal/vault"
)

func TestAddWordsBecomeTitle(t *testing.T) {
	root := newTestVault(t)
	stdout, stderr, code := runCmd(t, "", "add", "Docker", "cleanup", "notes")
	if code != 0 {
		t.Fatalf("code = %d, stderr = %q", code, stderr)
	}
	if strings.TrimSpace(stdout) != "+ nn/docker-cleanup-notes.md" {
		t.Errorf("stdout = %q, want the created path with a + prefix", stdout)
	}
	data, err := os.ReadFile(filepath.Join(root, "nn", "docker-cleanup-notes.md"))
	if err != nil {
		t.Fatalf("note was not written: %v", err)
	}
	if !strings.Contains(string(data), "aliases: [Docker cleanup notes]") {
		t.Errorf("note = %q, want an alias with the title", string(data))
	}
}

func TestAddWordsPlusStdinCombine(t *testing.T) {
	root := newTestVault(t)
	stdout, _, code := runCmd(t, "docker system prune -af\n", "add", "Docker", "cleanup")
	if code != 0 {
		t.Fatalf("code = %d", code)
	}
	path := filepath.Join(root, "nn", "docker-cleanup.md")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("note was not written: %v, stdout=%q", err, stdout)
	}
	if !strings.Contains(string(data), "docker system prune -af") {
		t.Errorf("note = %q, want the piped body", string(data))
	}
	if !strings.Contains(string(data), "aliases: [Docker cleanup]") {
		t.Errorf("note = %q, want the words as the title", string(data))
	}
}

func TestAddCodeFlagFencesBody(t *testing.T) {
	root := newTestVault(t)
	// A single shell argument, as "nn-last" passes it: words before the flag.
	_, stderr, code := runCmd(t, "", "add", "docker system prune -af", "--code=sh")
	if code != 0 {
		t.Fatalf("code = %d, stderr = %q", code, stderr)
	}
	entries, err := os.ReadDir(filepath.Join(root, "nn"))
	if err != nil || len(entries) != 1 {
		t.Fatalf("ReadDir: %v, entries=%v", err, entries)
	}
	data, err := os.ReadFile(filepath.Join(root, "nn", entries[0].Name()))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "```sh\ndocker system prune -af\n```") {
		t.Errorf("note = %q, want a fenced sh block", string(data))
	}
}

func TestAddDashLedTitleProtectedByEndOfOptions(t *testing.T) {
	root := newTestVault(t)
	_, stderr, code := runCmd(t, "", "add", "--", "-5 min plank")
	if code != 0 {
		t.Fatalf("code = %d, stderr = %q", code, stderr)
	}
	if _, err := os.ReadFile(filepath.Join(root, "nn", "5-min-plank.md")); err != nil {
		t.Errorf("note was not written as expected: %v", err)
	}
}

func TestAddPreviewDoesNotWrite(t *testing.T) {
	root := newTestVault(t)
	stdout, _, code := runCmd(t, "", "add", "Meeting", "notes", "--preview")
	if code != 0 {
		t.Fatalf("code = %d", code)
	}
	if !strings.Contains(stdout, "title: Meeting notes") {
		t.Errorf("stdout = %q, want a preview of the title", stdout)
	}
	entries, _ := os.ReadDir(filepath.Join(root, "nn"))
	if len(entries) != 0 {
		t.Errorf("--preview wrote %d file(s), want none", len(entries))
	}
}

func TestAddToAppendsExistingNote(t *testing.T) {
	root := newTestVault(t)
	writeNote(t, root, "nn/docker-notes.md", "---\ntags: [docker]\ndate: 2026-01-01\n---\n\nfirst line\n")

	stdout, stderr, code := runCmd(t, "", "add", "one", "more", "thing", "--to", "docker-notes")
	if code != 0 {
		t.Fatalf("code = %d, stderr = %q", code, stderr)
	}
	if !strings.HasPrefix(strings.TrimSpace(stdout), "~ ") {
		t.Errorf("stdout = %q, want a ~ prefix for an append", stdout)
	}
	data, err := os.ReadFile(filepath.Join(root, "nn", "docker-notes.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "first line\n\none more thing") {
		t.Errorf("note = %q, want the new line appended after a blank line", string(data))
	}
}

func TestAddSecretRefusedWithoutTTY(t *testing.T) {
	root := newTestVault(t)
	// Fixture is split so secret scanners do not flag it.
	_, stderr, code := runCmd(t, "github token gh"+"p_a1b2c3d4e5f6g7h8i9j0k1l2m3n4o5p6q7r8s9t0\n", "add", "leaked")
	if code != 2 {
		t.Fatalf("code = %d, want 2, stderr=%q", code, stderr)
	}
	if !strings.Contains(stderr, "--allow-secret") {
		t.Errorf("stderr = %q, want a mention of --allow-secret", stderr)
	}
	entries, _ := os.ReadDir(filepath.Join(root, "nn"))
	if len(entries) != 0 {
		t.Errorf("a secret was written despite the refusal: %v", entries)
	}
}

func TestAddAllowSecretSaves(t *testing.T) {
	newTestVault(t)
	_, _, code := runCmd(t, "github token gh"+"p_a1b2c3d4e5f6g7h8i9j0k1l2m3n4o5p6q7r8s9t0\n", "add", "leaked", "--allow-secret")
	if code != 0 {
		t.Fatalf("code = %d", code)
	}
}

func TestAddSimilarNoteWarnsWithoutTTYButStillCreatesNew(t *testing.T) {
	root := newTestVault(t)
	writeNote(t, root, "nn/docker-cleanup.md", "---\ntags: [docker]\ndate: 2026-01-01\naliases: [Docker cleanup]\n---\n\nold body about cleaning docker images and containers thoroughly\n")

	_, stderr, code := runCmd(t, "", "add", "Docker", "cleanup", "--new")
	if code != 0 {
		t.Fatalf("code = %d", code)
	}
	if strings.Contains(stderr, "similar note exists") {
		t.Errorf("--new should skip the similar-note warning: stderr = %q", stderr)
	}
	entries, err := os.ReadDir(filepath.Join(root, "nn"))
	if err != nil || len(entries) != 2 {
		t.Fatalf("ReadDir: %v, entries=%v, want the original plus a new one", err, entries)
	}
}

func TestAddSimilarNoteWarnsByDefaultWithoutTTY(t *testing.T) {
	root := newTestVault(t)
	writeNote(t, root, "nn/docker-cleanup.md", "---\ntags: [docker]\ndate: 2026-01-01\naliases: [Docker cleanup]\n---\n\nold body about cleaning docker images and containers thoroughly\n")

	_, stderr, code := runCmd(t, "", "add", "Docker", "cleanup")
	if code != 0 {
		t.Fatalf("code = %d, stderr = %q", code, stderr)
	}
	if !strings.Contains(stderr, "similar note exists") {
		t.Errorf("stderr = %q, want a warning about the similar note", stderr)
	}
	entries, err := os.ReadDir(filepath.Join(root, "nn"))
	if err != nil || len(entries) != 2 {
		t.Fatalf("ReadDir: %v, entries=%v, want the original plus a new one (no TTY means no auto-append)", err, entries)
	}
}

// "Alpha Beta" vs "Alpha Gamma" score 0.5, between the default threshold (0.6) and a lowered one.

func TestAddSimilarThresholdDefaultDoesNotWarn(t *testing.T) {
	root := newTestVault(t)
	writeNote(t, root, "nn/alpha-beta.md", "# Alpha Beta\n\nbody\n")

	_, stderr, code := runCmd(t, "", "add", "Alpha", "Gamma")
	if code != 0 {
		t.Fatalf("code = %d, stderr = %q", code, stderr)
	}
	if strings.Contains(stderr, "similar note exists") {
		t.Errorf("stderr = %q, want no warning at the default threshold (score 0.5 < 0.6)", stderr)
	}
}

func TestAddSimilarThresholdFromConfigLowersTheBar(t *testing.T) {
	root := newTestVault(t)
	writeConfig(t, "[capture]\nsimilar_threshold = 0.4\n")
	writeNote(t, root, "nn/alpha-beta.md", "# Alpha Beta\n\nbody\n")

	_, stderr, code := runCmd(t, "", "add", "Alpha", "Gamma")
	if code != 0 {
		t.Fatalf("code = %d, stderr = %q", code, stderr)
	}
	if !strings.Contains(stderr, "similar note exists") {
		t.Errorf("stderr = %q, want the warning once capture.similar_threshold = 0.4 (score 0.5 >= 0.4)", stderr)
	}
}

func TestAddSimilarLimitZeroFromConfigSuppressesTheWarning(t *testing.T) {
	root := newTestVault(t)
	writeConfig(t, "[capture]\nsimilar_limit = 0\n")
	writeNote(t, root, "nn/docker-cleanup.md", "---\naliases: [Docker cleanup]\n---\n\nbody\n")

	_, stderr, code := runCmd(t, "", "add", "Docker", "cleanup")
	if code != 0 {
		t.Fatalf("code = %d, stderr = %q", code, stderr)
	}
	if strings.Contains(stderr, "similar note exists") {
		t.Errorf("stderr = %q, want no warning (capture.similar_limit = 0)", stderr)
	}
}

// blockSimilarPrompt swaps the question for a stand-in recording whether it was reached.
func blockSimilarPrompt(t *testing.T) func() bool {
	t.Helper()
	asked := false
	saved := askSimilarNote
	askSimilarNote = func([]capture.Similar) string {
		asked = true
		return "new"
	}
	t.Cleanup(func() { askSimilarNote = saved })
	return func() bool { return asked }
}

func TestAddSimilarNoteNeverAsksWithoutTTY(t *testing.T) {
	root := newTestVault(t)
	asked := blockSimilarPrompt(t)
	writeNote(t, root, "nn/docker-cleanup.md", "---\ntags: [docker]\ndate: 2026-01-01\naliases: [Docker cleanup]\n---\n\nold body about cleaning docker images and containers thoroughly\n")

	_, stderr, code := runCmd(t, "docker system prune -af\n", "add", "Docker", "cleanup")
	if code != 0 {
		t.Fatalf("code = %d, stderr = %q", code, stderr)
	}
	if asked() {
		t.Error("add reached the [a]ppend/[n]ew/[c]ancel question with stdin on a pipe; SPEC 5: without a TTY nn asks nothing")
	}
	if !strings.Contains(stderr, "similar note exists") {
		t.Errorf("stderr = %q, want the similar-note warning instead of a question", stderr)
	}
	entries, err := os.ReadDir(filepath.Join(root, "nn"))
	if err != nil || len(entries) != 2 {
		t.Fatalf("ReadDir: %v, entries = %v, want the original plus a new note", err, entries)
	}
}

func TestAddToRefusesASearchOnlyMatch(t *testing.T) {
	root := newTestVault(t)
	const report = "---\ntags: [work]\ndate: 2026-01-01\n---\n\nthe salary review is due next quarter\n"
	writeNote(t, root, "nn/quarterly-report.md", report)

	_, stderr, code := runCmd(t, "", "add", "note for nothing", "--to", "salary")
	if code != 2 {
		t.Fatalf("code = %d, want 2: --to takes a note, not a query; stderr = %q", code, stderr)
	}
	if !strings.Contains(stderr, "no note matches") {
		t.Errorf("stderr = %q, want it to say no note matches the target", stderr)
	}
	data, err := os.ReadFile(filepath.Join(root, "nn", "quarterly-report.md"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != report {
		t.Errorf("a note the user never named was appended to:\n%s", data)
	}
}

func TestAddToNeverAppendsIntoAnImage(t *testing.T) {
	root := newTestVault(t)
	png := []byte("\x89PNG\r\n\x1a\nkubernetes dashboard pixels")
	writeImage(t, root, "nn/assets/kubernetes-dashboard.png", png)
	// The recognized text puts the image in the search corpus, findable by search but not a note.
	writeSidecar(t, root, "nn/assets/kubernetes-dashboard.png", []ocr.Line{{Text: "kubernetes dashboard"}})

	_, stderr, code := runCmd(t, "", "add", "this belongs in a note", "--to", "dashboard")
	if code != 2 {
		t.Fatalf("code = %d, want 2: an image is not a note; stderr = %q", code, stderr)
	}
	if !strings.Contains(stderr, "no note matches") {
		t.Errorf("stderr = %q, want --to to refuse a target only search could find", stderr)
	}
	data, err := os.ReadFile(filepath.Join(root, "nn", "assets", "kubernetes-dashboard.png"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != string(png) {
		t.Errorf("markdown was appended into an image: %q", data)
	}
}

// addTestTemplate is the starter template plus one key nn cannot carry into a note.
const addTestTemplate = `---
tags: [hotkey]
date: {{date}}
time: "{{time}}"
where: {{where}}
repo: {{repo}}
status: draft
---
## {{title}}

- Shortcut:
- Action:
`

func TestAddTemplateWritesOneFrontmatter(t *testing.T) {
	root := newTestVault(t)
	writeNote(t, root, "nn/_templates/hotkey.md", addTestTemplate)

	_, stderr, code := runCmd(t, "", "add", "-T", "hotkey", "--title", "Split pane")
	if code != 0 {
		t.Fatalf("code = %d, stderr = %q", code, stderr)
	}
	data, err := os.ReadFile(filepath.Join(root, "nn", "split-pane.md"))
	if err != nil {
		t.Fatalf("note was not written: %v", err)
	}
	fields, body, _, err := vault.ParseFrontmatter(string(data))
	if err != nil {
		t.Fatalf("frontmatter does not parse: %v\n%s", err, data)
	}
	if strings.Contains(body, "---") {
		t.Errorf("the template's own frontmatter was left in the body:\n%s", data)
	}
	if got := fmtField(fields["tags"]); got != "hotkey" {
		t.Errorf("tags = %v, want the template's tag in the note's own tags:\n%s", fields["tags"], data)
	}
	if got := fmtField(fields["aliases"]); got != "Split pane" {
		t.Errorf("aliases = %v, want the title:\n%s", fields["aliases"], data)
	}
	if !strings.Contains(body, "## Split pane") || !strings.Contains(body, "- Shortcut:") {
		t.Errorf("body = %q, want the rendered template text", body)
	}
	if !strings.Contains(stderr, `ignoring frontmatter key "status"`) {
		t.Errorf("stderr = %q, want a warning naming the key nn could not carry", stderr)
	}
}

func TestAddTemplateWithoutTitleStillWritesOneFrontmatter(t *testing.T) {
	root := newTestVault(t)
	writeNote(t, root, "nn/_templates/hotkey.md", addTestTemplate)

	_, stderr, code := runCmd(t, "", "add", "-T", "hotkey")
	if code != 0 {
		t.Fatalf("code = %d, stderr = %q", code, stderr)
	}
	notes := notesIn(t, filepath.Join(root, "nn"))
	if len(notes) != 1 {
		t.Fatalf("notes = %v, want exactly one", notes)
	}
	data, err := os.ReadFile(filepath.Join(root, "nn", notes[0]))
	if err != nil {
		t.Fatal(err)
	}
	fields, body, _, err := vault.ParseFrontmatter(string(data))
	if err != nil {
		t.Fatalf("frontmatter does not parse: %v\n%s", err, data)
	}
	if strings.Contains(body, "---") {
		t.Errorf("the template's own frontmatter was left in the body:\n%s", data)
	}
	if got := fmtField(fields["tags"]); got != "hotkey" {
		t.Errorf("tags = %v, want the template's tag:\n%s", fields["tags"], data)
	}
	if notes[0] == "tags-hotkey.md" {
		t.Errorf("the note was named after a line of the template's frontmatter: %s", notes[0])
	}
}

func TestAddTemplateAppliesWhenWordsGiveTheTitle(t *testing.T) {
	root := newTestVault(t)
	writeNote(t, root, "nn/_templates/hotkey.md", addTestTemplate)

	_, stderr, code := runCmd(t, "", "add", "Split", "pane", "-T", "hotkey")
	if code != 0 {
		t.Fatalf("code = %d, stderr = %q", code, stderr)
	}
	data, err := os.ReadFile(filepath.Join(root, "nn", "split-pane.md"))
	if err != nil {
		t.Fatalf("note was not written: %v", err)
	}
	fields, body, _, err := vault.ParseFrontmatter(string(data))
	if err != nil {
		t.Fatalf("frontmatter does not parse: %v\n%s", err, data)
	}
	if !strings.Contains(body, "- Shortcut:") {
		t.Errorf("body = %q, want the template's text: -T applies whatever the source was", body)
	}
	if !strings.Contains(body, "## Split pane") {
		t.Errorf("body = %q, want the words filled into the template's {{title}}", body)
	}
	if got := fmtField(fields["tags"]); got != "hotkey" {
		t.Errorf("tags = %v, want the template's tag in the note's own tags:\n%s", fields["tags"], data)
	}
	if got := fmtField(fields["aliases"]); got != "Split pane" {
		t.Errorf("aliases = %v, want the words as the title:\n%s", fields["aliases"], data)
	}
}

func TestAddTemplateKeepsCapturedBodyBelowIt(t *testing.T) {
	root := newTestVault(t)
	writeNote(t, root, "nn/_templates/hotkey.md", addTestTemplate)

	_, stderr, code := runCmd(t, "captured from the pipe\n", "add", "Split", "pane", "-T", "hotkey")
	if code != 0 {
		t.Fatalf("code = %d, stderr = %q", code, stderr)
	}
	data, err := os.ReadFile(filepath.Join(root, "nn", "split-pane.md"))
	if err != nil {
		t.Fatalf("note was not written: %v", err)
	}
	_, body, _, err := vault.ParseFrontmatter(string(data))
	if err != nil {
		t.Fatalf("frontmatter does not parse: %v\n%s", err, data)
	}
	shortcut := strings.Index(body, "- Shortcut:")
	piped := strings.Index(body, "captured from the pipe")
	if shortcut < 0 {
		t.Fatalf("body = %q, want the template's text alongside the piped body", body)
	}
	if piped < 0 {
		t.Fatalf("body = %q, want the piped body kept", body)
	}
	if piped < shortcut {
		t.Errorf("body = %q, want what was captured below the template's body", body)
	}
}

func TestAddMissingTemplateIsAnErrorAtEverySource(t *testing.T) {
	cases := []struct {
		name  string
		stdin string
		args  []string
	}{
		{"words", "", []string{"add", "Split", "pane", "-T", "hotkeyy"}},
		{"stdin", "piped text\n", []string{"add", "-T", "hotkeyy"}},
		{"words and stdin", "piped text\n", []string{"add", "Split", "pane", "-T", "hotkeyy"}},
		{"no source", "", []string{"add", "-T", "hotkeyy"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := newTestVault(t)
			writeNote(t, root, "nn/_templates/hotkey.md", addTestTemplate)

			_, stderr, code := runCmd(t, tc.stdin, tc.args...)
			if code != 2 {
				t.Fatalf("code = %d, want 2: a template name nothing answers to is an error, not a plain note; stderr = %q", code, stderr)
			}
			if !strings.Contains(stderr, "hotkeyy") {
				t.Errorf("stderr = %q, want it to name the template that does not exist", stderr)
			}
			if notes := notesIn(t, filepath.Join(root, "nn")); len(notes) != 0 {
				t.Errorf("a note was written despite the missing template: %v", notes)
			}
		})
	}
}

func TestAddRefusesATemplatesDirectoryOutsideTheVault(t *testing.T) {
	root := newTestVault(t)
	away := t.TempDir()
	writeNote(t, away, "t.md", "---\ntags: [x]\n---\nSTRANGER\n")
	if err := os.MkdirAll(filepath.Join(root, "nn"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(away, filepath.Join(root, "nn", "_templates")); err != nil {
		t.Fatal(err)
	}

	stdout, stderr, code := runCmd(t, "", "add", "Title", "-T", "t")
	if code == 0 {
		t.Fatalf("code = 0, stdout = %q: want the template refused", stdout)
	}
	if !strings.Contains(stderr, "outside the vault") {
		t.Errorf("stderr = %q, want the refusal to say the templates directory leaves the vault", stderr)
	}
	if notes := notesIn(t, filepath.Join(root, "nn")); len(notes) != 0 {
		t.Errorf("a note was written from a template outside the vault: %v", notes)
	}
	if entries, err := os.ReadDir(away); err != nil || len(entries) != 1 {
		t.Errorf("the far end of the link holds %v (%v), want only the template it started with", entries, err)
	}
}

// -T carries a whole note's skeleton, so there is nothing sensible to append it into.
func TestAddTemplateWithToIsUsageError(t *testing.T) {
	const existing = "---\ntags: [docker]\ndate: 2026-01-01\n---\n\nfirst line\n"
	cases := []struct {
		name  string
		stdin string
		args  []string
	}{
		{"words", "", []string{"add", "one more thing", "--to", "docker-notes", "-T", "hotkey"}},
		{"template flag first", "", []string{"add", "-T", "hotkey", "--to", "docker-notes"}},
		{"stdin", "piped text\n", []string{"add", "--to", "docker-notes", "-T", "hotkey"}},
		{"preview", "", []string{"add", "one more thing", "--to", "docker-notes", "-T", "hotkey", "--preview"}},
		{"target nothing answers to", "", []string{"add", "one more thing", "--to", "nosuch", "-T", "hotkey"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := newTestVault(t)
			writeNote(t, root, "nn/docker-notes.md", existing)
			writeNote(t, root, "nn/_templates/hotkey.md", addTestTemplate)

			_, stderr, code := runCmd(t, tc.stdin, tc.args...)
			if code != 2 {
				t.Fatalf("code = %d, want 2: -T and --to cannot be combined; stderr = %q", code, stderr)
			}
			if !strings.Contains(stderr, "-T and --to") {
				t.Errorf("stderr = %q, want it to name both flags", stderr)
			}
			data, err := os.ReadFile(filepath.Join(root, "nn", "docker-notes.md"))
			if err != nil {
				t.Fatal(err)
			}
			if string(data) != existing {
				t.Errorf("the target note was written to:\n%s", data)
			}
			if notes := notesIn(t, filepath.Join(root, "nn")); len(notes) != 1 {
				t.Errorf("notes = %v, want only the note that existed before", notes)
			}
		})
	}
}

func TestAddTemplateAndToStillWorkApart(t *testing.T) {
	t.Run("template without --to", func(t *testing.T) {
		root := newTestVault(t)
		writeNote(t, root, "nn/_templates/hotkey.md", addTestTemplate)

		_, stderr, code := runCmd(t, "", "add", "-T", "hotkey", "--title", "Split pane")
		if code != 0 {
			t.Fatalf("code = %d, stderr = %q", code, stderr)
		}
		data, err := os.ReadFile(filepath.Join(root, "nn", "split-pane.md"))
		if err != nil {
			t.Fatalf("note was not written: %v", err)
		}
		if _, body, _, perr := vault.ParseFrontmatter(string(data)); perr != nil {
			t.Fatalf("frontmatter does not parse: %v\n%s", perr, data)
		} else if !strings.Contains(body, "## Split pane") {
			t.Errorf("body = %q, want the rendered template", body)
		}
	})

	t.Run("--to without a template", func(t *testing.T) {
		root := newTestVault(t)
		writeNote(t, root, "nn/docker-notes.md", "---\ntags: [docker]\ndate: 2026-01-01\n---\n\nfirst line\n")
		writeNote(t, root, "nn/_templates/hotkey.md", addTestTemplate)

		_, stderr, code := runCmd(t, "", "add", "one more thing", "--to", "docker-notes")
		if code != 0 {
			t.Fatalf("code = %d, stderr = %q", code, stderr)
		}
		data, err := os.ReadFile(filepath.Join(root, "nn", "docker-notes.md"))
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(data), "first line\n\none more thing") {
			t.Errorf("note = %q, want the line appended", string(data))
		}
	})
}

func TestAddPreviewResolvesTheAppendTarget(t *testing.T) {
	root := newTestVault(t)
	const note = "---\ntags: [docker]\ndate: 2026-01-01\n---\n\nfirst line\n"
	writeNote(t, root, "nn/docker-notes.md", note)

	_, stderr, code := runCmd(t, "", "add", "one more thing", "--to", "nosuch", "--preview")
	if code != 2 {
		t.Fatalf("code = %d, want 2, the same as the run without --preview; stderr = %q", code, stderr)
	}
	if !strings.Contains(stderr, "no note matches") {
		t.Errorf("stderr = %q, want it to say no note matches the target", stderr)
	}

	stdout, stderr, code := runCmd(t, "", "add", "one more thing", "--to", "docker-notes", "--preview")
	if code != 0 {
		t.Fatalf("code = %d, stderr = %q", code, stderr)
	}
	if !strings.Contains(stdout, "to: nn/docker-notes.md") {
		t.Errorf("stdout = %q, want the resolved target the append would land in", stdout)
	}
	data, err := os.ReadFile(filepath.Join(root, "nn", "docker-notes.md"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != note {
		t.Errorf("--preview wrote to the target note:\n%s", data)
	}
}

func TestAddSimilarTagWarningGoesToTheCommandsStderr(t *testing.T) {
	root := newTestVault(t)
	writeNote(t, root, "nn/channels.md", "---\ntags: [golang]\ndate: 2026-01-01\naliases: [Channels]\n---\n\nbuffered and unbuffered\n")

	_, stderr, code := runCmd(t, "", "add", "Mutex", "notes", "-t", "go")
	if code != 0 {
		t.Fatalf("code = %d, stderr = %q", code, stderr)
	}
	if !strings.Contains(stderr, `nn: add: tag "go" is similar to existing "golang"`) {
		t.Errorf("stderr = %q, want the similar-tag warning on the command's own stderr, not the process's", stderr)
	}
}

func fmtField(value any) string {
	items, ok := value.([]any)
	if !ok {
		items = []any{value}
	}
	parts := make([]string, 0, len(items))
	for _, item := range items {
		s, _ := item.(string)
		parts = append(parts, strings.TrimSpace(s))
	}
	return strings.Join(parts, ", ")
}

func notesIn(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".md") {
			names = append(names, e.Name())
		}
	}
	return names
}

func TestAddPreviewJSONIsAnArray(t *testing.T) {
	newTestVault(t)
	stdout, stderr, code := runCmd(t, "", "add", "Meeting", "notes", "--preview", "--json")
	if code != 0 {
		t.Fatalf("code = %d, stderr = %q", code, stderr)
	}
	rows := decodeJSONRows[previewRow](t, stdout)
	if len(rows) != 1 || rows[0].Title != "Meeting notes" {
		t.Errorf("rows = %+v, want one row titled \"Meeting notes\"", rows)
	}
}

// The body carries a tab and newline; a second escaping pass would double the backslashes.
func TestAddPreviewHonoursTSV(t *testing.T) {
	newTestVault(t)
	stdout, stderr, code := runCmd(t, "line1\tcol2\nline2\n", "add", "Preview", "title", "--preview", "--tsv")
	if code != 0 {
		t.Fatalf("code = %d, stderr = %q", code, stderr)
	}
	lines := strings.Split(strings.TrimRight(stdout, "\n"), "\n")
	if len(lines) != 2 || lines[0] != "to\ttitle\ttags\tbody\timages" {
		t.Fatalf("stdout = %q, want a TSV header and one row", stdout)
	}
	cols := strings.Split(lines[1], "\t")
	if len(cols) != 5 {
		t.Fatalf("row = %q, want 5 columns, got %d", lines[1], len(cols))
	}
	if cols[1] != "Preview title" {
		t.Errorf("title column = %q, want %q", cols[1], "Preview title")
	}
	if want := `line1\tcol2\nline2\n`; cols[3] != want {
		t.Errorf("body column = %q, want %q", cols[3], want)
	}
	unescaped := strings.NewReplacer(`\\`, "\\", `\t`, "\t", `\n`, "\n", `\r`, "\r").Replace(cols[3])
	if want := "line1\tcol2\nline2\n"; unescaped != want {
		t.Errorf("body column decodes to %q, want %q", unescaped, want)
	}
}

func TestAddImageFromStdinMagic(t *testing.T) {
	root := newTestVault(t)
	png := []byte("\x89PNG\r\n\x1a\nrestofdata")
	_, stderr, code := runCmd(t, string(png), "add", "screenshot")
	if code != 0 {
		t.Fatalf("code = %d, stderr = %q", code, stderr)
	}
	entries, err := os.ReadDir(filepath.Join(root, "nn", "assets"))
	if err != nil || len(entries) != 1 {
		t.Fatalf("expected one saved asset: %v, %v", err, entries)
	}
}

func TestAddOCRDefaultOffFromConfigSkipsRecognition(t *testing.T) {
	newTestVault(t)
	writeConfig(t, "[capture]\nocr = false\n")
	engine := &fakeOCREngine{lines: []ocr.Line{{Text: "should not run"}}}
	withFakeOCR(t, engine)

	png := []byte("\x89PNG\r\n\x1a\nrestofdata")
	_, stderr, code := runCmd(t, string(png), "add", "screenshot")
	if code != 0 {
		t.Fatalf("code = %d, stderr = %q", code, stderr)
	}
	if engine.calls != 0 {
		t.Errorf("OCR engine was called %d times, want 0 with capture.ocr = false", engine.calls)
	}
}

func TestAddOCRFlagForcesOCROverConfig(t *testing.T) {
	newTestVault(t)
	writeConfig(t, "[capture]\nocr = false\n")
	engine := &fakeOCREngine{lines: []ocr.Line{{Text: "hello"}}}
	withFakeOCR(t, engine)

	png := []byte("\x89PNG\r\n\x1a\nrestofdata")
	_, stderr, code := runCmd(t, string(png), "add", "screenshot", "--ocr")
	if code != 0 {
		t.Fatalf("code = %d, stderr = %q", code, stderr)
	}
	if engine.calls != 1 {
		t.Errorf("OCR engine was called %d times, want 1 (--ocr beats capture.ocr = false)", engine.calls)
	}
}

func TestAddOCRFlagsCannotCombine(t *testing.T) {
	newTestVault(t)
	_, stderr, code := runCmd(t, "", "add", "x", "--ocr", "--no-ocr")
	if code != 2 {
		t.Errorf("code = %d, want 2, stderr = %q", code, stderr)
	}
	if !strings.Contains(stderr, "--ocr and --no-ocr cannot be combined") {
		t.Errorf("stderr = %q, want it to name the conflicting flags", stderr)
	}
}

func TestAddNothingToAddIsUsageError(t *testing.T) {
	newTestVault(t)
	_, stderr, code := runCmd(t, "", "add")
	if code != 2 {
		t.Fatalf("code = %d, want 2, stderr=%q", code, stderr)
	}
}

func TestAddUnknownOptionIsUsageError(t *testing.T) {
	newTestVault(t)
	_, stderr, code := runCmd(t, "", "add", "--bogus", "x")
	if code != 2 {
		t.Errorf("code = %d, want 2", code)
	}
	if !strings.Contains(stderr, "--bogus") {
		t.Errorf("stderr = %q, want it to name the bad flag", stderr)
	}
}

// installEditorThatWrites points VISUAL at a script that replaces the draft file with fixed content.
func installEditorThatWrites(t *testing.T, content string) {
	t.Helper()
	dir := t.TempDir()
	script := "#!/bin/sh\nprintf '%s' " + shellQuote(content) + " > \"$1\"\n"
	path := filepath.Join(dir, "vi")
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("VISUAL", path)
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func TestAddDashEOpensEditorOnAssembledBody(t *testing.T) {
	root := newTestVault(t)
	installEditorThatWrites(t, "written by the editor\n")

	_, stderr, code := runCmd(t, "", "add", "Docker", "cleanup", "-e")
	if code != 0 {
		t.Fatalf("code = %d, stderr = %q", code, stderr)
	}
	data, err := os.ReadFile(filepath.Join(root, "nn", "docker-cleanup.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "written by the editor") {
		t.Errorf("note = %q, want the editor's content in the body", string(data))
	}
}

func TestAddPostSaveHookRuns(t *testing.T) {
	root := newTestVault(t)
	writeConfigWithPostSave(t, `touch "$NN_NOTE.hook"`)

	_, stderr, code := runCmd(t, "", "add", "hook", "test")
	if code != 0 {
		t.Fatalf("code = %d, stderr = %q", code, stderr)
	}
	if _, err := os.Stat(filepath.Join(root, "nn", "hook-test.md.hook")); err != nil {
		t.Errorf("post_save hook did not run: %v", err)
	}
}

func TestAddPostSaveTimeoutFromConfig(t *testing.T) {
	newTestVault(t)
	writeConfig(t, "[hooks]\npost_save = \"sleep 5\"\npost_save_timeout = \"50ms\"\n")

	// PostSave's warning goes to the real os.Stderr, not captured stderr, so only timing is checked here.
	start := time.Now()
	_, _, code := runCmd(t, "", "add", "hook", "test")
	if code != 0 {
		t.Fatalf("code = %d", code)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Errorf("add took %s, want it to give up around hooks.post_save_timeout (50ms)", elapsed)
	}
}

// writeConfigWithPostSave rewrites NN_CONFIG's file with a hooks.post_save command; call after newTestVault.
func writeConfigWithPostSave(t *testing.T, cmd string) {
	t.Helper()
	path := os.Getenv("NN_CONFIG")
	if path == "" {
		t.Fatal("NN_CONFIG is not set; call newTestVault first")
	}
	root := os.Getenv("NN_ROOT")
	content := "[vault]\nroot = " + config.TOMLString(root) + "\n[hooks]\npost_save = " + config.TOMLString(cmd) + "\n"
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
