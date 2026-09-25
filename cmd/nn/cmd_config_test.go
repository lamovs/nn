package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lamovs/nn/internal/config"
)

func writeConfig(t *testing.T, body string) string {
	t.Helper()
	path := os.Getenv("NN_CONFIG")
	if path == "" {
		t.Fatal("NN_CONFIG is not set")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestConfigListsKeysWithSources(t *testing.T) {
	root := newTestVault(t)
	writeConfig(t, "[search]\nlimit = 50\n")
	stdout, stderr, code := runCmd(t, "", "config")
	if code != 0 {
		t.Fatalf("code = %d, stderr = %q", code, stderr)
	}
	for _, want := range []string{
		"# config file: " + os.Getenv("NN_CONFIG"),
		"# state file:",
		`= "` + root + `"  # env NN_ROOT`,
		`= "nn"  # default`,
		"= 50  # file",
		"ai.tasks.shot.run",
	} {
		if !strings.Contains(stdout, want) {
			t.Errorf("stdout lacks %q:\n%s", want, stdout)
		}
	}
}

func TestConfigJSON(t *testing.T) {
	root := newTestVault(t)
	stdout, stderr, code := runCmd(t, "", "config", "--json")
	if code != 0 {
		t.Fatalf("code = %d, stderr = %q", code, stderr)
	}
	rows := decodeJSONRows[config.Entry](t, stdout)
	found := false
	for _, r := range rows {
		if r.Key == "vault.root" {
			found = true
			if r.Value != root || r.Source != "env NN_ROOT" {
				t.Errorf("vault.root row = %+v, want %s from env NN_ROOT", r, root)
			}
		}
	}
	if !found {
		t.Fatalf("rows = %+v, want a vault.root row", rows)
	}
}

func TestConfigPrintsOneKey(t *testing.T) {
	newTestVault(t)
	writeConfig(t, "[ocr]\nlangs = [\"rus\", \"eng\"]\n")
	for key, want := range map[string]string{
		"vault.inbox":       "nn\n",
		"ocr.langs":         "rus\neng\n",
		"state.half_life":   "14d\n",
		"ai.tasks.shot.run": "always\n",
	} {
		stdout, stderr, code := runCmd(t, "", "config", key)
		if code != 0 || stdout != want {
			t.Errorf("config %s = %q (code %d, stderr %q), want %q", key, stdout, code, stderr, want)
		}
	}
}

func TestConfigKeyOfAnUndeclaredProfileIsNotFound(t *testing.T) {
	newTestVault(t)
	_, stderr, code := runCmd(t, "", "config", "ai.profiles.work.model")
	if code != 1 || !strings.Contains(stderr, "not set") {
		t.Errorf("code = %d, stderr = %q; want 1 and not set", code, stderr)
	}
}

func TestConfigSetWritesConfigFile(t *testing.T) {
	// Deliberately does not call newTestVault: NN_ROOT would override whatever this writes to the file.
	t.Setenv("NN_ROOT", "")
	t.Setenv("NN_CONFIG", filepath.Join(t.TempDir(), "config.toml"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(t.TempDir(), "xdg-data"))
	newRoot := filepath.Join(t.TempDir(), "my vault")
	stdout, stderr, code := runCmd(t, "", "config", "vault.root", newRoot)
	if code != 0 {
		t.Fatalf("code = %d, stderr = %q", code, stderr)
	}
	if !strings.Contains(stdout, newRoot) {
		t.Errorf("stdout = %q, want it to echo the new root", stdout)
	}
	cfg, _, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Vault.Root != newRoot {
		t.Errorf("cfg.Vault.Root = %q, want %q", cfg.Vault.Root, newRoot)
	}
}

func TestConfigSetJoinsTheValueWords(t *testing.T) {
	t.Setenv("NN_ROOT", "")
	t.Setenv("NN_CONFIG", filepath.Join(t.TempDir(), "config.toml"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(t.TempDir(), "xdg-data"))
	newRoot := filepath.Join(t.TempDir(), "two words")
	if err := os.MkdirAll(newRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	_, stderr, code := runCmd(t, "", append([]string{"config", "vault.root"}, strings.Fields(newRoot)...)...)
	if code != 0 {
		t.Fatalf("code = %d, stderr = %q", code, stderr)
	}
	stdout, _, _ := runCmd(t, "", "config", "vault.root")
	if stdout != newRoot+"\n" {
		t.Errorf("vault.root = %q, want %q", stdout, newRoot)
	}
}

func TestConfigSetBeforeTheRootWarnsButSucceeds(t *testing.T) {
	t.Setenv("NN_ROOT", "")
	path := filepath.Join(t.TempDir(), "config.toml")
	t.Setenv("NN_CONFIG", path)
	t.Setenv("XDG_DATA_HOME", filepath.Join(t.TempDir(), "xdg-data"))
	_, stderr, code := runCmd(t, "", "config", "search.limit", "5")
	if code != 0 {
		t.Fatalf("code = %d, stderr = %q", code, stderr)
	}
	if !strings.Contains(stderr, "vault.root is not set") {
		t.Errorf("stderr = %q, want a warning that vault.root is still missing", stderr)
	}
	if got, _ := os.ReadFile(path); string(got) != "[search]\nlimit = 5\n" {
		t.Errorf("file = %q", got)
	}
}

func TestConfigSetNotesAnEnvOverride(t *testing.T) {
	newTestVault(t)
	_, stderr, code := runCmd(t, "", "config", "vault.root", t.TempDir())
	if code != 0 {
		t.Fatalf("code = %d, stderr = %q", code, stderr)
	}
	if !strings.Contains(stderr, "NN_ROOT is set and overrides vault.root") {
		t.Errorf("stderr = %q, want a note that NN_ROOT wins", stderr)
	}
}

func TestConfigSetPrintsTheLineWhenItCannotEdit(t *testing.T) {
	newTestVault(t)
	body := "vault.inbox = \"notes\"\n"
	path := writeConfig(t, body)

	_, stderr, code := runCmd(t, "", "config", "vault.root", "/new")
	if code != 1 {
		t.Fatalf("code = %d, want 1, stderr = %q", code, stderr)
	}
	for _, want := range []string{"under [vault]:", "\nroot = \"/new\"\n", "dotted keys"} {
		if !strings.Contains(stderr, want) {
			t.Errorf("stderr lacks %q:\n%s", want, stderr)
		}
	}
	if got, _ := os.ReadFile(path); string(got) != body {
		t.Errorf("file changed: %q", got)
	}
}

func TestConfigSetRefusesBadValues(t *testing.T) {
	for _, args := range [][]string{
		{"config", "search.limit", "many"},
		{"config", "ocr.langs", "rus"},
		{"config", "root", "/x"},
		{"config", "tui.theme", "dark"},
	} {
		newTestVault(t)
		_, stderr, code := runCmd(t, "", args...)
		if code != 2 {
			t.Errorf("%v: code = %d, want 2, stderr = %q", args, code, stderr)
		}
	}
}

func TestConfigOldRootKeyPointsAtItsReplacement(t *testing.T) {
	newTestVault(t)
	_, stderr, code := runCmd(t, "", "config", "root")
	if code != 2 || !strings.Contains(stderr, "vault.root") {
		t.Errorf("code = %d, stderr = %q; want 2 and a pointer at vault.root", code, stderr)
	}
}

func TestConfigUnknownSubcommandIsUsageError(t *testing.T) {
	newTestVault(t)
	_, stderr, code := runCmd(t, "", "config", "bogus")
	if code != 2 {
		t.Errorf("code = %d, want 2, stderr = %q", code, stderr)
	}
}

func TestConfigWarnsAboutBadValuesButNotUnknownKeys(t *testing.T) {
	newTestVault(t)
	writeConfig(t, "[search]\nlimit = \"x\"\nlimt = 3\n[tui]\ntheme = \"dark\"\n")
	stdout, stderr, code := runCmd(t, "", "config", "search.limit")
	if code != 0 || stdout != "20\n" {
		t.Fatalf("code = %d, stdout = %q, stderr = %q; want the default 20", code, stdout, stderr)
	}
	if n := strings.Count(stderr, "\n"); n != 1 || !strings.Contains(stderr, "nn: warning: config: search.limit") {
		t.Errorf("stderr = %q, want exactly the one warning for search.limit", stderr)
	}
}

func TestConfigDefaultsLoadBack(t *testing.T) {
	newTestVault(t)
	stdout, stderr, code := runCmd(t, "", "config", "--defaults")
	if code != 0 {
		t.Fatalf("code = %d, stderr = %q", code, stderr)
	}
	writeConfig(t, stdout)
	_, problems, err := config.Load()
	if err != nil || len(problems) != 0 {
		t.Errorf("--defaults does not load cleanly: %v, %+v", err, problems)
	}
}

func TestConfigDefaultsTakesNoKey(t *testing.T) {
	newTestVault(t)
	if _, _, code := runCmd(t, "", "config", "search.limit", "--defaults"); code != 2 {
		t.Errorf("code = %d, want 2", code)
	}
}

// With NN_ROOT overriding the file, the line printed is the one written, not the value in effect.
func TestConfigSetPrintsWhatItWrote(t *testing.T) {
	root := newTestVault(t)
	stdout, stderr, code := runCmd(t, "", "config", "vault.root", "/written")
	if code != 0 {
		t.Fatalf("code = %d, stderr = %q", code, stderr)
	}
	if stdout != "vault.root = \"/written\"\n" {
		t.Errorf("stdout = %q, want the written line, not NN_ROOT's %s", stdout, root)
	}
	if !strings.Contains(stderr, "NN_ROOT is set and overrides vault.root") {
		t.Errorf("stderr = %q, want the note about NN_ROOT", stderr)
	}
}

func TestConfigOutputFormOnlyForReading(t *testing.T) {
	newTestVault(t)
	path := writeConfig(t, "[search]\nlimit = 3\n")
	for _, args := range [][]string{
		{"config", "--defaults", "--json"},
		{"config", "search.limit", "5", "--json"},
		{"config", "search.limit", "5", "--tsv"},
	} {
		stdout, stderr, code := runCmd(t, "", args...)
		if code != 2 || stdout != "" || !strings.Contains(stderr, "does not apply") {
			t.Errorf("%v: code = %d, stdout = %q, stderr = %q; want a usage error", args, code, stdout, stderr)
		}
	}
	if got, _ := os.ReadFile(path); string(got) != "[search]\nlimit = 3\n" {
		t.Errorf("file changed: %q", got)
	}
}

func TestConfigReadsWithoutARoot(t *testing.T) {
	newTestVault(t)
	t.Setenv("NN_ROOT", "")
	writeConfig(t, "[search]\nlimit = 3\n")

	stdout, stderr, code := runCmd(t, "", "config")
	if code != 0 || !strings.Contains(stdout, "= 3  # file") {
		t.Errorf("config: code = %d, stdout = %q, stderr = %q", code, stdout, stderr)
	}
	if !strings.Contains(stderr, "vault.root is not set") {
		t.Errorf("config: stderr = %q, want the missing root", stderr)
	}
	stdout, stderr, code = runCmd(t, "", "config", "search.limit")
	if code != 0 || stdout != "3\n" || !strings.Contains(stderr, "vault.root is not set") {
		t.Errorf("config search.limit: code = %d, stdout = %q, stderr = %q", code, stdout, stderr)
	}
	stdout, stderr, code = runCmd(t, "", "config", "--json")
	if code != 0 || !strings.HasPrefix(stdout, "[") {
		t.Errorf("config --json: code = %d, stdout = %q, stderr = %q", code, stdout, stderr)
	}

	writeConfig(t, "[search\n")
	for _, args := range [][]string{{"config"}, {"config", "search.limit"}} {
		if stdout, stderr, code := runCmd(t, "", args...); code == 0 || stdout != "" {
			t.Errorf("%v on a broken file: code = %d, stdout = %q, stderr = %q; want a failure", args, code, stdout, stderr)
		}
	}
}
