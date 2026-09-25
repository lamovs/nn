package main

import (
	"encoding/base64"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lamovs/nn/internal/app"
	"github.com/lamovs/nn/internal/ocr"
	"github.com/lamovs/nn/internal/state"
	"github.com/lamovs/nn/internal/termimg"
)

func TestShowRendersTitleTagsMetaAndBody(t *testing.T) {
	root := newTestVault(t)
	writeNote(t, root, "nn/docker-cleanup.md",
		"---\ntags: [docker]\ndate: 2026-09-01\nrepo: nn\n---\n\n# Docker cleanup\n\nRun docker system prune.\n")

	stdout, _, code := runCmd(t, "", "show", "docker-cleanup")
	if code != 0 {
		t.Fatalf("code = %d", code)
	}
	for _, want := range []string{"Docker cleanup", "#docker", "date: 2026-09-01", "repo: nn", "Run docker system prune."} {
		if !strings.Contains(stdout, want) {
			t.Errorf("stdout = %q, want it to contain %q", stdout, want)
		}
	}
}

func TestShowUsesConfigOutputColor(t *testing.T) {
	root := newTestVault(t)
	writeConfig(t, "[output]\ncolor = \"always\"\n")
	writeNote(t, root, "nn/docker-cleanup.md", "# Docker cleanup\n\nRun docker system prune.\n")

	stdout, stderr, code := runCmd(t, "", "show", "docker-cleanup")
	if code != 0 {
		t.Fatalf("code = %d, stderr = %q", code, stderr)
	}
	if !strings.Contains(stdout, "\x1b[") {
		t.Errorf("stdout = %q, want color (show honors output.color = always)", stdout)
	}
}

func TestShowNotFoundFails(t *testing.T) {
	newTestVault(t)
	_, stderr, code := runCmd(t, "", "show", "nowhere-at-all")
	if code != 2 {
		t.Errorf("code = %d, want 2", code)
	}
	if stderr == "" {
		t.Error("expected an error message on stderr")
	}
}

func TestShowLayoutFallbackDisabledByConfig(t *testing.T) {
	root := newTestVault(t)
	writeConfig(t, "[search]\nlayout_fallback = false\n")
	writeNote(t, root, "nn/docker.md", "# Docker\n\nRun docker system prune.\n")

	_, _, code := runCmd(t, "", "show", cyrillicDocker)
	if code == 0 {
		t.Errorf("code = %d, want a failure (search.layout_fallback = false must stop the swap)", code)
	}
}

func TestShowLayoutFallbackEnabledByDefault(t *testing.T) {
	root := newTestVault(t)
	writeNote(t, root, "nn/docker.md", "# Docker\n\nRun docker system prune.\n")

	stdout, stderr, code := runCmd(t, "", "show", cyrillicDocker)
	if code != 0 {
		t.Fatalf("code = %d, stderr = %q, want the layout fallback to resolve it", code, stderr)
	}
	if !strings.Contains(stdout, "Run docker system prune.") {
		t.Errorf("stdout = %q, want the note body", stdout)
	}
}

func TestShowFromStdin(t *testing.T) {
	root := newTestVault(t)
	writeNote(t, root, "nn/a.md", "# A\n\nbody text\n")

	stdout, _, code := runCmd(t, "nn/a.md\n", "show", "-")
	if code != 0 {
		t.Fatalf("code = %d", code)
	}
	if !strings.Contains(stdout, "body text") {
		t.Errorf("stdout = %q", stdout)
	}
}

func TestShowFromStdinTakesFirstPath(t *testing.T) {
	root := newTestVault(t)
	writeNote(t, root, "nn/a.md", "# A\n\nbody a\n")
	writeNote(t, root, "nn/b.md", "# B\n\nbody b\n")

	stdout, stderr, code := runCmd(t, "nn/a.md\nnn/b.md\n", "show", "-")
	if code != 0 {
		t.Fatalf("code = %d, stderr = %q", code, stderr)
	}
	if !strings.Contains(stdout, "body a") {
		t.Errorf("stdout = %q, want the first piped note", stdout)
	}
	if strings.Contains(stdout, "body b") {
		t.Errorf("stdout = %q, want only the first piped note", stdout)
	}
	if !strings.Contains(stderr, "nn/a.md") || !strings.Contains(stderr, "2 paths") {
		t.Errorf("stderr = %q, want a note about the paths that were skipped", stderr)
	}
}

func TestShowRecordsOpen(t *testing.T) {
	root := newTestVault(t)
	writeNote(t, root, "nn/a.md", "# A\n\nbody\n")

	if _, _, code := runCmd(t, "", "show", "a"); code != 0 {
		t.Fatalf("code = %d", code)
	}

	statePath, err := state.Path()
	if err != nil {
		t.Fatal(err)
	}
	st, err := state.LoadFile(statePath, 14*24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if f := st.Frecency("nn/a.md"); f <= 0 {
		t.Errorf("Frecency(nn/a.md) = %v, want > 0 after show", f)
	}
	_ = root
}

func TestShowImageFallbackWhenNotATerminal(t *testing.T) {
	root := newTestVault(t)
	writeImage(t, root, "nn/assets/pic.png", []byte("fake"))
	writeNote(t, root, "nn/with-image.md", "# With image\n\n![[nn/assets/pic.png]]\n")

	stdout, _, code := runCmd(t, "", "show", "with-image")
	if code != 0 {
		t.Fatalf("code = %d", code)
	}
	if !strings.Contains(stdout, "[image: nn/assets/pic.png]") {
		t.Errorf("stdout = %q, want the fallback placeholder (not a real terminal)", stdout)
	}
}

func TestShowNoImagesForcesFallbackAndOCRStillPrints(t *testing.T) {
	root := newTestVault(t)
	writeImage(t, root, "nn/assets/pic.png", []byte("fake"))
	writeSidecar(t, root, "nn/assets/pic.png", []ocr.Line{{Text: "recognized text"}})
	writeNote(t, root, "nn/with-image.md", "# With image\n\n![[nn/assets/pic.png]]\n")

	old, oldProto := showIsTerminal, showProtocol
	showIsTerminal = func(any) bool { return true }
	showProtocol = func() termimg.Protocol { return termimg.ProtocolITerm2 }
	defer func() { showIsTerminal, showProtocol = old, oldProto }()

	stdout, _, code := runCmd(t, "", "show", "with-image", "--no-images", "--ocr")
	if code != 0 {
		t.Fatalf("code = %d", code)
	}
	if !strings.Contains(stdout, "[image: nn/assets/pic.png]") {
		t.Errorf("stdout = %q, want the placeholder even though the terminal is forced on", stdout)
	}
	if strings.Contains(stdout, "\x1b]1337") {
		t.Errorf("stdout = %q, must not contain an inline-image escape with --no-images", stdout)
	}
	if !strings.Contains(stdout, "recognized text") {
		t.Errorf("stdout = %q, want the OCR text under the placeholder", stdout)
	}
}

func TestShowImagesDefaultNeverFromConfig(t *testing.T) {
	root := newTestVault(t)
	writeConfig(t, "[show]\nimages = \"never\"\n")
	writeImage(t, root, "nn/assets/pic.png", []byte("fake"))
	writeNote(t, root, "nn/with-image.md", "# With image\n\n![[nn/assets/pic.png]]\n")
	forceInlineImages(t)

	stdout, stderr, code := runCmd(t, "", "show", "with-image")
	if code != 0 {
		t.Fatalf("code = %d, stderr = %q", code, stderr)
	}
	if !strings.Contains(stdout, "[image: nn/assets/pic.png]") {
		t.Errorf("stdout = %q, want the placeholder (show.images = never)", stdout)
	}
}

func TestShowImagesFlagForcesAutoOverConfig(t *testing.T) {
	root := newTestVault(t)
	writeConfig(t, "[show]\nimages = \"never\"\n")
	writeImage(t, root, "nn/assets/pic.png", []byte("fake"))
	writeNote(t, root, "nn/with-image.md", "# With image\n\n![[nn/assets/pic.png]]\n")
	forceInlineImages(t)

	stdout, stderr, code := runCmd(t, "", "show", "with-image", "--images")
	if code != 0 {
		t.Fatalf("code = %d, stderr = %q", code, stderr)
	}
	if strings.Contains(stdout, "[image: nn/assets/pic.png]") {
		t.Errorf("stdout = %q, want an inline image, not the placeholder (--images beats show.images = never)", stdout)
	}
}

func TestShowImagesFlagsCannotCombine(t *testing.T) {
	newTestVault(t)
	_, stderr, code := runCmd(t, "", "show", "x", "--images", "--no-images")
	if code != 2 {
		t.Errorf("code = %d, want 2, stderr = %q", code, stderr)
	}
	if !strings.Contains(stderr, "--images and --no-images cannot be combined") {
		t.Errorf("stderr = %q, want it to name the conflicting flags", stderr)
	}
}

func TestShowOCRDefaultFromConfig(t *testing.T) {
	root := newTestVault(t)
	writeConfig(t, "[show]\nocr = true\n")
	writeImage(t, root, "nn/assets/pic.png", []byte("fake"))
	writeSidecar(t, root, "nn/assets/pic.png", []ocr.Line{{Text: "recognized text"}})
	writeNote(t, root, "nn/with-image.md", "# With image\n\n![[nn/assets/pic.png]]\n")

	stdout, stderr, code := runCmd(t, "", "show", "with-image")
	if code != 0 {
		t.Fatalf("code = %d, stderr = %q", code, stderr)
	}
	if !strings.Contains(stdout, "recognized text") {
		t.Errorf("stdout = %q, want the OCR text (show.ocr = true)", stdout)
	}
}

func TestShowNoOCRFlagBeatsConfig(t *testing.T) {
	root := newTestVault(t)
	writeConfig(t, "[show]\nocr = true\n")
	writeImage(t, root, "nn/assets/pic.png", []byte("fake"))
	writeSidecar(t, root, "nn/assets/pic.png", []ocr.Line{{Text: "recognized text"}})
	writeNote(t, root, "nn/with-image.md", "# With image\n\n![[nn/assets/pic.png]]\n")

	stdout, stderr, code := runCmd(t, "", "show", "with-image", "--no-ocr")
	if code != 0 {
		t.Fatalf("code = %d, stderr = %q", code, stderr)
	}
	if strings.Contains(stdout, "recognized text") {
		t.Errorf("stdout = %q, want no OCR text (--no-ocr beats show.ocr = true)", stdout)
	}
}

func TestShowOCRFlagsCannotCombine(t *testing.T) {
	newTestVault(t)
	_, stderr, code := runCmd(t, "", "show", "x", "--ocr", "--no-ocr")
	if code != 2 {
		t.Errorf("code = %d, want 2, stderr = %q", code, stderr)
	}
	if !strings.Contains(stderr, "--ocr and --no-ocr cannot be combined") {
		t.Errorf("stderr = %q, want it to name the conflicting flags", stderr)
	}
}

// An embed is untrusted text a note carries: "![[../../../../etc/passwd]]" is as easy to write
// as any other. The tests below cover the three ways out - climbing with "..", an absolute
// path, and a symlink that lives inside the vault but points out of it.

// vaultWithFileOutside puts a file at base/outside/NAME, next to the vault at base/vault.
func vaultWithFileOutside(t *testing.T, name string, data []byte) (root, outside string) {
	t.Helper()
	base := newTestVault(t)
	root = filepath.Join(base, "vault")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("NN_ROOT", root)
	writeImage(t, base, path.Join("outside", name), data)
	return root, filepath.Join(base, "outside", name)
}

func forceInlineImages(t *testing.T) {
	t.Helper()
	oldTerm, oldProto := showIsTerminal, showProtocol
	showIsTerminal = func(any) bool { return true }
	showProtocol = func() termimg.Protocol { return termimg.ProtocolKitty }
	t.Cleanup(func() { showIsTerminal, showProtocol = oldTerm, oldProto })
}

// assertImageNotLeaked checks for the base64-encoded bytes and the graphics escape a real inline read would produce.
func assertImageNotLeaked(t *testing.T, stdout string, data []byte) {
	t.Helper()
	if strings.Contains(stdout, base64.StdEncoding.EncodeToString(data)) {
		t.Errorf("stdout carries the bytes of a file outside the vault: %q", stdout)
	}
	if strings.Contains(stdout, "\x1b_G") || strings.Contains(stdout, "\x1b]1337") {
		t.Errorf("stdout = %q, want no inline image for a file outside the vault", stdout)
	}
}

func TestShowRelativeEmbedOutOfVaultIsNotRead(t *testing.T) {
	secret := []byte("secret-bytes-outside-the-vault")
	root, _ := vaultWithFileOutside(t, "secret.png", secret)
	writeNote(t, root, "nn/with-image.md", "# With image\n\n![[../outside/secret.png]]\n")
	forceInlineImages(t)

	stdout, stderr, code := runCmd(t, "", "show", "with-image")
	if code != 0 {
		t.Fatalf("code = %d, stderr = %q", code, stderr)
	}
	assertImageNotLeaked(t, stdout, secret)
	if !strings.Contains(stdout, "With image") {
		t.Errorf("stdout = %q, want the note itself to keep rendering", stdout)
	}
	if !strings.Contains(stdout, "[image: ../outside/secret.png]") {
		t.Errorf("stdout = %q, want the placeholder a missing image gets", stdout)
	}
	if !strings.Contains(stderr, "outside the vault") {
		t.Errorf("stderr = %q, want a warning naming the vault boundary", stderr)
	}
}

// vault.Abs cannot be trusted with an absolute embed either: it silently re-roots a plain one
// inside the vault, and joining enough ".." walks back out of it entirely. Both must be refused
// by their real target. vault.resolveEmbed never produces an absolute embed itself, so this
// drives renderImage directly, since the boundary belongs where the file is opened.
func TestShowAbsoluteEmbedOutOfVaultIsNotRead(t *testing.T) {
	secret := []byte("secret-bytes-outside-the-vault")
	root, outside := vaultWithFileOutside(t, "secret.png", secret)
	forceInlineImages(t)

	env, err := app.Open()
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct{ name, embed string }{
		{"plain", outside},
		{"climbing back out of the root", climbOutEmbed(root, outside)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var stdout, stderr strings.Builder
			renderImage(&stdout, &stderr, env, tc.embed, showOptions{}, termimg.ProtocolKitty)
			assertImageNotLeaked(t, stdout.String(), secret)
			if !strings.Contains(stdout.String(), "[image: ") {
				t.Errorf("stdout = %q, want the placeholder a missing image gets", stdout.String())
			}
			if !strings.Contains(stderr.String(), "outside the vault") {
				t.Errorf("stderr = %q, want a warning naming the vault boundary", stderr.String())
			}
		})
	}
}

// climbOutEmbed builds an absolute embed with enough ".." to walk off the filesystem root and
// back down to target: joined onto root and Cleaned, it lands on target itself.
func climbOutEmbed(root, target string) string {
	up := strings.Count(filepath.ToSlash(filepath.Clean(root)), "/") + 1
	return "/" + strings.Repeat("../", up) + strings.TrimPrefix(filepath.ToSlash(target), "/")
}

func TestShowSymlinkEmbedOutOfVaultIsNotRead(t *testing.T) {
	secret := []byte("secret-bytes-outside-the-vault")
	root, outside := vaultWithFileOutside(t, "secret.png", secret)
	link := filepath.Join(root, "nn", "assets", "link.png")
	if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}
	writeNote(t, root, "nn/with-image.md", "# With image\n\n![[nn/assets/link.png]]\n")
	forceInlineImages(t)

	stdout, stderr, code := runCmd(t, "", "show", "with-image")
	if code != 0 {
		t.Fatalf("code = %d, stderr = %q", code, stderr)
	}
	assertImageNotLeaked(t, stdout, secret)
	if !strings.Contains(stdout, "[image: nn/assets/link.png]") {
		t.Errorf("stdout = %q, want the placeholder a missing image gets", stdout)
	}
	if !strings.Contains(stderr, "outside the vault") {
		t.Errorf("stderr = %q, want a warning naming the vault boundary", stderr)
	}
}

func TestShowSymlinkEmbedInsideVaultStillRenders(t *testing.T) {
	root := newTestVault(t)
	writeImage(t, root, "nn/assets/pic.png", []byte("fake-image-bytes"))
	link := filepath.Join(root, "nn", "assets", "link.png")
	if err := os.Symlink(filepath.Join(root, "nn", "assets", "pic.png"), link); err != nil {
		t.Fatal(err)
	}
	writeNote(t, root, "nn/with-image.md", "# With image\n\n![[nn/assets/link.png]]\n")
	forceInlineImages(t)

	stdout, stderr, code := runCmd(t, "", "show", "with-image")
	if code != 0 {
		t.Fatalf("code = %d, stderr = %q", code, stderr)
	}
	if !strings.Contains(stdout, "\x1b_G") {
		t.Errorf("stdout = %q, want the image inline: the link stays inside the vault", stdout)
	}
	if strings.Contains(stderr, "outside the vault") {
		t.Errorf("stderr = %q, want no boundary warning for a link inside the vault", stderr)
	}
}

// The sidecar lookup must use the same vetted path as the image read: a sidecar under .nn/ocr
// never itself leaves the vault, but its cached text is the note's recognized content for a
// file show otherwise refuses to show, so a lookup on the raw embed text would leak it anyway.

func TestShowOCRSkipsSidecarForSymlinkEmbedOutOfVault(t *testing.T) {
	root, outside := vaultWithFileOutside(t, "secret.png", []byte("secret-bytes"))
	link := filepath.Join(root, "nn", "assets", "link.png")
	if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}
	writeSidecar(t, root, "nn/assets/link.png", []ocr.Line{{Text: "secret-text-outside-the-vault"}})
	writeNote(t, root, "nn/with-image.md", "# With image\n\n![[nn/assets/link.png]]\n")

	stdout, stderr, code := runCmd(t, "", "show", "with-image", "--no-images", "--ocr")
	if code != 0 {
		t.Fatalf("code = %d, stderr = %q", code, stderr)
	}
	if strings.Contains(stdout, "secret-text-outside-the-vault") {
		t.Errorf("stdout carries the OCR text of a file outside the vault: %q", stdout)
	}
	if !strings.Contains(stdout, "[image: nn/assets/link.png]") {
		t.Errorf("stdout = %q, want the note to keep rendering around the refused embed", stdout)
	}
}

// The sidecar is looked up under the path the note spells, not the one a symlink resolves to:
// "nn ocr" files it under the former, so resolving first would lose every sidecar behind a link.
func TestShowOCRSymlinkEmbedInsideVaultStillPrints(t *testing.T) {
	root := newTestVault(t)
	writeImage(t, root, "nn/assets/pic.png", []byte("fake-image-bytes"))
	link := filepath.Join(root, "nn", "assets", "link.png")
	if err := os.Symlink(filepath.Join(root, "nn", "assets", "pic.png"), link); err != nil {
		t.Fatal(err)
	}
	writeSidecar(t, root, "nn/assets/link.png", []ocr.Line{{Text: "text behind the link"}})
	writeNote(t, root, "nn/with-image.md", "# With image\n\n![[nn/assets/link.png]]\n")

	stdout, stderr, code := runCmd(t, "", "show", "with-image", "--no-images", "--ocr")
	if code != 0 {
		t.Fatalf("code = %d, stderr = %q", code, stderr)
	}
	if !strings.Contains(stdout, "text behind the link") {
		t.Errorf("stdout = %q, want the sidecar of a link that stays inside the vault", stdout)
	}
}

// Neither "nn s" nor "nn show --ocr" reads a sidecar that is not a regular file - a .json
// sidecar symlinked to a file outside the vault must read as no text, the same as one that does not parse.
func TestReadersIgnoreALinkedSidecarFile(t *testing.T) {
	root := newTestVault(t)
	writeImage(t, root, "nn/assets/a.png", []byte("bytes"))
	writeNote(t, root, "nn/n.md", "# N\n\n![[nn/assets/a.png]]\n")
	stranger := filepath.Join(t.TempDir(), "s.json")
	if err := os.WriteFile(stranger, []byte(`{"lines":[{"text":"STRANGER"}]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, ".nn", "ocr", "nn", "assets", "a.png.json")
	if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(stranger, link); err != nil {
		t.Fatal(err)
	}

	stdout, stderr, code := runCmd(t, "", "s", "STRANGER", "--paths")
	if code != 1 || stdout != "" {
		t.Errorf("nn s STRANGER: code = %d, stdout = %q, stderr = %q; want nothing found", code, stdout, stderr)
	}
	stdout, stderr, code = runCmd(t, "", "show", "nn/n.md", "--ocr")
	if code != 0 {
		t.Fatalf("nn show --ocr: code = %d, stderr = %q", code, stderr)
	}
	if strings.Contains(stdout, "STRANGER") {
		t.Errorf("nn show --ocr printed the text of a file outside the vault: %q", stdout)
	}
	if !strings.Contains(stdout, "[image: nn/assets/a.png]") {
		t.Errorf("stdout = %q, want the note to keep rendering around the image", stdout)
	}
}

// An embed nn cannot follow must warn on stderr exactly once, whether or not the inline
// branch already said so, instead of silently printing the placeholder with no reason given.
func TestShowOCRWarnsAboutAnImageItCannotFollow(t *testing.T) {
	for _, tc := range []struct {
		name string
		lay  func(t *testing.T, root string) (embed string)
	}{
		{
			name: "a directory nn may not search",
			lay: func(t *testing.T, root string) string {
				writeImage(t, root, "nn/locked/pic.png", []byte("fake-image-bytes"))
				writeSidecar(t, root, "nn/locked/pic.png", []ocr.Line{{Text: "cached text"}})
				locked := filepath.Join(root, "nn", "locked")
				if err := os.Chmod(locked, 0o000); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { os.Chmod(locked, 0o755) })
				if _, err := os.Lstat(filepath.Join(locked, "pic.png")); !errors.Is(err, fs.ErrPermission) {
					t.Skipf("a directory with mode 000 can still be searched here (Lstat under it = %v)", err)
				}
				return "nn/locked/pic.png"
			},
		},
		{
			name: "a link that loops",
			lay: func(t *testing.T, root string) string {
				writeSidecar(t, root, "nn/loop/pic.png", []ocr.Line{{Text: "cached text"}})
				loop := filepath.Join(root, "nn", "loop")
				if err := os.MkdirAll(filepath.Dir(loop), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(loop, loop); err != nil {
					t.Fatal(err)
				}
				return "nn/loop/pic.png"
			},
		},
	} {
		for _, inline := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s, inline %v", tc.name, inline), func(t *testing.T) {
				root := newTestVault(t)
				embed := tc.lay(t, root)
				env, err := app.Open()
				if err != nil {
					t.Fatal(err)
				}
				proto := termimg.ProtocolNone
				if inline {
					forceInlineImages(t)
					proto = termimg.ProtocolKitty
				}

				var stdout, stderr strings.Builder
				renderImage(&stdout, &stderr, env, embed, showOptions{ocr: true}, proto)
				if strings.Contains(stdout.String(), "cached text") {
					t.Errorf("stdout = %q, want no text for an image nn refuses to read", stdout.String())
				}
				if !strings.Contains(stdout.String(), "[image: "+embed+"]") {
					t.Errorf("stdout = %q, want the placeholder", stdout.String())
				}
				warnings := strings.Count(stderr.String(), "nn: show: warning: ")
				if warnings != 1 || !strings.Contains(stderr.String(), "cannot tell where") {
					t.Errorf("stderr = %q, want one warning saying what stopped nn", stderr.String())
				}
			})
		}
	}
}

// The sidecar lookup for an absolute embed inside the vault must use the vetted relative path,
// not the raw absolute text: a leading "/" is merely trimmed, so a raw lookup finds nothing under .nn/ocr.
func TestShowOCRUsesVettedPathForAbsoluteEmbed(t *testing.T) {
	root := newTestVault(t)
	writeImage(t, root, "nn/assets/pic.png", []byte("fake-image-bytes"))
	writeSidecar(t, root, "nn/assets/pic.png", []ocr.Line{{Text: "recognized text"}})

	env, err := app.Open()
	if err != nil {
		t.Fatal(err)
	}
	var stdout, stderr strings.Builder
	embed := filepath.Join(root, "nn", "assets", "pic.png")
	renderImage(&stdout, &stderr, env, embed, showOptions{ocr: true}, termimg.ProtocolNone)
	if !strings.Contains(stdout.String(), "recognized text") {
		t.Errorf("stdout = %q, want the sidecar the absolute embed resolves to", stdout.String())
	}
}

func TestShowInlineImageWithForcedProtocol(t *testing.T) {
	root := newTestVault(t)
	writeImage(t, root, "nn/assets/pic.png", []byte("fake-image-bytes"))
	writeNote(t, root, "nn/with-image.md", "# With image\n\n![[nn/assets/pic.png]]\n")

	old, oldProto := showIsTerminal, showProtocol
	showIsTerminal = func(any) bool { return true }
	showProtocol = func() termimg.Protocol { return termimg.ProtocolKitty }
	defer func() { showIsTerminal, showProtocol = old, oldProto }()

	stdout, _, code := runCmd(t, "", "show", "with-image")
	if code != 0 {
		t.Fatalf("code = %d", code)
	}
	if !strings.Contains(stdout, "\x1b_G") {
		t.Errorf("stdout = %q, want a kitty graphics escape sequence", stdout)
	}
	if strings.Contains(stdout, "[image:") {
		t.Errorf("stdout = %q, should not fall back when a protocol is forced", stdout)
	}
}

// show's boundary check goes through ocr.Locate, the same checker "nn ocr" uses, so a vault
// root reached through a symlink (macOS's /tmp -> /private/tmp) resolves the same way for both:
// an embed naming the resolved spelling of a file plainly inside the vault must still render.
func TestShowUsesTheOneVaultChecker(t *testing.T) {
	base := newTestVault(t)
	real := filepath.Join(base, "real")
	if err := os.MkdirAll(real, 0o755); err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(base, "vault")
	if err := os.Symlink(real, root); err != nil {
		t.Skipf("symlinks unavailable here: %v", err)
	}
	t.Setenv("NN_ROOT", root)
	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	writeImage(t, root, "nn/assets/pic.png", []byte("fake-image-bytes"))
	writeSidecar(t, root, "nn/assets/pic.png", []ocr.Line{{Text: "recognized text"}})

	env, err := app.Open()
	if err != nil {
		t.Fatal(err)
	}
	for _, embed := range []string{
		filepath.Join(root, "nn", "assets", "pic.png"),
		filepath.Join(realRoot, "nn", "assets", "pic.png"),
	} {
		var stdout, stderr strings.Builder
		renderImage(&stdout, &stderr, env, embed, showOptions{ocr: true}, termimg.ProtocolNone)
		if strings.Contains(stderr.String(), "outside the vault") {
			t.Errorf("%s: stderr = %q, want no boundary warning for a file inside the vault", embed, stderr.String())
		}
		if !strings.Contains(stdout.String(), "recognized text") {
			t.Errorf("%s: stdout = %q, want the sidecar the embed resolves to", embed, stdout.String())
		}
	}
}
