// Package config loads and edits nn's TOML configuration file, driven by
// one schema (schema.go).
package config

import (
	"errors"
	"fmt"
	"io/fs"
	"math/rand/v2"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
)

const dirName = "nn"
const fileName = "config.toml"

// Config: every field with a key tag is a schema key or section of that name.
type Config struct {
	Vault    Vault    `key:"vault"`
	Editor   Editor   `key:"editor"`
	Capture  Capture  `key:"capture"`
	Hooks    Hooks    `key:"hooks"`
	Shot     Shot     `key:"shot"`
	OCR      OCR      `key:"ocr"`
	Search   Search   `key:"search"`
	Ls       Ls       `key:"ls"`
	Show     Show     `key:"show"`
	Graph    Graph    `key:"graph"`
	Stats    Stats    `key:"stats"`
	Output   Output   `key:"output"`
	Obsidian Obsidian `key:"obsidian"`
	State    State    `key:"state"`
	Notify   Notify   `key:"notify"`
	AI       AI       `key:"ai"`

	Path string // loaded from, or would be written to; may not exist yet

	sources map[string]Source // by dotted key; absent means default
}

type Vault struct {
	Root  string `key:"root"`
	Inbox string `key:"inbox"`
}

type Editor struct {
	Command string `key:"command"`
}

type Capture struct {
	OCR              bool    `key:"ocr"`
	SimilarThreshold float64 `key:"similar_threshold"`
	SimilarLimit     int     `key:"similar_limit"`
}

type Hooks struct {
	PostSave        string        `key:"post_save"`
	PostSaveTimeout time.Duration `key:"post_save_timeout"`
}

type Shot struct {
	CopyText bool   `key:"copy_text"`
	Tool     string `key:"tool"`
}

type OCR struct {
	Langs        []string `key:"langs"`
	TesseractPSM int      `key:"tesseract_psm"`
}

type Search struct {
	Limit          int  `key:"limit"`
	LayoutFallback bool `key:"layout_fallback"`
}

type Ls struct {
	Sort  string `key:"sort"`
	Limit int    `key:"limit"`
}

type Show struct {
	Images string `key:"images"`
	OCR    bool   `key:"ocr"`
}

type Graph struct {
	Format string `key:"format"`
}

type Stats struct {
	By string `key:"by"`
}

type Output struct {
	Color string `key:"color"`
}

type Obsidian struct {
	Check bool `key:"check"`
}

type State struct {
	TrackOpens bool          `key:"track_opens"`
	HalfLife   time.Duration `key:"half_life"`
}

type Notify struct {
	Enabled bool `key:"enabled"`
}

type AI struct {
	Mode    string        `key:"mode"`
	Profile string        `key:"profile"`
	Timeout time.Duration `key:"timeout"`
	Context AIContext     `key:"context"`

	Profiles map[string]Profile `key:"profiles"` // built-ins plus declared
	Tasks    map[string]Task    `key:"tasks"`    // always every Tasks()
	Consent  map[string]string  `key:"consent"`  // built-ins plus EngineCommand profiles
}

type AIContext struct {
	Notes        int `key:"notes"`
	Chars        int `key:"chars"`
	SearchRounds int `key:"search_rounds"`
}

type Profile struct {
	Engine  string        `key:"engine"`
	Model   string        `key:"model"`
	Effort  string        `key:"effort"`
	Command []string      `key:"command"`
	Timeout time.Duration `key:"timeout"`
}

type Task struct {
	Profile    string `key:"profile"`
	Run        string `key:"run"`
	PromptFile string `key:"prompt_file"`
	Image      string `key:"image"`
}

// Source is "default", "file", or "env NAME".
type Source string

const (
	SourceDefault Source = "default"
	SourceFile    Source = "file"
)

func sourceEnv(name string) Source { return Source("env " + name) }

type Kind string

const (
	KindSyntax   Kind = "syntax"   // the file is not valid TOML
	KindUnknown  Kind = "unknown"  // a key the schema does not have
	KindMoved    Kind = "moved"    // a key of the old flat format
	KindType     Kind = "type"     // a value of the wrong type
	KindEnum     Kind = "enum"     // a value outside the allowed set
	KindRange    Kind = "range"    // a number or duration out of range
	KindRef      Kind = "ref"      // a name that points at nothing
	KindReserved Kind = "reserved" // a section nn accepts and ignores
	KindRequired Kind = "required" // vault.root, set nowhere
)

// Problem: only Fatal stops a command; any other leaves its key at the default.
type Problem struct {
	Kind    Kind
	Key     string
	Message string
	Fix     string // a command, or the line to add and its section
	Fatal   bool

	detail string // Message without what nn did about it
}

func (p Problem) Error() string {
	if p.Fix == "" {
		return p.Message
	}
	return p.Message + "; " + p.Fix
}

// Path: $NN_CONFIG, else $XDG_CONFIG_HOME/nn/config.toml, else ~/.config/nn/config.toml.
func Path() (string, error) {
	if override := strings.TrimSpace(os.Getenv("NN_CONFIG")); override != "" {
		return expandHome(override)
	}
	if dir := os.Getenv("XDG_CONFIG_HOME"); filepath.IsAbs(dir) {
		return filepath.Join(dir, dirName, fileName), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".config", dirName, fileName), nil
}

func Default() Config {
	cfg := Config{AI: AI{
		Profiles: map[string]Profile{},
		Tasks:    map[string]Task{},
		Consent:  map[string]string{},
	}}
	for _, name := range builtinProfiles {
		cfg.AI.Profiles[name] = defaultProfile(name)
	}
	for _, task := range tasks {
		cfg.AI.Tasks[task] = defaultTask(task)
	}
	for _, name := range builtinProfiles {
		cfg.AI.Consent[name] = defaultOf("ai.consent.KEY").(string)
	}
	rv := reflect.ValueOf(&cfg).Elem()
	for i := range schema {
		k := &schema[i]
		if k.Status == StatusReserved || slices.ContainsFunc(k.segs, isPlaceholder) {
			continue
		}
		mustSet(rv, k.segs, defaultOf(k.Path))
	}
	return cfg
}

func defaultProfile(name string) Profile {
	var p Profile
	fillDefaults(reflect.ValueOf(&p).Elem(), "ai.profiles.NAME.")
	if slices.Contains(builtinProfiles, name) {
		p.Engine = name
		if model, ok := builtinModels[name]; ok {
			p.Model = model
		}
	}
	return p
}

func defaultTask(task string) Task {
	var t Task
	prefix := "ai.tasks.TASK."
	rv := reflect.ValueOf(&t).Elem()
	for i := range schema {
		k := &schema[i]
		leaf, ok := strings.CutPrefix(k.Path, prefix)
		if !ok || (k.Tasks != nil && !slices.Contains(k.Tasks, task)) {
			continue
		}
		mustSet(rv, []string{leaf}, defaultOf(k.Path))
	}
	return t
}

func fillDefaults(v reflect.Value, prefix string) {
	for i := range schema {
		k := &schema[i]
		if leaf, ok := strings.CutPrefix(k.Path, prefix); ok {
			mustSet(v, []string{leaf}, defaultOf(k.Path))
		}
	}
}

func mustSet(v reflect.Value, path []string, val any) {
	if !setIn(v, path, val) {
		panic(fmt.Sprintf("config: no field for %s", strings.Join(path, ".")))
	}
}

func defaultOf(pattern string) any {
	for i := range schema {
		if k := &schema[i]; k.Path == pattern {
			val, _, _ := convert(k, k.Default)
			return val
		}
	}
	panic("config: no schema key " + pattern)
}

// Load: err is non-nil only for what no command can run without - a file
// that does not parse, or a vault.root set nowhere - and is the fatal Problem.
func Load() (cfg Config, problems []Problem, err error) {
	path, err := Path()
	if err != nil {
		return Default(), nil, err
	}

	var doc map[string]any
	src, err := os.ReadFile(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
	case err != nil:
		cfg = Default()
		cfg.Path = path
		return cfg, nil, err
	default:
		if doc, err = decode(src); err != nil {
			cfg = Default()
			cfg.Path = path
			p := Problem{
				Kind:    KindSyntax,
				Message: fmt.Sprintf("parse %s: %v", path, err),
				Fix:     "fix that line in the file",
				Fatal:   true,
			}
			return cfg, []Problem{p}, p
		}
	}

	c := check(doc)
	cfg, problems = c.cfg, c.problems
	cfg.Path = path

	for i := range schema {
		k := &schema[i]
		if k.Env == "" {
			continue
		}
		raw := strings.TrimSpace(os.Getenv(k.Env))
		if raw == "" {
			continue
		}
		val, kind, want := convert(k, raw)
		if kind != "" {
			return cfg, problems, fmt.Errorf("%s=%q: want %s", k.Env, raw, want)
		}
		mustSet(reflect.ValueOf(&cfg).Elem(), k.segs, val)
		cfg.sources[k.Path] = sourceEnv(k.Env)
	}

	if cfg.Vault.Root, err = expandHome(cfg.Vault.Root); err != nil {
		return cfg, problems, err
	}
	// A prompt_file nn cannot expand only affects the task that reads it.
	for _, name := range tasks {
		t := cfg.AI.Tasks[name]
		expanded, err := expandHome(t.PromptFile)
		if err != nil {
			key := "ai.tasks." + name + ".prompt_file"
			def := defaultOf("ai.tasks.TASK.prompt_file")
			detail := fmt.Sprintf("%s = %s: cannot expand ~: %v", key, literal(t.PromptFile), err)
			problems = append(problems, Problem{
				Kind:    KindType,
				Key:     key,
				Message: detail + "; using " + literal(def),
				Fix:     fmt.Sprintf("set HOME, or give an absolute path: nn config %s PATH", key),
				detail:  detail,
			})
			expanded = def.(string)
			delete(cfg.sources, key)
		}
		t.PromptFile = expanded
		cfg.AI.Tasks[name] = t
	}

	if cfg.Vault.Root == "" {
		p := rootProblem(problems)
		if i := slices.IndexFunc(problems, func(q Problem) bool { return q.Kind == KindMoved && q.Key == "root" }); i >= 0 {
			problems[i] = p
		} else {
			problems = append(problems, p)
		}
		return cfg, problems, p
	}
	return cfg, problems, nil
}

func rootProblem(problems []Problem) Problem {
	for _, q := range problems {
		if q.Kind == KindMoved && q.Key == "root" {
			q.Message = "vault.root is not set, and root is its old name"
			q.Fatal = true
			return q
		}
	}
	return Problem{
		Kind:    KindRequired,
		Key:     "vault.root",
		Message: "vault.root is not set",
		Fix:     `run "nn config vault.root PATH"`,
		Fatal:   true,
	}
}

func decode(src []byte) (map[string]any, error) {
	doc := map[string]any{}
	if _, err := toml.Decode(string(src), &doc); err != nil {
		return nil, err
	}
	return doc, nil
}

type Entry struct {
	Key    string `json:"key"`
	Value  any    `json:"value"`
	Source Source `json:"source"`
}

func (e Entry) TOML() string { return literal(e.Value) }

func (c Config) Entries() []Entry {
	var out []Entry
	rv := reflect.ValueOf(c)
	add := func(path []string) {
		val, ok := getIn(rv, path)
		if !ok {
			return
		}
		key := joinKey(path)
		src, ok := c.sources[key]
		if !ok {
			src = SourceDefault
		}
		out = append(out, Entry{Key: key, Value: displayValue(val), Source: src})
	}
	done := map[string]bool{}
	for i := range schema {
		k := &schema[i]
		if k.Status == StatusReserved {
			continue
		}
		p := slices.IndexFunc(k.segs, isPlaceholder)
		if p < 0 {
			add(k.segs)
			continue
		}
		group := strings.Join(k.segs[:p+1], ".")
		if done[group] {
			continue
		}
		done[group] = true
		for _, name := range c.names(k.segs[:p+1]) {
			for j := i; j < len(schema); j++ {
				g := &schema[j]
				if len(g.segs) <= p || strings.Join(g.segs[:p+1], ".") != group {
					continue
				}
				path := slices.Clone(g.segs)
				path[p] = name
				if _, ok := lookup(path); ok {
					add(path)
				}
			}
		}
	}
	return out
}

func (c Config) names(pattern []string) []string {
	var builtin []string
	var m reflect.Value
	switch pattern[len(pattern)-1] {
	case placeholderTask:
		return slices.Clone(tasks)
	case placeholderName:
		builtin, m = builtinProfiles, reflect.ValueOf(c.AI.Profiles)
	case placeholderKey:
		builtin, m = builtinProfiles, reflect.ValueOf(c.AI.Consent)
	}
	var rest []string
	for _, k := range m.MapKeys() {
		if name := k.String(); !slices.Contains(builtin, name) {
			rest = append(rest, name)
		}
	}
	sort.Strings(rest)
	var out []string
	for _, name := range builtin {
		if m.MapIndex(reflect.ValueOf(name)).IsValid() {
			out = append(out, name)
		}
	}
	return append(out, rest...)
}

// Get fails for a key the schema lacks, or a dynamic entry cfg does not hold.
func (c Config) Get(key string) (Entry, error) {
	segs := strings.Split(key, ".")
	if _, err := schemaKey(segs); err != nil {
		return Entry{}, err
	}
	for _, e := range c.Entries() {
		if e.Key == joinKey(segs) {
			return e, nil
		}
	}
	return Entry{}, &NotSetError{Key: key}
}

func Lookup(key string) (Key, error) {
	k, err := schemaKey(strings.Split(key, "."))
	if err != nil {
		return Key{}, err
	}
	out := *k
	out.segs = nil
	return out, nil
}

type NotSetError struct{ Key string }

func (e *NotSetError) Error() string { return e.Key + " is not set" }

func schemaKey(segs []string) (*Key, error) {
	if k, ok := lookup(segs); ok {
		return k, nil
	}
	key := joinKey(segs)
	for _, m := range moved {
		if key == m.old {
			return nil, fmt.Errorf("%s was renamed to %s", m.old, m.new)
		}
	}
	if segs[0] == "tui" {
		return nil, fmt.Errorf("%s: [tui] is reserved, and nn reads nothing from it yet", key)
	}
	if k, ok := lookupAnyTask(segs); ok {
		return nil, fmt.Errorf("%s: %s applies to %s only", key, k.segs[len(k.segs)-1], strings.Join(k.Tasks, ", "))
	}
	if isTable(segs) {
		return nil, fmt.Errorf("%s is a section, not a key", key)
	}
	if s := suggest(segs); s != "" {
		return nil, fmt.Errorf("unknown key %s; did you mean %s?", key, s)
	}
	return nil, fmt.Errorf("unknown key %s; nn config lists every key", key)
}

func displayValue(val any) any {
	switch x := val.(type) {
	case time.Duration:
		return formatDuration(x)
	case []string:
		if x == nil {
			return []string{}
		}
	}
	return val
}

// atomicWrite: temp file synced before the rename; permission bits kept.
func atomicWrite(path string, data []byte) error {
	dir := filepath.Dir(path)
	tmp, err := createTemp(dir)
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)

	_, writeErr := tmp.Write(data)
	var chmodErr error
	if info, err := os.Stat(path); err == nil {
		chmodErr = tmp.Chmod(info.Mode().Perm())
	}
	syncErr := tmp.Sync()
	closeErr := tmp.Close()
	if err := errors.Join(writeErr, chmodErr, syncErr, closeErr); err != nil {
		return err
	}
	return os.Rename(tmpPath, path)
}

// createTemp asks for 0644, not CreateTemp's 0600, so the umask applies normally.
func createTemp(dir string) (*os.File, error) {
	for range 10000 {
		name := filepath.Join(dir, ".nn-config-"+strconv.FormatUint(uint64(rand.Uint32()), 36)+".tmp")
		f, err := os.OpenFile(name, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o644)
		if errors.Is(err, fs.ErrExist) {
			continue
		}
		return f, err
	}
	return nil, fmt.Errorf("create a temp file in %s: too many attempts", dir)
}

func expandHome(path string) (string, error) {
	if path == "" {
		return "", nil
	}
	if path != "~" && !strings.HasPrefix(path, "~/") {
		return path, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	if path == "~" {
		return home, nil
	}
	return filepath.Join(home, path[2:]), nil
}
