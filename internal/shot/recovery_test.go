package shot

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/png"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/lamovs/nn/internal/ai"
	"github.com/lamovs/nn/internal/app"
	"github.com/lamovs/nn/internal/config"
	"github.com/lamovs/nn/internal/ocr"
	"github.com/lamovs/nn/internal/vault"
)

type recoveryOCR struct{ calls int }

func (*recoveryOCR) Name() string                { return "fake-recovery-ocr" }
func (*recoveryOCR) Check(context.Context) error { return nil }
func (e *recoveryOCR) Recognize(_ context.Context, _ string, langs []string) (*ocr.Result, error) {
	e.calls++
	return &ocr.Result{Engine: e.Name(), Langs: langs, Width: 1, Height: 1, Lines: []ocr.Line{{Text: "OCR preserved error 42", Confidence: 1}}}, nil
}

func recoverySetup(t *testing.T, script string) (*app.Env, *Plan, []byte, string) {
	t.Helper()
	base := t.TempDir()
	for _, name := range []string{"home", "cache", "data", "state", "config", "tmp", "vault", "bin"} {
		if err := os.Mkdir(filepath.Join(base, name), 0700); err != nil {
			t.Fatal(err)
		}
	}
	for key, value := range map[string]string{"HOME": filepath.Join(base, "home"), "NN_CONFIG": filepath.Join(base, "config", "nn.toml"), "NN_ROOT": filepath.Join(base, "vault"), "XDG_CONFIG_HOME": filepath.Join(base, "config"), "XDG_CACHE_HOME": filepath.Join(base, "cache"), "XDG_DATA_HOME": filepath.Join(base, "data"), "XDG_STATE_HOME": filepath.Join(base, "state"), "TMPDIR": filepath.Join(base, "tmp"), "PATH": filepath.Join(base, "bin"), "NN_AI": ""} {
		t.Setenv(key, value)
	}
	marker := filepath.Join(base, "ran")
	t.Setenv("NN_RECOVERY_RAN", marker)
	engine := filepath.Join(base, "bin", "recovery-model")
	if err := os.WriteFile(engine, []byte("#!/bin/sh\n: > \"$NN_RECOVERY_RAN\"\n/bin/cat >/dev/null\n"+script+"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	cfg := config.Default()
	cfg.Vault.Root = filepath.Join(base, "vault")
	cfg.AI.Profile = "recovery"
	cfg.AI.Consent["recovery"] = "always"
	cfg.AI.Profiles["recovery"] = config.Profile{Engine: config.EngineCommand, Command: []string{engine, "{image}"}, Timeout: 2 * time.Second}
	v, err := vault.Open(cfg)
	if err != nil {
		t.Fatal(err)
	}
	env := &app.Env{Cfg: cfg, Vault: v, Now: func() time.Time { return time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC) }}
	plan, err := Prepare(&env.Cfg, ai.Overrides{AI: true}, "wait", nil, io.Discard)
	if err != nil || plan == nil {
		t.Fatalf("prepare: %v", err)
	}
	var img bytes.Buffer
	if err := png.Encode(&img, image.NewRGBA(image.Rect(0, 0, 1, 1))); err != nil {
		t.Fatal(err)
	}
	return env, plan, img.Bytes(), marker
}

func recoveryOriginal(t *testing.T, data []byte) string {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(os.Getenv("XDG_CACHE_HOME"), "nn", "ai", "shot-*", "original.png"))
	if err != nil || len(matches) != 1 {
		t.Fatalf("retained originals: %v (%v)", matches, err)
	}
	got, err := os.ReadFile(matches[0])
	if err != nil || !bytes.Equal(got, data) {
		t.Fatalf("original changed or missing: %v", err)
	}
	return matches[0]
}

func TestRecoveryModelFailureKeepsOCRAndOriginal(t *testing.T) {
	for _, tc := range []struct{ name, script, want, image string }{
		{"failure", "printf 'fake model failed' >&2; exit 3", "failed", "discard"},
		{"failure embed", "exit 3", "failed", "embed"},
		{"timeout", "while :; do :; done", "did not answer within", "discard"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env, plan, img, marker := recoverySetup(t, tc.script)
			task := env.Cfg.AI.Tasks["shot"]
			task.Image = tc.image
			env.Cfg.AI.Tasks["shot"] = task
			engine := &recoveryOCR{}
			var stderr bytes.Buffer
			note, err := Save(context.Background(), env, plan, Input{Data: img, Ext: "png", OCR: true, Engine: engine, Langs: []string{"eng"}, Title: "Manual capture", Tags: []string{"manual"}}, &stderr)
			if err != nil {
				t.Fatal(err)
			}
			original := recoveryOriginal(t, img)
			if !strings.Contains(note.Body, "OCR preserved error 42") || !strings.Contains(note.Body, original) {
				t.Fatalf("fallback lost data: %s", note.Body)
			}
			if note.Title != "Manual capture" || !slices.Contains(note.Tags, "manual") {
				t.Fatalf("fallback metadata: %+v", note)
			}
			if engine.calls != 1 {
				t.Fatalf("OCR calls=%d", engine.calls)
			}
			if tc.image == "embed" {
				if len(note.Embeds) != 1 {
					t.Fatal("fallback lost embedded image")
				}
				stored, err := os.ReadFile(env.Vault.Abs(note.Embeds[0]))
				if err != nil || !bytes.Equal(stored, img) {
					t.Fatalf("fallback changed embedded original: %v", err)
				}
				if _, err := ocr.LoadSidecar(env.Vault.Root, note.Embeds[0]); err != nil {
					t.Fatalf("fallback lost OCR sidecar: %v", err)
				}
			}
			if _, err := os.Stat(marker); err != nil {
				t.Fatalf("fake model did not run: %s", stderr.String())
			}
			if !strings.Contains(stderr.String(), tc.want) {
				t.Fatalf("missing failure reason: %s", stderr.String())
			}
		})
	}
}

func TestRecoveryOversizeKeepsOCRWithoutModel(t *testing.T) {
	env, plan, img, marker := recoverySetup(t, "exit 91")
	img = append(img, make([]byte, ai.MaxImageBytes+1-len(img))...)
	engine := &recoveryOCR{}
	var stderr bytes.Buffer
	note, err := Save(context.Background(), env, plan, Input{Data: img, Ext: "png", OCR: true, Engine: engine, Langs: []string{"eng"}}, &stderr)
	if err != nil {
		t.Fatal(err)
	}
	original := recoveryOriginal(t, img)
	if !strings.Contains(note.Body, "OCR preserved error 42") || !strings.Contains(note.Body, original) {
		t.Fatal("oversize fallback lost OCR or recovery path")
	}
	if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("oversize input started model: %v", err)
	}
	if !strings.Contains(stderr.String(), "image exceeds") {
		t.Fatalf("missing size reason: %s", stderr.String())
	}
}

func TestRecoveryWaitMetadataEmbedAndSidecar(t *testing.T) {
	for _, manual := range []bool{false, true} {
		t.Run(map[bool]string{false: "model title", true: "manual title"}[manual], func(t *testing.T) {
			env, plan, img, _ := recoverySetup(t, `printf '%s' '{"title":"Model title","tags":["known","invented","known"],"body":"Model analysis"}'`)
			task := env.Cfg.AI.Tasks["shot"]
			task.Image = "embed"
			env.Cfg.AI.Tasks["shot"] = task
			if _, err := env.Vault.Create(vault.NewNote{Title: "Dictionary source", Tags: []string{"known"}, Body: "Existing note", Now: env.Now()}); err != nil {
				t.Fatal(err)
			}
			title := ""
			if manual {
				title = "Manual title"
			}
			engine := &recoveryOCR{}
			note, err := Save(context.Background(), env, plan, Input{Data: img, Ext: "png", Title: title, Tags: []string{"manual"}, OCR: true, Engine: engine, Langs: []string{"eng"}}, io.Discard)
			if err != nil {
				t.Fatal(err)
			}
			wantTitle := "Model title"
			if manual {
				wantTitle = title
			}
			if note.Title != wantTitle {
				t.Fatalf("title=%q", note.Title)
			}
			if !slices.Contains(note.Tags, "manual") || !slices.Contains(note.Tags, "known") || !slices.Contains(note.Tags, "invented") || !strings.HasSuffix(note.Body, "#known #invented\n") {
				t.Fatalf("tags=%v", note.Tags)
			}
			if !strings.Contains(note.Body, "Model analysis") || len(note.Embeds) != 1 {
				t.Fatalf("answer/image lost: %+v", note)
			}
			stored, err := os.ReadFile(env.Vault.Abs(note.Embeds[0]))
			if err != nil || !bytes.Equal(stored, img) {
				t.Fatalf("embedded original changed: %v", err)
			}
			sidecar, err := ocr.LoadSidecar(env.Vault.Root, note.Embeds[0])
			if err != nil || len(sidecar.Lines) != 1 || sidecar.Lines[0].Text != "OCR preserved error 42" {
				t.Fatalf("OCR cache lost: %+v, %v", sidecar, err)
			}
			entries, err := os.ReadDir(filepath.Join(os.Getenv("XDG_CACHE_HOME"), "nn", "ai"))
			if err != nil || len(entries) != 0 {
				t.Fatalf("successful job cache left behind: %v %v", entries, err)
			}
		})
	}
}
