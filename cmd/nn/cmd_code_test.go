package main

import (
	"strings"
	"testing"
)

const codeNoteBody = "# Snippets\n\n" +
	"```sh\necho one\n```\n\n" +
	"```python\nprint(2)\n```\n\n" +
	"```sh\necho three\n```\n"

func TestCodePrintsAllBlocksBlankLineSeparated(t *testing.T) {
	root := newTestVault(t)
	writeNote(t, root, "nn/snippets.md", codeNoteBody)

	stdout, _, code := runCmd(t, "", "code", "snippets")
	if code != 0 {
		t.Fatalf("code = %d", code)
	}
	if !strings.Contains(stdout, "echo one\n\nprint(2)\n\necho three") {
		t.Errorf("stdout = %q, want all three blocks blank-line separated", stdout)
	}
}

func TestCodeRecordsOpen(t *testing.T) {
	root := newTestVault(t)
	writeNote(t, root, "nn/snippets.md", codeNoteBody)

	if _, stderr, code := runCmd(t, "", "code", "snippets"); code != 0 {
		t.Fatalf("code = %d, stderr = %q", code, stderr)
	}
	if f := noteFrecency(t, "nn/snippets.md"); f <= 0 {
		t.Errorf("Frecency = %v, want the open to have been recorded", f)
	}
}

func TestCodeLangFilter(t *testing.T) {
	root := newTestVault(t)
	writeNote(t, root, "nn/snippets.md", codeNoteBody)

	stdout, _, code := runCmd(t, "", "code", "snippets", "--lang", "sh")
	if code != 0 {
		t.Fatalf("code = %d", code)
	}
	if strings.Contains(stdout, "print(2)") {
		t.Errorf("stdout = %q, --lang sh should exclude the python block", stdout)
	}
	if !strings.Contains(stdout, "echo one") || !strings.Contains(stdout, "echo three") {
		t.Errorf("stdout = %q, want both sh blocks", stdout)
	}
}

func TestCodeSelectByNumber(t *testing.T) {
	root := newTestVault(t)
	writeNote(t, root, "nn/snippets.md", codeNoteBody)

	stdout, _, code := runCmd(t, "", "code", "snippets", "--lang", "sh", "-n", "2")
	if code != 0 {
		t.Fatalf("code = %d", code)
	}
	if strings.TrimSpace(stdout) != "echo three" {
		t.Errorf("stdout = %q, want just the second sh block", stdout)
	}
}

func TestCodeOutOfRangeIsNotFound(t *testing.T) {
	root := newTestVault(t)
	writeNote(t, root, "nn/snippets.md", codeNoteBody)

	_, _, code := runCmd(t, "", "code", "snippets", "-n", "99")
	if code != 1 {
		t.Errorf("code = %d, want 1", code)
	}
}

func TestCodeNoBlocksIsNotFound(t *testing.T) {
	root := newTestVault(t)
	writeNote(t, root, "nn/plain.md", "# Plain\n\nno code here\n")

	_, _, code := runCmd(t, "", "code", "plain")
	if code != 1 {
		t.Errorf("code = %d, want 1", code)
	}
}

func TestCodeJSONAndPaths(t *testing.T) {
	root := newTestVault(t)
	writeNote(t, root, "nn/snippets.md", codeNoteBody)

	stdout, _, code := runCmd(t, "", "code", "snippets", "--json")
	if code != 0 {
		t.Fatalf("code = %d", code)
	}
	rows := decodeJSONRows[codeRow](t, stdout)
	if len(rows) != 3 {
		t.Fatalf("rows = %+v, want 3", rows)
	}
	if rows[0].Lang != "sh" || rows[1].Lang != "python" {
		t.Errorf("rows = %+v", rows)
	}

	stdout, _, code = runCmd(t, "", "code", "snippets", "--paths")
	if code != 0 {
		t.Fatalf("code = %d", code)
	}
	if strings.TrimSpace(stdout) != "nn/snippets.md" {
		t.Errorf("stdout = %q, want the path once", stdout)
	}
}

func TestCodeEmptyJSONIsAnArray(t *testing.T) {
	root := newTestVault(t)
	writeNote(t, root, "nn/plain.md", "# Plain\n\nno code here\n")

	stdout, _, code := runCmd(t, "", "code", "plain", "--json")
	if code != 1 {
		t.Errorf("code = %d, want 1", code)
	}
	if got := strings.TrimSpace(stdout); got != "[]" {
		t.Errorf("stdout = %q, want []", got)
	}
}

func TestCodeTSVKeepsOneRecordPerBlock(t *testing.T) {
	root := newTestVault(t)
	writeNote(t, root, "nn/snippets.md", "# Snippets\n\n```sh\necho one\necho two\n```\n")

	stdout, _, code := runCmd(t, "", "code", "snippets", "--tsv")
	if code != 0 {
		t.Fatalf("code = %d", code)
	}
	lines := strings.Split(strings.TrimRight(stdout, "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("stdout = %q, want a header and exactly one record", stdout)
	}
	fields := strings.Split(lines[1], "\t")
	if len(fields) != 5 {
		t.Fatalf("record = %q has %d columns, want 5", lines[1], len(fields))
	}
	if fields[0] != "nn/snippets.md" {
		t.Errorf("path column = %q", fields[0])
	}
	if fields[4] != `echo one\necho two` {
		t.Errorf("code column = %q, want the block on one line, newline escaped", fields[4])
	}
}

func TestCodeFromStdinTakesFirstPath(t *testing.T) {
	root := newTestVault(t)
	writeNote(t, root, "nn/a.md", "# A\n\n```sh\necho a\n```\n")
	writeNote(t, root, "nn/b.md", "# B\n\n```sh\necho b\n```\n")

	stdout, stderr, code := runCmd(t, "nn/a.md\nnn/b.md\n", "code", "-")
	if code != 0 {
		t.Fatalf("code = %d, stderr = %q", code, stderr)
	}
	if strings.TrimSpace(stdout) != "echo a" {
		t.Errorf("stdout = %q, want only the first piped note's block", stdout)
	}
	if !strings.Contains(stderr, "2 paths") {
		t.Errorf("stderr = %q, want a note about the paths that were skipped", stderr)
	}
}

func TestParseCodeOptionsCopyFlag(t *testing.T) {
	// Pure parse, never through cmdCode: --copy there calls platform.CopyText, the real system clipboard.
	opt, words, err := parseCodeOptions([]string{"a", "--copy", "--lang", "sh"})
	if err != nil {
		t.Fatal(err)
	}
	if !opt.copy || opt.lang != "sh" {
		t.Errorf("opt = %+v", opt)
	}
	if len(words) != 1 || words[0] != "a" {
		t.Errorf("words = %v", words)
	}
}
