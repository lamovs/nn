package vault

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
)

func linkedVault(t *testing.T, away string) *Vault {
	t.Helper()
	root := t.TempDir()
	if err := os.Symlink(away, filepath.Join(root, "nn")); err != nil {
		t.Fatal(err)
	}
	return &Vault{Root: root, Inbox: "nn"}
}

func writeOutside(t *testing.T, dir, name string) (path, content string) {
	t.Helper()
	path = filepath.Join(dir, name)
	content = "# not nn's\n\nthis file belongs to someone else\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path, content
}

func TestAppendFollowsLinkedNoteOutOfVault(t *testing.T) {
	v := tempVault(t)
	outside := t.TempDir()
	filed := filepath.Join(outside, "filed.md")
	before := "# filed elsewhere\n\nthe note the link stands for\n"
	if err := os.WriteFile(filed, []byte(before), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filed, v.Abs("nn/link.md")); err != nil {
		t.Fatal(err)
	}

	if _, err := v.Append("nn/link.md", "capture goes here", nil); err != nil {
		t.Fatalf("Append through a linked note: %v", err)
	}
	info, err := os.Lstat(v.Abs("nn/link.md"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("the link was replaced by a plain file: mode = %v", info.Mode())
	}
	if dest, err := os.Readlink(v.Abs("nn/link.md")); err != nil || dest != filed {
		t.Fatalf("the link now points at %q (%v), want %q", dest, err, filed)
	}
	if got := readFile(t, filed); !strings.HasPrefix(got, before) || !strings.Contains(got, "capture goes here") {
		t.Fatalf("the append did not reach the file the link names:\n%s", got)
	}
	noTempFiles(t, outside)
	noTempFiles(t, v.Root)
}

func TestAppendFollowsLinkedNoteInsideVault(t *testing.T) {
	v := tempVault(t)
	filed := v.Abs("archive/filed.md")
	before := "# filed\n\nthe note the link stands for\n"
	if err := os.WriteFile(filed, []byte(before), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filed, v.Abs("nn/link.md")); err != nil {
		t.Fatal(err)
	}

	note, err := v.Append("nn/link.md", "capture goes here", nil)
	if err != nil {
		t.Fatalf("Append through a link inside the vault: %v", err)
	}
	if note.Path != "nn/link.md" || !strings.Contains(note.Body, "capture goes here") {
		t.Fatalf("Append returned %s with body %q", note.Path, note.Body)
	}
	if got := readFile(t, filed); !strings.HasPrefix(got, before) || !strings.Contains(got, "capture goes here") {
		t.Fatalf("the append did not reach the note the link names:\n%s", got)
	}
	if info, err := os.Lstat(v.Abs("nn/link.md")); err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("the link was replaced by a plain file: Lstat = %v, %v", info, err)
	}

	var rels []string
	if err := v.Walk(context.Background(), func(rel string) error {
		rels = append(rels, rel)
		return nil
	}); err != nil {
		t.Fatalf("Walk after the append: %v", err)
	}
	for _, want := range []string{"nn/link.md", "archive/filed.md"} {
		if !slices.Contains(rels, want) {
			t.Fatalf("the walk no longer reaches %s: %v", want, rels)
		}
	}
	noTempFiles(t, v.Root)
}

func TestAppendRefusesLinkedDirOutOfVault(t *testing.T) {
	v := tempVault(t)
	outside := t.TempDir()
	note, before := writeOutside(t, outside, "note.md")
	if err := os.Symlink(outside, v.Abs("away")); err != nil {
		t.Fatal(err)
	}

	_, err := v.Append("away/note.md", "capture goes here", nil)
	if !errors.Is(err, ErrOutsideVault) {
		t.Fatalf("Append through a linked directory: err = %v, want %v", err, ErrOutsideVault)
	}
	if got := readFile(t, note); got != before {
		t.Fatalf("the file outside the vault was rewritten:\n%s", got)
	}
	noTempFiles(t, outside)
}

func TestAppendRefusesLinkedInbox(t *testing.T) {
	away := t.TempDir()
	v := linkedVault(t, away)
	note := filepath.Join(away, "note.md")
	before := "# moved\n\nthis note lives on the other disk\n"
	if err := os.WriteFile(note, []byte(before), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := v.Append("nn/note.md", "capture goes here", nil)
	if !errors.Is(err, ErrOutsideVault) {
		t.Fatalf("Append into a linked inbox: err = %v, want %v", err, ErrOutsideVault)
	}
	if got := readFile(t, note); got != before {
		t.Fatalf("the note outside the vault was rewritten:\n%s", got)
	}
	noTempFiles(t, away)
}

func TestAppendRefusesClimbingPath(t *testing.T) {
	v := tempVault(t)
	sibling, before := writeOutside(t, filepath.Dir(v.Root), "sibling.md")

	_, err := v.Append("../"+filepath.Base(sibling), "capture goes here", nil)
	if !errors.Is(err, ErrOutsideVault) {
		t.Fatalf("Append into ..: err = %v, want %v", err, ErrOutsideVault)
	}
	if got := readFile(t, sibling); got != before {
		t.Fatalf("the file outside the vault was rewritten:\n%s", got)
	}
}

// A path with ".." is refused even when it resolves back inside the vault.
func TestAppendRefusesAClimbThatComesBack(t *testing.T) {
	v := tempVault(t)
	rel := "../" + filepath.Base(v.Root) + "/nn/inbox-note.md"
	before := readFile(t, v.Abs("nn/inbox-note.md"))

	_, err := v.Append(rel, "capture goes here", nil)
	if !errors.Is(err, ErrOutsideVault) {
		t.Fatalf("Append(%q): err = %v, want %v", rel, err, ErrOutsideVault)
	}
	if got := readFile(t, v.Abs("nn/inbox-note.md")); got != before {
		t.Fatalf("the note was rewritten through a path that climbs out of the root:\n%s", got)
	}
	noTempFiles(t, v.Root)
}

func TestAppendThroughALinkedDirectoryInsideTheVault(t *testing.T) {
	v := tempVault(t)
	if err := os.Symlink(v.Abs("archive"), v.Abs("shelf")); err != nil {
		t.Fatal(err)
	}
	before := readFile(t, v.Abs("archive/docker.md"))

	note, err := v.Append("shelf/docker.md", "capture goes here", nil)
	if err != nil {
		t.Fatalf("Append through a linked directory inside the vault: %v", err)
	}
	if note.Path != "shelf/docker.md" || !strings.Contains(note.Body, "capture goes here") {
		t.Fatalf("Append returned %s with body %q", note.Path, note.Body)
	}
	if got := readFile(t, v.Abs("archive/docker.md")); !strings.HasPrefix(got, strings.TrimRight(before, " \t\r\n")) || !strings.Contains(got, "capture goes here") {
		t.Fatalf("the append did not reach the directory the link names:\n%s", got)
	}
	if info, err := os.Lstat(v.Abs("shelf")); err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("the link was replaced: Lstat = %v, %v", info, err)
	}
	noTempFiles(t, v.Root)
}

func TestCreateIntoALinkedInboxInsideTheVault(t *testing.T) {
	root := t.TempDir()
	real := filepath.Join(root, "captures")
	if err := os.Mkdir(real, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(real, filepath.Join(root, "nn")); err != nil {
		t.Fatal(err)
	}
	v := &Vault{Root: root, Inbox: "nn"}

	note, err := v.Create(NewNote{
		Title:  "Linked inbox",
		Body:   "body text",
		Images: []Image{{Data: []byte("png-bytes"), Ext: "png"}},
		Now:    createNow,
	})
	if err != nil {
		t.Fatalf("Create into an inbox linked inside the vault: %v", err)
	}
	if note.Path != "nn/linked-inbox.md" {
		t.Fatalf("Path = %s, want nn/linked-inbox.md", note.Path)
	}
	for _, name := range []string{"linked-inbox.md", filepath.Join("assets", "linked-inbox.png")} {
		if _, err := os.Lstat(filepath.Join(real, name)); err != nil {
			t.Errorf("%s did not land in the directory the inbox really is: %v", name, err)
		}
	}
	if info, err := os.Lstat(filepath.Join(root, "nn")); err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("the inbox link was replaced: Lstat = %v, %v", info, err)
	}
	noTempFiles(t, root)
}

func TestRealDir(t *testing.T) {
	v := tempVault(t)
	outside := t.TempDir()
	if err := os.Symlink(outside, v.Abs("away")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(v.Abs("loop"), v.Abs("loop")); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name, rel  string
		wantInside bool
		wantErr    bool
	}{
		{"a directory of the vault", "nn", true, false},
		{"a link out of the vault", "away", false, false},
		{"a link one level down", "away/assets", false, false},
		{"a link two levels up from a directory not there yet", "away/assets/deeper", false, false},
		{"a directory that is not there yet", "nn/later", true, false},
		{"directories several levels from being there", "nn/later/still/deeper", true, false},
		{"a path that climbs out", "../elsewhere", false, false},
		{"a link that loops", "loop", false, true},
		{"a directory under a link that loops", "loop/nn", false, true},
	}
	for _, tt := range tests {
		real, inside, err := v.RealDir(tt.rel)
		if (err != nil) != tt.wantErr || inside != tt.wantInside {
			t.Errorf("%s: RealDir(%q) = %q, %v, %v; want inside = %v, an error = %v", tt.name, tt.rel, real, inside, err, tt.wantInside, tt.wantErr)
		}
	}

	entries, err := os.ReadDir(outside)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Errorf("RealDir created %d entries outside the vault", len(entries))
	}
	if _, err := os.Lstat(v.Abs("nn/later")); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("RealDir created the directory it was asked about: Lstat = %v", err)
	}
}

func TestEnsureDirRefusesALinkItCannotRead(t *testing.T) {
	root := t.TempDir()
	away := t.TempDir()
	link := filepath.Join(root, "a")
	if err := os.Symlink(away, link); err != nil {
		t.Fatal(err)
	}
	unreadableLink(t, link)
	v := &Vault{Root: root, Inbox: "a/b/nn"}

	_, err := v.EnsureDir(v.Inbox)
	if !errors.Is(err, fs.ErrPermission) {
		t.Errorf("EnsureDir through a link nn cannot read: err = %v, want the permission error", err)
	}
	if errors.Is(err, ErrOutsideVault) {
		t.Errorf("EnsureDir through a link nn cannot read: err = %v, which says outside the vault about a link nobody followed", err)
	}
	entries, err := os.ReadDir(away)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		names := make([]string, len(entries))
		for i, e := range entries {
			names[i] = e.Name()
		}
		t.Fatalf("outside the vault holds %v, want nothing: the directory is refused before it is created", names)
	}
}

func TestEnsureDirDoesNotCallAPathItCannotFollowOutside(t *testing.T) {
	v := tempVault(t)
	if err := os.Symlink(v.Abs("loop"), v.Abs("loop")); err != nil {
		t.Fatal(err)
	}

	_, err := v.EnsureDir("loop/nn")
	if err == nil {
		t.Fatal("EnsureDir under a link that loops: err = nil, want a refusal")
	}
	if errors.Is(err, ErrOutsideVault) {
		t.Errorf("EnsureDir under a link that loops: err = %v, want it not called outside the vault", err)
	}
	if !strings.Contains(err.Error(), v.Abs("loop/nn")) {
		t.Errorf("EnsureDir under a link that loops: err = %v, want it to name %s", err, v.Abs("loop/nn"))
	}
}

func TestEnsureDirRefusesALinkTooLongToFollow(t *testing.T) {
	root := t.TempDir()
	far := linkTooLongToFollow(t, t.TempDir())
	if err := os.Symlink(far, filepath.Join(root, "a")); err != nil {
		t.Fatal(err)
	}
	v := &Vault{Root: root, Inbox: "a/b/nn"}

	_, ensureErr := v.EnsureDir(v.Inbox)
	writeErr := v.WriteNew("a/b/nn/new.md", []byte("mine"))
	_, createErr := v.Create(NewNote{
		Title:  "Behind a link",
		Body:   "body text",
		Images: []Image{{Data: []byte("png-bytes"), Ext: "png"}},
		Now:    createNow,
	})
	_, inside, realErr := v.RealDir(v.Inbox)
	for _, got := range []struct {
		call string
		err  error
	}{{"EnsureDir", ensureErr}, {"WriteNew", writeErr}, {"Create", createErr}, {"RealDir", realErr}} {
		if !errors.Is(got.err, syscall.ENAMETOOLONG) || errors.Is(got.err, ErrOutsideVault) {
			t.Errorf("%s through a link nn cannot follow: err = %v, want the error that stopped it, not ErrOutsideVault", got.call, got.err)
		}
	}
	if inside {
		t.Errorf("RealDir(%q) answered inside for a path it could not follow", v.Inbox)
	}
	if ensureErr != nil && !strings.Contains(ensureErr.Error(), v.Abs("a/b/nn")) {
		t.Errorf("EnsureDir: err = %v, want it to name %s", ensureErr, v.Abs("a/b/nn"))
	}
	entries, err := os.ReadDir(far)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		names := make([]string, len(entries))
		for i, e := range entries {
			names[i] = e.Name()
		}
		t.Fatalf("the far end of the link holds %v, want nothing: the directory is refused before it is created", names)
	}
}

func unreadableLink(t *testing.T, link string) {
	t.Helper()
	if out, err := exec.Command("chmod", "-h", "000", link).CombinedOutput(); err != nil {
		msg, _, _ := bytes.Cut(bytes.TrimSpace(out), []byte("\n"))
		t.Skipf("a symlink's own permissions cannot be taken away here (chmod -h: %v: %s)", err, msg)
	}
	t.Cleanup(func() { exec.Command("chmod", "-h", "755", link).Run() })
	if _, err := os.Readlink(link); !errors.Is(err, fs.ErrPermission) {
		t.Skipf("a symlink's own permissions are not enforced here: readlink of a link with mode 000 = %v", err)
	}
}

// linkTooLongToFollow builds a link chain whose full spelling exceeds PATH_MAX for filepath.EvalSymlinks.
func linkTooLongToFollow(t *testing.T, dir string) string {
	t.Helper()
	seg := strings.Repeat("d", 200)
	rel := filepath.Join(seg, seg, seg)
	r, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	far := dir
	for i := 1; i <= 10; i++ {
		name := fmt.Sprintf("l%d", i)
		if err := r.MkdirAll(rel, 0o755); err != nil {
			r.Close()
			t.Fatal(err)
		}
		if err := r.Symlink(rel, name); err != nil {
			r.Close()
			t.Skipf("symlinks unavailable here: %v", err)
		}
		next, err := r.OpenRoot(name)
		r.Close()
		if err != nil {
			t.Fatal(err)
		}
		r, far = next, filepath.Join(far, name)
	}
	r.Close()
	if info, err := os.Stat(far); err != nil || !info.IsDir() {
		t.Skipf("the kernel does not walk a chain of links spelling out a path longer than PATH_MAX here: Stat = %v, %v", info, err)
	}
	if _, err := filepath.EvalSymlinks(far); !errors.Is(err, syscall.ENAMETOOLONG) {
		t.Fatalf("filepath.EvalSymlinks(%s) = %v, want ENAMETOOLONG: the chain is no longer a link nn cannot follow", far, err)
	}
	return far
}

func TestWriteNewRefusesALinkedDirectory(t *testing.T) {
	v := tempVault(t)
	outside := t.TempDir()
	if err := os.Symlink(outside, v.Abs("away")); err != nil {
		t.Fatal(err)
	}

	if err := v.WriteNew("away/new.md", []byte("mine")); !errors.Is(err, ErrOutsideVault) {
		t.Fatalf("WriteNew through a linked directory: err = %v, want %v", err, ErrOutsideVault)
	}
	entries, err := os.ReadDir(outside)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Errorf("WriteNew left %d entries outside the vault", len(entries))
	}

	if err := v.WriteNew("away/deeper/new.md", []byte("mine")); !errors.Is(err, ErrOutsideVault) {
		t.Fatalf("WriteNew two directories through a link: err = %v, want %v", err, ErrOutsideVault)
	}
	entries, err = os.ReadDir(outside)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("outside the vault holds %v, want nothing: the directory is refused before it is created", entries)
	}
}

func TestWriteNewKeepsANameThatIsTaken(t *testing.T) {
	v := tempVault(t)
	stolen := filepath.Join(t.TempDir(), "stolen.md")
	if err := os.Symlink(stolen, v.Abs("nn/link.md")); err != nil {
		t.Fatal(err)
	}

	if err := v.WriteNew("nn/link.md", []byte("mine")); !errors.Is(err, fs.ErrExist) {
		t.Fatalf("WriteNew onto a dangling link: err = %v, want %v", err, fs.ErrExist)
	}
	if _, err := os.Lstat(stolen); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("the write followed the link to %s: Lstat = %v", stolen, err)
	}

	before := readFile(t, v.Abs("nn/inbox-note.md"))
	if err := v.WriteNew("nn/inbox-note.md", []byte("mine")); !errors.Is(err, fs.ErrExist) {
		t.Fatalf("WriteNew onto a note that is there: err = %v, want %v", err, fs.ErrExist)
	}
	if got := readFile(t, v.Abs("nn/inbox-note.md")); got != before {
		t.Fatalf("the note was rewritten:\n%s", got)
	}
	noTempFiles(t, v.Root)
}

func TestCreateRefusesClimbingInbox(t *testing.T) {
	root := t.TempDir()
	v := &Vault{Root: root, Inbox: "../escape"}

	_, err := v.Create(NewNote{Title: "Escaped", Body: "text", Now: createNow})
	if !errors.Is(err, ErrOutsideVault) {
		t.Fatalf("Create into an inbox above the root: err = %v, want %v", err, ErrOutsideVault)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(root), "escape")); err == nil {
		t.Fatal("the inbox above the root was created anyway")
	}
}

func TestCreateRefusesLinkedInbox(t *testing.T) {
	away := t.TempDir()
	v := linkedVault(t, away)

	_, err := v.Create(NewNote{
		Title:  "Away note",
		Body:   "body text",
		Images: []Image{{Data: []byte("png-bytes"), Ext: "png"}},
		Now:    createNow,
	})
	if !errors.Is(err, ErrOutsideVault) {
		t.Fatalf("Create into a linked inbox: err = %v, want %v", err, ErrOutsideVault)
	}
	entries, err := os.ReadDir(away)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("the linked inbox holds %d entries, want nothing written through the link", len(entries))
	}
	noTempFiles(t, away)
}

func TestCreateRefusesAnInboxBehindALinkedAncestor(t *testing.T) {
	root := t.TempDir()
	away := t.TempDir()
	if err := os.Symlink(away, filepath.Join(root, "a")); err != nil {
		t.Fatal(err)
	}
	v := &Vault{Root: root, Inbox: "a/b/nn"}

	_, err := v.Create(NewNote{
		Title:  "Behind a link",
		Body:   "body text",
		Images: []Image{{Data: []byte("png-bytes"), Ext: "png"}},
		Now:    createNow,
	})
	if !errors.Is(err, ErrOutsideVault) {
		t.Fatalf("Create into an inbox behind a linked ancestor: err = %v, want %v", err, ErrOutsideVault)
	}
	entries, err := os.ReadDir(away)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		names := make([]string, len(entries))
		for i, e := range entries {
			names[i] = e.Name()
		}
		t.Fatalf("outside the vault holds %v, want nothing: the inbox is refused before it is created", names)
	}
}

func TestCreateRefusesLinkedAssets(t *testing.T) {
	v := tempVault(t)
	away := t.TempDir()
	if err := os.RemoveAll(v.Abs("nn/assets")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(away, v.Abs("nn/assets")); err != nil {
		t.Fatal(err)
	}

	_, err := v.Create(NewNote{
		Title:  "Capture two",
		Images: []Image{{Data: []byte("image-bytes"), Ext: "png"}},
		Now:    createNow,
	})
	if !errors.Is(err, ErrOutsideVault) {
		t.Fatalf("Create with a linked assets folder: err = %v, want %v", err, ErrOutsideVault)
	}
	entries, err := os.ReadDir(away)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("the linked assets folder holds %d entries, want nothing written through the link", len(entries))
	}
	if _, err := os.Lstat(v.Abs("nn/capture-two.md")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the note was filed without its image: Lstat = %v", err)
	}
	noTempFiles(t, away)
	noTempFiles(t, v.Root)
}

func TestCreateDoesNotWriteThroughLink(t *testing.T) {
	v := tempVault(t)
	outside := t.TempDir()
	secret, before := writeOutside(t, outside, "secret.md")
	if err := os.Symlink(secret, v.Abs("nn/taken.md")); err != nil {
		t.Fatal(err)
	}

	note, err := v.Create(NewNote{Title: "Taken", Body: "mine", Now: createNow})
	if err != nil {
		t.Fatal(err)
	}
	if note.Path != "nn/taken-2.md" {
		t.Fatalf("Path = %s, want nn/taken-2.md", note.Path)
	}
	if got := readFile(t, secret); got != before {
		t.Fatalf("the file outside the vault was rewritten:\n%s", got)
	}
}

func TestRenderTemplateRefusesATemplatesDirectoryItShouldNotRead(t *testing.T) {
	const stranger = "---\ntags: [x]\n---\nSTRANGER\n"
	for _, tc := range []struct {
		name    string
		lay     func(t *testing.T, root string)
		outside bool
	}{
		{
			name: "a link out of the vault", outside: true,
			lay: func(t *testing.T, root string) {
				away := t.TempDir()
				if err := os.WriteFile(filepath.Join(away, "t.md"), []byte(stranger), 0o644); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(away, filepath.Join(root, "nn", "_templates")); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "a link that loops",
			lay: func(t *testing.T, root string) {
				link := filepath.Join(root, "nn", "_templates")
				if err := os.Symlink(link, link); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "a link too long to follow",
			lay: func(t *testing.T, root string) {
				far := linkTooLongToFollow(t, t.TempDir())
				if err := os.WriteFile(filepath.Join(far, "t.md"), []byte(stranger), 0o644); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(far, filepath.Join(root, "nn", "_templates")); err != nil {
					t.Fatal(err)
				}
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			if err := os.MkdirAll(filepath.Join(root, "nn"), 0o755); err != nil {
				t.Fatal(err)
			}
			tc.lay(t, root)
			v := &Vault{Root: root, Inbox: "nn"}

			got, err := v.RenderTemplate("t", nil)
			if err == nil || strings.Contains(got, "STRANGER") {
				t.Fatalf("RenderTemplate = %q, %v; want a refusal with nothing read", got, err)
			}
			if errors.Is(err, ErrNotFound) {
				t.Errorf("RenderTemplate = %v, which reads as a template that is not there", err)
			}
			if tc.outside != errors.Is(err, ErrOutsideVault) {
				t.Errorf("RenderTemplate = %v, want ErrOutsideVault = %v", err, tc.outside)
			}
			if want := "cannot tell where " + v.Abs("nn/_templates") + " leads"; !tc.outside && !strings.Contains(err.Error(), want) {
				t.Errorf("RenderTemplate = %v, want a refusal saying %q", err, want)
			}
		})
	}
}

func TestRenderTemplateFollowsWhatTheBoundaryAllows(t *testing.T) {
	root := t.TempDir()
	v := &Vault{Root: root, Inbox: "nn"}
	if _, err := v.RenderTemplate("t", nil); !errors.Is(err, ErrNotFound) {
		t.Errorf("RenderTemplate with no templates directory = %v, want %v", err, ErrNotFound)
	}

	shared := filepath.Join(root, "shared", "templates")
	if err := os.MkdirAll(shared, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(shared, "t.md"), []byte("# shared\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "nn"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(shared, filepath.Join(root, "nn", "_templates")); err != nil {
		t.Fatal(err)
	}
	if got, err := v.RenderTemplate("t", nil); err != nil || got != "# shared\n" {
		t.Errorf("RenderTemplate through a link inside the vault = %q, %v; want the template", got, err)
	}

	elsewhere := filepath.Join(t.TempDir(), "mine.md")
	if err := os.WriteFile(elsewhere, []byte("# linked by hand\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(elsewhere, filepath.Join(shared, "linked.md")); err != nil {
		t.Fatal(err)
	}
	if got, err := v.RenderTemplate("linked", nil); err != nil || got != "# linked by hand\n" {
		t.Errorf("RenderTemplate of a template file linked from outside the vault = %q, %v; want it followed", got, err)
	}
}
