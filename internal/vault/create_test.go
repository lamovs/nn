package vault

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
)

var createNow = time.Date(2026, 9, 16, 14, 2, 0, 0, time.Local)

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func noTempFiles(t *testing.T, root string) {
	t.Helper()
	filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err == nil && strings.HasPrefix(d.Name(), ".nn-") {
			t.Errorf("temp file left behind: %s", p)
		}
		return nil
	})
}

func TestCreateFormat(t *testing.T) {
	v := tempVault(t)
	note, err := v.Create(NewNote{
		Title: "Docker cleanup",
		Body:  "docker system prune -a\n\n",
		Tags:  []string{"#docker", "Docker", "dev ops"},
		Via:   "text", Where: "~/src/demo", Repo: "demo",
		Now: createNow,
	})
	if err != nil {
		t.Fatal(err)
	}
	if note.Path != "nn/docker-cleanup.md" {
		t.Fatalf("Path = %s", note.Path)
	}
	want := "---\ndate: 2026-09-16\ntags: [docker, dev-ops]\naliases: [Docker cleanup]\ntime: \"14:02\"\n" +
		"where: ~/src/demo\nrepo: demo\nvia: text\n---\ndocker system prune -a\n"
	if got := readFile(t, v.Abs(note.Path)); got != want {
		t.Fatalf("file =\n%s\nwant\n%s", got, want)
	}
	if note.Title != "Docker cleanup" || note.BodyStartLine != 10 || !note.InInbox || !note.Date.Equal(createNow) {
		t.Fatalf("loaded note = %+v", note)
	}

	untitled, err := v.Create(NewNote{Body: "```sh\nls -la\n```", Now: createNow})
	if err != nil {
		t.Fatal(err)
	}
	if untitled.Path != "nn/ls-la.md" {
		t.Fatalf("body slug Path = %s", untitled.Path)
	}
	content := readFile(t, v.Abs(untitled.Path))
	if strings.Contains(content, "aliases") || strings.Contains(content, "where") || !strings.Contains(content, "tags: []\n") {
		t.Fatalf("untitled note =\n%s", content)
	}

	if _, err := v.Create(NewNote{Title: "  ", Body: "\n"}); err == nil {
		t.Fatal("empty note was created")
	}
}

func TestCreateNames(t *testing.T) {
	v := tempVault(t)
	tests := []struct {
		name string
		n    NewNote
		want string
	}{
		{"cyrillic title", NewNote{Title: "Чашка ТВ дешевле цены рынка"}, "nn/chashka-tv-deshevle-ceny-rynka.md"},
		{"collision in inbox", NewNote{Title: "Чашка ТВ дешевле цены рынка"}, "nn/chashka-tv-deshevle-ceny-rynka-2.md"},
		{"second collision", NewNote{Title: "chashka tv deshevle ceny rynka"}, "nn/chashka-tv-deshevle-ceny-rynka-3.md"},
		{"stem taken elsewhere in the vault", NewNote{Title: "Colima"}, "nn/colima-2.md"},
		{"template names do not count", NewNote{Title: "hotkey"}, "nn/hotkey.md"},
		{"first six words", NewNote{Body: "\n\none two three four five six seven\nnext"}, "nn/one-two-three-four-five-six.md"},
		{"image without text", NewNote{Images: []Image{{Data: []byte("img-a"), Ext: "png"}}}, "nn/shot-2026-09-16-1402.md"},
		{"image with tag", NewNote{Tags: []string{"UI/Hotkeys"}, Images: []Image{{Data: []byte("img-b"), Ext: "png"}}}, "nn/ui-hotkeys-2026-09-16-1402.md"},
		{"unsluggable text", NewNote{Body: "*** ???"}, "nn/note-2026-09-16-1402.md"},
	}
	for _, tt := range tests {
		tt.n.Now = createNow
		note, err := v.Create(tt.n)
		if err != nil {
			t.Fatalf("%s: %v", tt.name, err)
		}
		if note.Path != tt.want {
			t.Errorf("%s: Path = %s, want %s", tt.name, note.Path, tt.want)
		}
	}
	noTempFiles(t, v.Root)
}

func TestCreateImages(t *testing.T) {
	v := tempVault(t)
	existing := readFile(t, v.Abs("nn/assets/shot.png"))

	note, err := v.Create(NewNote{
		Title: "Screens",
		Body:  "two screens",
		Images: []Image{
			{Data: []byte(existing), Ext: "PNG"},
			{Data: []byte("new image"), Ext: ".jpeg"},
			{Data: []byte("new image"), Ext: "jpg"},
			{Data: []byte("third"), Ext: "webp"},
		},
		Now: createNow,
	})
	if err != nil {
		t.Fatal(err)
	}
	wantBody := "two screens\n\n![[nn/assets/shot.png]]\n![[nn/assets/screens.jpg]]\n![[nn/assets/screens.jpg]]\n![[nn/assets/screens.webp]]\n"
	if note.Body != wantBody {
		t.Fatalf("Body = %q, want %q", note.Body, wantBody)
	}
	if want := []string{"nn/assets/shot.png", "nn/assets/screens.jpg", "nn/assets/screens.webp"}; !reflect.DeepEqual(note.Embeds, want) {
		t.Fatalf("Embeds = %v", note.Embeds)
	}
	if got := readFile(t, v.Abs("nn/assets/screens.jpg")); got != "new image" {
		t.Fatalf("asset content = %q", got)
	}

	again, err := v.Create(NewNote{Title: "Screens", Images: []Image{{Data: []byte("another"), Ext: "jpg"}}, Now: createNow})
	if err != nil {
		t.Fatal(err)
	}
	if again.Path != "nn/screens-2.md" || !strings.Contains(again.Body, "![[nn/assets/screens-2.jpg]]") {
		t.Fatalf("second note %s body %q", again.Path, again.Body)
	}

	entries, _ := os.ReadDir(v.Abs("nn/assets"))
	if len(entries) != 4 {
		t.Fatalf("assets = %d files", len(entries))
	}

	if _, err := v.Create(NewNote{Title: "Bad", Images: []Image{{Data: []byte("ok"), Ext: "gif"}, {Data: []byte("x"), Ext: "bmp"}}}); err == nil {
		t.Fatal("unsupported image type accepted")
	}
	if _, err := os.Stat(v.Abs("nn/bad.md")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatal("note written despite image error")
	}
	if _, err := os.Stat(v.Abs("nn/assets/bad.gif")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatal("image from a failed create was kept")
	}
	noTempFiles(t, v.Root)
}

func TestWriteAssetReceiptsPreserveInputOrderAndReuse(t *testing.T) {
	v := tempVault(t)
	a := Image{Data: []byte("asset A"), Ext: "png"}
	b := Image{Data: []byte("asset B"), Ext: "png"}
	created, err := v.CreateWithAssets(NewNote{Title: "Receipts", Images: []Image{a, b, a}})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"nn/assets/receipts.png", "nn/assets/receipts-2.png", "nn/assets/receipts.png"}
	if !reflect.DeepEqual(created.Images, want) || len(created.Note.Embeds) != 2 {
		t.Fatalf("create receipt=%+v", created)
	}
	appended, err := v.AppendWithAssets(created.Note.Path, "Append reused images", []Image{b, a, b})
	if err != nil {
		t.Fatal(err)
	}
	want = []string{created.Images[1], created.Images[0], created.Images[1]}
	if !reflect.DeepEqual(appended.Images, want) || len(appended.Note.Embeds) != 2 {
		t.Fatalf("append receipt=%+v", appended)
	}
	for i, rel := range appended.Images {
		if got := readFile(t, v.Abs(rel)); got != string([]Image{b, a, b}[i].Data) {
			t.Fatalf("receipt %d refers to %q", i, got)
		}
	}
	textOnly, err := v.AppendWithAssets(created.Note.Path, "No new images", nil)
	if err != nil || len(textOnly.Images) != 0 {
		t.Fatalf("text receipt=%+v err=%v", textOnly, err)
	}
}

func TestAppend(t *testing.T) {
	v := tempVault(t)
	path := v.Abs("colima.md")
	before := readFile(t, path)
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}

	note, err := v.Append("colima.md", "\nAppended line.\n\n", []Image{{Data: []byte("pic"), Ext: "png"}})
	if err != nil {
		t.Fatal(err)
	}
	after := readFile(t, path)
	want := strings.TrimRight(before, "\n") + "\n\nAppended line.\n\n![[nn/assets/colima.png]]\n"
	if after != want {
		t.Fatalf("after append =\n%q\nwant\n%q", after, want)
	}
	if !strings.HasPrefix(after, before[:strings.Index(before, "\n---\n")+5]) {
		t.Fatal("frontmatter changed")
	}
	if info, _ := os.Stat(path); info.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %v", info.Mode().Perm())
	}
	if note.Path != "colima.md" || !reflect.DeepEqual(note.Embeds, []string{"nn/assets/colima.png"}) {
		t.Fatalf("note = %+v", note)
	}

	if _, err := v.Append("colima.md", "second", nil); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, path); got != after+"\nsecond\n" {
		t.Fatalf("second append = %q", got)
	}

	crlf := v.Abs("crlf.md")
	os.WriteFile(crlf, []byte("---\r\ntags: [a]\r\n---\r\nbody\r\n"), 0o644)
	if _, err := v.Append("crlf.md", "one\ntwo", nil); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, crlf); got != "---\r\ntags: [a]\r\n---\r\nbody\r\n\r\none\r\ntwo\r\n" {
		t.Fatalf("crlf append = %q", got)
	}

	empty := v.Abs("empty.md")
	os.WriteFile(empty, nil, 0o644)
	if _, err := v.Append("empty.md", "first", nil); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, empty); got != "first\n" {
		t.Fatalf("empty append = %q", got)
	}

	if _, err := v.Append("colima.md", "  \n", nil); err == nil {
		t.Fatal("empty append accepted")
	}
	if _, err := v.Append("missing.md", "x", nil); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("missing note err = %v", err)
	}
	noTempFiles(t, v.Root)
}

func TestAppendConcurrent(t *testing.T) {
	v := tempVault(t)
	rel := "nn/day.md"
	if err := os.WriteFile(v.Abs(rel), []byte("base\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	const n = 8
	errs := make(chan error, n)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := range n {
		wg.Go(func() {
			<-start
			_, err := v.Append(rel, fmt.Sprintf("entry-%d", i), nil)
			errs <- err
		})
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("Append: %v", err)
		}
	}

	got := readFile(t, v.Abs(rel))
	kept := 0
	for i := range n {
		if strings.Contains(got, fmt.Sprintf("entry-%d\n", i)) {
			kept++
		}
	}
	if kept != n {
		t.Fatalf("kept %d of %d entries, file is:\n%s", kept, n, got)
	}
	if !strings.HasPrefix(got, "base\n") {
		t.Fatalf("original content lost:\n%s", got)
	}

	entries, err := os.ReadDir(v.Abs("nn"))
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	sort.Strings(names)
	if want := []string{"_templates", "assets", "day.md", "inbox-note.md"}; !reflect.DeepEqual(names, want) {
		t.Errorf("inbox = %v, want %v", names, want)
	}
	noTempFiles(t, v.Root)
}

func TestCreateHonorsUmask(t *testing.T) {
	v := tempVault(t)
	defer func(old fs.FileMode) { processUmask = old }(processUmask)

	tests := []struct{ umask, want fs.FileMode }{
		{0o022, 0o644},
		{0o077, 0o600},
		{0o002, 0o664},
	}
	for _, tt := range tests {
		processUmask = tt.umask
		note, err := v.Create(NewNote{
			Title:  fmt.Sprintf("Private thoughts %03o", uint32(tt.umask)),
			Body:   "secret",
			Images: []Image{{Data: fmt.Appendf(nil, "image-%03o", uint32(tt.umask)), Ext: "png"}},
			Now:    createNow,
		})
		if err != nil {
			t.Fatal(err)
		}
		info, err := os.Stat(v.Abs(note.Path))
		if err != nil {
			t.Fatal(err)
		}
		if got := info.Mode().Perm(); got != tt.want {
			t.Errorf("umask %03o: note mode = %03o, want %03o", uint32(tt.umask), uint32(got), uint32(tt.want))
		}
		if len(note.Embeds) != 1 {
			t.Fatalf("embeds = %v", note.Embeds)
		}
		asset, err := os.Stat(v.Abs(note.Embeds[0]))
		if err != nil {
			t.Fatal(err)
		}
		if got := asset.Mode().Perm(); got != tt.want {
			t.Errorf("umask %03o: asset mode = %03o, want %03o", uint32(tt.umask), uint32(got), uint32(tt.want))
		}
	}
}

func TestAppendOnlyNotes(t *testing.T) {
	v := tempVault(t)
	rel := "nn/assets/shot.png"
	before, err := os.ReadFile(v.Abs(rel))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := v.Append(rel, "markdown text", nil); err == nil {
		t.Fatalf("%s: append into a non-note accepted", rel)
	}
	after, err := os.ReadFile(v.Abs(rel))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatalf("%s: bytes changed: %q -> %q", rel, before, after)
	}

	binary := v.Abs("nn/assets/trailing.webp")
	want := []byte("RIFF....WEBPVP8 \n")
	if err := os.WriteFile(binary, want, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := v.Append("nn/assets/trailing.webp", "oops", nil); err == nil {
		t.Fatal("append into a webp accepted")
	}
	if got, _ := os.ReadFile(binary); !bytes.Equal(got, want) {
		t.Fatalf("trailing bytes lost: %q -> %q", want, got)
	}
	noTempFiles(t, v.Root)
}

func TestWriteAtomic(t *testing.T) {
	dir := t.TempDir()
	dst := filepath.Join(dir, "note.md")

	if err := writeAtomic(dst, []byte("one"), 0o644, true); err != nil {
		t.Fatal(err)
	}
	err := writeAtomic(dst, []byte("two"), 0o644, true)
	if !errors.Is(err, fs.ErrExist) {
		t.Fatalf("noClobber over existing file: %v", err)
	}
	if got := readFile(t, dst); got != "one" {
		t.Fatalf("existing file clobbered: %q", got)
	}

	if err := writeAtomic(dst, []byte("three"), 0o644, false); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, dst); got != "three" {
		t.Fatalf("replace = %q", got)
	}

	blocker := filepath.Join(dir, "dir.md")
	if err := os.MkdirAll(filepath.Join(blocker, "child"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := writeAtomic(blocker, []byte("x"), 0o644, false); err == nil {
		t.Fatal("rename over a non-empty directory succeeded")
	}
	if err := writeAtomic(filepath.Join(dir, "missing", "x.md"), []byte("x"), 0o644, false); err == nil {
		t.Fatal("write into a missing directory succeeded")
	}
	noTempFiles(t, dir)
}

func TestTemplates(t *testing.T) {
	v := tempVault(t)
	os.WriteFile(v.Abs("nn/_templates/.hidden.md"), []byte("x"), 0o644)
	os.WriteFile(v.Abs("nn/_templates/readme.txt"), []byte("x"), 0o644)
	os.WriteFile(v.Abs("nn/_templates/bug.md"), []byte("x"), 0o644)

	names, err := v.Templates()
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"bug", "hotkey"}; !reflect.DeepEqual(names, want) {
		t.Fatalf("Templates = %v", names)
	}

	got, err := v.RenderTemplate("hotkey.md", map[string]string{"title": "Screenshot", "keys": "Cmd+Shift+4"})
	if err != nil {
		t.Fatal(err)
	}
	today := time.Now().Format("2006-01-02")
	want := "---\ntags: [hotkey]\ndate: " + today + "\n---\n# Screenshot\n\nKeys: Cmd+Shift+4 {{name:default}}  \n"
	if got != want {
		t.Fatalf("RenderTemplate =\n%q\nwant\n%q", got, want)
	}

	got, err = v.RenderTemplate("hotkey", map[string]string{"date": "2000-01-01", "where": "~/x", "repo": "nn"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "date: 2000-01-01") || !strings.Contains(got, "{{keys}} {{name:default}} ~/x nn") {
		t.Fatalf("RenderTemplate with vars = %q", got)
	}

	if _, err := v.RenderTemplate("missing", nil); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing template err = %v", err)
	}
	if _, err := v.RenderTemplate("../colima", nil); err == nil {
		t.Fatal("path traversal accepted")
	}

	empty := &Vault{Root: t.TempDir(), Inbox: "nn"}
	if names, err := empty.Templates(); err != nil || names != nil {
		t.Fatalf("no templates dir: %v, %v", names, err)
	}
}

func TestCreateThenLoadRoundTrip(t *testing.T) {
	v := tempVault(t)
	note, err := v.Create(NewNote{
		Title: `Quotes "and" colons: here`,
		Body:  "#inline tag here",
		Tags:  []string{"c#", "a,b"},
		Where: "/tmp/with # hash",
		Now:   createNow,
	})
	if err != nil {
		t.Fatal(err)
	}
	if note.Title != `Quotes "and" colons: here` || note.Where != "/tmp/with # hash" {
		t.Fatalf("round trip: %+v", note)
	}
	if want := []string{"c#", "a,b", "inline"}; !reflect.DeepEqual(note.Tags, want) {
		t.Fatalf("Tags = %v", note.Tags)
	}
	if !bytes.HasPrefix([]byte(readFile(t, v.Abs(note.Path))), []byte("---\ndate: 2026-09-16\ntags: [c#, \"a,b\"]\n")) {
		t.Fatalf("file = %s", readFile(t, v.Abs(note.Path)))
	}
}
