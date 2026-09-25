package main

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestSetupZshPrintsFragment(t *testing.T) {
	stdout, stderr, code := runCmd(t, "", "setup", "zsh")
	if code != 0 {
		t.Fatalf("code = %d, stderr = %q", code, stderr)
	}
	for _, want := range []string{"nn-last()", "nn-snip-widget", "compdef _nn nn"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("stdout does not contain %q", want)
		}
	}
	if strings.Contains(stdout, "\nbindkey") {
		t.Errorf("stdout assigns a live bindkey; want only a commented example")
	}
}

func TestSetupZshCompletionNotLimitedByLsLimit(t *testing.T) {
	if !strings.Contains(zshIntegration, "nn ls --paths -n all 2>/dev/null") {
		t.Errorf("zshIntegration does not call \"nn ls --paths -n all\"; note completion would be limited by ls.limit")
	}
}

func TestSetupZshMatchesIntegrationFile(t *testing.T) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	repoRoot := filepath.Dir(filepath.Dir(filepath.Dir(file)))
	data, err := os.ReadFile(filepath.Join(repoRoot, "integrations", "zsh", "nn.zsh"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != zshIntegration {
		t.Errorf("integrations/zsh/nn.zsh and the embedded zshIntegration constant have drifted apart")
	}
}

func TestSetupZshCompletionListsAllCommands(t *testing.T) {
	start := strings.Index(zshIntegration, "commands=(")
	if start < 0 {
		t.Fatal("zshIntegration has no commands=( block")
	}
	end := strings.Index(zshIntegration[start:], ")\n")
	if end < 0 {
		t.Fatal("zshIntegration commands=( block has no closing )")
	}
	block := zshIntegration[start : start+end]
	for verb := range commands {
		found := false
		for _, field := range strings.Fields(block) {
			if strings.HasPrefix(field, verb+":'") {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("zsh completion commands=( ) is missing verb %q", verb)
		}
	}
}

func TestSetupNoArgsShowsHelp(t *testing.T) {
	stdout, stderr, code := runCmd(t, "", "setup")
	if code != 0 {
		t.Fatalf("code = %d, stderr = %q", code, stderr)
	}
	if !strings.Contains(stdout, "zsh") || !strings.Contains(stdout, "install") {
		t.Errorf("stdout = %q, want it to mention zsh and install", stdout)
	}
}

func TestSetupUnknownTargetIsUsageError(t *testing.T) {
	_, stderr, code := runCmd(t, "", "setup", "bogus")
	if code != 2 {
		t.Errorf("code = %d, want 2, stderr = %q", code, stderr)
	}
}

func TestSetupTooManyArgsIsUsageError(t *testing.T) {
	_, stderr, code := runCmd(t, "", "setup", "zsh", "extra")
	if code != 2 {
		t.Errorf("code = %d, want 2, stderr = %q", code, stderr)
	}
}

func fakeExecutable(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "nn")
	if err := os.WriteFile(path, []byte("fake binary\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func withFakeInstallTargets(t *testing.T, home, exe string, resourcesDir string, resourcesOK bool) {
	t.Helper()
	prevHome, prevExe, prevRes := userHomeDir, executablePath, installResources
	userHomeDir = func() (string, error) { return home, nil }
	executablePath = func() (string, error) { return exe, nil }
	installResources = func() (string, bool) { return resourcesDir, resourcesOK }
	t.Cleanup(func() {
		userHomeDir, executablePath, installResources = prevHome, prevExe, prevRes
	})
}

func TestSetupInstallCopiesBinary(t *testing.T) {
	home := t.TempDir()
	exe := fakeExecutable(t)
	withFakeInstallTargets(t, home, exe, "", false)

	stdout, stderr, code := runCmd(t, "", "setup", "install")
	if code != 0 {
		t.Fatalf("code = %d, stderr = %q", code, stderr)
	}
	target := filepath.Join(home, ".local", "bin", "nn")
	data, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("nn was not installed: %v", err)
	}
	if string(data) != "fake binary\n" {
		t.Errorf("installed content = %q", string(data))
	}
	if info, err := os.Stat(target); err != nil || info.Mode().Perm()&0o111 == 0 {
		t.Errorf("installed binary is not executable: %v, %v", err, info)
	}
	if !strings.Contains(stdout, target) {
		t.Errorf("stdout = %q, want it to name the installed path", stdout)
	}
}

func TestSetupInstallAlsoCopiesHelperWhenBundled(t *testing.T) {
	home := t.TempDir()
	exe := fakeExecutable(t)
	share := t.TempDir()
	if err := os.WriteFile(filepath.Join(share, "nn-vision"), []byte("fake helper\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	withFakeInstallTargets(t, home, exe, share, true)

	_, stderr, code := runCmd(t, "", "setup", "install")
	if code != 0 {
		t.Fatalf("code = %d, stderr = %q", code, stderr)
	}
	helperDst := filepath.Join(home, ".local", "share", "nn", "nn-vision")
	if data, err := os.ReadFile(helperDst); err != nil || string(data) != "fake helper\n" {
		t.Errorf("nn-vision helper was not installed: %v, %q", err, string(data))
	}
}

func TestSetupInstallWithoutBundledResourcesSkipsHelper(t *testing.T) {
	home := t.TempDir()
	exe := fakeExecutable(t)
	withFakeInstallTargets(t, home, exe, "", false)

	_, stderr, code := runCmd(t, "", "setup", "install")
	if code != 0 {
		t.Fatalf("code = %d, stderr = %q", code, stderr)
	}
	if _, err := os.Stat(filepath.Join(home, ".local", "share", "nn", "nn-vision")); err == nil {
		t.Errorf("a helper was installed despite install.Resources reporting none bundled")
	}
}
