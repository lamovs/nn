package vault

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
)

func TestNormalizeKeywords(t *testing.T) {
	got := NormalizeKeywords([]string{" #Go ", "go", "##Dev Ops", "Тест", "ui/hotkeys/", "snake_case", "123", "---", "x](bad)", "C++", "go\nnow"})
	want := []string{"go", "dev-ops", "тест", "ui/hotkeys", "snake_case", "go-now"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("keywords = %#v, want %#v", got, want)
	}
	if tags := InlineTags(keywordFooter(got, nil)); !reflect.DeepEqual(tags, want) {
		t.Fatalf("footer tags = %#v", tags)
	}
}

func TestCreateTrailingTagsAndFilenameHint(t *testing.T) {
	v := tempVault(t)
	n, err := v.Create(NewNote{
		FilenameHint: "pending-shot", Body: "capture #existing", Tags: []string{"Manual"},
		TrailingTags: []string{"Manual", "ai", "AI", "existing"},
		Images:       []Image{{Data: []byte("new capture image"), Ext: "png"}}, Now: createNow,
	})
	if err != nil {
		t.Fatal(err)
	}
	if n.Path != "nn/pending-shot.md" || len(n.Aliases) != 0 || HasTitle(n) {
		t.Fatalf("provisional note = %+v", n)
	}
	if !strings.HasSuffix(n.Body, "![[nn/assets/pending-shot.png]]\n\n#ai\n") || strings.Count(n.Body, "#existing") != 1 {
		t.Fatalf("footer order: %q", n.Body)
	}
	updated, err := v.AppendEnrichment(n.Path, Enrichment{Title: "AI title", AllowTitle: true, Tags: []string{"extra"}})
	if err != nil || updated.Path != n.Path || updated.Title != "AI title" {
		t.Fatalf("enrichment = %+v, %v", updated, err)
	}
	manual, err := v.Create(NewNote{Title: "Manual title", FilenameHint: "wrong", TrailingTags: []string{"go"}})
	if err != nil || manual.Path != "nn/manual-title.md" || manual.Title != "Manual title" {
		t.Fatalf("manual title = %+v, %v", manual, err)
	}
}

func TestAppendEnrichmentPreservesCurrentEdits(t *testing.T) {
	for _, title := range []string{"aliases: [Manual title]\n", "title: Manual title\n", ""} {
		t.Run(fmt.Sprintf("title-%q", title), func(t *testing.T) {
			v := tempVault(t)
			n, err := v.Create(NewNote{Body: "original"})
			if err != nil {
				t.Fatal(err)
			}
			_, unlock, err := lockNote(v.Abs(n.Path))
			if err != nil {
				t.Fatal(err)
			}
			done := make(chan error, 1)
			go func() {
				_, err := v.AppendEnrichment(n.Path, Enrichment{Title: "AI title", Body: "Analysis", Tags: []string{"manual", "inline", "new tag"}, AllowTitle: true})
				done <- err
			}()
			before := "---\r\n" + strings.ReplaceAll(title, "\n", "\r\n") + "tags: [Manual]\r\ncustom: keep-me\r\n---\r\n"
			if title == "" {
				before += "# Manual title\r\n"
			}
			before += "current user edit #Inline  \r\n\r\n"
			if err := os.WriteFile(v.Abs(n.Path), []byte(before), 0o600); err != nil {
				unlock()
				t.Fatal(err)
			}
			unlock()
			if err := <-done; err != nil {
				t.Fatal(err)
			}
			after := readFile(t, v.Abs(n.Path))
			if !strings.HasPrefix(after, before) || strings.Contains(after, "AI title") || !strings.HasSuffix(after, "Analysis\r\n\r\n#new-tag\r\n") {
				t.Fatalf("current bytes/title/tags changed: %q", after)
			}
			current, err := v.Load(n.Path)
			if err != nil || current.Title != "Manual title" || !reflect.DeepEqual(current.Tags, []string{"Manual", "Inline", "new-tag"}) {
				t.Fatalf("current = %+v, %v", current, err)
			}
		})
	}
}

func TestAppendEnrichmentConcurrentWithAppend(t *testing.T) {
	v := tempVault(t)
	n, err := v.Create(NewNote{Body: "original"})
	if err != nil {
		t.Fatal(err)
	}
	const count = 12
	errs := make(chan error, count)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := range count {
		wg.Go(func() {
			<-start
			var err error
			if i%2 == 0 {
				_, err = v.Append(n.Path, fmt.Sprintf("entry-%d", i), nil)
			} else {
				_, err = v.AppendEnrichment(n.Path, Enrichment{Body: fmt.Sprintf("entry-%d", i), Title: "AI title", Tags: []string{"shared"}, AllowTitle: true})
			}
			errs <- err
		})
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	content := readFile(t, v.Abs(n.Path))
	for i := range count {
		if strings.Count(content, fmt.Sprintf("entry-%d\n", i)) != 1 {
			t.Fatalf("entry %d lost or duplicated: %s", i, content)
		}
	}
	if strings.Count(content, "# AI title\n") != 1 || strings.Count(content, "#shared") != 1 {
		t.Fatalf("metadata duplicated: %s", content)
	}
	noTempFiles(t, v.Root)
}

func TestMetadataOutsideUnfinishedFences(t *testing.T) {
	for _, fence := range []string{"```go", "~~~~text"} {
		t.Run(fence, func(t *testing.T) {
			v := tempVault(t)
			n, err := v.Create(NewNote{Body: fence + "\nunfinished code", TrailingTags: []string{"visible"}, Images: []Image{{Data: []byte("fenced capture"), Ext: "png"}}})
			if err != nil || !reflect.DeepEqual(InlineTags(n.Body), []string{"visible"}) {
				t.Fatalf("create footer hidden: %+v, %v", n, err)
			}
			if len(n.Embeds) != 1 || !strings.Contains(MaskCode(n.Body), "![[") || !strings.HasSuffix(n.Body, "]]\n\n#visible\n") {
				t.Fatalf("image embed hidden or footer misplaced: %+v", n)
			}
			if _, err := v.Append(n.Path, fence+"\nmore unfinished code", nil); err != nil {
				t.Fatal(err)
			}
			before := readFile(t, v.Abs(n.Path))
			n, err = v.AppendEnrichment(n.Path, Enrichment{Title: "Visible title", Body: fence + "\nunfinished analysis", Tags: []string{"extra"}, AllowTitle: true})
			if err != nil || n.Title != "Visible title" || !reflect.DeepEqual(InlineTags(n.Body), []string{"visible", "extra"}) {
				t.Fatalf("enrichment hidden: %+v, %v", n, err)
			}
			if !strings.HasPrefix(readFile(t, v.Abs(n.Path)), before) {
				t.Fatal("existing bytes changed")
			}
		})
	}
}

func TestAppendEnrichmentBoundariesAndNoop(t *testing.T) {
	v := tempVault(t)
	outside := t.TempDir()
	target := filepath.Join(outside, "target.md")
	before := "# Manual\n\nbody  \n\n"
	if err := os.WriteFile(target, []byte(before), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, v.Abs("nn/link.md")); err != nil {
		t.Fatal(err)
	}
	if _, err := v.AppendEnrichment("nn/link.md", Enrichment{Title: "Skip", AllowTitle: true}); err != nil {
		t.Fatal(err)
	}
	if readFile(t, target) != before {
		t.Fatal("no-op rewrote note")
	}
	if _, err := v.AppendEnrichment("nn/link.md", Enrichment{Tags: []string{"linked"}}); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Lstat(v.Abs("nn/link.md")); err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("note link replaced: %v, %v", info, err)
	}
	if !strings.HasPrefix(readFile(t, target), before) || !strings.HasSuffix(readFile(t, target), "#linked\n") {
		t.Fatal("linked target not enriched")
	}
	if err := os.Symlink(outside, v.Abs("escape")); err != nil {
		t.Fatal(err)
	}
	for _, rel := range []string{"escape/target.md", "../target.md", "nn/assets/shot.png"} {
		if _, err := v.AppendEnrichment(rel, Enrichment{Tags: []string{"forbidden"}}); err == nil {
			t.Errorf("invalid target accepted: %s", rel)
		}
	}
}
