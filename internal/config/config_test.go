package config

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

func withEnv(t *testing.T, key, value string) {
	t.Helper()
	old, had := os.LookupEnv(key)
	if err := os.Setenv(key, value); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if had {
			os.Setenv(key, old)
		} else {
			os.Unsetenv(key)
		}
	})
}

func isolate(t *testing.T) (xdgConfig string) {
	t.Helper()
	home := t.TempDir()
	xdgConfig = filepath.Join(home, "xdg-config")
	withEnv(t, "XDG_CONFIG_HOME", xdgConfig)
	withEnv(t, "NN_CONFIG", "")
	withEnv(t, "NN_ROOT", "")
	withEnv(t, "XDG_DATA_HOME", filepath.Join(home, "xdg-data"))
	withEnv(t, "HOME", home)
	return xdgConfig
}

// writeConfigFile puts body at the config path isolate() set up, creating
// the directory for it, and returns that path.
func writeConfigFile(t *testing.T, xdgConfig, body string) string {
	t.Helper()
	path := filepath.Join(xdgConfig, "nn", "config.toml")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func readConfigFile(t *testing.T, path string) string {
	t.Helper()
	src, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(src)
}

// loadBody writes body as the config file and loads it with a vault root
// from NN_ROOT, so the file itself is all a test has to say.
func loadBody(t *testing.T, body string) (Config, []Problem) {
	t.Helper()
	xdgConfig := isolate(t)
	writeConfigFile(t, xdgConfig, body)
	withEnv(t, "NN_ROOT", "/vault")
	cfg, problems, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	return cfg, problems
}

func problemFor(t *testing.T, problems []Problem, key string) Problem {
	t.Helper()
	for _, p := range problems {
		if p.Key == key {
			return p
		}
	}
	t.Fatalf("no problem for %s in %+v", key, problems)
	return Problem{}
}

func entryFor(t *testing.T, cfg Config, key string) Entry {
	t.Helper()
	e, err := cfg.Get(key)
	if err != nil {
		t.Fatalf("Get(%s): %v", key, err)
	}
	return e
}

func TestLoadRequiresRoot(t *testing.T) {
	isolate(t)
	_, problems, err := Load()
	if err == nil {
		t.Fatal("expected an error when vault.root is not set")
	}
	if !strings.Contains(err.Error(), "nn config vault.root") {
		t.Errorf("error = %q, want a hint mentioning \"nn config vault.root\"", err)
	}
	var p Problem
	if !errors.As(err, &p) || p.Kind != KindRequired || !p.Fatal {
		t.Errorf("error = %#v, want the fatal required Problem", err)
	}
	if len(problems) != 1 || problems[0].Key != "vault.root" {
		t.Errorf("problems = %+v, want the one for vault.root", problems)
	}
}

func TestLoadDefaultsAndEnvOverride(t *testing.T) {
	xdgConfig := isolate(t)
	path := writeConfigFile(t, xdgConfig, "[vault]\nroot = \"/vault-from-file\"\n")

	cfg, problems, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(problems) != 0 {
		t.Errorf("problems = %+v, want none", problems)
	}
	if cfg.Vault.Root != "/vault-from-file" {
		t.Errorf("Root = %q", cfg.Vault.Root)
	}
	if cfg.Vault.Inbox != "nn" {
		t.Errorf("Inbox default = %q, want \"nn\"", cfg.Vault.Inbox)
	}
	if cfg.Path != path {
		t.Errorf("Path = %q, want %q", cfg.Path, path)
	}
	if e := entryFor(t, cfg, "vault.root"); e.Source != SourceFile {
		t.Errorf("vault.root source = %q, want file", e.Source)
	}
	if e := entryFor(t, cfg, "vault.inbox"); e.Source != SourceDefault || e.Value != "nn" {
		t.Errorf("vault.inbox = %+v, want the default nn", e)
	}

	withEnv(t, "NN_ROOT", "/vault-from-env")
	cfg, _, err = Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Vault.Root != "/vault-from-env" {
		t.Errorf("NN_ROOT override: Root = %q", cfg.Vault.Root)
	}
	if e := entryFor(t, cfg, "vault.root"); e.Source != "env NN_ROOT" {
		t.Errorf("vault.root source = %q, want env NN_ROOT", e.Source)
	}
}

func TestLoadExpandsHomeInRoot(t *testing.T) {
	xdgConfig := isolate(t)
	home := os.Getenv("HOME")
	writeConfigFile(t, xdgConfig, "[vault]\nroot = \"~/notes\"\n[ai.tasks.ask]\nprompt_file = \"~/prompts/ask.md\"\n")

	cfg, _, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(home, "notes"); cfg.Vault.Root != want {
		t.Errorf("Root = %q, want %q", cfg.Vault.Root, want)
	}
	if want := filepath.Join(home, "prompts", "ask.md"); cfg.AI.Tasks["ask"].PromptFile != want {
		t.Errorf("prompt_file = %q, want %q", cfg.AI.Tasks["ask"].PromptFile, want)
	}
}

func TestNNConfigOverridesPath(t *testing.T) {
	isolate(t)
	custom := filepath.Join(t.TempDir(), "custom.toml")
	if err := os.WriteFile(custom, []byte("[vault]\nroot = \"/somewhere\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	withEnv(t, "NN_CONFIG", custom)

	cfg, _, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Path != custom {
		t.Errorf("Path = %q, want %q", cfg.Path, custom)
	}
	if cfg.Vault.Root != "/somewhere" {
		t.Errorf("Root = %q", cfg.Vault.Root)
	}
}

func TestLoadSyntaxErrorIsFatal(t *testing.T) {
	xdgConfig := isolate(t)
	writeConfigFile(t, xdgConfig, "[vault]\nroot = \"/v\n")
	withEnv(t, "NN_ROOT", "/vault")

	_, problems, err := Load()
	if err == nil {
		t.Fatal("expected a syntax error")
	}
	if len(problems) != 1 || problems[0].Kind != KindSyntax || !problems[0].Fatal || problems[0].Fix == "" {
		t.Errorf("problems = %+v, want one fatal syntax problem with a fix", problems)
	}
}

func TestLoadOldRootNamesItsReplacement(t *testing.T) {
	xdgConfig := isolate(t)
	writeConfigFile(t, xdgConfig, "root = \"~/Notes\"\n")

	_, problems, err := Load()
	if err == nil {
		t.Fatal("expected an error: vault.root is not set")
	}
	for _, want := range []string{"vault.root", "[vault]", `root = "~/Notes"`} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error = %q, want it to mention %s", err, want)
		}
	}
	if len(problems) != 1 || problems[0].Kind != KindMoved || !problems[0].Fatal {
		t.Errorf("problems = %+v, want one fatal moved problem, not a second required one", problems)
	}
}

func TestLoadOldRootWithNNRootIsNotFatal(t *testing.T) {
	cfg, problems := loadBody(t, "root = \"/old\"\n")
	p := problemFor(t, problems, "root")
	if p.Kind != KindMoved || p.Fatal {
		t.Errorf("problem = %+v, want a moved warning", p)
	}
	if cfg.Vault.Root != "/vault" {
		t.Errorf("Root = %q, want NN_ROOT's, not the old key's", cfg.Vault.Root)
	}
}

func TestLoadMovedKeys(t *testing.T) {
	cfg, problems := loadBody(t, "inbox = \"notes\"\neditor = \"hx\"\nocr_langs = [\"rus\", \"eng\"]\n")
	for key, fix := range map[string]string{
		"inbox":     `move it under [vault]: inbox = "notes"`,
		"editor":    `move it under [editor]: command = "hx"`,
		"ocr_langs": `move it under [ocr]: langs = ["rus", "eng"]`,
	} {
		p := problemFor(t, problems, key)
		if p.Kind != KindMoved || p.Fix != fix {
			t.Errorf("%s: problem = %+v, want moved with fix %q", key, p, fix)
		}
	}
	// Old keys are not aliases: the new ones keep their defaults.
	if cfg.Vault.Inbox != "nn" || cfg.Editor.Command != "" || len(cfg.OCR.Langs) != 0 {
		t.Errorf("old keys leaked into the new ones: %+v %+v %+v", cfg.Vault, cfg.Editor, cfg.OCR)
	}
}

func TestLoadEditorSectionIsNotTheMovedKey(t *testing.T) {
	cfg, problems := loadBody(t, "[editor]\ncommand = \"hx\"\n")
	if len(problems) != 0 {
		t.Errorf("problems = %+v, want none", problems)
	}
	if cfg.Editor.Command != "hx" {
		t.Errorf("Editor.Command = %q", cfg.Editor.Command)
	}
}

func TestLoadReadsEveryKind(t *testing.T) {
	body := `
[vault]
inbox = "inbox"
[capture]
ocr = false
similar_threshold = 1
similar_limit = 5
[hooks]
post_save_timeout = "1m30s"
[ocr]
langs = ["rus"]
[state]
half_life = "2w"
[ai]
timeout = "90s"
`
	cfg, problems := loadBody(t, body)
	if len(problems) != 0 {
		t.Fatalf("problems = %+v, want none", problems)
	}
	if cfg.Vault.Inbox != "inbox" || cfg.Capture.OCR || cfg.Capture.SimilarThreshold != 1 || cfg.Capture.SimilarLimit != 5 {
		t.Errorf("capture/vault = %+v %+v", cfg.Vault, cfg.Capture)
	}
	if cfg.Hooks.PostSaveTimeout != 90*time.Second {
		t.Errorf("PostSaveTimeout = %v", cfg.Hooks.PostSaveTimeout)
	}
	if !slices.Equal(cfg.OCR.Langs, []string{"rus"}) {
		t.Errorf("Langs = %v", cfg.OCR.Langs)
	}
	if cfg.State.HalfLife != 14*24*time.Hour {
		t.Errorf("HalfLife = %v", cfg.State.HalfLife)
	}
	if e := entryFor(t, cfg, "state.half_life"); e.Value != "14d" || e.Source != SourceFile {
		t.Errorf("state.half_life entry = %+v, want 14d from the file", e)
	}
	if e := entryFor(t, cfg, "ai.timeout"); e.Value != "1m30s" {
		t.Errorf("ai.timeout entry = %+v", e)
	}
}

func TestLoadValueProblemsUseTheDefault(t *testing.T) {
	cases := []struct {
		name, body, key string
		kind            Kind
		get             func(Config) any
		want            any
	}{
		{"int as string", "[search]\nlimit = \"20\"\n", "search.limit", KindType,
			func(c Config) any { return c.Search.Limit }, 20},
		{"enum", "[ls]\nsort = \"size\"\n", "ls.sort", KindEnum,
			func(c Config) any { return c.Ls.Sort }, "modified"},
		{"empty enum", "[shot]\ntool = \"\"\n", "shot.tool", KindEnum,
			func(c Config) any { return c.Shot.Tool }, "auto"},
		{"float range", "[capture]\nsimilar_threshold = 1.5\n", "capture.similar_threshold", KindRange,
			func(c Config) any { return c.Capture.SimilarThreshold }, 0.6},
		{"int range", "[ai.context]\nsearch_rounds = 4\n", "ai.context.search_rounds", KindRange,
			func(c Config) any { return c.AI.Context.SearchRounds }, 1},
		{"int below min", "[ai.context]\nchars = 999\n", "ai.context.chars", KindRange,
			func(c Config) any { return c.AI.Context.Chars }, 12000},
		{"float for int", "[search]\nlimit = 20.0\n", "search.limit", KindType,
			func(c Config) any { return c.Search.Limit }, 20},
		{"bad duration", "[state]\nhalf_life = \"2 weeks\"\n", "state.half_life", KindType,
			func(c Config) any { return c.State.HalfLife }, 14 * 24 * time.Hour},
		{"zero duration", "[hooks]\npost_save_timeout = \"0s\"\n", "hooks.post_save_timeout", KindRange,
			func(c Config) any { return c.Hooks.PostSaveTimeout }, 10 * time.Second},
		{"empty duration", "[ai]\ntimeout = \"\"\n", "ai.timeout", KindType,
			func(c Config) any { return c.AI.Timeout }, 120 * time.Second},
		{"list as string", "[ocr]\nlangs = \"rus\"\n", "ocr.langs", KindType,
			func(c Config) any { return len(c.OCR.Langs) }, 0},
		{"list of ints", "[ocr]\nlangs = [1]\n", "ocr.langs", KindType,
			func(c Config) any { return len(c.OCR.Langs) }, 0},
		{"bool as string", "[notify]\nenabled = \"no\"\n", "notify.enabled", KindType,
			func(c Config) any { return c.Notify.Enabled }, true},
		{"blank inbox", "[vault]\ninbox = \" \"\n", "vault.inbox", KindRange,
			func(c Config) any { return c.Vault.Inbox }, "nn"},
		{"section as value", "search = 5\n", "search", KindType,
			func(c Config) any { return c.Search.Limit }, 20},
		{"profile effort", "[ai.profiles.work]\neffort = \"huge\"\n", "ai.profiles.work.effort", KindEnum,
			func(c Config) any { return c.AI.Profiles["work"].Effort }, ""},
		{"profile engine", "[ai.profiles.work]\nengine = \"gpt\"\n", "ai.profiles.work.engine", KindEnum,
			func(c Config) any { return c.AI.Profiles["work"].Engine }, "claude"},
		{"consent", "[ai.consent]\nclaude = \"yes\"\n", "ai.consent.claude", KindEnum,
			func(c Config) any { return c.AI.Consent["claude"] }, "ask"},
		{"task run", "[ai.tasks.shot]\nrun = \"sometimes\"\n", "ai.tasks.shot.run", KindEnum,
			func(c Config) any { return c.AI.Tasks["shot"].Run }, "always"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg, problems := loadBody(t, tc.body)
			p := problemFor(t, problems, tc.key)
			if p.Kind != tc.kind || p.Fatal {
				t.Errorf("problem = %+v, want non-fatal %s", p, tc.kind)
			}
			if p.Fix == "" {
				t.Errorf("problem %+v has no fix", p)
			}
			if tc.kind != KindType || tc.key != "search" {
				if !strings.Contains(p.Message, "using") {
					t.Errorf("message = %q, want it to say what nn uses instead", p.Message)
				}
			}
			if got := tc.get(cfg); got != tc.want {
				t.Errorf("value = %#v, want the default %#v", got, tc.want)
			}
		})
	}
}

func TestLoadValueProblemFixSetsTheDefault(t *testing.T) {
	_, problems := loadBody(t, "[search]\nlimit = \"20\"\n[ocr]\nlangs = \"rus\"\n")
	if p := problemFor(t, problems, "search.limit"); p.Fix != "nn config search.limit 20" {
		t.Errorf("fix = %q", p.Fix)
	}
	if p := problemFor(t, problems, "ocr.langs"); p.Fix != "set it by hand under [ocr]: langs = []" {
		t.Errorf("fix = %q", p.Fix)
	}
}

func TestLoadUnknownKeys(t *testing.T) {
	body := `
[search]
limt = 5
[serach]
limit = 5
[zzzzzz]
foo = 1
[ai.profiles.work]
modle = "x"
[ai.tasks.ask]
run = "flag"
[ai.tasks.shoot]
run = "flag"
`
	cfg, problems := loadBody(t, body)
	for key, fix := range map[string]string{
		"search.limt":            "did you mean search.limit?",
		"serach":                 "did you mean search?",
		"ai.profiles.work.modle": "did you mean ai.profiles.work.model?",
		"ai.tasks.shoot":         "did you mean ai.tasks.shot?",
		"ai.tasks.ask.run":       "remove it",
		"zzzzzz":                 `remove it; "nn config --defaults" lists every key`,
	} {
		p := problemFor(t, problems, key)
		if p.Kind != KindUnknown || p.Fatal || p.Fix != fix {
			t.Errorf("%s: problem = %+v, want unknown with fix %q", key, p, fix)
		}
	}
	if !strings.Contains(problemFor(t, problems, "ai.tasks.ask.run").Message, "shot, title") {
		t.Errorf("ai.tasks.ask.run message should name the tasks run applies to")
	}
	if cfg.Search.Limit != 20 {
		t.Errorf("unknown keys changed search.limit: %d", cfg.Search.Limit)
	}
}

func TestLoadReservedTUI(t *testing.T) {
	_, problems := loadBody(t, "[tui]\ntheme = \"dark\"\n")
	if len(problems) != 1 {
		t.Fatalf("problems = %+v, want exactly one", problems)
	}
	p := problems[0]
	if p.Kind != KindReserved || p.Key != "tui" || p.Fatal || !strings.Contains(p.Message, "reserved, ignored") {
		t.Errorf("problem = %+v, want reserved, ignored", p)
	}
}

func TestLoadRefProblems(t *testing.T) {
	body := `
[ai]
profile = "missing"
[ai.profiles.claude]
engine = "codex"
[ai.profiles.local]
engine = "command"
[ai.tasks.shot]
profile = "nope"
[ai.consent]
gemini = "always"
local = "never"
`
	cfg, problems := loadBody(t, body)
	for _, key := range []string{"ai.profile", "ai.profiles.claude.engine", "ai.profiles.local.command", "ai.tasks.shot.profile", "ai.consent.gemini"} {
		if p := problemFor(t, problems, key); p.Kind != KindRef || p.Fatal || p.Fix == "" {
			t.Errorf("%s: problem = %+v, want non-fatal ref with a fix", key, p)
		}
	}
	if cfg.AI.Profile != "claude" {
		t.Errorf("ai.profile = %q, want the default", cfg.AI.Profile)
	}
	if cfg.AI.Profiles["claude"].Engine != "claude" {
		t.Errorf("built-in claude engine = %q", cfg.AI.Profiles["claude"].Engine)
	}
	if cfg.AI.Tasks["shot"].Profile != "" {
		t.Errorf("shot profile = %q, want empty (ai.profile)", cfg.AI.Tasks["shot"].Profile)
	}
	if _, ok := cfg.AI.Consent["gemini"]; ok {
		t.Errorf("consent for an unknown engine was kept")
	}
	if cfg.AI.Consent["local"] != "never" {
		t.Errorf("consent for the command profile = %q, want never", cfg.AI.Consent["local"])
	}
	for _, p := range problems {
		if p.Key == "ai.consent.local" {
			t.Errorf("consent for a command profile is valid, got %+v", p)
		}
	}
}

func TestLoadDeclaredProfile(t *testing.T) {
	body := `
[ai]
profile = "local"
[ai.profiles.local]
engine = "command"
command = ["llm", "{prompt}"]
timeout = "5m"
[ai.profiles.fast]
model = "haiku"
[ai.tasks.title]
profile = "fast"
run = "flag"
`
	cfg, problems := loadBody(t, body)
	if len(problems) != 0 {
		t.Fatalf("problems = %+v, want none", problems)
	}
	local := cfg.AI.Profiles["local"]
	if local.Engine != "command" || !slices.Equal(local.Command, []string{"llm", "{prompt}"}) || local.Timeout != 5*time.Minute {
		t.Errorf("local = %+v", local)
	}
	if fast := cfg.AI.Profiles["fast"]; fast.Engine != "claude" || fast.Model != "haiku" {
		t.Errorf("fast = %+v, want the default engine and its own model", fast)
	}
	if cfg.AI.Consent["local"] != "ask" {
		t.Errorf("consent for local = %q, want ask", cfg.AI.Consent["local"])
	}
	if _, ok := cfg.AI.Consent["fast"]; ok {
		t.Errorf("consent is per engine: a claude profile gets no entry of its own")
	}
	if cfg.AI.Profiles["codex"].Engine != "codex" {
		t.Errorf("built-in codex engine = %q", cfg.AI.Profiles["codex"].Engine)
	}
	if e := entryFor(t, cfg, "ai.profiles.local.engine"); e.Source != SourceFile {
		t.Errorf("entry = %+v", e)
	}
	if e := entryFor(t, cfg, "ai.profiles.fast.engine"); e.Source != SourceDefault {
		t.Errorf("entry = %+v", e)
	}
	if t2 := cfg.AI.Tasks["title"]; t2.Profile != "fast" || t2.Run != "flag" {
		t.Errorf("title = %+v", t2)
	}
}

func TestEntriesListEveryKey(t *testing.T) {
	cfg, _ := loadBody(t, "[ai.profiles.work]\nmodel = \"m\"\n")
	var keys []string
	for _, e := range cfg.Entries() {
		keys = append(keys, e.Key)
	}
	for _, want := range []string{
		"vault.root", "ai.context.chars",
		"ai.profiles.claude.engine", "ai.profiles.codex.timeout", "ai.profiles.work.model",
		"ai.tasks.shot.image", "ai.tasks.title.run", "ai.tasks.digest.prompt_file",
		"ai.consent.claude", "ai.consent.codex",
	} {
		if !slices.Contains(keys, want) {
			t.Errorf("entries lack %s", want)
		}
	}
	for _, unwanted := range []string{"ai.tasks.ask.run", "ai.tasks.title.image", "ai.consent.work", "tui"} {
		if slices.Contains(keys, unwanted) {
			t.Errorf("entries have %s", unwanted)
		}
	}
	if i, j := slices.Index(keys, "ai.profiles.codex.engine"), slices.Index(keys, "ai.profiles.work.engine"); i > j {
		t.Errorf("built-in profiles should come first: %v", keys)
	}
}

func TestGetExplainsAMiss(t *testing.T) {
	cfg := Default()
	for key, want := range map[string]string{
		"root":               "renamed to vault.root",
		"search.limt":        "did you mean search.limit?",
		"vault":              "a section",
		"tui.theme":          "reserved",
		"ai.tasks.ask.image": "shot only",
	} {
		_, err := cfg.Get(key)
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("Get(%s) = %v, want an error mentioning %q", key, err, want)
		}
	}
	var notSet *NotSetError
	if _, err := cfg.Get("ai.profiles.work.model"); !errors.As(err, &notSet) {
		t.Errorf("Get of an undeclared profile = %v, want *NotSetError", err)
	}
}

func TestParseDuration(t *testing.T) {
	for in, want := range map[string]time.Duration{
		"10s": 10 * time.Second, "1m30s": 90 * time.Second, "14d": 14 * 24 * time.Hour,
		"2w": 14 * 24 * time.Hour, "36h": 36 * time.Hour,
	} {
		got, err := ParseDuration(in)
		if err != nil || got != want {
			t.Errorf("ParseDuration(%q) = %v, %v; want %v", in, got, err, want)
		}
	}
	for _, in := range []string{"", "d", "1.5d", "2 weeks", "1x"} {
		if _, err := ParseDuration(in); err == nil {
			t.Errorf("ParseDuration(%q) succeeded", in)
		}
	}
	for d, want := range map[time.Duration]string{
		14 * 24 * time.Hour: "14d", 10 * time.Second: "10s", 2 * time.Minute: "2m",
		36 * time.Hour: "36h", 90 * time.Minute: "1h30m",
	} {
		if got := formatDuration(d); got != want {
			t.Errorf("formatDuration(%v) = %q, want %q", d, got, want)
		}
	}
}

func TestLoadCommandWithoutTheCommandEngine(t *testing.T) {
	body := `
[ai.profiles.work]
command = ["llm", "{prompt}"]
[ai.profiles.codex]
command = ["llm"]
[ai.profiles.local]
engine = "command"
command = ["llm"]
`
	cfg, problems := loadBody(t, body)
	for key, want := range map[string]struct{ message, fix string }{
		"ai.profiles.work.command": {
			`ai.profiles.work.command is set, but engine is "claude"; ignored`,
			"nn config ai.profiles.work.engine command",
		},
		"ai.profiles.codex.command": {
			`ai.profiles.codex.command is set, but engine is "codex"; ignored`,
			"remove command from [ai.profiles.codex]",
		},
	} {
		p := problemFor(t, problems, key)
		if p.Kind != KindRef || p.Fatal || p.Message != want.message || p.Fix != want.fix {
			t.Errorf("%s: problem = %+v, want ref %q with fix %q", key, p, want.message, want.fix)
		}
		if e := entryFor(t, cfg, key); e.Source != SourceDefault {
			t.Errorf("%s: entry = %+v, want the ignored command back at its default", key, e)
		}
	}
	for _, p := range problems {
		if p.Key == "ai.profiles.local.command" {
			t.Errorf("a command profile's command is valid, got %+v", p)
		}
	}
	if got := cfg.AI.Profiles["local"].Command; !slices.Equal(got, []string{"llm"}) {
		t.Errorf("local command = %q", got)
	}
}

func TestLoadBadConsentValueUnderAnUnknownName(t *testing.T) {
	body := `
[ai.profiles.local]
engine = "command"
command = ["llm"]
[ai.profiles.work]
engine = "codex"
[ai.consent]
claude = "yes"
local = "yes"
work = "yes"
gemini = "yes"
[ai.consent.foo]
x = 1
`
	_, problems := loadBody(t, body)
	for key, fix := range map[string]string{
		"ai.consent.claude": "nn config ai.consent.claude ask",
		"ai.consent.local":  "nn config ai.consent.local ask",
		"ai.consent.work":   "remove it",
		"ai.consent.gemini": "remove it",
		"ai.consent.foo":    "remove it",
	} {
		if p := problemFor(t, problems, key); p.Fix != fix {
			t.Errorf("%s: fix = %q, want %q", key, p.Fix, fix)
		}
	}
}

// TestLoadFixForAQuotedKey: nn config splits a key at every dot and does
// not read TOML quotes, so a name TOML has to quote gets the line to add
// by hand instead of an nn config command.
func TestLoadFixForAQuotedKey(t *testing.T) {
	body := `
[ai.profiles."a.b"]
engine = "command"
command = ["llm"]
timeout = "soon"
[ai.profiles.work]
engine = "codex"
timeout = "soon"
[ai.consent]
"a.b" = "yes"
claude = "yes"
`
	_, problems := loadBody(t, body)
	for key, fix := range map[string]string{
		`ai.consent."a.b"`:          `set it by hand under [ai.consent]: "a.b" = "ask"`,
		`ai.profiles."a.b".timeout`: `set it by hand under [ai.profiles."a.b"]: timeout = ""`,
		"ai.consent.claude":         "nn config ai.consent.claude ask",
		"ai.profiles.work.timeout":  `nn config ai.profiles.work.timeout ""`,
	} {
		if p := problemFor(t, problems, key); p.Fix != fix {
			t.Errorf("%s: fix = %q, want %q", key, p.Fix, fix)
		}
	}
}

func TestLoadEngineFixForAQuotedProfile(t *testing.T) {
	_, problems := loadBody(t, "[ai.profiles.\"a.b\"]\nengine = \"claude\"\ncommand = [\"llm\"]\n")
	p := problemFor(t, problems, `ai.profiles."a.b".command`)
	want := `set it by hand under [ai.profiles."a.b"]: engine = "command"`
	if p.Kind != KindRef || p.Message != `ai.profiles."a.b".command is set, but engine is "claude"; ignored` || p.Fix != want {
		t.Errorf("problem = %+v, want fix %q", p, want)
	}
}

func TestLoadPromptFileWithoutHome(t *testing.T) {
	xdgConfig := isolate(t)
	writeConfigFile(t, xdgConfig, "[ai.tasks.ask]\nprompt_file = \"~/prompts/ask.md\"\n[ai.tasks.title]\nprompt_file = \"/abs/title.md\"\n")
	withEnv(t, "NN_ROOT", "/vault")
	withEnv(t, "HOME", "")

	cfg, problems, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	p := problemFor(t, problems, "ai.tasks.ask.prompt_file")
	if p.Kind != KindType || p.Fatal || !strings.Contains(p.Message, `using ""`) || !strings.Contains(p.Fix, "nn config ai.tasks.ask.prompt_file") {
		t.Errorf("problem = %+v, want a non-fatal type problem using the default", p)
	}
	if got := cfg.AI.Tasks["ask"].PromptFile; got != "" {
		t.Errorf("ask prompt_file = %q, want the default", got)
	}
	if e := entryFor(t, cfg, "ai.tasks.ask.prompt_file"); e.Source != SourceDefault {
		t.Errorf("entry = %+v, want source default", e)
	}
	if got := cfg.AI.Tasks["title"].PromptFile; got != "/abs/title.md" {
		t.Errorf("title prompt_file = %q, want it untouched", got)
	}
}

func TestTasksAndBuiltinProfilesAreCopies(t *testing.T) {
	got := Tasks()
	if !slices.Equal(got, tasks) || len(got) == 0 {
		t.Fatalf("Tasks() = %q, want %q", got, tasks)
	}
	got[0] = "changed"
	_ = append(got[:1], "appended")
	if Tasks()[0] != "shot" || slices.Contains(tasks, "changed") || slices.Contains(tasks, "appended") {
		t.Errorf("changing the result of Tasks() changed the tasks: %q", tasks)
	}

	profiles := BuiltinProfiles()
	if !slices.Equal(profiles, []string{"claude", "codex"}) {
		t.Fatalf("BuiltinProfiles() = %q", profiles)
	}
	profiles[0] = "changed"
	if BuiltinProfiles()[0] != "claude" {
		t.Errorf("changing the result of BuiltinProfiles() changed the profiles: %q", builtinProfiles)
	}
	if _, ok := Default().AI.Profiles["claude"]; !ok {
		t.Errorf("Default() lost the built-in claude profile: %v", Default().AI.Profiles)
	}
}

func TestBuiltinClaudeModel(t *testing.T) {
	cfg, problems := loadBody(t, "[ai.profiles.work]\nengine = \"claude\"\n")
	if len(problems) != 0 {
		t.Fatalf("problems = %+v, want none", problems)
	}
	if e := entryFor(t, cfg, "ai.profiles.claude.model"); e.Value != "sonnet" || e.Source != SourceDefault {
		t.Errorf("claude model entry = %+v, want sonnet from default", e)
	}
	for _, name := range []string{"codex", "work"} {
		if got := cfg.AI.Profiles[name].Model; got != "" {
			t.Errorf("%s model = %q, want empty", name, got)
		}
	}
	if got := Default().AI.Profiles["claude"].Model; got != "sonnet" {
		t.Errorf("Default() claude model = %q", got)
	}

	cfg, _ = loadBody(t, "[ai.profiles.claude]\nmodel = \"\"\n")
	if e := entryFor(t, cfg, "ai.profiles.claude.model"); e.Value != "" || e.Source != SourceFile {
		t.Errorf("claude model entry = %+v, want empty from the file", e)
	}

	cfg, problems = loadBody(t, "[ai.profiles.claude]\nmodel = 5\n")
	p := problemFor(t, problems, "ai.profiles.claude.model")
	if !strings.Contains(p.Message, `using "sonnet"`) || p.Fix != "nn config ai.profiles.claude.model sonnet" {
		t.Errorf("problem = %+v, want it to name the model the profile keeps", p)
	}
	if got := cfg.AI.Profiles["claude"].Model; got != "sonnet" {
		t.Errorf("claude model after a bad value = %q, want sonnet", got)
	}
}
