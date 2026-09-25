package main

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/lamovs/nn/internal/capture"
	"github.com/lamovs/nn/internal/config"
	"github.com/lamovs/nn/internal/ocr"
	"github.com/lamovs/nn/internal/platform"
	"github.com/lamovs/nn/internal/vault"
)

func addAIFixture(t *testing.T, imageCapable bool) (root, marker string) {
	t.Helper()
	root = newTestVault(t)
	base := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", filepath.Join(base, "cache"))
	marker = filepath.Join(base, "request")
	t.Setenv("NN_ADD_AI_REQUEST", marker)
	t.Setenv("NN_ADD_AI_IMAGE", filepath.Join(base, "image"))
	engine := filepath.Join(base, "fake-model")
	script := "#!/bin/sh\n/bin/cat > \"$NN_ADD_AI_REQUEST\"\n"
	if imageCapable {
		script += "/bin/cat \"$1\" > \"$NN_ADD_AI_IMAGE\"\n"
	}
	script += "printf '%s' '{\"title\":\"Generated note\",\"tags\":[\"known\",\"New Word\",\"new-word\",\"123\",\"bad!\"],\"body\":\"MODEL REWRITE MUST NOT LAND\"}'\n"
	if err := os.WriteFile(engine, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	command := "[" + config.TOMLString(engine)
	if imageCapable {
		command += ",\"{image}\""
	}
	command += "]"
	text := "[capture]\nocr=false\n[ai]\nprofile=\"metadata\"\n[ai.tasks.title]\nrun=\"flag\"\n[ai.profiles.metadata]\nengine=\"command\"\ncommand=" + command + "\n[ai.consent]\nmetadata=\"always\"\n[notify]\nenabled=false\n"
	if err := os.WriteFile(os.Getenv("NN_CONFIG"), []byte(text), 0600); err != nil {
		t.Fatal(err)
	}
	writeNote(t, root, "known-vocabulary.md", "---\ntags: [Known]\n---\nPrivate old note not supplied\n")
	return root, marker
}

func addAINote(t *testing.T, root, stdout string) *vault.Note {
	t.Helper()
	rel := strings.TrimSpace(strings.TrimPrefix(strings.TrimPrefix(stdout, "+ "), "~ "))
	v := &vault.Vault{Root: root, Inbox: "nn"}
	n, err := v.Load(rel)
	if err != nil {
		t.Fatalf("load %q: %v", rel, err)
	}
	return n
}

func withFakeClipboard(t *testing.T, data []byte, ext string, imageErr error, text []byte, textErr error) *int {
	t.Helper()
	oldImage, oldText := captureClipboardImage, captureClipboardText
	calls := 0
	captureClipboardImage = func(context.Context) ([]byte, string, error) { return data, ext, imageErr }
	captureClipboardText = func(context.Context) ([]byte, error) { calls++; return text, textErr }
	t.Cleanup(func() { captureClipboardImage, captureClipboardText = oldImage, oldText })
	return &calls
}

func addPNG(t *testing.T) []byte {
	t.Helper()
	var b bytes.Buffer
	if err := png.Encode(&b, image.NewRGBA(image.Rect(0, 0, 1, 1))); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func TestAddAITitleAndTagSourceMatrix(t *testing.T) {
	for _, tc := range []struct {
		name, stdin string
		args        []string
		title, body string
	}{
		{"stdin", "Original stdin text\n", nil, "Generated note", "Original stdin text"},
		{"words", "", []string{"Manual words"}, "Manual words", ""},
		{"words and stdin", "Original stdin text\n", []string{"Manual words"}, "Manual words", "Original stdin text"},
		{"explicit title", "", []string{"Original words", "--title", "Explicit title"}, "Explicit title", "Original words"},
		{"code", "", []string{"printf hello", "--code=sh"}, "Generated note", "```sh\nprintf hello\n```"},
		{"editor", "Initial draft", []string{"-e"}, "Generated note", "Edited original source"},
		{"template", "Captured after template", []string{"-T", "example"}, "Generated note", "Template introduction\n\nCaptured after template"},
		{"body heading", "# Manual heading\n\nBody", nil, "Manual heading", "# Manual heading"},
		{"clipboard text", "ignored stdin", []string{"--clip"}, "Generated note", "Clipboard text\nsecond line"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root, marker := addAIFixture(t, false)
			if tc.name == "editor" {
				installEditorThatWrites(t, "Edited original source\n")
			}
			if tc.name == "template" {
				writeNote(t, root, "nn/_templates/example.md", "---\ntags: [manual]\n---\nTemplate introduction\n")
			}
			if tc.name == "clipboard text" {
				withFakeClipboard(t, nil, "", platform.ErrNoImage, []byte("Clipboard text\nsecond line\n"), nil)
			}
			args := append([]string{"add"}, tc.args...)
			args = append(args, "--ai", "--ai-mode", "wait", "--new")
			stdout, stderr, code := runCmd(t, tc.stdin, args...)
			if code != 0 {
				t.Fatalf("exit=%d stderr=%s", code, stderr)
			}
			n := addAINote(t, root, stdout)
			if n.Title != tc.title || !strings.Contains(n.Body, tc.body) || strings.Contains(n.Body, "MODEL REWRITE") || !strings.HasSuffix(n.Body, "#Known #new-word\n") {
				t.Fatalf("note=%+v", n)
			}
			sent, err := os.ReadFile(marker)
			if err != nil || bytes.Contains(sent, []byte("Private old note")) {
				t.Fatalf("request=%s err=%v", sent, err)
			}
			tags, _, code := runCmd(t, "", "tags", "--names")
			if code != 0 || !strings.Contains(tags, "new-word") {
				t.Fatalf("new tags not listed: %q", tags)
			}
			found, _, code := runCmd(t, "", "s", "-t", "new-word", "--paths")
			if code != 0 || !strings.Contains(found, n.Path) {
				t.Fatalf("new tags not searchable: %q", found)
			}
		})
	}
}

func TestAddAIImageSourcesAndOCROnce(t *testing.T) {
	for _, source := range []string{"stdin", "clipboard"} {
		for _, capable := range []bool{true, false} {
			t.Run(source+map[bool]string{true: " image", false: " OCR"}[capable], func(t *testing.T) {
				root, marker := addAIFixture(t, capable)
				img := addPNG(t)
				engine := &fakeOCREngine{lines: []ocr.Line{{Text: "Recognized new OCR content"}}}
				withFakeOCR(t, engine)
				stdin := string(img)
				args := []string{"add", "--ocr", "--ai", "--ai-mode", "wait", "--new"}
				if source == "clipboard" {
					calls := withFakeClipboard(t, img, "png", nil, nil, errors.New("must not read text"))
					defer func() {
						if *calls != 0 {
							t.Fatal("image-first read text")
						}
					}()
					args = append(args, "--clip")
					stdin = ""
				}
				stdout, stderr, code := runCmd(t, stdin, args...)
				if code != 0 {
					t.Fatalf("exit=%d %s", code, stderr)
				}
				n := addAINote(t, root, stdout)
				if len(n.Embeds) != 1 || !strings.HasSuffix(n.Body, "]]\n\n#Known #new-word\n") {
					t.Fatalf("note=%+v", n)
				}
				if engine.calls != 1 {
					t.Fatalf("OCR ran %d times", engine.calls)
				}
				if result, err := ocr.LoadSidecar(root, n.Embeds[0]); err != nil || len(result.Lines) != 1 {
					t.Fatalf("sidecar=%+v err=%v", result, err)
				}
				request, _ := os.ReadFile(marker)
				if !bytes.Contains(request, []byte("Recognized new OCR content")) {
					t.Fatal("OCR absent from request")
				}
				if capable {
					got, err := os.ReadFile(os.Getenv("NN_ADD_AI_IMAGE"))
					if err != nil || !bytes.Equal(got, img) {
						t.Fatal("original image not sent")
					}
				}
			})
		}
	}
}

func TestAddAIPreviewAndSecrets(t *testing.T) {
	// Fixture is split so secret scanners do not flag it.
	secret := "github token gh" + "p_a1b2c3d4e5f6g7h8i9j0k1l2m3n4o5p6q7r8s9t0"
	for _, tc := range []string{"preview", "local approval", "allow secret", "allow secret needs consent", "no ai"} {
		t.Run(tc, func(t *testing.T) {
			root, marker := addAIFixture(t, false)
			before, _ := os.ReadFile(os.Getenv("NN_CONFIG"))
			args := []string{"add", "--ai", "--ai-mode", "wait", "--new"}
			input := secret
			if tc == "preview" {
				args = append(args, "--preview", "--ai=missing-profile")
				input = "Preview source"
			}
			if tc == "allow secret" || tc == "allow secret needs consent" {
				args = append(args, "--allow-secret")
			}
			if tc == "allow secret needs consent" {
				before = []byte(strings.ReplaceAll(string(before), "metadata=\"always\"", "metadata=\"ask\""))
				os.WriteFile(os.Getenv("NN_CONFIG"), before, 0600)
			}
			if tc == "local approval" || tc == "no ai" {
				oldInteractive, oldConfirm := addInteractive, addConfirm
				addInteractive = func() bool { return true }
				addConfirm = func(string, bool) (bool, error) { return true, nil }
				t.Cleanup(func() { addInteractive, addConfirm = oldInteractive, oldConfirm })
			}
			if tc == "no ai" {
				args = append(args, "--no-ai")
			}
			stdout, stderr, code := runCmd(t, input, args...)
			if code != 0 {
				t.Fatalf("exit=%d %s", code, stderr)
			}
			_, runErr := os.Stat(marker)
			if tc == "allow secret" {
				if runErr != nil {
					t.Fatal("explicit transfer did not run")
				}
			} else if !errors.Is(runErr, os.ErrNotExist) {
				t.Fatal("unauthorized model call")
			}
			if tc == "local approval" && !strings.Contains(stderr, "AI skipped: possible secret") {
				t.Fatalf("missing AI skip warning: %s", stderr)
			}
			if tc == "no ai" && strings.Contains(stderr, "AI skipped") {
				t.Fatalf("no-ai warned: %s", stderr)
			}
			if tc == "preview" {
				if _, err := os.Stat(filepath.Join(os.Getenv("XDG_CACHE_HOME"), "nn", "ai")); !errors.Is(err, os.ErrNotExist) {
					t.Fatal("preview created job cache")
				}
			} else {
				n := addAINote(t, root, stdout)
				if !strings.Contains(n.Body, secret) {
					t.Fatal("secret locally approved but lost")
				}
			}
			after, _ := os.ReadFile(os.Getenv("NN_CONFIG"))
			if !bytes.Equal(before, after) {
				t.Fatal("consent config changed")
			}
		})
	}
}

func TestAddAIAppendAndSimilarityPreserveTarget(t *testing.T) {
	for _, similarity := range []bool{false, true} {
		t.Run(map[bool]string{false: "explicit", true: "similarity"}[similarity], func(t *testing.T) {
			root, marker := addAIFixture(t, false)
			before := "---\naliases: [Manual target]\ntags: [manual]\n---\nPrivate old target body\n"
			writeNote(t, root, "nn/target.md", before)
			args := []string{"add", "New fragment", "--to", "target"}
			if similarity {
				args = []string{"add", "Manual target"}
				oldInteractive, oldSimilar := addInteractive, askSimilarNote
				addInteractive = func() bool { return true }
				askSimilarNote = func([]capture.Similar) string { return "append" }
				t.Cleanup(func() { addInteractive, askSimilarNote = oldInteractive, oldSimilar })
			}
			args = append(args, "--ai", "--ai-mode", "wait")
			stdout, stderr, code := runCmd(t, "New supplied body", args...)
			if code != 0 {
				t.Fatalf("exit=%d %s", code, stderr)
			}
			n := addAINote(t, root, stdout)
			after, _ := os.ReadFile(filepath.Join(root, "nn/target.md"))
			if n.Path != "nn/target.md" || n.Title != "Manual target" || !bytes.HasPrefix(after, []byte(before)) || !strings.HasSuffix(n.Body, "#Known #new-word\n") {
				t.Fatalf("target changed: %+v", n)
			}
			sent, _ := os.ReadFile(marker)
			if bytes.Contains(sent, []byte("Private old target")) || bytes.Contains(sent, []byte("Manual target")) {
				t.Fatalf("old target sent: %s", sent)
			}
		})
	}
}

func TestAddClipboardFallbackBoundaries(t *testing.T) {
	for _, imageErr := range []error{platform.ErrNoImage, platform.ErrImageUnavailable, platform.ErrCancelled, errors.New("decode failed")} {
		t.Run(imageErr.Error(), func(t *testing.T) {
			newTestVault(t)
			calls := withFakeClipboard(t, nil, "", imageErr, []byte("clipboard source\n"), nil)
			_, _, code := runCmd(t, "", "add", "--clip", "--no-ai")
			fallback := errors.Is(imageErr, platform.ErrNoImage) || errors.Is(imageErr, platform.ErrImageUnavailable)
			if fallback && (code != 0 || *calls != 1) || !fallback && (code == 0 || *calls != 0) {
				t.Fatalf("fallback=%v code=%d calls=%d", fallback, code, *calls)
			}
		})
	}
	newTestVault(t)
	withFakeClipboard(t, nil, "", platform.ErrNoImage, []byte(" \n\t"), nil)
	if _, _, code := runCmd(t, "", "add", "Positional title", "--clip", "--no-ai"); code == 0 {
		t.Fatal("empty clipboard created title-only note")
	}
}

func TestAddAIGIFAndWebP(t *testing.T) {
	for _, img := range [][]byte{[]byte("GIF89aoriginal"), []byte("RIFF\x10\x00\x00\x00WEBPoriginal")} {
		t.Run(string(img[:4]), func(t *testing.T) {
			root, _ := addAIFixture(t, true)
			stdout, stderr, code := runCmd(t, string(img), "add", "--no-ocr", "--ai", "--ai-mode", "wait", "--new")
			if code != 0 {
				t.Fatalf("exit=%d %s", code, stderr)
			}
			n := addAINote(t, root, stdout)
			if !slices.Contains(n.Tags, "new-word") {
				t.Fatalf("generic image rejected: %+v %s", n, stderr)
			}
			got, err := os.ReadFile(os.Getenv("NN_ADD_AI_IMAGE"))
			if err != nil || !bytes.Equal(got, img) {
				t.Fatal("generic image changed")
			}
		})
	}
}

func TestAddOCRSecretKeepsLocalImageWithoutAIConsent(t *testing.T) {
	for _, consent := range []string{"never", "ask"} {
		t.Run(consent, func(t *testing.T) {
			root, marker := addAIFixture(t, true)
			cfg, _ := os.ReadFile(os.Getenv("NN_CONFIG"))
			cfg = []byte(strings.ReplaceAll(strings.ReplaceAll(string(cfg), "run=\"flag\"", "run=\"always\""), "metadata=\"always\"", "metadata="+config.TOMLString(consent)))
			if err := os.WriteFile(os.Getenv("NN_CONFIG"), cfg, 0600); err != nil {
				t.Fatal(err)
			}
			engine := &fakeOCREngine{lines: []ocr.Line{{Text: "gh" + "p_a1b2c3d4e5f6g7h8i9j0k1l2m3n4o5p6q7r8s9t0"}}}
			withFakeOCR(t, engine)
			stdout, stderr, code := runCmd(t, string(addPNG(t)), "add", "--ocr", "--new")
			if code != 0 {
				t.Fatalf("local image refused: %d %s", code, stderr)
			}
			note := addAINote(t, root, stdout)
			if len(note.Embeds) != 1 || engine.calls != 1 {
				t.Fatalf("image/OCR lost: %+v calls=%d", note, engine.calls)
			}
			if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("OCR secret sent to model")
			}
			if !strings.Contains(stderr, "AI skipped: possible secret") {
				t.Fatalf("missing transfer guard: %s", stderr)
			}
		})
	}
}

func TestAddRepeatedImageKeepsOtherOCR(t *testing.T) {
	for _, aiEnabled := range []bool{false, true} {
		t.Run(map[bool]string{false: "ordinary OCR", true: "precomputed AI OCR"}[aiEnabled], func(t *testing.T) {
			root, _ := addAIFixture(t, true)
			a := addPNG(t)
			pixel := image.NewRGBA(image.Rect(0, 0, 1, 1))
			pixel.SetRGBA(0, 0, color.RGBA{R: 255, A: 255})
			var b bytes.Buffer
			if err := png.Encode(&b, pixel); err != nil {
				t.Fatal(err)
			}
			v := &vault.Vault{Root: root, Inbox: "nn"}
			written, err := v.CreateWithAssets(vault.NewNote{Title: "Target", Body: "Original", Images: []vault.Image{{Data: a, Ext: "png"}, {Data: b.Bytes(), Ext: "png"}}})
			if err != nil {
				t.Fatal(err)
			}
			imageA, imageB := written.Images[0], written.Images[1]
			if err := ocr.SaveSidecar(root, imageB, &ocr.Result{Lines: []ocr.Line{{Text: "Correct B OCR"}}}); err != nil {
				t.Fatal(err)
			}
			bText, bJSON := ocr.SidecarPaths(root, imageB)
			beforeText, _ := os.ReadFile(bText)
			beforeJSON, _ := os.ReadFile(bJSON)
			engine := &fakeOCREngine{lines: []ocr.Line{{Text: "Correct A OCR"}}}
			withFakeOCR(t, engine)
			args := []string{"add", "--to", written.Note.Path, "--ocr", "--no-ai"}
			if aiEnabled {
				args = []string{"add", "--to", written.Note.Path, "--ocr", "--ai", "--ai-mode", "wait"}
			}
			_, stderr, code := runCmd(t, string(a), args...)
			if code != 0 {
				t.Fatalf("exit=%d %s", code, stderr)
			}
			afterText, _ := os.ReadFile(bText)
			afterJSON, _ := os.ReadFile(bJSON)
			if !bytes.Equal(beforeText, afterText) || !bytes.Equal(beforeJSON, afterJSON) {
				t.Fatal("unrelated B sidecar changed")
			}
			result, err := ocr.LoadSidecar(root, imageA)
			if err != nil || len(result.Lines) != 1 || result.Lines[0].Text != "Correct A OCR" || engine.calls != 1 {
				t.Fatalf("A OCR=%+v err=%v calls=%d", result, err, engine.calls)
			}
		})
	}
}
