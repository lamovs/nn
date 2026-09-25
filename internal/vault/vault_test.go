package vault

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"testing"
	"time"

	"github.com/lamovs/nn/internal/config"
)

const fixture = "testdata/vault"

func fixtureVault(t *testing.T) *Vault {
	t.Helper()
	root, err := filepath.Abs(fixture)
	if err != nil {
		t.Fatal(err)
	}
	return &Vault{Root: root, Inbox: "nn"}
}

func tempVault(t *testing.T) *Vault {
	t.Helper()
	root := t.TempDir()
	err := filepath.WalkDir(fixture, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(fixture, p)
		dst := filepath.Join(root, rel)
		if d.IsDir() {
			return os.MkdirAll(dst, 0o755)
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		return os.WriteFile(dst, data, 0o644)
	})
	if err != nil {
		t.Fatal(err)
	}
	return &Vault{Root: root, Inbox: "nn"}
}

func TestOpen(t *testing.T) {
	root := t.TempDir()
	v, err := Open(config.Config{Vault: config.Vault{Root: root}})
	if err != nil {
		t.Fatal(err)
	}
	if v.Root != root || v.Inbox != "nn" {
		t.Fatalf("Open = %+v", v)
	}
	if v.Abs("nn/x.md") != filepath.Join(root, "nn", "x.md") {
		t.Fatalf("Abs = %s", v.Abs("nn/x.md"))
	}

	v, err = Open(config.Config{Vault: config.Vault{Root: root, Inbox: filepath.Join(root, "inbox", "sub")}})
	if err != nil || v.Inbox != "inbox/sub" {
		t.Fatalf("absolute inbox: %+v, %v", v, err)
	}

	for name, cfg := range map[string]config.Config{
		"missing root":   {Vault: config.Vault{Root: filepath.Join(root, "missing")}},
		"empty root":     {},
		"escaping inbox": {Vault: config.Vault{Root: root, Inbox: "../elsewhere"}},
	} {
		if _, err := Open(cfg); err == nil {
			t.Errorf("%s: Open succeeded", name)
		}
	}
}

func TestWalkSkipsHiddenAndTemplates(t *testing.T) {
	v := fixtureVault(t)
	var got []string
	if err := v.Walk(context.Background(), func(rel string) error {
		got = append(got, rel)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"archive/docker.md", "broken.md", "colima.md", "nn/inbox-note.md",
		"notes/docker.md", "plain.md", "work/4217.md",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Walk = %v, want %v", got, want)
	}

	images, err := v.Images(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"images/pic.png", "nn/assets/shot.png"}; !reflect.DeepEqual(images, want) {
		t.Fatalf("Images = %v, want %v", images, want)
	}
}

func TestWalkCancelled(t *testing.T) {
	v := fixtureVault(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := v.Walk(ctx, func(string) error { return nil })
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Walk err = %v", err)
	}
	if _, err := v.LoadAll(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("LoadAll err = %v", err)
	}
}

func TestLoad(t *testing.T) {
	v := fixtureVault(t)

	colima, err := v.Load("colima.md")
	if err != nil {
		t.Fatal(err)
	}
	if colima.Title != "Colima" {
		t.Errorf("Title = %q", colima.Title)
	}
	if want := []string{"docker", "devtools", "macos", "containers"}; !reflect.DeepEqual(colima.Tags, want) {
		t.Errorf("Tags = %v, want %v", colima.Tags, want)
	}
	if want := []string{"Colima", "Docker альтернатива"}; !reflect.DeepEqual(colima.Aliases, want) {
		t.Errorf("Aliases = %v", colima.Aliases)
	}
	if colima.BodyStartLine != 7 || colima.Body[:1] != "\n" {
		t.Errorf("BodyStartLine = %d, body %q", colima.BodyStartLine, colima.Body)
	}
	if !colima.Date.Equal(time.Date(2026, 5, 12, 0, 0, 0, 0, time.Local)) {
		t.Errorf("Date = %v", colima.Date)
	}
	if colima.Fields["reviewed"] != "2026-09-01" || colima.InInbox || colima.Modified.IsZero() {
		t.Errorf("Fields = %v, InInbox = %v", colima.Fields, colima.InInbox)
	}

	docker, err := v.Load("notes/docker.md")
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"docker", "cli", "cleanup"}; !reflect.DeepEqual(docker.Tags, want) {
		t.Errorf("block list tags = %v, want %v", docker.Tags, want)
	}
	if docker.Title != "Docker notes" {
		t.Errorf("Title = %q", docker.Title)
	}

	inbox, err := v.Load("./nn//inbox-note.md")
	if err != nil {
		t.Fatal(err)
	}
	if inbox.Path != "nn/inbox-note.md" || !inbox.InInbox {
		t.Errorf("Path = %q, InInbox = %v", inbox.Path, inbox.InInbox)
	}
	if want := []string{"nn/assets/shot.png", "images/pic.png"}; !reflect.DeepEqual(inbox.Embeds, want) {
		t.Errorf("Embeds = %v, want %v", inbox.Embeds, want)
	}
	if !inbox.Date.Equal(time.Date(2026, 9, 16, 14, 2, 0, 0, time.Local)) {
		t.Errorf("Date with time = %v", inbox.Date)
	}
	if inbox.Where != "~/src/demo" || inbox.Repo != "demo" || inbox.Via != "shot" {
		t.Errorf("context = %q %q %q", inbox.Where, inbox.Repo, inbox.Via)
	}
	if inbox.Title != "inbox-note" {
		t.Errorf("stem title = %q", inbox.Title)
	}

	broken, err := v.Load("broken.md")
	if err != nil {
		t.Fatal(err)
	}
	if broken.BodyStartLine != 1 || len(broken.Fields) != 0 || broken.Body[:3] != "---" {
		t.Errorf("broken frontmatter: start %d fields %v body %q", broken.BodyStartLine, broken.Fields, broken.Body)
	}
	if want := []string{"broken"}; !reflect.DeepEqual(broken.Tags, want) {
		t.Errorf("broken tags = %v", broken.Tags)
	}

	plain, err := v.Load("plain.md")
	if err != nil {
		t.Fatal(err)
	}
	if plain.Title != "Plain Title" || plain.Fields == nil {
		t.Errorf("H1 title = %q, fields %v", plain.Title, plain.Fields)
	}

	if _, err := v.Load("missing.md"); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("missing note err = %v", err)
	}
}

func TestLoadAll(t *testing.T) {
	v := fixtureVault(t)
	notes, err := v.LoadAll(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var paths []string
	for _, n := range notes {
		paths = append(paths, n.Path)
	}
	if !sort.StringsAreSorted(paths) || len(paths) != 7 {
		t.Fatalf("LoadAll paths = %v", paths)
	}

	stop := errors.New("stop")
	count := 0
	err = v.LoadEach(context.Background(), func(*Note) error {
		count++
		if count == 2 {
			return stop
		}
		return nil
	})
	if !errors.Is(err, stop) || count != 2 {
		t.Fatalf("LoadEach err = %v after %d notes", err, count)
	}
}

func TestResolve(t *testing.T) {
	v := fixtureVault(t)
	tests := []struct {
		ref       string
		want      string
		ambiguous []string
		notFound  bool
	}{
		{ref: "colima.md", want: "colima.md"},
		{ref: "colima", want: "colima.md"},
		{ref: "COLIMA.MD", want: "colima.md"},
		{ref: "notes/docker", want: "notes/docker.md"},
		{ref: "work/4217.md", want: "work/4217.md"},
		{ref: "4217", want: "work/4217.md"},
		{ref: "docker", ambiguous: []string{"archive/docker.md", "notes/docker.md"}},
		{ref: "Docker notes", want: "notes/docker.md"},
		{ref: "кр abc-4217", want: "work/4217.md"},
		{ref: "Plain Title", want: "plain.md"},
		{ref: filepath.Join(v.Root, "plain.md"), want: "plain.md"},
		{ref: "nn/_templates/hotkey", want: "nn/_templates/hotkey.md"},
		{ref: "nothing-like-this", notFound: true},
		{ref: "", notFound: true},
	}
	for _, tt := range tests {
		got, err := v.Resolve(tt.ref)
		var amb *AmbiguousError
		switch {
		case tt.notFound:
			if !errors.Is(err, ErrNotFound) {
				t.Errorf("Resolve(%q) = %q, %v; want ErrNotFound", tt.ref, got, err)
			}
		case tt.ambiguous != nil:
			if !errors.As(err, &amb) || !reflect.DeepEqual(amb.Candidates, tt.ambiguous) {
				t.Errorf("Resolve(%q) = %q, %v; want ambiguous %v", tt.ref, got, err, tt.ambiguous)
			}
		default:
			if err != nil || got != tt.want {
				t.Errorf("Resolve(%q) = %q, %v; want %q", tt.ref, got, err, tt.want)
			}
		}
	}
}

func TestClosest(t *testing.T) {
	tests := []struct {
		from  string
		cands []string
		want  string
	}{
		{"a/b/note.md", []string{"x/img.png", "a/b/img.png", "a/img.png"}, "a/b/img.png"},
		{"a/b/note.md", []string{"x/y/img.png", "a/img.png"}, "a/img.png"},
		{"note.md", []string{"z/img.png", "b/img.png"}, "b/img.png"},
		{"note.md", nil, ""},
	}
	for _, tt := range tests {
		if got := Closest(tt.from, tt.cands); got != tt.want {
			t.Errorf("Closest(%q, %v) = %q, want %q", tt.from, tt.cands, got, tt.want)
		}
	}
}
