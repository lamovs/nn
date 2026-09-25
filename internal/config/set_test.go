package config

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/BurntSushi/toml"
)

func TestSetCreatesFileAndPreservesKeys(t *testing.T) {
	xdgConfig := isolate(t)
	path := filepath.Join(xdgConfig, "nn", "config.toml")

	if err := Set("vault.root", "/first-root"); err != nil {
		t.Fatal(err)
	}
	if got, want := readConfigFile(t, path), "[vault]\nroot = \"/first-root\"\n"; got != want {
		t.Errorf("new file = %q, want just the key it was asked for, %q", got, want)
	}
	cfg, _, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Vault.Root != "/first-root" {
		t.Errorf("Root = %q", cfg.Vault.Root)
	}

	existing := readConfigFile(t, path)
	edited := existing + "\n[editor]\ncommand = \"nvim\"\n\n[hooks]\npost_save = \"echo hi\"\n"
	if err := os.WriteFile(path, []byte(edited), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := Set("vault.root", "/second-root"); err != nil {
		t.Fatal(err)
	}
	cfg, _, err = Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Vault.Root != "/second-root" {
		t.Errorf("Root = %q, want /second-root", cfg.Vault.Root)
	}
	if cfg.Editor.Command != "nvim" {
		t.Errorf("Editor.Command = %q, want it preserved as \"nvim\"", cfg.Editor.Command)
	}
	if cfg.Hooks.PostSave != "echo hi" {
		t.Errorf("Hooks.PostSave = %q, want it preserved", cfg.Hooks.PostSave)
	}
}

func TestSetKeepsCommentsAndUnknownKeys(t *testing.T) {
	xdgConfig := isolate(t)
	body := "# my nn configuration\n" +
		"future_key = 42\n" +
		"\n" +
		"[vault]\n" +
		"root = \"~/old-vault\"   # required; NN_ROOT overrides\n" +
		"inbox = \"notes\" # relative to root\n" +
		"\n" +
		"[editor]\n" +
		"command = \"hx\"\n" +
		"\n" +
		"[hooks]\n" +
		"# runs after every capture\n" +
		"post_save = \"echo saved\"\n"
	path := writeConfigFile(t, xdgConfig, body)

	if err := Set("vault.root", "~/Notes"); err != nil {
		t.Fatal(err)
	}

	got := readConfigFile(t, path)
	want := strings.Replace(body, "root = \"~/old-vault\"", "root = \"~/Notes\"", 1)
	if got != want {
		t.Errorf("config file was rewritten instead of edited in place:\ngot:\n%s\nwant:\n%s", got, want)
	}

	cfg, _, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(os.Getenv("HOME"), "Notes"); cfg.Vault.Root != want {
		t.Errorf("Root = %q, want %q", cfg.Vault.Root, want)
	}
	if cfg.Vault.Inbox != "notes" || cfg.Editor.Command != "hx" || cfg.Hooks.PostSave != "echo saved" {
		t.Errorf("other keys changed: inbox=%q editor=%q post_save=%q", cfg.Vault.Inbox, cfg.Editor.Command, cfg.Hooks.PostSave)
	}
}

func TestSetKeepsLiteralStringAndSpacing(t *testing.T) {
	xdgConfig := isolate(t)
	body := "[vault]\n  root='/old'\t# indented, literal, no spaces around =\n"
	path := writeConfigFile(t, xdgConfig, body)

	if err := Set("vault.root", "/new"); err != nil {
		t.Fatal(err)
	}

	got := readConfigFile(t, path)
	want := "[vault]\n  root=\"/new\"\t# indented, literal, no spaces around =\n"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestSetReplacesEveryScalarType(t *testing.T) {
	xdgConfig := isolate(t)
	body := "[search]\nlimit = 20 # results\n" +
		"[capture]\nsimilar_threshold = 0.6\nocr = true\n" +
		"[state]\nhalf_life = \"14d\"\n" +
		"[ls]\nsort = \"modified\"\n"
	path := writeConfigFile(t, xdgConfig, body)

	for key, value := range map[string]string{
		"search.limit":              "5",
		"capture.similar_threshold": "1",
		"capture.ocr":               "false",
		"state.half_life":           "2w",
		"ls.sort":                   "title",
	} {
		if err := Set(key, value); err != nil {
			t.Fatalf("Set(%s, %s): %v", key, value, err)
		}
	}

	want := "[search]\nlimit = 5 # results\n" +
		"[capture]\nsimilar_threshold = 1.0\nocr = false\n" +
		"[state]\nhalf_life = \"2w\"\n" +
		"[ls]\nsort = \"title\"\n"
	if got := readConfigFile(t, path); got != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestSetInsertsAtTheEndOfItsSection(t *testing.T) {
	xdgConfig := isolate(t)
	body := "# vault set through NN_ROOT so far\n" +
		"[vault]\n" +
		"  inbox = \"notes\"\n" +
		"\n" +
		"# hooks follow\n" +
		"[hooks]\n" +
		"post_save = \"echo saved\"\n"
	path := writeConfigFile(t, xdgConfig, body)

	if err := Set("vault.root", "/new-root"); err != nil {
		t.Fatal(err)
	}

	got := readConfigFile(t, path)
	want := "# vault set through NN_ROOT so far\n" +
		"[vault]\n" +
		"  inbox = \"notes\"\n" +
		"  root = \"/new-root\"\n" +
		"\n" +
		"# hooks follow\n" +
		"[hooks]\n" +
		"post_save = \"echo saved\"\n"
	if got != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}

	cfg, _, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Vault.Root != "/new-root" || cfg.Hooks.PostSave != "echo saved" {
		t.Errorf("Root = %q, PostSave = %q", cfg.Vault.Root, cfg.Hooks.PostSave)
	}
}

func TestSetInsertsRightAfterAnEmptySectionHeader(t *testing.T) {
	xdgConfig := isolate(t)
	path := writeConfigFile(t, xdgConfig, "[vault] # nothing yet\n\n[hooks]\npost_save = \"x\"\n")

	if err := Set("vault.root", "/v"); err != nil {
		t.Fatal(err)
	}
	if got, want := readConfigFile(t, path), "[vault] # nothing yet\nroot = \"/v\"\n\n[hooks]\npost_save = \"x\"\n"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestSetAppendsAMissingSection(t *testing.T) {
	xdgConfig := isolate(t)
	body := "[vault]\nroot = \"/v\"\n"
	path := writeConfigFile(t, xdgConfig, body)

	if err := Set("ai.tasks.shot.run", "flag"); err != nil {
		t.Fatal(err)
	}
	want := body + "\n[ai.tasks.shot]\nrun = \"flag\"\n"
	if got := readConfigFile(t, path); got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestSetAppendsWhenFileHasNoTable(t *testing.T) {
	xdgConfig := isolate(t)
	path := writeConfigFile(t, xdgConfig, "# nothing set yet")

	if err := Set("vault.root", "/new-root"); err != nil {
		t.Fatal(err)
	}

	got := readConfigFile(t, path)
	want := "# nothing set yet\n\n[vault]\nroot = \"/new-root\"\n"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestSetQuotesAwkwardPaths(t *testing.T) {
	xdgConfig := isolate(t)
	path := writeConfigFile(t, xdgConfig, "[vault]\nroot = \"/old\"\n")

	awkward := "/vaults/with \"quotes\" and \\ backslash"
	if err := Set("vault.root", awkward); err != nil {
		t.Fatal(err)
	}
	if got, want := readConfigFile(t, path), "[vault]\nroot = \"/vaults/with \\\"quotes\\\" and \\\\ backslash\"\n"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}

	cfg, _, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Vault.Root != awkward {
		t.Errorf("Root = %q, want %q", cfg.Vault.Root, awkward)
	}
}

func TestSetQuotesAwkwardSectionNames(t *testing.T) {
	xdgConfig := isolate(t)
	path := writeConfigFile(t, xdgConfig, "[ai.profiles.\"my box\"]\nengine = \"command\"\ncommand = [\"x\"]\n")

	if err := Set("ai.consent.my box", "always"); err != nil {
		t.Fatal(err)
	}
	want := "[ai.profiles.\"my box\"]\nengine = \"command\"\ncommand = [\"x\"]\n\n[ai.consent]\n\"my box\" = \"always\"\n"
	if got := readConfigFile(t, path); got != want {
		t.Errorf("got %q, want %q", got, want)
	}
	if err := Set("ai.profiles.my box.model", "m"); err != nil {
		t.Fatal(err)
	}
	withEnv(t, "NN_ROOT", "/vault")
	cfg, problems, err := Load()
	if err != nil || len(problems) != 0 {
		t.Fatalf("Load: %v, %+v", err, problems)
	}
	if cfg.AI.Profiles["my box"].Model != "m" || cfg.AI.Consent["my box"] != "always" {
		t.Errorf("profile = %+v, consent = %v", cfg.AI.Profiles["my box"], cfg.AI.Consent)
	}
}

var bomPrefix = string(utf8BOM)

func TestSetRewritesKeyInFileWithBOM(t *testing.T) {
	xdgConfig := isolate(t)
	body := bomPrefix + "[vault]\n" +
		"root = \"/old\"\n" +
		"# my nn configuration\n" +
		"inbox = \"notes\"\n"
	path := writeConfigFile(t, xdgConfig, body)

	if err := Set("vault.root", "/new-root"); err != nil {
		t.Fatal(err)
	}

	got := readConfigFile(t, path)
	if n := strings.Count(got, "[vault]"); n != 1 {
		t.Errorf("file has %d [vault] sections, want 1:\n%q", n, got)
	}
	want := bomPrefix + "[vault]\n" +
		"root = \"/new-root\"\n" +
		"# my nn configuration\n" +
		"inbox = \"notes\"\n"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}

	cfg, _, err := Load()
	if err != nil {
		t.Fatalf("config no longer loads after Set: %v", err)
	}
	if cfg.Vault.Root != "/new-root" || cfg.Vault.Inbox != "notes" {
		t.Errorf("Root = %q, Inbox = %q", cfg.Vault.Root, cfg.Vault.Inbox)
	}
}

func TestSetIsNotFooledByAnArrayLineThatLooksLikeATable(t *testing.T) {
	xdgConfig := isolate(t)
	body := "[vault]\n" +
		"inbox = \"notes\"\n" +
		"matrix = [\n" +
		"  [1, 2],\n" +
		"]\n" +
		"[hooks]\n" +
		"post_save = \"x\"\n"
	path := writeConfigFile(t, xdgConfig, body)

	if err := Set("vault.root", "/new-root"); err != nil {
		t.Fatal(err)
	}

	got := readConfigFile(t, path)
	want := "[vault]\n" +
		"inbox = \"notes\"\n" +
		"matrix = [\n" +
		"  [1, 2],\n" +
		"]\n" +
		"root = \"/new-root\"\n" +
		"[hooks]\n" +
		"post_save = \"x\"\n"
	if got != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestSetEditsInPlaceOrLeavesTheFileAlone(t *testing.T) {
	cases := []struct {
		name, body string
		refused    bool
	}{
		{"empty file", "", false},
		{"blank file", "\n\n", false},
		{"comment only", "# nothing set yet", false},
		{"plain root", "[vault]\nroot = \"/old\"\n", false},
		{"bom with root", bomPrefix + "[vault]\nroot = \"/old\"\n", false},
		{"bom without section", bomPrefix + "# just a comment\n[hooks]\npost_save = \"x\"\n", false},
		{"bom only", bomPrefix, false},
		{"multi line array", "[vault]\nmatrix = [\n  [1, 2],\n]\n", false},
		{"multi line array then table", "matrix = [\n  [1, 2],\n]\n\n[hooks]\npost_save = \"echo hi\"\n", false},
		{"multi line array with comment", "[vault]\nlist = [ # [not a table]\n  \"a\", # ]\n]\n", false},
		{"root decoy inside multi line string", "[hooks]\npost_save = \"\"\"\n[vault]\nroot = \"decoy\"\n\"\"\"\n[vault]\nroot = \"/old\"\n", false},
		{"literal multi line string", "[hooks]\npost_save = '''\n[vault]\n'''\n", false},
		{"string with quotes at the close", "[hooks]\npost_save = \"\"\"a \"\"\"\"\n[vault]\nroot = \"/old\"\n", false},
		{"crlf", "[vault]\r\nroot = \"/old\"\r\ninbox = \"notes\"\r\n", false},
		{"crlf insert", "[vault]\r\ninbox = \"notes\"\r\n", false},
		{"crlf new section", "[hooks]\r\npost_save = \"x\"\r\n", false},
		{"crlf no trailing newline", "[vault]\r\ninbox = \"notes\"", false},
		{"target then multi line array", "[vault]\nroot = \"/old\"\nmatrix = [\n  [1, 2],\n]\n[hooks]\npost_save = \"x\"\n", false},
		{"nan elsewhere", "[vault]\nroot = \"/old\"\n[x]\ny = nan\nz = [nan, 1.0]\n", false},
		{"empty section elsewhere", "[vault]\nroot = \"/old\"\n[hooks]\n", false},
		{"old flat root", "root = \"/old\"\n", false},
		{"array of tables", "[[x]]\ny = 1\n", false},
		{"no trailing newline", "[vault]\ninbox = \"notes\"", false},
		{"sub-table first", "[vault.extra]\na = 1\n", false},
		{"multi line string root", "[vault]\nroot = \"\"\"\n/old\n\"\"\"\n", true},
		{"one line triple quoted root", "[vault]\nroot = \"\"\"/old\"\"\"\n", true},
		{"dotted key at top", "vault.root = \"/old\"\n", true},
		{"dotted key elsewhere", "hooks.post_save = \"x\"\n[vault]\ninbox = \"n\"\n", false},
		{"inline table", "vault = { root = \"/old\" }\n", true},
		{"section made by dotted keys", "vault.inbox = \"notes\"\n", true},
		{"array of vault tables", "[[vault]]\nroot = \"/old\"\n", true},
		{"root is a table", "[vault.root]\na = 1\n", true},
		{"root is an array of tables", "[[vault.root.x]]\na = 1\n", true},
		{"vault is a value", "vault = 5\n", true},
		{"does not parse", "[vault]\nroot = \n", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			xdgConfig := isolate(t)
			path := writeConfigFile(t, xdgConfig, tc.body)

			err := Set("vault.root", "/new-root")
			got := readConfigFile(t, path)

			var manual *ManualEditError
			if tc.refused {
				if !errors.As(err, &manual) {
					t.Fatalf("Set = %v, want a *ManualEditError\nfile:\n%q", err, got)
				}
				if manual.Section != "vault" || manual.Line != `root = "/new-root"` || manual.Reason == "" {
					t.Errorf("manual edit = %+v, want the [vault] line and a reason", manual)
				}
				if got != tc.body {
					t.Errorf("file changed on a refusal:\ngot  %q\nwant %q", got, tc.body)
				}
				return
			}
			if err != nil {
				t.Fatalf("Set: %v\nfile:\n%q", err, tc.body)
			}

			// This oracle does not share code with Set.
			var want, after map[string]any
			if _, err := toml.Decode(strings.TrimPrefix(tc.body, bomPrefix), &want); err != nil {
				t.Fatal(err)
			}
			if _, err := toml.Decode(got, &after); err != nil {
				t.Fatalf("config no longer parses after Set: %v\nfile:\n%q", err, got)
			}
			if want == nil {
				want = map[string]any{}
			}
			vault, ok := want["vault"].(map[string]any)
			if !ok {
				vault = map[string]any{}
				want["vault"] = vault
			}
			vault["root"] = "/new-root"
			// NaN is never equal to itself, so it is checked separately.
			if nanPaths(want) != nanPaths(after) {
				t.Errorf("NaNs moved: before %s, after %s", nanPaths(want), nanPaths(after))
			}
			if !reflect.DeepEqual(dropNaN(want), dropNaN(after)) {
				t.Fatalf("Set changed more than vault.root:\nbefore %q\nafter  %q", tc.body, got)
			}
			if strings.HasPrefix(tc.body, bomPrefix) != strings.HasPrefix(got, bomPrefix) {
				t.Errorf("BOM not kept as it was: %q", got)
			}
			if strings.Contains(tc.body, "\r\n") && strings.Count(got, "\n") != strings.Count(got, "\r\n") {
				t.Errorf("line endings mixed in a CRLF file: %q", got)
			}
		})
	}
}

// nanPaths lists where v holds a NaN, one path per line, sorted.
func nanPaths(v any) string {
	var paths []string
	var walk func(prefix string, v any)
	walk = func(prefix string, v any) {
		switch x := v.(type) {
		case float64:
			if math.IsNaN(x) {
				paths = append(paths, prefix)
			}
		case map[string]any:
			for k, item := range x {
				walk(prefix+"/"+k, item)
			}
		case []any:
			for i, item := range x {
				walk(fmt.Sprintf("%s/%d", prefix, i), item)
			}
		}
	}
	walk("", v)
	slices.Sort(paths)
	return strings.Join(paths, "\n")
}

func dropNaN(v any) any {
	switch x := v.(type) {
	case float64:
		if math.IsNaN(x) {
			return "<NaN>"
		}
	case map[string]any:
		out := map[string]any{}
		for k, item := range x {
			out[k] = dropNaN(item)
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i, item := range x {
			out[i] = dropNaN(item)
		}
		return out
	}
	return v
}

func TestSetRefusalLeavesMultiLineValueAlone(t *testing.T) {
	xdgConfig := isolate(t)
	body := "[hooks]\npost_save = \"\"\"\necho hi\n\"\"\"\n"
	path := writeConfigFile(t, xdgConfig, body)

	err := Set("hooks.post_save", "echo bye")
	var manual *ManualEditError
	if !errors.As(err, &manual) {
		t.Fatalf("Set = %v, want *ManualEditError", err)
	}
	if manual.Section != "hooks" || manual.Line != `post_save = "echo bye"` || manual.File != path {
		t.Errorf("manual = %+v", manual)
	}
	if !strings.Contains(manual.Reason, "more than one line") {
		t.Errorf("reason = %q", manual.Reason)
	}
	if got := readConfigFile(t, path); got != body {
		t.Errorf("file changed: %q", got)
	}
}

func TestSetRefusesBadKeysAndValues(t *testing.T) {
	cases := []struct{ key, value, want string }{
		{"root", "/x", "renamed to vault.root"},
		{"search.limt", "5", "did you mean search.limit?"},
		{"search", "5", "a section"},
		{"tui.theme", "dark", "reserved"},
		{"ocr.langs", "rus", "edit the file"},
		{"ai.profiles.work.command", "llm", "edit the file"},
		{"search.limit", "many", "want an integer"},
		{"search.limit", "0", "want >= 1"},
		{"capture.similar_threshold", "2", "want 0..1"},
		{"ls.sort", "size", "want one of modified, date, opened, title"},
		{"state.half_life", "soon", "want a duration"},
		{"capture.ocr", "maybe", "want true or false"},
		{"vault.root", "  ", "must not be empty"},
		{"vault.inbox", "", "must not be empty"},
		{"ai.tasks.ask.run", "flag", "shot, title only"},
		{"ai.tasks.shot.profile", "nope", "no such profile"},
		{"ai.profile", "nope", "no such profile"},
		{"ai.profiles.claude.engine", "codex", "built-in profile claude"},
		{"ai.consent.gemini", "always", "not an engine"},
	}
	for _, tc := range cases {
		t.Run(tc.key+"="+tc.value, func(t *testing.T) {
			xdgConfig := isolate(t)
			path := writeConfigFile(t, xdgConfig, "[vault]\nroot = \"/v\"\n")
			err := Set(tc.key, tc.value)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Set = %v, want an error mentioning %q", err, tc.want)
			}
			var manual *ManualEditError
			if errors.As(err, &manual) {
				t.Errorf("a refused value is not a manual edit: %v", err)
			}
			if got := readConfigFile(t, path); got != "[vault]\nroot = \"/v\"\n" {
				t.Errorf("file changed: %q", got)
			}
		})
	}
}

func TestSetAcceptsReferencesTheFileDeclares(t *testing.T) {
	xdgConfig := isolate(t)
	writeConfigFile(t, xdgConfig, "[vault]\nroot = \"/v\"\n[ai.profiles.local]\nengine = \"command\"\ncommand = [\"llm\"]\n")

	for key, value := range map[string]string{
		"ai.tasks.shot.profile":   "local",
		"ai.consent.local":        "always",
		"ai.profiles.work.effort": "high",
		"ai.profiles.codex.model": "o3",
		"ai.tasks.ask.profile":    "",
	} {
		if err := Set(key, value); err != nil {
			t.Errorf("Set(%s, %q): %v", key, value, err)
		}
	}
	cfg, problems, err := Load()
	if err != nil || len(problems) != 0 {
		t.Fatalf("Load: %v, %+v", err, problems)
	}
	if cfg.AI.Tasks["shot"].Profile != "local" || cfg.AI.Consent["local"] != "always" ||
		cfg.AI.Profiles["work"].Effort != "high" || cfg.AI.Profiles["codex"].Model != "o3" {
		t.Errorf("ai = %+v", cfg.AI)
	}
}

func TestOnlyChangedCatchesAnyOtherDifference(t *testing.T) {
	decode := func(s string) map[string]any {
		var m map[string]any
		if _, err := toml.Decode(s, &m); err != nil {
			t.Fatal(err)
		}
		return m
	}
	before := decode("[vault]\nroot = \"/old\"\ninbox = \"n\"\n[hooks]\npost_save = \"x\"\n")
	target := []string{"vault", "root"}
	if !onlyChanged(before, decode("[vault]\nroot = \"/new\"\ninbox = \"n\"\n[hooks]\npost_save = \"x\"\n"), target, "/new") {
		t.Error("a clean edit was rejected")
	}
	for name, after := range map[string]string{
		"value not set":    "[vault]\nroot = \"/old\"\ninbox = \"n\"\n[hooks]\npost_save = \"x\"\n",
		"other key lost":   "[vault]\nroot = \"/new\"\n[hooks]\npost_save = \"x\"\n",
		"other key moved":  "[vault]\nroot = \"/new\"\ninbox = \"n\"\npost_save = \"x\"\n",
		"key added":        "[vault]\nroot = \"/new\"\ninbox = \"n\"\n[hooks]\npost_save = \"x\"\nextra = 1\n",
		"value wrong type": "[vault]\nroot = 1\ninbox = \"n\"\n[hooks]\npost_save = \"x\"\n",
	} {
		if onlyChanged(before, decode(after), target, "/new") {
			t.Errorf("%s: accepted", name)
		}
	}

	for name, tc := range map[string]struct {
		before, after string
		ok            bool
	}{
		"empty section lost":            {"[vault]\nroot = \"/old\"\n[hooks]\n", "[vault]\nroot = \"/new\"\n", false},
		"empty section added":           {"[vault]\nroot = \"/old\"\n", "[vault]\nroot = \"/new\"\n[hooks]\n", false},
		"target's empty section filled": {"[vault]\n[hooks]\n", "[vault]\nroot = \"/new\"\n[hooks]\n", true},
		"nan kept":                      {"[vault]\nroot = \"/old\"\n[x]\ny = nan\nz = [nan]\n", "[vault]\nroot = \"/new\"\n[x]\ny = nan\nz = [nan]\n", true},
		"nan lost":                      {"[vault]\nroot = \"/old\"\n[x]\ny = nan\n", "[vault]\nroot = \"/new\"\n[x]\ny = 1.0\n", false},
	} {
		if got := onlyChanged(decode(tc.before), decode(tc.after), target, "/new"); got != tc.ok {
			t.Errorf("%s: onlyChanged = %v, want %v", name, got, tc.ok)
		}
	}
}

func TestSetIsAtomic(t *testing.T) {
	xdgConfig := isolate(t)
	path := filepath.Join(xdgConfig, "nn", "config.toml")
	if err := Set("vault.root", "/root-one"); err != nil {
		t.Fatal(err)
	}
	if err := Set("search.limit", "5"); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.Contains(e.Name(), ".nn-config-") {
			t.Errorf("leftover temp file: %s", e.Name())
		}
	}
}

func TestSetFollowsASymlinkAndKeepsTheMode(t *testing.T) {
	xdgConfig := isolate(t)
	dotfiles := t.TempDir()
	real := filepath.Join(dotfiles, "nn.toml")
	if err := os.WriteFile(real, []byte("[vault]\nroot = \"/old\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(real, 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(xdgConfig, "nn", "config.toml")
	if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}

	if err := Set("vault.root", "/new"); err != nil {
		t.Fatal(err)
	}
	info, err := os.Lstat(link)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&fs.ModeSymlink == 0 {
		t.Errorf("config path is no longer a symlink: %v", info.Mode())
	}
	if got := readConfigFile(t, real); got != "[vault]\nroot = \"/new\"\n" {
		t.Errorf("link target = %q, want the edit there", got)
	}
	if info, err := os.Stat(real); err != nil || info.Mode().Perm() != 0o600 {
		t.Errorf("link target mode = %v (%v), want 0600 kept", info.Mode().Perm(), err)
	}
	entries, err := os.ReadDir(dotfiles)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.Contains(e.Name(), ".nn-config-") {
			t.Errorf("leftover temp file: %s", e.Name())
		}
	}
}

func TestSetThroughADanglingSymlinkCreatesItsTarget(t *testing.T) {
	xdgConfig := isolate(t)
	real := filepath.Join(t.TempDir(), "nn.toml")
	link := filepath.Join(xdgConfig, "nn", "config.toml")
	if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	if err := Set("vault.root", "/new"); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Lstat(link); err != nil || info.Mode()&fs.ModeSymlink == 0 {
		t.Errorf("config path is no longer a symlink: %v", err)
	}
	if got := readConfigFile(t, real); got != "[vault]\nroot = \"/new\"\n" {
		t.Errorf("link target = %q", got)
	}
}

func TestSetNewFileHasTheUsualMode(t *testing.T) {
	xdgConfig := isolate(t)
	path := filepath.Join(xdgConfig, "nn", "config.toml")
	if err := Set("vault.root", "/new"); err != nil {
		t.Fatal(err)
	}
	probe := filepath.Join(t.TempDir(), "probe")
	f, err := os.OpenFile(probe, os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	f.Close()
	want, err := os.Stat(probe)
	if err != nil {
		t.Fatal(err)
	}
	got, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got.Mode().Perm() != want.Mode().Perm() {
		t.Errorf("new config mode = %v, want %v", got.Mode().Perm(), want.Mode().Perm())
	}
}

func TestSetKeepsCRLFWithoutATrailingNewline(t *testing.T) {
	xdgConfig := isolate(t)
	path := writeConfigFile(t, xdgConfig, "[vault]\r\ninbox = \"notes\"")
	if err := Set("vault.root", "/new"); err != nil {
		t.Fatal(err)
	}
	if err := Set("search.limit", "5"); err != nil {
		t.Fatal(err)
	}
	want := "[vault]\r\ninbox = \"notes\"\r\nroot = \"/new\"\r\n\r\n[search]\r\nlimit = 5\r\n"
	if got := readConfigFile(t, path); got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestSetWithNaNElsewhereInTheFile(t *testing.T) {
	xdgConfig := isolate(t)
	path := writeConfigFile(t, xdgConfig, "[vault]\nroot = \"/v\"\n[x]\ny = nan\n")
	if err := Set("search.limit", "3"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if got, want := readConfigFile(t, path), "[vault]\nroot = \"/v\"\n[x]\ny = nan\n\n[search]\nlimit = 3\n"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestLiteralSpecialFloats(t *testing.T) {
	for v, want := range map[float64]string{
		math.Inf(1):  "inf",
		math.Inf(-1): "-inf",
		2:            "2.0",
		0.25:         "0.25",
	} {
		if got := literal(v); got != want {
			t.Errorf("literal(%v) = %q, want %q", v, got, want)
		}
	}
	if got := literal(math.NaN()); got != "nan" {
		t.Errorf("literal(NaN) = %q, want nan", got)
	}
	for _, s := range []string{"nan", "inf", "-inf"} {
		var doc map[string]any
		if _, err := toml.Decode("x = "+literal(decodeFloat(t, s)), &doc); err != nil {
			t.Errorf("%s does not read back: %v", s, err)
		}
	}
}

func TestTOMLKeyAndString(t *testing.T) {
	for _, s := range []string{"claude", "my-box_2", "my box", "a.b", `q"uote`, `back\slash`, "tab\there", "\x01ctl\x7f", "line\nbreak\r\f\b", ""} {
		var doc map[string]string
		if _, err := toml.Decode(TOMLKey(s)+" = "+TOMLString(s), &doc); err != nil || len(doc) != 1 || doc[s] != s {
			t.Errorf("%q spelled %s = %s reads back as %q, %v", s, TOMLKey(s), TOMLString(s), doc, err)
		}
	}
	for _, s := range []string{"claude", "my-box_2", "A1"} {
		if got := TOMLKey(s); got != s {
			t.Errorf("TOMLKey(%q) = %s, want it bare", s, got)
		}
	}
}

func decodeFloat(t *testing.T, s string) float64 {
	t.Helper()
	var doc struct{ X float64 }
	if _, err := toml.Decode("X = "+s, &doc); err != nil {
		t.Fatal(err)
	}
	return doc.X
}

func TestSetTrimsEveryScalarType(t *testing.T) {
	xdgConfig := isolate(t)
	path := writeConfigFile(t, xdgConfig, "[vault]\nroot = \"/v\"\n")
	for key, value := range map[string]string{
		"ls.sort":           " title ",
		"state.half_life":   "\t2w ",
		"search.limit":      " 5\n",
		"capture.ocr":       " false",
		"editor.command":    " hx ",
		"hooks.post_save":   " echo hi ",
		"ai.tasks.shot.run": " flag ",
	} {
		if err := Set(key, value); err != nil {
			t.Errorf("Set(%s, %q): %v", key, value, err)
		}
	}
	got := readConfigFile(t, path)
	for _, want := range []string{`sort = "title"`, `half_life = "2w"`, "limit = 5\n", "ocr = false", `command = "hx"`, `post_save = "echo hi"`, `run = "flag"`} {
		if !strings.Contains(got, want) {
			t.Errorf("file lacks %q:\n%s", want, got)
		}
	}
}

func TestSetTakesTurns(t *testing.T) {
	xdgConfig := isolate(t)
	path := writeConfigFile(t, xdgConfig, "[vault]\nroot = \"/v\"\n")
	keys := map[string]string{
		"search.limit":   "7",
		"ls.limit":       "9",
		"ls.sort":        "title",
		"editor.command": "hx",
		"graph.format":   "dot",
		"stats.by":       "month",
	}
	for range 5 {
		var wg sync.WaitGroup
		errs := make(chan error, len(keys))
		for key, value := range keys {
			wg.Add(1)
			go func() {
				defer wg.Done()
				errs <- Set(key, value)
			}()
		}
		wg.Wait()
		close(errs)
		for err := range errs {
			if err != nil {
				t.Fatal(err)
			}
		}
	}
	withEnv(t, "NN_ROOT", "")
	cfg, problems, err := Load()
	if err != nil || len(problems) != 0 {
		t.Fatalf("Load: %v, %+v\n%s", err, problems, readConfigFile(t, path))
	}
	for key, value := range keys {
		if e := entryFor(t, cfg, key); fmt.Sprint(e.Value) != value || e.Source != SourceFile {
			t.Errorf("%s = %+v, want %s from the file", key, e, value)
		}
	}
}

func TestSetExplainsWhyAKeyIsNotATable(t *testing.T) {
	for _, tc := range []struct{ body, key, value, reason string }{
		{"ai = 5\n", "ai.profile", "claude", "ai on line 1 is a value, not a table"},
		{"ai = { mode = \"off\" }\n", "ai.profile", "claude", "ai on line 1 is an inline table"},
		{"[[vault.root.x]]\na = 1\n", "vault.root", "/x", "[[vault.root.x]] on line 1 makes vault.root an array of tables"},
	} {
		xdgConfig := isolate(t)
		writeConfigFile(t, xdgConfig, tc.body)
		var manual *ManualEditError
		if err := Set(tc.key, tc.value); !errors.As(err, &manual) || manual.Reason != tc.reason {
			t.Errorf("%q: Set = %v, want the reason %q", tc.body, err, tc.reason)
		}
	}
}

func TestAssignment(t *testing.T) {
	isolate(t)
	for key, want := range map[string]string{
		"search.limit":              "search.limit = 5",
		"ai.consent.my box":         `ai.consent."my box" = "always"`,
		"capture.similar_threshold": "capture.similar_threshold = 1.0",
	} {
		value := map[string]string{"search.limit": " 5", "ai.consent.my box": "always", "capture.similar_threshold": "1"}[key]
		if got, err := Assignment(key, value); err != nil || got != want {
			t.Errorf("Assignment(%s, %q) = %q, %v; want %q", key, value, got, err, want)
		}
	}
	if _, err := Assignment("search.limit", "many"); err == nil {
		t.Error("Assignment accepted a bad value")
	}
}

func TestSetLockLivesUnderTheDataDirectory(t *testing.T) {
	xdgConfig := isolate(t)
	path := writeConfigFile(t, xdgConfig, "[vault]\nroot = \"/v\"\n")
	if err := Set("search.limit", "5"); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "config.toml" {
		t.Errorf("config directory holds %v, want config.toml alone", entries)
	}
	real, err := filepath.EvalSymlinks(path)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte(real))
	lock := filepath.Join(os.Getenv("XDG_DATA_HOME"), "nn", "locks", "config-"+hex.EncodeToString(sum[:8])+".lock")
	if _, err := os.Stat(lock); err != nil {
		t.Errorf("lock file: %v", err)
	}
}

func TestSetRefusalCreatesNothingNextToTheConfig(t *testing.T) {
	xdgConfig := isolate(t)
	if err := Set("ai.profile", "nope"); err == nil {
		t.Fatal("Set accepted a profile that does not exist")
	}
	if _, err := os.Stat(xdgConfig); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("config directory was created on a refusal: %v", err)
	}

	path := writeConfigFile(t, xdgConfig, "vault.inbox = \"notes\"\n")
	var manual *ManualEditError
	if err := Set("vault.root", "/new"); !errors.As(err, &manual) {
		t.Fatalf("Set = %v, want a manual edit", err)
	}
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Errorf("config directory holds %v after a refusal, want config.toml alone", entries)
	}
}

func TestLockPathStableAcrossCreation(t *testing.T) {
	t.Run("symlinked directory", func(t *testing.T) {
		isolate(t)
		realDir := t.TempDir()
		linkDir := filepath.Join(t.TempDir(), "cfg-link")
		if err := os.Symlink(realDir, linkDir); err != nil {
			t.Fatal(err)
		}
		withEnv(t, "NN_CONFIG", filepath.Join(linkDir, "config.toml"))
		assertLockStable(t)
	})
	t.Run("dangling symlink into a missing directory", func(t *testing.T) {
		xdgConfig := isolate(t)
		base := t.TempDir()
		baseLink := filepath.Join(t.TempDir(), "base-link")
		if err := os.Symlink(base, baseLink); err != nil {
			t.Fatal(err)
		}
		link := filepath.Join(xdgConfig, "nn", "config.toml")
		if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(filepath.Join(baseLink, "missing", "nn.toml"), link); err != nil {
			t.Fatal(err)
		}
		assertLockStable(t)
	})
}

func assertLockStable(t *testing.T) {
	t.Helper()
	lockFor := func() string {
		path, err := Path()
		if err != nil {
			t.Fatal(err)
		}
		target, err := resolveLink(path)
		if err != nil {
			t.Fatal(err)
		}
		lock, err := lockPath(target)
		if err != nil {
			t.Fatal(err)
		}
		return lock
	}
	before := lockFor()
	if err := Set("search.limit", "5"); err != nil {
		t.Fatal(err)
	}
	if after := lockFor(); after != before {
		t.Errorf("lock path before Set %s, after %s", before, after)
	}
}

func TestSetWithoutHome(t *testing.T) {
	isolate(t)
	path := filepath.Join(t.TempDir(), "cfg", "config.toml")
	withEnv(t, "NN_CONFIG", path)
	withEnv(t, "HOME", "")
	withEnv(t, "XDG_DATA_HOME", "")
	if err := Set("search.limit", "5"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if got := readConfigFile(t, path); got != "[search]\nlimit = 5\n" {
		t.Errorf("file = %q", got)
	}
}

func TestSetWithAReadOnlyDataDirectory(t *testing.T) {
	xdgConfig := isolate(t)
	data := filepath.Join(t.TempDir(), "data")
	if err := os.Mkdir(data, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(data, 0o700) })
	if err := os.Mkdir(filepath.Join(data, "probe"), 0o700); err == nil {
		t.Skip("running with privileges that write a read-only directory")
	}
	withEnv(t, "XDG_DATA_HOME", data)
	if err := Set("search.limit", "5"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if got := readConfigFile(t, filepath.Join(xdgConfig, "nn", "config.toml")); got != "[search]\nlimit = 5\n" {
		t.Errorf("file = %q", got)
	}
}

// TestSetWithAnUnwritableLocksDirectory: unlike ReadOnlyDataDirectory, the
// locks directory exists; creating the lock file inside it fails.
func TestSetWithAnUnwritableLocksDirectory(t *testing.T) {
	xdgConfig := isolate(t)
	data := filepath.Join(t.TempDir(), "data")
	locks := filepath.Join(data, "nn", "locks")
	if err := os.MkdirAll(locks, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(locks, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(locks, 0o700) })
	if _, err := os.Create(filepath.Join(locks, "probe")); err == nil {
		t.Skip("running with privileges that write a read-only directory")
	}
	withEnv(t, "XDG_DATA_HOME", data)
	if err := Set("search.limit", "5"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if got := readConfigFile(t, filepath.Join(xdgConfig, "nn", "config.toml")); got != "[search]\nlimit = 5\n" {
		t.Errorf("file = %q", got)
	}
}

func TestSetGivesUpOnAHeldLock(t *testing.T) {
	xdgConfig := isolate(t)
	path := writeConfigFile(t, xdgConfig, "[search]\nlimit = 3\n")
	old := lockWait
	lockWait = 50 * time.Millisecond
	t.Cleanup(func() { lockWait = old })

	target, err := resolveLink(path)
	if err != nil {
		t.Fatal(err)
	}
	file, err := lockPath(target)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		t.Fatal(err)
	}
	held, err := os.OpenFile(file, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer held.Close()
	if err := lockFile(held); errors.Is(err, errNoLocking) {
		t.Skip("no advisory locking on this platform")
	} else if err != nil {
		t.Fatal(err)
	}
	defer unlockFile(held)

	err = Set("search.limit", "5")
	if err == nil || !strings.Contains(err.Error(), "being edited by another nn") {
		t.Fatalf("Set = %v, want a refusal naming the other nn", err)
	}
	if got := readConfigFile(t, path); got != "[search]\nlimit = 3\n" {
		t.Errorf("file changed under a held lock: %q", got)
	}
}

func TestSetKeepsCRLFInABlankFile(t *testing.T) {
	xdgConfig := isolate(t)
	path := writeConfigFile(t, xdgConfig, "\r\n\r\n")
	if err := Set("search.limit", "5"); err != nil {
		t.Fatal(err)
	}
	if got, want := readConfigFile(t, path), "[search]\r\nlimit = 5\r\n"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}
