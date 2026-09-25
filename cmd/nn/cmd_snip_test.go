package main

import (
	"strings"
	"testing"
	"time"

	"github.com/lamovs/nn/internal/output"
	"github.com/lamovs/nn/internal/state"
)

func TestSnipPicksBlockMatchingQuery(t *testing.T) {
	root := newTestVault(t)
	writeNote(t, root, "nn/docker.md", "# Docker\n\n```sh\necho hello\n```\n\n```sh\ndocker system prune -af\n```\n")

	stdout, stderr, code := runCmd(t, "", "snip", "docker", "prune")
	if code != 0 {
		t.Fatalf("code = %d, stderr = %q", code, stderr)
	}
	if strings.TrimSpace(stdout) != "docker system prune -af" {
		t.Errorf("stdout = %q, want the matching block only", stdout)
	}
}

func TestSnipLayoutFallbackDisabledByConfig(t *testing.T) {
	root := newTestVault(t)
	writeConfig(t, "[search]\nlayout_fallback = false\n")
	writeNote(t, root, "nn/docker.md", "# Docker\n\n```sh\ndocker system prune -af\n```\n")

	stdout, stderr, code := runCmd(t, "", "snip", cyrillicDocker)
	if code != output.ExitNotFound {
		t.Errorf("code = %d, want %d (no fallback, nothing found)", code, output.ExitNotFound)
	}
	if !strings.Contains(stderr, "no results for") {
		t.Errorf("stderr = %q, want a no-results note", stderr)
	}
	if stdout != "" {
		t.Errorf("stdout = %q, want nothing", stdout)
	}
}

func TestSnipLayoutFallbackEnabledByDefault(t *testing.T) {
	root := newTestVault(t)
	writeNote(t, root, "nn/docker.md", "# Docker\n\n```sh\ndocker system prune -af\n```\n")
	stdout, stderr, code := runCmd(t, "", "snip", cyrillicDocker)
	if code != 0 {
		t.Fatalf("code = %d, stderr = %q, want the layout fallback to find it", code, stderr)
	}
	if strings.TrimSpace(stdout) != "docker system prune -af" {
		t.Errorf("stdout = %q, want the block", stdout)
	}
}

func TestSnipMultipleMatchesPicksBestByRankWithoutTTY(t *testing.T) {
	root := newTestVault(t)
	writeNote(t, root, "nn/docker-prune.md", "# Docker prune\n\n```sh\ndocker system prune -af\n```\n")
	writeNote(t, root, "nn/other.md", "# Other\n\nSee docker prune elsewhere.\n\n```sh\ndocker image prune\n```\n")

	stdout, stderr, code := runCmd(t, "", "snip", "docker", "prune")
	if code != 0 {
		t.Fatalf("code = %d, stderr = %q", code, stderr)
	}
	if !strings.Contains(stderr, "matches") {
		t.Errorf("stderr = %q, want a note about multiple matches", stderr)
	}
	if !strings.Contains(stdout, "docker system prune -af") {
		t.Errorf("stdout = %q, want the title-matching note's block (best ranked)", stdout)
	}
}

func TestSnipSetFillsPlaceholder(t *testing.T) {
	root := newTestVault(t)
	writeNote(t, root, "nn/docker.md", "# Docker\n\n```sh\ndocker run --name {{name:app}} -d nginx\n```\n")

	stdout, stderr, code := runCmd(t, "", "snip", "docker", "run", "--set", "name=web")
	if code != 0 {
		t.Fatalf("code = %d, stderr = %q", code, stderr)
	}
	if !strings.Contains(stdout, "--name web") {
		t.Errorf("stdout = %q, want name replaced with web", stdout)
	}
}

func TestSnipMissingPlaceholderExitsTwoWithoutTTY(t *testing.T) {
	root := newTestVault(t)
	writeNote(t, root, "nn/docker.md", "# Docker\n\n```sh\ndocker run --name {{name}} -d nginx\n```\n")

	_, stderr, code := runCmd(t, "", "snip", "docker", "run")
	if code != 2 {
		t.Fatalf("code = %d, want 2, stderr = %q", code, stderr)
	}
	if !strings.Contains(stderr, "name") {
		t.Errorf("stderr = %q, want it to name the missing placeholder", stderr)
	}
}

func TestSnipRawSkipsSubstitution(t *testing.T) {
	root := newTestVault(t)
	writeNote(t, root, "nn/docker.md", "# Docker\n\n```sh\ndocker run --name {{name:app}} -d nginx\n```\n")

	stdout, stderr, code := runCmd(t, "", "snip", "docker", "run", "--raw")
	if code != 0 {
		t.Fatalf("code = %d, stderr = %q", code, stderr)
	}
	if !strings.Contains(stdout, "{{name:app}}") {
		t.Errorf("stdout = %q, want the placeholder left as-is", stdout)
	}
}

func TestSnipListJSON(t *testing.T) {
	root := newTestVault(t)
	writeNote(t, root, "nn/docker.md", "---\ntags: [docker]\n---\n\n# Docker\n\n```sh\ndocker system prune -af\n```\n")

	stdout, stderr, code := runCmd(t, "", "snip", "-t", "docker", "--list", "--json")
	if code != 0 {
		t.Fatalf("code = %d, stderr = %q", code, stderr)
	}
	rows := decodeJSONRows[snipListRow](t, stdout)
	if len(rows) != 1 || rows[0].Path != "nn/docker.md" {
		t.Fatalf("rows = %+v", rows)
	}
}

func TestSnipListNoMatchExitsOne(t *testing.T) {
	newTestVault(t)
	_, stderr, code := runCmd(t, "", "snip", "nonexistentword", "--list")
	if code != 1 {
		t.Errorf("code = %d, want 1, stderr = %q", code, stderr)
	}
}

func TestSnipNoMatchExitsOne(t *testing.T) {
	newTestVault(t)
	_, stderr, code := runCmd(t, "", "snip", "nonexistentword")
	if code != 1 {
		t.Errorf("code = %d, want 1, stderr = %q", code, stderr)
	}
}

func TestSnipCopyPutsResultOnClipboard(t *testing.T) {
	root := newTestVault(t)
	writeNote(t, root, "nn/docker.md", "# Docker\n\n```sh\ndocker system prune -af\n```\n")
	copied := withFakeClipboardCopy(t)

	_, stderr, code := runCmd(t, "", "snip", "docker", "prune", "--copy")
	if code != 0 {
		t.Fatalf("code = %d, stderr = %q", code, stderr)
	}
	if *copied != "docker system prune -af" {
		t.Errorf("copied = %q", *copied)
	}
}

func TestSnipRecordsOpen(t *testing.T) {
	root := newTestVault(t)
	writeNote(t, root, "nn/docker.md", "# Docker\n\n```sh\ndocker system prune -af\n```\n")

	_, stderr, code := runCmd(t, "", "snip", "docker", "prune")
	if code != 0 {
		t.Fatalf("code = %d, stderr = %q", code, stderr)
	}
	statePath, err := state.Path()
	if err != nil {
		t.Fatal(err)
	}
	st, err := state.LoadFile(statePath, 14*24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if st.Frecency("nn/docker.md") <= 0 {
		t.Errorf("the matched note's open was not recorded")
	}
}

func TestSnipUnknownOptionIsUsageError(t *testing.T) {
	newTestVault(t)
	_, stderr, code := runCmd(t, "", "snip", "--bogus")
	if code != 2 {
		t.Errorf("code = %d, want 2, stderr = %q", code, stderr)
	}
}
