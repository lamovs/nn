package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/lamovs/nn/internal/ai"
	"github.com/lamovs/nn/internal/config"
)

const digestTestAnswer = `{"points":[{"text":"Use docker builder prune to clear the build cache.","source_ids":["S1"]}]}`

type digestFixture struct {
	root, dir, cfg string
}

// newDigestFixture wires a fake command-engine profile and a small vault (a cited note, an unrelated one).
func newDigestFixture(t *testing.T) digestFixture {
	t.Helper()
	root := newTestVault(t)
	dir := t.TempDir()
	t.Setenv("NN_DIGEST_FAKE", dir)
	t.Setenv("NN_AI", "")
	t.Setenv("XDG_CACHE_HOME", filepath.Join(t.TempDir(), "cache"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(t.TempDir(), "state"))
	previous := digestAsker
	digestAsker = func(context.Context) ai.Asker { return &scriptedAsker{} }
	t.Cleanup(func() { digestAsker = previous })
	engine := filepath.Join(dir, "fake-model")
	script := `#!/bin/sh
n=0
if [ -f "$NN_DIGEST_FAKE/calls" ]; then read -r n < "$NN_DIGEST_FAKE/calls"; fi
n=$((n + 1))
printf '%s\n' "$n" > "$NN_DIGEST_FAKE/calls"
/bin/cat > "$NN_DIGEST_FAKE/request-$n"
printf '%s\n' "$@" > "$NN_DIGEST_FAKE/args-$n"
if [ -f "$NN_DIGEST_FAKE/wait" ]; then sleep 10; fi
/bin/cat "$NN_DIGEST_FAKE/answer"
`
	if err := os.WriteFile(engine, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	cfg := "[ai]\nprofile=\"digest-choice\"\nmode=\"background\"\n" +
		"[ai.profiles.digest-choice]\nengine=\"command\"\nmodel=\"base-model\"\neffort=\"low\"\ncommand=[" +
		config.TOMLString(engine) + ",\"digest-choice\",\"{model}\",\"{effort}\"]\n" +
		"[ai.consent]\ndigest-choice=\"always\"\n[hooks]\npost_save=\"touch MUST_NOT_RUN\"\n"
	f := digestFixture{root: root, dir: dir, cfg: cfg}
	f.write(t, "answer", digestTestAnswer)
	writeConfig(t, cfg)
	writeNote(t, root, "notes/docker.md", "# Docker cache\n\nUse docker builder prune to clear the build cache. #devops\n")
	writeNote(t, root, "notes/unrelated.md", "# Garden\n\nSome other note text.\n")
	return f
}

func (f digestFixture) write(t *testing.T, name, text string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(f.dir, name), []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
}

func (f digestFixture) read(t *testing.T, name string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(f.dir, name))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func (f digestFixture) modelRanOnce(t *testing.T) bool {
	t.Helper()
	return f.read(t, "calls") == "1\n"
}

func (f digestFixture) modelNeverRan(t *testing.T) bool {
	t.Helper()
	_, err := os.Stat(filepath.Join(f.dir, "calls"))
	return os.IsNotExist(err)
}

func TestDigestHelpAndIndex(t *testing.T) {
	newTestVault(t)
	stdout, stderr, code := runCmd(t, "", "digest", "--help")
	if code != 0 || !strings.Contains(stdout, "nn digest") || !strings.Contains(stdout, "--save") || !strings.Contains(stdout, "--effort") {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	stdout, _, _ = runCmd(t, "")
	if !strings.Contains(stdout, "digest") {
		t.Fatal("digest absent from command index")
	}
}

func TestDigestZshCompletionListsDigest(t *testing.T) {
	if !strings.Contains(zshIntegration, "digest:'summarize selected notes'") || !strings.Contains(zshIntegration, "\n    digest)\n") {
		t.Error("zshIntegration does not list a digest command and completion case")
	}
}

func TestDigestBasicSuccessSynchronous(t *testing.T) {
	f := newDigestFixture(t)
	stdout, stderr, code := runCmd(t, "STDIN MUST NOT BE SENT", "digest", "docker", "-t", "devops", "--since", "7d")
	if code != 0 {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	for _, want := range []string{"Digest of 1 note", "Use docker builder prune to clear the build cache. [1]", "Sources:", "[1] [Docker cache](obsidian://open?path=", "notes/docker.md"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("stdout missing %q: %q", want, stdout)
		}
	}
	if strings.Contains(stdout, "Unrelated") || strings.Contains(stdout, "Garden") {
		t.Errorf("stdout leaked the unrelated note: %q", stdout)
	}
	if !f.modelRanOnce(t) {
		t.Fatal("expected exactly one synchronous model call")
	}
	request := f.read(t, "request-1")
	if strings.Contains(request, "Garden") || strings.Contains(request, "STDIN MUST NOT BE SENT") {
		t.Errorf("request leaked unrelated content: %q", request)
	}
}

func TestDigestWordsAnywhereEquivalence(t *testing.T) {
	// No --since here: it is parsed against the real clock, so both runs must match exactly.
	newDigestFixture(t)
	stdoutA, _, codeA := runCmd(t, "", "digest", "-t", "devops", "docker")
	stdoutB, _, codeB := runCmd(t, "", "digest", "docker", "-t", "devops")
	if codeA != 0 || codeB != 0 || stdoutA != stdoutB {
		t.Fatalf("codeA=%d codeB=%d stdoutA=%q stdoutB=%q", codeA, codeB, stdoutA, stdoutB)
	}
}

func TestDigestDoubleDashProtectsTopicWord(t *testing.T) {
	newDigestFixture(t)
	stdout, stderr, code := runCmd(t, "", "digest", "--", "-x")
	// "-x" must be accepted as TOPIC text, not rejected as an unknown flag.
	if code != 1 || stdout != "" || !strings.Contains(stderr, "no notes match the selection") {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
}

func TestDigestDashBeforeDoubleDashIsCollection(t *testing.T) {
	// A "-" before "--" still selects the collection; only one after "--" is forced literal TOPIC text.
	f := newDigestFixture(t)
	writeNote(t, f.root, "notes/hyphen.md", "# Hyphen note\n\nwell-known pre-commit hooks\n")
	stdout, stderr, code := runCmd(t, "notes/docker.md\n", "digest", "-", "--")
	if code != 0 || !strings.Contains(stdout, "notes/docker.md") || strings.Contains(stdout, "hyphen") {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	if !f.modelRanOnce(t) {
		t.Fatal("expected exactly one model call for the collection")
	}
	stdout, stderr, code = runCmd(t, "notes/docker.md\n", "digest", "-", "--", "docker")
	if code != 2 || stdout != "" || !strings.Contains(stderr, `a lone "-" cannot be combined`) {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
}

func TestDigestCollectionFromStdin(t *testing.T) {
	f := newDigestFixture(t)
	stdout, stderr, code := runCmd(t, "notes/docker.md\n", "digest", "-")
	if code != 0 || !strings.Contains(stdout, "notes/docker.md") {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	if !f.modelRanOnce(t) {
		t.Fatal("expected exactly one model call")
	}
}

func TestDigestEmptyDashInputSelectsNothing(t *testing.T) {
	// An empty "-" list must select nothing, not fall back to the whole vault under other filters.
	f := newDigestFixture(t)
	stdout, stderr, code := runCmd(t, "", "digest", "--since", "7d", "-")
	if code != 1 || stdout != "" || stderr != "nn: digest: no notes match the selection\n" {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	if !f.modelNeverRan(t) {
		t.Fatal("model ran for an empty collection")
	}
}

func TestDigestNoNotesMatch(t *testing.T) {
	f := newDigestFixture(t)
	stdout, stderr, code := runCmd(t, "", "digest", "zzunknownzz")
	if code != 1 || stdout != "" || stderr != "nn: digest: no notes match the selection\n" {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	if !f.modelNeverRan(t) {
		t.Fatal("model ran despite an empty selection")
	}
}

func TestDigestUnknownListedPath(t *testing.T) {
	f := newDigestFixture(t)
	stdout, stderr, code := runCmd(t, "notes/ghost.md\n", "digest", "-")
	if code != 2 || stdout != "" || !strings.Contains(stderr, "nn: digest:") {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	if !f.modelNeverRan(t) {
		t.Fatal("model ran for an unknown listed path")
	}
}

func TestDigestNoSelectorMisuse(t *testing.T) {
	newDigestFixture(t)
	stdout, stderr, code := runCmd(t, "", "digest")
	if code != 2 || stdout != "" || !strings.Contains(stderr, "a topic") || !strings.Contains(stderr, "use - to read note paths from stdin") {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
}

func TestDigestDashOnTerminalMisuse(t *testing.T) {
	old := filterIsTerminal
	filterIsTerminal = func(any) bool { return true }
	defer func() { filterIsTerminal = old }()
	f := newDigestFixture(t)
	stdout, stderr, code := runCmd(t, "", "digest", "-")
	if code != 2 || stdout != "" || stderr == "" {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	if !f.modelNeverRan(t) {
		t.Fatal("model ran for a terminal \"-\"")
	}
	// The no-selector hint must not suggest piping when stdin looks like a terminal.
	stdoutNoSel, stderrNoSel, codeNoSel := runCmd(t, "", "digest")
	if codeNoSel != 2 || stdoutNoSel != "" || strings.Contains(stderrNoSel, "use - to read note paths from stdin") {
		t.Fatalf("code=%d stdout=%q stderr=%q", codeNoSel, stdoutNoSel, stderrNoSel)
	}
}

func TestDigestInvalidFlags(t *testing.T) {
	longTopic := strings.Repeat("x", 201)
	longTag := strings.Repeat("y", 65)
	var manyTags []string
	for i := 0; i < 17; i++ {
		manyTags = append(manyTags, "-t", "tag"+strconv.Itoa(i))
	}
	cases := [][]string{
		{"digest", "docker", "--no-ai"},
		{"digest", "docker", "--bogus"},
		{"digest", "--notes", "0", "docker"},
		{"digest", "--notes", "65", "docker"},
		{"digest", "--chars", "999", "docker"},
		{"digest", "--chars", "32001", "docker"},
		{"digest", "docker", "--since", "not-a-date"},
		{"digest", "docker", "--until", "not-a-date"},
		{"digest", "docker", "-"},
		{"digest", longTopic},
		{"digest", "docker", "-t", longTag},
	}
	cases = append(cases, append([]string{"digest", "docker"}, manyTags...))
	for _, args := range cases {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			f := newDigestFixture(t)
			stdout, stderr, code := runCmd(t, "ignored", args...)
			if code != 2 || stdout != "" || stderr == "" {
				t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout, stderr)
			}
			if !f.modelNeverRan(t) {
				t.Fatal("model ran for invalid input")
			}
		})
	}
}

func TestDigestUntilBeforeSince(t *testing.T) {
	f := newDigestFixture(t)
	stdout, stderr, code := runCmd(t, "", "digest", "docker", "--since", "2026-09-10", "--until", "2026-09-01")
	if code != 2 || stdout != "" || !strings.Contains(stderr, "nn: digest:") {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	if !f.modelNeverRan(t) {
		t.Fatal("model ran despite until before since")
	}
}

func TestDigestHeaderDateVariants(t *testing.T) {
	f := newDigestFixture(t)
	writeNote(t, f.root, "notes/dated.md", "---\ndate: 2026-09-17\n---\n# Dated docker note\n\nSee docker notes. #devops\n")
	stdout, stderr, code := runCmd(t, "", "digest", "docker", "--since", "2026-09-01", "--until", "2026-09-30")
	if code != 0 {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	if !strings.Contains(stdout, "selection: since 2026-09-01; until 2026-09-30; topic") {
		t.Fatalf("literal since/until dates not shown as given: %q", stdout)
	}

	stdout, stderr, code = runCmd(t, "", "digest", "docker", "--since", "7d")
	if code != 0 {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	// A relative --since is never exactly midnight, so the header must show the time too.
	if !regexp.MustCompile(`selection: since \d{4}-\d{2}-\d{2} \d{2}:\d{2}; topic`).MatchString(stdout) {
		t.Fatalf("relative since not shown as the exact instant: %q", stdout)
	}
}

func TestDigestImagesOnlyListDisclosesSkip(t *testing.T) {
	f := newDigestFixture(t)
	writeNote(t, f.root, "img/pic.png", "\x89PNG\r\n\x1a\nnot a full image")
	stdout, stderr, code := runCmd(t, "img/pic.png\n", "digest", "-")
	want := "nn: digest: 1 listed path is not a note and was skipped\nnn: digest: no notes match the selection\n"
	if code != 1 || stdout != "" || stderr != want {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	if !f.modelNeverRan(t) {
		t.Fatal("model ran for an images-only list")
	}
}

func TestDigestLayoutRetryNoted(t *testing.T) {
	newDigestFixture(t)
	// "docker" typed in the Russian keyboard layout.
	stdout, stderr, code := runCmd(t, "", "digest", "вщслук")
	if code != 0 || !strings.Contains(stderr, `no results for "`) || !strings.Contains(stderr, `used "docker"`) || !strings.Contains(stdout, "notes/docker.md") {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
}

func TestDigestCancelDuringModelCall(t *testing.T) {
	f := newDigestFixture(t)
	f.write(t, "wait", "yes")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var stdout, stderr strings.Builder
	done := make(chan int, 1)
	go func() { done <- runText(ctx, []string{"digest", "docker"}, strings.NewReader(""), &stdout, &stderr) }()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(filepath.Join(f.dir, "args-1")); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("fake model never started")
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	if code := <-done; code != 130 || stdout.Len() != 0 || stderr.String() != "nn: digest: cancelled\n" {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
}

func TestDigestParseUntilIsNextLocalMidnight(t *testing.T) {
	loc := time.FixedZone("UTC+3", 3*3600)
	now := time.Date(2026, 9, 24, 15, 4, 0, 0, loc)
	got, err := parseDigestUntil("2026-09-30", now)
	if err != nil || !got.Equal(time.Date(2026, 10, 1, 0, 0, 0, 0, loc)) || got.Location() != loc {
		t.Fatalf("got %v, %v", got, err)
	}
}

func TestDigestEmptySelectionAsksNothing(t *testing.T) {
	f := newDigestFixture(t)
	writeConfig(t, strings.Replace(f.cfg, `digest-choice="always"`, `digest-choice="ask"`, 1))
	asker := &scriptedAsker{interactive: true, answers: []string{"y"}}
	digestAsker = func(context.Context) ai.Asker { return asker }
	stdout, stderr, code := runCmd(t, "", "digest", "zzunknownzz")
	if code != 1 || stdout != "" || len(asker.questions) != 0 || !f.modelNeverRan(t) {
		t.Fatalf("code=%d stdout=%q stderr=%q questions=%q", code, stdout, stderr, asker.questions)
	}
}

func TestDigestConsentRefusals(t *testing.T) {
	for _, tc := range []struct{ consent, want string }{
		{"never", "is never"},
		{"ask", "AI needs consent"},
	} {
		t.Run(tc.consent, func(t *testing.T) {
			f := newDigestFixture(t)
			writeConfig(t, strings.Replace(f.cfg, `digest-choice="always"`, `digest-choice="`+tc.consent+`"`, 1))
			stdout, stderr, code := runCmd(t, "", "digest", "docker")
			if code != 2 || stdout != "" || !strings.Contains(stderr, tc.want) {
				t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout, stderr)
			}
			if !f.modelNeverRan(t) {
				t.Fatal("model ran without consent")
			}
		})
	}
}

func TestDigestSecretGateThroughCLI(t *testing.T) {
	f := newDigestFixture(t)
	token := "ghp_" + "abcdefghijklmnopqrstuvwxyz0123456789ABCD"
	writeNote(t, f.root, "notes/creds.md", "# Deploy creds\n\ndocker deploy uses "+token+"\n")

	stdout, stderr, code := runCmd(t, "", "digest", "docker")
	if code != 0 || !strings.Contains(stdout, "\nNot included: 1 note that may contain credentials (--allow-secret includes them).\n") {
		t.Fatalf("gate: code=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	if req := f.read(t, "request-1"); strings.Contains(req, token) || strings.Contains(req, "notes/creds.md") || strings.Contains(stdout+stderr, token) {
		t.Fatal("secret-looking note reached the model or the output")
	}

	stdout, _, code = runCmd(t, "", "digest", "docker", "--allow-secret")
	if code != 0 || strings.Contains(stdout, "may contain credentials") || !strings.Contains(f.read(t, "request-2"), token) {
		t.Fatalf("--allow-secret: code=%d stdout=%q", code, stdout)
	}

	stdout, stderr, code = runCmd(t, "notes/creds.md\n", "digest", "-")
	if code != 2 || stdout != "" || stderr != "nn: digest: no matching note can be sent: 1 may contain credentials (review them and use --allow-secret)\n" || f.read(t, "calls") != "2\n" {
		t.Fatalf("all secret: code=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
}

func TestDigestBudgetFlagsThroughCLI(t *testing.T) {
	f := newDigestFixture(t)
	writeNote(t, f.root, "notes/docker2.md", "# Docker two\n\ndocker compose notes\n")
	stdout, _, code := runCmd(t, "", "digest", "docker", "--notes", "1")
	if code != 0 || !strings.Contains(stdout, "\nNot included: 1 matching note beyond the limit of 1 note / 12000 characters.\n") {
		t.Fatalf("--notes 1: code=%d stdout=%q", code, stdout)
	}
	writeConfig(t, f.cfg+"\n[ai.context]\nnotes=1000\nchars=50000\n")
	for i := 0; i < 64; i++ {
		writeNote(t, f.root, fmt.Sprintf("bulk/d%02d.md", i), "docker bulk note\n")
	}
	stdout, _, code = runCmd(t, "", "digest", "docker")
	if code != 0 || !strings.Contains(stdout, "Digest of 64 notes") || !strings.Contains(stdout, "\nNot included: 2 matching notes beyond the limit of 64 notes / 32000 characters.\n") {
		t.Fatalf("clamp: code=%d stdout=%q", code, stdout)
	}
}

func TestDigestConsentQuestionNamesWhatIsSent(t *testing.T) {
	f := newDigestFixture(t)
	writeConfig(t, strings.Replace(f.cfg, `digest-choice="always"`, `digest-choice="ask"`, 1))
	asker := &scriptedAsker{interactive: true, answers: []string{"once"}}
	digestAsker = func(context.Context) ai.Asker { return asker }
	stdout, stderr, code := runCmd(t, "", "digest", "docker")
	if code != 0 || len(asker.questions) != 1 || !strings.Contains(asker.questions[0], "the selection and the paths, titles, dates, tags and excerpts of 1 selected note (") || !f.modelRanOnce(t) {
		t.Fatalf("code=%d stdout=%q stderr=%q questions=%q", code, stdout, stderr, asker.questions)
	}
}

func TestDigestDashAfterDoubleDashRefusedWhenPiped(t *testing.T) {
	// A lone "-" after "--" is TOPIC text, but silently ignoring a piped list for it is refused instead.
	f := newDigestFixture(t)
	stdout, stderr, code := runCmd(t, "a.md\n", "digest", "--", "-")
	if code != 2 || stdout != "" || !strings.Contains(stderr, `a lone "-" after "--" is topic text`) {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	if !f.modelNeverRan(t) {
		t.Fatal("model ran for a piped list silently turned into a topic search")
	}
}

func TestDigestProtocolFailureEmptyStdout(t *testing.T) {
	f := newDigestFixture(t)
	f.write(t, "answer", strings.Replace(digestTestAnswer, "S1", "S999", 1))
	stdout, stderr, code := runCmd(t, "", "digest", "docker")
	if code != 2 || stdout != "" || stderr == "" {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
}

func TestDigestTimeoutLeavesStdoutEmpty(t *testing.T) {
	f := newDigestFixture(t)
	f.write(t, "wait", "yes")
	writeConfig(t, strings.Replace(f.cfg, "[ai.profiles.digest-choice]\n", "[ai.profiles.digest-choice]\ntimeout=\"50ms\"\n", 1))
	stdout, stderr, code := runCmd(t, "", "digest", "docker")
	if code != 2 || stdout != "" || stderr == "" {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
}

func TestDigestCancellation(t *testing.T) {
	newDigestFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var stdout, stderr strings.Builder
	code := runText(ctx, []string{"digest", "docker"}, strings.NewReader(""), &stdout, &stderr)
	if code != 130 || stdout.Len() != 0 {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
}

func TestDigestConfigLimitsAboveCapsAreClamped(t *testing.T) {
	f := newDigestFixture(t)
	writeConfig(t, f.cfg+"\n[ai.context]\nnotes=1000\nchars=50000\n")
	stdout, stderr, code := runCmd(t, "", "digest", "docker")
	if code != 0 || strings.Contains(stderr, "invalid digest limits") {
		t.Fatalf("config limits above the caps were not clamped: code=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
}

func TestDigestSaveCreatesNoteWithVerifiedWikilink(t *testing.T) {
	f := newDigestFixture(t)
	cfg := strings.Replace(f.cfg, `post_save="touch MUST_NOT_RUN"`, `post_save="printf 'hook\\n' >> \"$NN_DIGEST_FAKE/hooks\""`, 1)
	writeConfig(t, cfg)
	stdout, stderr, code := runCmd(t, "", "digest", "docker", "-t", "devops", "--save")
	if code != 0 || !strings.Contains(stdout, "Digest of") {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	lines := strings.Split(strings.TrimRight(stderr, "\n"), "\n")
	savedPath := lines[len(lines)-1]
	if !strings.HasPrefix(savedPath, "nn/") || !strings.HasSuffix(savedPath, ".md") {
		t.Fatalf("unexpected saved path on stderr: %q (stderr=%q)", savedPath, stderr)
	}
	data, err := os.ReadFile(filepath.Join(f.root, filepath.FromSlash(savedPath)))
	if err != nil {
		t.Fatalf("read saved note: %v", err)
	}
	body := string(data)
	if !strings.Contains(body, "via: digest") {
		t.Fatalf("saved note missing via: digest frontmatter: %q", body)
	}
	if !strings.Contains(body, "[[./") && !strings.Contains(body, "[[../") {
		t.Fatalf("saved note has no relative wikilink to a source: %q", body)
	}
	if got := f.read(t, "hooks"); got != "hook\n" {
		t.Fatalf("PostSave ran %q times, want exactly once", got)
	}
}

func TestDigestSaveFailureKeepsStdout(t *testing.T) {
	f := newDigestFixture(t)
	// Block the inbox directory with a plain file, so EnsureDir fails to create it.
	if err := os.WriteFile(filepath.Join(f.root, "nn"), []byte("blocker"), 0o644); err != nil {
		t.Fatal(err)
	}
	stdout, stderr, code := runCmd(t, "", "digest", "docker", "--save")
	if code != 2 || !strings.Contains(stdout, "Digest of") || !strings.Contains(stderr, "save digest") {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
}

func TestDigestWithoutSaveLeavesVaultUnchanged(t *testing.T) {
	newDigestFixture(t)
	watched := []string{os.Getenv("NN_ROOT"), os.Getenv("XDG_DATA_HOME"), os.Getenv("XDG_CACHE_HOME"), os.Getenv("XDG_STATE_HOME")}
	before := make([]map[string]string, len(watched))
	for i, root := range watched {
		before[i] = askTree(t, root)
	}
	configBefore, _ := os.ReadFile(os.Getenv("NN_CONFIG"))
	stdout, stderr, code := runCmd(t, "", "digest", "docker", "-t", "devops", "--since", "7d")
	if code != 0 || !strings.Contains(stdout, "Digest of") {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	for i, root := range watched {
		if after := askTree(t, root); !reflect.DeepEqual(before[i], after) {
			t.Errorf("digest without --save changed %s: before=%v after=%v", root, before[i], after)
		}
	}
	configAfter, _ := os.ReadFile(os.Getenv("NN_CONFIG"))
	if string(configBefore) != string(configAfter) {
		t.Fatal("digest changed config without a consent change")
	}
}

func TestDigestBackgroundModeStillWaits(t *testing.T) {
	// The fixture sets ai.mode = "background"; synchronous stdout below proves digest ignores it.
	f := newDigestFixture(t)
	stdout, stderr, code := runCmd(t, "", "digest", "docker")
	if code != 0 || !strings.Contains(stdout, "Digest of 1 note") {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	if !f.modelRanOnce(t) {
		t.Fatal("expected exactly one synchronous model call")
	}
}
