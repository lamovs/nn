package corpus

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/lamovs/nn/internal/ocr"
	"github.com/lamovs/nn/internal/search"
	"github.com/lamovs/nn/internal/vault"
)

func testVault(t *testing.T) *vault.Vault {
	t.Helper()
	root := t.TempDir()
	write := func(rel, content string) {
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("nn/shot.md", "---\ntags: [shot, ui]\ndate: 2026-09-16\naliases: [Hotkeys]\nwhere: ~/src/demo\nrepo: demo\n---\nBefore the picture.\n\n![[nn/assets/hotkeys.png]]\n\n#more text\n")
	write("notes/plain.md", "No frontmatter.\n\nSecond paragraph.\n")
	write("nn/assets/hotkeys.png", "fake png")
	write("nn/assets/orphan.png", "fake png too")
	write("images/deep/loose.png", "another")
	write("nn/_templates/skip.md", "template")
	write(".obsidian/app.json", "{}")

	v := &vault.Vault{Root: root, Inbox: "nn"}
	result := &ocr.Result{
		Engine: "test", Langs: []string{"en-US"}, Width: 100, Height: 50, SHA256: "abc",
		Lines: []ocr.Line{
			{Text: "Cmd+Shift+4", Box: ocr.Box{X: 0.1, Y: 0.2, W: 0.3, H: 0.1}, Confidence: 0.9},
			{Text: "screenshot to file", Confidence: 0.8},
		},
	}
	if err := ocr.SaveSidecar(root, "nn/assets/hotkeys.png", result); err != nil {
		t.Fatal(err)
	}
	if err := ocr.SaveSidecar(root, "images/deep/loose.png", &ocr.Result{Engine: "test", Lines: []ocr.Line{{Text: "loose text"}}}); err != nil {
		t.Fatal(err)
	}
	return v
}

func TestLoad(t *testing.T) {
	v := testVault(t)
	docs, err := Load(context.Background(), v)
	if err != nil {
		t.Fatal(err)
	}
	var paths []string
	for _, d := range docs {
		paths = append(paths, d.Path)
	}
	want := []string{"images/deep/loose.png", "nn/assets/orphan.png", "nn/shot.md", "notes/plain.md"}
	if !reflect.DeepEqual(paths, want) {
		t.Fatalf("paths = %v, want %v", paths, want)
	}

	shot := docs[2]
	if shot.Title != "Hotkeys" || shot.Repo != "demo" || shot.Where != "~/src/demo" || !shot.InInbox {
		t.Errorf("note doc = %+v", shot)
	}
	if want := []string{"shot", "ui", "more"}; !reflect.DeepEqual(shot.Tags, want) {
		t.Errorf("tags = %v, want %v", shot.Tags, want)
	}
	wantLines := []search.Line{
		{Num: 8, Text: "Before the picture."},
		{Num: 10, Text: "![[nn/assets/hotkeys.png]]"},
		{Num: 12, Text: "#more text"},
	}
	if !reflect.DeepEqual(shot.Lines, wantLines) {
		t.Errorf("lines = %+v, want %+v", shot.Lines, wantLines)
	}
	if len(shot.Images) != 1 || shot.Images[0].Path != "nn/assets/hotkeys.png" || len(shot.Images[0].Lines) != 2 {
		t.Fatalf("images = %+v", shot.Images)
	}
	if shot.Images[0].Lines[0].Text != "Cmd+Shift+4" || shot.Images[0].Lines[0].Box.X != 0.1 {
		t.Errorf("ocr line = %+v", shot.Images[0].Lines[0])
	}
	if shot.Date.IsZero() || shot.Modified.IsZero() {
		t.Errorf("dates = %v, %v", shot.Date, shot.Modified)
	}

	plain := docs[3]
	if plain.Title != "plain" || plain.InInbox || len(plain.Lines) != 2 || plain.Lines[1].Num != 3 {
		t.Errorf("plain doc = %+v", plain)
	}

	loose := docs[0]
	if loose.Title != "loose.png" || len(loose.Lines) != 0 || loose.InInbox {
		t.Errorf("loose image doc = %+v", loose)
	}
	if len(loose.Images) != 1 || loose.Images[0].Path != "images/deep/loose.png" || len(loose.Images[0].Lines) != 1 {
		t.Errorf("loose image = %+v", loose.Images)
	}
	if loose.Modified.IsZero() {
		t.Errorf("image doc has no mtime")
	}

	orphan := docs[1]
	if !orphan.InInbox || len(orphan.Images) != 1 || len(orphan.Images[0].Lines) != 0 {
		t.Errorf("orphan doc = %+v", orphan)
	}
}

func TestStreamOrderAndStop(t *testing.T) {
	v := testVault(t)
	var seen []string
	if err := Stream(context.Background(), v, func(d *search.Doc) error {
		seen = append(seen, d.Path)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	want := []string{"nn/shot.md", "notes/plain.md", "images/deep/loose.png", "nn/assets/orphan.png"}
	if !reflect.DeepEqual(seen, want) {
		t.Fatalf("stream order = %v, want %v", seen, want)
	}

	stop := errors.New("stop")
	count := 0
	err := Stream(context.Background(), v, func(*search.Doc) error {
		count++
		return stop
	})
	if !errors.Is(err, stop) || count != 1 {
		t.Fatalf("stop: err %v after %d docs", err, count)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := Stream(ctx, v, func(*search.Doc) error { return nil }); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled stream err = %v", err)
	}

	if err := Stream(context.Background(), nil, func(*search.Doc) error { return nil }); err == nil {
		t.Fatal("nil vault accepted")
	}

	empty := &vault.Vault{Root: t.TempDir(), Inbox: "nn"}
	docs, err := Load(context.Background(), empty)
	if err != nil || len(docs) != 0 {
		t.Fatalf("empty vault = %v, %v", docs, err)
	}
}

func TestNoteDocCarriesVia(t *testing.T) {
	root := t.TempDir()
	for rel, content := range map[string]string{
		"nn/digest.md": "---\nvia: digest\n---\nSummary.\n",
		"nn/plain.md":  "Plain.\n",
	} {
		if err := os.MkdirAll(filepath.Join(root, filepath.Dir(rel)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, rel), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	docs, err := Load(context.Background(), &vault.Vault{Root: root, Inbox: "nn"})
	if err != nil {
		t.Fatal(err)
	}
	if len(docs) != 2 || docs[0].Path != "nn/digest.md" || docs[0].Via != "digest" || docs[1].Via != "" {
		t.Fatalf("docs = %+v", docs)
	}
}
