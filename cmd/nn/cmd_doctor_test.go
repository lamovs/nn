package main

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"

	"github.com/lamovs/nn/internal/config"
	"github.com/lamovs/nn/internal/platform"
	"github.com/lamovs/nn/internal/state"
)

func withFakePlatformChecks(t *testing.T, checks []platform.Check) {
	t.Helper()
	prev := platformChecks
	platformChecks = func(ctx context.Context, cfg config.Config) []platform.Check { return checks }
	t.Cleanup(func() { platformChecks = prev })
}

var healthyPlatformChecks = []platform.Check{
	{Name: "OCR (fake)", OK: true, Detail: "languages eng"},
	{Name: "screenshot", OK: true, Detail: "fake"},
	{Name: "clipboard", OK: true, Detail: "fake"},
}

func newDoctorTestVault(t *testing.T) string {
	t.Helper()
	root := newTestVault(t)
	writeConfig(t, "[ai.consent]\nclaude = \"never\"\n")
	return root
}

func TestDoctorHealthyVaultExitsZero(t *testing.T) {
	newDoctorTestVault(t)
	withFakePlatformChecks(t, healthyPlatformChecks)

	stdout, stderr, code := runCmd(t, "", "doctor")
	if code != 0 {
		t.Fatalf("code = %d, stdout = %q, stderr = %q", code, stdout, stderr)
	}
	if !strings.Contains(stdout, "[ok] config:") {
		t.Errorf("stdout = %q, want the config check to pass", stdout)
	}
}

func TestDoctorLockFileIsNotAProblem(t *testing.T) {
	newDoctorTestVault(t)
	withFakePlatformChecks(t, healthyPlatformChecks)
	statePath, err := state.Path()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(statePath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(statePath+".lock", nil, 0o644); err != nil {
		t.Fatal(err)
	}

	_, stderr, code := runCmd(t, "", "doctor")
	if code != 0 {
		t.Errorf("code = %d, want 0: a state.json.lock file must not be a problem, stderr = %q", code, stderr)
	}
}

func TestDoctorMissingRootIsAProblem(t *testing.T) {
	t.Setenv("NN_ROOT", "")
	t.Setenv("NN_CONFIG", filepath.Join(t.TempDir(), "config.toml"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(t.TempDir(), "xdg-config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(t.TempDir(), "xdg-data"))
	withFakePlatformChecks(t, healthyPlatformChecks)

	stdout, _, code := runCmd(t, "", "doctor")
	if code != 1 {
		t.Fatalf("code = %d, want 1, stdout = %q", code, stdout)
	}
	if !strings.Contains(stdout, "[!!] config:") {
		t.Errorf("stdout = %q, want the config check to fail", stdout)
	}
}

func TestDoctorPlatformProblemSetsExitCodeOne(t *testing.T) {
	newDoctorTestVault(t)
	withFakePlatformChecks(t, []platform.Check{{Name: "OCR (fake)", OK: false, Detail: "not installed"}})

	_, _, code := runCmd(t, "", "doctor")
	if code != 1 {
		t.Errorf("code = %d, want 1", code)
	}
}

func TestDoctorHeadlessLinuxScreenshotAndClipboardAreInformational(t *testing.T) {
	newDoctorTestVault(t)
	t.Setenv("DISPLAY", "")
	t.Setenv("WAYLAND_DISPLAY", "")
	withFakePlatformChecks(t, []platform.Check{
		{Name: "screenshot", OK: false, Detail: "no graphical session"},
		{Name: "clipboard", OK: false, Detail: "no graphical session"},
	})

	_, _, code := runCmd(t, "", "doctor")
	want := 0
	if runtime.GOOS != "linux" {
		want = 1
	}
	if code != want {
		t.Errorf("code = %d, want %d (GOOS=%s)", code, want, runtime.GOOS)
	}
}

func TestDoctorFixCreatesInboxDirectoriesAndTemplate(t *testing.T) {
	root := newDoctorTestVault(t)
	withFakePlatformChecks(t, healthyPlatformChecks)

	_, stderr, code := runCmd(t, "", "doctor", "--fix")
	if code != 0 {
		t.Fatalf("code = %d, stderr = %q", code, stderr)
	}
	for _, dir := range []string{"nn", "nn/assets", "nn/_templates"} {
		if info, err := os.Stat(filepath.Join(root, dir)); err != nil || !info.IsDir() {
			t.Errorf("%s was not created: %v", dir, err)
		}
	}
	if _, err := os.Stat(filepath.Join(root, "nn", "_templates", "hotkey.md")); err != nil {
		t.Errorf("hotkey.md template was not created: %v", err)
	}
}

func TestDoctorFixDoesNotClobberExistingTemplate(t *testing.T) {
	root := newDoctorTestVault(t)
	withFakePlatformChecks(t, healthyPlatformChecks)
	tmplPath := filepath.Join(root, "nn", "_templates", "hotkey.md")
	if err := os.MkdirAll(filepath.Dir(tmplPath), 0o755); err != nil {
		t.Fatal(err)
	}
	custom := "custom content\n"
	if err := os.WriteFile(tmplPath, []byte(custom), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, _, code := runCmd(t, "", "doctor", "--fix"); code != 0 {
		t.Fatalf("code = %d", code)
	}
	data, err := os.ReadFile(tmplPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != custom {
		t.Errorf("template was overwritten: %q", string(data))
	}
}

func linkedInboxVault(t *testing.T) (root, away string) {
	t.Helper()
	root = newDoctorTestVault(t)
	away = t.TempDir()
	if err := os.Symlink(away, filepath.Join(root, "nn")); err != nil {
		t.Fatal(err)
	}
	withFakePlatformChecks(t, healthyPlatformChecks)
	return root, away
}

func TestDoctorReportsAnInboxThatLeavesTheVault(t *testing.T) {
	root, away := linkedInboxVault(t)

	stdout, stderr, code := runCmd(t, "", "doctor")
	if code != 1 {
		t.Fatalf("code = %d, want 1, stdout = %q, stderr = %q", code, stdout, stderr)
	}
	if !strings.Contains(stdout, "[!!] inbox:") || !strings.Contains(stdout, "outside the vault") {
		t.Errorf("stdout = %q, want the inbox check to fail and say the inbox leaves the vault", stdout)
	}
	if !strings.Contains(stdout, filepath.Join(root, "nn")) || !strings.Contains(stdout, away) {
		t.Errorf("stdout = %q, want both the inbox and what it resolves to named", stdout)
	}
	entries, err := os.ReadDir(away)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Errorf("doctor left %d entries outside the vault: %v", len(entries), entries)
	}
}

func TestDoctorFixDoesNotBuildTheInboxOutsideTheVault(t *testing.T) {
	root, away := linkedInboxVault(t)

	stdout, stderr, code := runCmd(t, "", "doctor", "--fix")
	if code != 1 {
		t.Fatalf("code = %d, want 1, stdout = %q, stderr = %q", code, stdout, stderr)
	}
	if !strings.Contains(stdout, "[!!] inbox:") {
		t.Errorf("stdout = %q, want the inbox check to fail", stdout)
	}
	entries, err := os.ReadDir(away)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		names := make([]string, len(entries))
		for i, e := range entries {
			names[i] = e.Name()
		}
		t.Errorf("--fix created %v outside the vault", names)
	}
	if info, err := os.Lstat(filepath.Join(root, "nn")); err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Errorf("the inbox link was replaced: Lstat = %v, %v", info, err)
	}
}

// awayDir resolves symlinks first: on macOS the temp dir itself is reached through one.
func awayDir(t *testing.T) string {
	t.Helper()
	away, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return away
}

// writeConfigWithInbox rewrites NN_CONFIG's file with a custom inbox; call after newTestVault.
func writeConfigWithInbox(t *testing.T, inbox string) {
	t.Helper()
	path := os.Getenv("NN_CONFIG")
	if path == "" {
		t.Fatal("NN_CONFIG is not set; call newTestVault first")
	}
	content := "[vault]\nroot = " + config.TOMLString(os.Getenv("NN_ROOT")) + "\ninbox = " + config.TOMLString(inbox) + "\n[ai.consent]\nclaude = \"never\"\n"
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// ancestorLinkedInboxVault: "a" is a symlink to another disk, the inbox is "a/b/nn".
func ancestorLinkedInboxVault(t *testing.T) (root, away string) {
	t.Helper()
	root = newDoctorTestVault(t)
	away = awayDir(t)
	if err := os.Symlink(away, filepath.Join(root, "a")); err != nil {
		t.Fatal(err)
	}
	writeConfigWithInbox(t, "a/b/nn")
	withFakePlatformChecks(t, healthyPlatformChecks)
	return root, away
}

func TestDoctorReportsAnInboxBehindALinkedAncestor(t *testing.T) {
	root, away := ancestorLinkedInboxVault(t)

	stdout, stderr, code := runCmd(t, "", "doctor")
	if code != 1 {
		t.Fatalf("code = %d, want 1, stdout = %q, stderr = %q", code, stdout, stderr)
	}
	if !strings.Contains(stdout, "[!!] inbox:") || !strings.Contains(stdout, "outside the vault") {
		t.Errorf("stdout = %q, want the inbox check to fail and say the inbox leaves the vault", stdout)
	}
	if strings.Contains(stdout, "created on first capture") {
		t.Errorf("stdout = %q, want no promise to create an inbox outside the vault", stdout)
	}
	if !strings.Contains(stdout, filepath.Join(root, "a", "b", "nn")) || !strings.Contains(stdout, filepath.Join(away, "b", "nn")) {
		t.Errorf("stdout = %q, want both the inbox and what it resolves to named", stdout)
	}
	// The writability probe is skipped too, so nothing lands at the far end either.
	entries, err := os.ReadDir(away)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Errorf("doctor left %d entries outside the vault: %v", len(entries), entries)
	}
}

func TestDoctorFixDoesNotBuildAnInboxBehindALinkedAncestor(t *testing.T) {
	_, away := ancestorLinkedInboxVault(t)

	stdout, stderr, code := runCmd(t, "", "doctor", "--fix")
	if code != 1 {
		t.Fatalf("code = %d, want 1, stdout = %q, stderr = %q", code, stdout, stderr)
	}
	if !strings.Contains(stdout, "[!!] inbox:") {
		t.Errorf("stdout = %q, want the inbox check to fail", stdout)
	}
	entries, err := os.ReadDir(away)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		names := make([]string, len(entries))
		for i, e := range entries {
			names[i] = e.Name()
		}
		t.Errorf("--fix created %v outside the vault", names)
	}
}

// Unlike the two tests above, the link sits directly above the inbox.
func TestDoctorFixIsNotRunForAnInboxThatLeavesTheVault(t *testing.T) {
	root := newDoctorTestVault(t)
	away := awayDir(t)
	if err := os.Symlink(away, filepath.Join(root, "nn")); err != nil {
		t.Fatal(err)
	}
	writeConfigWithInbox(t, "nn/inbox")
	withFakePlatformChecks(t, healthyPlatformChecks)

	stdout, stderr, code := runCmd(t, "", "doctor", "--fix")
	if code != 1 {
		t.Fatalf("code = %d, want 1, stdout = %q, stderr = %q", code, stdout, stderr)
	}
	if !strings.Contains(stdout, "[!!] inbox:") {
		t.Errorf("stdout = %q, want the inbox check to fail", stdout)
	}
	if strings.Contains(stdout, "[!!] --fix") {
		t.Errorf("stdout = %q, want no --fix row: --fix is not started for an inbox that leads out of the vault", stdout)
	}
	entries, err := os.ReadDir(away)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		names := make([]string, len(entries))
		for i, e := range entries {
			names[i] = e.Name()
		}
		t.Errorf("--fix created %v outside the vault", names)
	}
}

// A directory nn cannot search gets a permission error, not an outside-the-vault claim.
func TestDoctorDoesNotCallAnInboxItCannotFollowOutsideTheVault(t *testing.T) {
	root := newDoctorTestVault(t)
	locked := filepath.Join(root, "locked")
	if err := os.Mkdir(locked, 0o755); err != nil {
		t.Fatal(err)
	}
	writeConfigWithInbox(t, "locked/nn")
	withFakePlatformChecks(t, healthyPlatformChecks)
	if err := os.Chmod(locked, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(locked, 0o755) })
	if _, err := os.Lstat(filepath.Join(locked, "nn")); !errors.Is(err, fs.ErrPermission) {
		t.Skipf("a directory with mode 000 can still be searched here (Lstat under it = %v): there is no inbox nn cannot follow to report", err)
	}

	stdout, stderr, code := runCmd(t, "", "doctor", "--fix")
	if code != 1 {
		t.Fatalf("code = %d, want 1, stdout = %q, stderr = %q", code, stdout, stderr)
	}
	if !strings.Contains(stdout, "[!!] inbox:") || !strings.Contains(stdout, "permission denied") {
		t.Errorf("stdout = %q, want the inbox check to fail with the permission problem", stdout)
	}
	if !strings.Contains(stdout, filepath.Join(root, "locked", "nn")) {
		t.Errorf("stdout = %q, want the inbox named", stdout)
	}
	if strings.Contains(stdout, "outside the vault") {
		t.Errorf("stdout = %q, want no claim that an inbox nn could not follow leaves the vault", stdout)
	}
	if strings.Contains(stdout, "[!!] --fix") {
		t.Errorf("stdout = %q, want no --fix row: --fix is not started for an inbox nn cannot follow", stdout)
	}
	if err := os.Chmod(locked, 0o755); err != nil {
		t.Fatal(err)
	}
	if entries, err := os.ReadDir(locked); err != nil || len(entries) != 0 {
		t.Errorf("the locked directory holds %v (%v), want nothing created in it", entries, err)
	}
}

func TestDoctorReportsAnInboxThatLoops(t *testing.T) {
	root := newDoctorTestVault(t)
	withFakePlatformChecks(t, healthyPlatformChecks)
	inbox := filepath.Join(root, "nn")
	if err := os.Symlink(inbox, inbox); err != nil {
		t.Fatal(err)
	}

	stdout, stderr, code := runCmd(t, "", "doctor", "--fix")
	if code != 1 {
		t.Fatalf("code = %d, want 1, stdout = %q, stderr = %q", code, stdout, stderr)
	}
	if want := "[!!] inbox: cannot tell where " + inbox + " leads"; !strings.Contains(stdout, want) {
		t.Errorf("stdout = %q, want the inbox row to say %q", stdout, want)
	}
	if strings.Contains(stdout, "outside the vault") {
		t.Errorf("stdout = %q, want no claim that an inbox nn could not follow leaves the vault", stdout)
	}
	if strings.Contains(stdout, "[!!] --fix") {
		t.Errorf("stdout = %q, want no --fix row: --fix is not started for an inbox nn cannot follow", stdout)
	}
	for _, sub := range []string{"assets", "_templates"} {
		if strings.Contains(stdout, "inbox/"+sub) {
			t.Errorf("stdout = %q, want no row for %s under an inbox that has one of its own", stdout, sub)
		}
	}
	if dest, err := os.Readlink(inbox); err != nil || dest != inbox {
		t.Errorf("the inbox link now reads %q (%v), want it left as it was", dest, err)
	}
}

func TestDoctorReportsAnInboxThatIsAFileOnce(t *testing.T) {
	root := newDoctorTestVault(t)
	withFakePlatformChecks(t, healthyPlatformChecks)
	if err := os.WriteFile(filepath.Join(root, "nn"), nil, 0o644); err != nil {
		t.Fatal(err)
	}

	stdout, stderr, code := runCmd(t, "", "doctor")
	if code != 1 {
		t.Fatalf("code = %d, want 1, stdout = %q, stderr = %q", code, stdout, stderr)
	}
	var rows []string
	for _, line := range strings.Split(stdout, "\n") {
		if strings.HasPrefix(line, "[!!] inbox") {
			rows = append(rows, line)
		}
	}
	if len(rows) != 1 || !strings.HasPrefix(rows[0], "[!!] inbox: ") {
		t.Errorf("inbox rows = %q, want the one row for the inbox itself", rows)
	}
	for _, sub := range []string{"assets", "_templates"} {
		if strings.Contains(stdout, "inbox/"+sub) {
			t.Errorf("stdout = %q, want no row for %s under an inbox that is a file", stdout, sub)
		}
	}
}

func TestDoctorReportsAnInboxSubdirectoryItCannotFollow(t *testing.T) {
	root := newDoctorTestVault(t)
	withFakePlatformChecks(t, healthyPlatformChecks)
	assets := filepath.Join(root, "nn", "assets")
	if err := os.MkdirAll(filepath.Dir(assets), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(assets, assets); err != nil {
		t.Fatal(err)
	}

	stdout, stderr, code := runCmd(t, "", "doctor")
	if code != 1 {
		t.Fatalf("code = %d, want 1, stdout = %q, stderr = %q", code, stdout, stderr)
	}
	if !strings.Contains(stdout, "[!!] inbox/assets:") || !strings.Contains(stdout, assets) {
		t.Errorf("stdout = %q, want a failing check that names the assets folder", stdout)
	}
	if strings.Contains(stdout, "outside the vault") {
		t.Errorf("stdout = %q, want no claim that a folder nn could not follow leaves the vault", stdout)
	}
	if !strings.Contains(stdout, "[ok] inbox: ") {
		t.Errorf("stdout = %q, want the inbox check itself to pass", stdout)
	}
}

func TestDoctorFixStopsAtTheFirstRefusal(t *testing.T) {
	root, _ := linkedSubdirVault(t, "assets")

	if _, stderr, code := runCmd(t, "", "doctor", "--fix"); code != 1 {
		t.Fatalf("code = %d, want 1, stderr = %q", code, stderr)
	}
	for _, rel := range []string{"nn/_templates", "nn/_templates/hotkey.md"} {
		if _, err := os.Lstat(filepath.Join(root, filepath.FromSlash(rel))); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("%s was created after the run stopped at assets: Lstat = %v", rel, err)
		}
	}
}

func TestDoctorFixDoesNotFollowADanglingTemplateLink(t *testing.T) {
	root := newDoctorTestVault(t)
	withFakePlatformChecks(t, healthyPlatformChecks)
	templates := filepath.Join(root, "nn", "_templates")
	if err := os.MkdirAll(templates, 0o755); err != nil {
		t.Fatal(err)
	}
	stolen := filepath.Join(t.TempDir(), "stolen.md")
	link := filepath.Join(templates, "hotkey.md")
	if err := os.Symlink(stolen, link); err != nil {
		t.Fatal(err)
	}

	_, stderr, code := runCmd(t, "", "doctor", "--fix")
	if code != 0 {
		t.Fatalf("code = %d, want 0, stderr = %q", code, stderr)
	}
	if _, err := os.Lstat(stolen); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("the template was written through the link, to %s (Lstat = %v)", stolen, err)
	}
	if dest, err := os.Readlink(link); err != nil || dest != stolen {
		t.Errorf("the link now points at %q (%v), want %q", dest, err, stolen)
	}
	// Nothing else landed either, a temp file from the write included.
	entries, err := os.ReadDir(templates)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "hotkey.md" {
		t.Errorf("_templates holds %v, want just the link the user put there", entries)
	}
}

// A second --fix must leave an already-edited template alone (same inode), not overwrite it.
func TestDoctorFixRepeatsWithoutRewritingTheTemplate(t *testing.T) {
	root := newDoctorTestVault(t)
	withFakePlatformChecks(t, healthyPlatformChecks)

	if _, stderr, code := runCmd(t, "", "doctor", "--fix"); code != 0 {
		t.Fatalf("first --fix: code = %d, stderr = %q", code, stderr)
	}
	tmpl := filepath.Join(root, "nn", "_templates", "hotkey.md")
	first, err := os.Stat(tmpl)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(tmpl)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != hotkeyTemplate {
		t.Errorf("template =\n%s\nwant the starter template\n%s", data, hotkeyTemplate)
	}

	if _, stderr, code := runCmd(t, "", "doctor", "--fix"); code != 0 {
		t.Fatalf("second --fix: code = %d, stderr = %q", code, stderr)
	}
	second, err := os.Stat(tmpl)
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(first, second) {
		t.Error("the second --fix replaced the template with a new file")
	}
	for _, dir := range []string{"nn", "nn/assets", "nn/_templates"} {
		if info, err := os.Stat(filepath.Join(root, filepath.FromSlash(dir))); err != nil || !info.IsDir() {
			t.Errorf("%s is gone after the second --fix: %v", dir, err)
		}
	}
}

func linkedSubdirVault(t *testing.T, sub string) (root, away string) {
	t.Helper()
	root = newDoctorTestVault(t)
	if err := os.MkdirAll(filepath.Join(root, "nn"), 0o755); err != nil {
		t.Fatal(err)
	}
	away = t.TempDir()
	if err := os.Symlink(away, filepath.Join(root, "nn", sub)); err != nil {
		t.Fatal(err)
	}
	withFakePlatformChecks(t, healthyPlatformChecks)
	return root, away
}

func TestDoctorReportsInboxSubdirectoriesThatLeaveTheVault(t *testing.T) {
	for _, sub := range []string{"assets", "_templates"} {
		t.Run(sub, func(t *testing.T) {
			root, away := linkedSubdirVault(t, sub)

			stdout, stderr, code := runCmd(t, "", "doctor")
			if code != 1 {
				t.Fatalf("code = %d, want 1, stdout = %q, stderr = %q", code, stdout, stderr)
			}
			if !strings.Contains(stdout, "[!!] inbox/"+sub+":") {
				t.Errorf("stdout = %q, want a failing check for the %s directory", stdout, sub)
			}
			if !strings.Contains(stdout, filepath.Join(root, "nn", sub)) || !strings.Contains(stdout, away) {
				t.Errorf("stdout = %q, want both %s and what it resolves to named", stdout, sub)
			}
			if !strings.Contains(stdout, "outside the vault") {
				t.Errorf("stdout = %q, want the report to say why that is a problem", stdout)
			}
			if !strings.Contains(stdout, "[ok] inbox: ") {
				t.Errorf("stdout = %q, want the inbox check itself to pass", stdout)
			}
		})
	}
}

func TestDoctorKeepsOneRowPerInboxSubdirectoryProblem(t *testing.T) {
	newDoctorTestVault(t)
	withFakePlatformChecks(t, healthyPlatformChecks)

	if _, _, code := runCmd(t, "", "doctor", "--fix"); code != 0 {
		t.Fatalf("--fix on a healthy vault: code = %d", code)
	}
	stdout, _, code := runCmd(t, "", "doctor")
	if code != 0 {
		t.Fatalf("code = %d, want 0, stdout = %q", code, stdout)
	}
	for _, sub := range []string{"assets", "_templates"} {
		if strings.Contains(stdout, "inbox/"+sub) {
			t.Errorf("stdout = %q, want no row for %s on a healthy vault", stdout, sub)
		}
	}
}

func danglingInboxVault(t *testing.T) (root, target string) {
	t.Helper()
	root = newDoctorTestVault(t)
	target = filepath.Join(t.TempDir(), "gone")
	if err := os.Symlink(target, filepath.Join(root, "nn")); err != nil {
		t.Fatal(err)
	}
	withFakePlatformChecks(t, healthyPlatformChecks)
	return root, target
}

func TestDoctorReportsADanglingInboxLinkInsteadOfPromisingToCreateIt(t *testing.T) {
	root, target := danglingInboxVault(t)

	stdout, stderr, code := runCmd(t, "", "doctor")
	if code != 1 {
		t.Fatalf("code = %d, want 1, stdout = %q, stderr = %q", code, stdout, stderr)
	}
	if !strings.Contains(stdout, "[!!] inbox:") {
		t.Errorf("stdout = %q, want the inbox check to fail", stdout)
	}
	if strings.Contains(stdout, "created on first capture") {
		t.Errorf("stdout = %q, want no promise that the inbox will be created", stdout)
	}
	if !strings.Contains(stdout, "symlink") || !strings.Contains(stdout, target) {
		t.Errorf("stdout = %q, want the link and where it points named", stdout)
	}
	if !strings.Contains(stdout, "remove "+filepath.Join(root, "nn")) {
		t.Errorf("stdout = %q, want a fix the user can carry out", stdout)
	}
}

// The state directory has no vault boundary, so it keeps the offer regardless.
func TestDoctorOffersToCreateALinkTargetOnlyInsideTheVault(t *testing.T) {
	for _, tc := range []struct {
		name string
		// target is what the link at <root>/nn points at.
		target func(t *testing.T, root string) string
		offer  bool
	}{
		{"a directory outside the vault", func(t *testing.T, root string) string { return filepath.Join(t.TempDir(), "gone") }, false},
		{"a relative path that climbs out of the vault", func(t *testing.T, root string) string { return filepath.Join("..", "gone") }, false},
		{"a directory inside the vault", func(t *testing.T, root string) string { return filepath.Join(root, "shelf", "inbox") }, true},
		{"a relative path inside the vault", func(t *testing.T, root string) string { return filepath.Join("shelf", "inbox") }, true},
	} {
		t.Run("an inbox link to "+tc.name, func(t *testing.T) {
			root := newDoctorTestVault(t)
			withFakePlatformChecks(t, healthyPlatformChecks)
			target := tc.target(t, root)
			if err := os.Symlink(target, filepath.Join(root, "nn")); err != nil {
				t.Fatal(err)
			}

			stdout, stderr, code := runCmd(t, "", "doctor")
			if code != 1 {
				t.Fatalf("code = %d, want 1, stdout = %q, stderr = %q", code, stdout, stderr)
			}
			if !strings.Contains(stdout, "[!!] inbox:") || !strings.Contains(stdout, "remove "+filepath.Join(root, "nn")) {
				t.Errorf("stdout = %q, want the inbox row and its fix of removing the link", stdout)
			}
			if got := strings.Contains(stdout, "or create "+target); got != tc.offer {
				t.Errorf("stdout = %q, offers to create %s = %v, want %v", stdout, target, got, tc.offer)
			}
		})
	}

	t.Run("the state directory", func(t *testing.T) {
		newDoctorTestVault(t)
		withFakePlatformChecks(t, healthyPlatformChecks)
		statePath, err := state.Path()
		if err != nil {
			t.Fatal(err)
		}
		dir := filepath.Dir(statePath)
		if err := os.MkdirAll(filepath.Dir(dir), 0o755); err != nil {
			t.Fatal(err)
		}
		target := filepath.Join(t.TempDir(), "gone")
		if err := os.Symlink(target, dir); err != nil {
			t.Fatal(err)
		}

		stdout, stderr, code := runCmd(t, "", "doctor")
		if code != 1 {
			t.Fatalf("code = %d, want 1, stdout = %q, stderr = %q", code, stdout, stderr)
		}
		if !strings.Contains(stdout, "[!!] state directory:") || !strings.Contains(stdout, "or create "+target) {
			t.Errorf("stdout = %q, want the state directory row to keep offering to create %s", stdout, target)
		}
	})
}

func TestDoctorReportsAFixThatCouldNotFinish(t *testing.T) {
	t.Run("refused by the boundary", func(t *testing.T) {
		_, away := linkedSubdirVault(t, "assets")

		stdout, stderr, code := runCmd(t, "", "doctor", "--fix")
		if code != 1 {
			t.Fatalf("code = %d, want 1, stdout = %q, stderr = %q", code, stdout, stderr)
		}
		if !strings.Contains(stdout, "[!!] --fix:") {
			t.Errorf("stdout = %q, want the failed --fix in the report", stdout)
		}
		entries, err := os.ReadDir(away)
		if err != nil {
			t.Fatal(err)
		}
		if len(entries) != 0 {
			t.Errorf("--fix created %d entries outside the vault", len(entries))
		}
	})

	t.Run("blocked by what is already there", func(t *testing.T) {
		danglingInboxVault(t)

		stdout, stderr, code := runCmd(t, "", "doctor", "--fix")
		if code != 1 {
			t.Fatalf("code = %d, want 1, stdout = %q, stderr = %q", code, stdout, stderr)
		}
		if !strings.Contains(stdout, "[!!] --fix:") {
			t.Errorf("stdout = %q, want the failed --fix in the report", stdout)
		}
	})
}

func TestDoctorJSONOutput(t *testing.T) {
	newDoctorTestVault(t)
	withFakePlatformChecks(t, healthyPlatformChecks)

	stdout, stderr, code := runCmd(t, "", "doctor", "--json")
	if code != 0 {
		t.Fatalf("code = %d, stderr = %q", code, stderr)
	}
	if !strings.HasPrefix(strings.TrimSpace(stdout), "[") {
		t.Errorf("stdout = %q, want a JSON array", stdout)
	}
}

func TestDoctorJSONKeysAreLowercase(t *testing.T) {
	newDoctorTestVault(t)
	withFakePlatformChecks(t, []platform.Check{
		{Name: "OCR (fake)", OK: false, Detail: "not installed", Fix: []string{"brew install tesseract"}},
	})

	stdout, _, code := runCmd(t, "", "doctor", "--json")
	if code != 1 {
		t.Fatalf("code = %d, want 1", code)
	}
	var rows []map[string]any
	if err := json.Unmarshal([]byte(stdout), &rows); err != nil {
		t.Fatalf("decode: %v\noutput: %s", err, stdout)
	}
	if len(rows) == 0 {
		t.Fatal("no rows")
	}
	for _, key := range []string{"name", "ok", "detail", "fix"} {
		if _, present := rows[0][key]; !present {
			t.Errorf("row keys = %v, want a lowercase %q", keysOf(rows[0]), key)
		}
	}

	failing := 0
	for _, r := range rows {
		if ok, _ := r["ok"].(bool); !ok {
			failing++
			if r["fix"] == nil {
				t.Errorf("row %v has a null fix; want an array so .fix[] works", r["name"])
			}
		}
	}
	if failing != 1 {
		t.Errorf("rows with ok == false: %d, want 1", failing)
	}
}

func keysOf(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func TestDoctorRejectsUnknownOption(t *testing.T) {
	newDoctorTestVault(t)
	_, stderr, code := runCmd(t, "", "doctor", "--bogus")
	if code != 2 {
		t.Errorf("code = %d, want 2, stderr = %q", code, stderr)
	}
}

func TestDoctorReportsConfigProblemsWithFixes(t *testing.T) {
	newDoctorTestVault(t)
	withFakePlatformChecks(t, healthyPlatformChecks)
	path := writeConfig(t, "[search]\nlimit = \"x\"\n[serach]\nlimit = 1\n[tui]\ntheme = \"dark\"\n")

	stdout, _, code := runCmd(t, "", "doctor")
	if code != 1 {
		t.Fatalf("code = %d, want 1, stdout = %q", code, stdout)
	}
	for _, want := range []string{
		"[ok] config: " + path + "\n",
		"[!!] config: search.limit = \"x\": want an integer; using 20\n      nn config search.limit 20\n",
		"[!!] config: unknown key serach; ignored\n      did you mean search?\n",
		"[ok] config: [tui]: reserved, ignored\n",
	} {
		if !strings.Contains(stdout, want) {
			t.Errorf("stdout lacks %q:\n%s", want, stdout)
		}
	}
}

func TestDoctorReservedTUIIsNotAProblem(t *testing.T) {
	newDoctorTestVault(t)
	withFakePlatformChecks(t, healthyPlatformChecks)
	writeConfig(t, "[tui]\ntheme = \"dark\"\n[ai.consent]\nclaude = \"never\"\n")

	stdout, _, code := runCmd(t, "", "doctor")
	if code != 0 {
		t.Errorf("code = %d, want 0, stdout = %q", code, stdout)
	}
}

func TestDoctorNamesTheReplacementOfTheOldRootKey(t *testing.T) {
	newDoctorTestVault(t)
	t.Setenv("NN_ROOT", "")
	withFakePlatformChecks(t, healthyPlatformChecks)
	writeConfig(t, "root = \"/old/vault\"\n")

	stdout, _, code := runCmd(t, "", "doctor")
	if code != 1 {
		t.Fatalf("code = %d, want 1, stdout = %q", code, stdout)
	}
	want := "[!!] config: vault.root is not set, and root is its old name\n      move it under [vault]: root = \"/old/vault\"\n"
	if !strings.Contains(stdout, want) {
		t.Errorf("stdout lacks %q:\n%s", want, stdout)
	}
	if strings.Contains(stdout, "vault root:") {
		t.Errorf("vault checks ran on a config that did not load:\n%s", stdout)
	}
}

// A config that cannot be read at all is not one of config.Load's problems, but still needs a fix row.
func TestDoctorUnreadableConfigSaysWhatToDo(t *testing.T) {
	newDoctorTestVault(t)
	withFakePlatformChecks(t, healthyPlatformChecks)
	path := writeConfig(t, "[search]\nlimit = 3\n")
	if err := os.Chmod(path, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := os.ReadFile(path); err == nil {
		t.Skip("running with privileges that read a mode 0 file")
	}

	stdout, _, code := runCmd(t, "", "doctor")
	if code != 1 {
		t.Fatalf("code = %d, want 1, stdout = %q", code, stdout)
	}
	want := "      make " + path + " readable, or point NN_CONFIG at another file\n"
	if !strings.Contains(stdout, "[!!] config: ") || !strings.Contains(stdout, want) {
		t.Errorf("stdout lacks the failed config row with %q:\n%s", want, stdout)
	}
}

func TestDoctorConfigThatIsADirectory(t *testing.T) {
	newDoctorTestVault(t)
	withFakePlatformChecks(t, healthyPlatformChecks)
	path := t.TempDir()
	t.Setenv("NN_CONFIG", path)

	stdout, _, code := runCmd(t, "", "doctor")
	if code != 1 {
		t.Fatalf("code = %d, want 1, stdout = %q", code, stdout)
	}
	want := "      " + path + " is a directory; point NN_CONFIG at a file\n"
	if !strings.Contains(stdout, "[!!] config: ") || !strings.Contains(stdout, want) {
		t.Errorf("stdout lacks the failed config row with %q:\n%s", want, stdout)
	}
}
