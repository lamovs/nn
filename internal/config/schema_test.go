package config

import (
	"flag"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"
)

var update = flag.Bool("update", false, "rewrite the config key table in README.md from the schema")

// structPaths lists every key path Config's fields map to, with a map's
// keys as "*".
func structPaths(t reflect.Type, prefix string) []string {
	var out []string
	for i := range t.NumField() {
		f := t.Field(i)
		tag, ok := f.Tag.Lookup("key")
		if !ok {
			continue
		}
		path := tag
		if prefix != "" {
			path = prefix + "." + tag
		}
		ft := f.Type
		if ft.Kind() == reflect.Map {
			path += ".*"
			ft = ft.Elem()
		}
		if ft.Kind() == reflect.Struct && ft != reflect.TypeOf(time.Duration(0)) {
			out = append(out, structPaths(ft, path)...)
			continue
		}
		out = append(out, path)
	}
	return out
}

func TestSchemaMatchesConfig(t *testing.T) {
	fields := structPaths(reflect.TypeOf(Config{}), "")
	var keys []string
	for _, k := range schema {
		if k.Status == StatusReserved {
			continue
		}
		segs := slices.Clone(k.segs)
		for i, s := range segs {
			if isPlaceholder(s) {
				segs[i] = "*"
			}
		}
		keys = append(keys, strings.Join(segs, "."))
	}
	slices.Sort(fields)
	slices.Sort(keys)
	if !slices.Equal(fields, keys) {
		t.Errorf("Config fields and schema keys differ:\nfields: %v\nschema: %v", fields, keys)
	}
}

func TestSchemaKeysAreWellFormed(t *testing.T) {
	seen := map[string]bool{}
	for _, k := range schema {
		if seen[k.Path] {
			t.Errorf("%s is in the schema twice", k.Path)
		}
		seen[k.Path] = true
		if k.Doc == "" {
			t.Errorf("%s has no doc", k.Path)
		}
		for _, s := range []string{k.Path, k.Doc, strings.Join(k.Enum, "")} {
			if strings.IndexFunc(s, func(r rune) bool { return r > 0x7e }) >= 0 {
				t.Errorf("%s: non-ASCII text %q", k.Path, s)
			}
		}
		if k.Status == StatusReserved {
			continue
		}
		if _, kind, want := convert(&k, k.Default); kind != "" {
			t.Errorf("%s: default %#v is invalid: want %s", k.Path, k.Default, want)
		}
		if (k.Type == TypeEnum) != (len(k.Enum) > 0) {
			t.Errorf("%s: enum values on a %s key", k.Path, k.Type)
		}
		segs := slices.Clone(k.segs)
		for i, s := range segs {
			switch s {
			case placeholderName:
				segs[i] = "work"
			case placeholderKey:
				segs[i] = "claude"
			case placeholderTask:
				segs[i] = "shot"
			}
		}
		if got, ok := lookup(segs); !ok || got.Path != k.Path {
			t.Errorf("lookup(%v) = %v, %v; want %s", segs, got, ok, k.Path)
		}
	}
	for _, m := range moved {
		if _, ok := seen[m.new]; !ok {
			t.Errorf("moved key %s points at %s, which is not in the schema", m.old, m.new)
		}
	}
}

func TestDefaultFileLoadsWithoutProblems(t *testing.T) {
	cfg, problems := loadBody(t, string(DefaultFile()))
	if len(problems) != 0 {
		t.Fatalf("problems = %+v, want none", problems)
	}
	def := Default()
	def.Vault.Root = "/vault"
	got, want := cfg.Entries(), def.Entries()
	if len(got) != len(want) {
		t.Fatalf("entries: %d from the file, %d by default", len(got), len(want))
	}
	for i := range got {
		if got[i].Key != want[i].Key || !reflect.DeepEqual(got[i].Value, want[i].Value) {
			t.Errorf("%s = %#v from --defaults, want %#v", got[i].Key, got[i].Value, want[i].Value)
		}
	}
	for _, line := range strings.Split(string(DefaultFile()), "\n") {
		if strings.IndexFunc(line, func(r rune) bool { return r > 0x7e }) >= 0 {
			t.Errorf("non-ASCII line: %q", line)
		}
	}
}

// TestDefaultFileDocumentsEveryKey: each dynamic table appears as a
// commented example, so uncommenting it gives a working section.
func TestDefaultFileDocumentsEveryKey(t *testing.T) {
	file := string(DefaultFile())
	for _, k := range schema {
		leaf := k.segs[len(k.segs)-1]
		if !strings.Contains(file, k.Doc) {
			t.Errorf("--defaults lacks the doc of %s", k.Path)
		}
		if k.Type != TypeTable && !strings.Contains(file, leaf+" = "+literal(k.Default)) {
			t.Errorf("--defaults lacks %s = %s", leaf, literal(k.Default))
		}
	}
	for _, header := range []string{"# [ai.profiles.NAME]", "# [ai.tasks.TASK]", "# [ai.consent]", "# [tui]", "\n[ai.context]\n"} {
		if !strings.Contains(file, header) {
			t.Errorf("--defaults lacks %q", header)
		}
	}
}

const (
	tableStart = "<!-- config-table:start -->"
	tableEnd   = "<!-- config-table:end -->"
)

// TestREADMEKeyTable keeps the key table in README.md what the schema
// renders. go test ./internal/config -update rewrites it.
func TestREADMEKeyTable(t *testing.T) {
	path := filepath.Join("..", "..", "README.md")
	src, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	readme := string(src)
	before, rest, ok := strings.Cut(readme, tableStart)
	if !ok {
		t.Fatalf("README.md has no %s marker", tableStart)
	}
	current, after, ok := strings.Cut(rest, tableEnd)
	if !ok {
		t.Fatalf("README.md has no %s marker", tableEnd)
	}
	want := "\n" + KeyTable() + "\n"
	if current == want {
		return
	}
	if !*update {
		t.Fatalf("README.md config table is out of date; run: go test ./internal/config -update\ngot:\n%s\nwant:\n%s", current, want)
	}
	if err := os.WriteFile(path, []byte(before+tableStart+want+tableEnd+after), 0o644); err != nil {
		t.Fatal(err)
	}
}
