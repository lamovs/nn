package noteai

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"image"
	"image/png"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/lamovs/nn/internal/ai"
	"github.com/lamovs/nn/internal/app"
	"github.com/lamovs/nn/internal/config"
	"github.com/lamovs/nn/internal/vault"
)

type onceAsker struct{}

func (onceAsker) Interactive() bool          { return true }
func (onceAsker) Ask(string) (string, error) { return "once", nil }

func metadataFixture(t *testing.T, imageCapable bool, answer string) (*app.Env, *Plan, string) {
	t.Helper()
	base := t.TempDir()
	for _, dir := range []string{"home", "config", "cache", "data", "state", "tmp", "vault"} {
		if err := os.Mkdir(filepath.Join(base, dir), 0700); err != nil {
			t.Fatal(err)
		}
	}
	for key, value := range map[string]string{"HOME": filepath.Join(base, "home"), "NN_CONFIG": filepath.Join(base, "config", "nn.toml"), "NN_ROOT": filepath.Join(base, "vault"), "NN_AI": "", "XDG_CONFIG_HOME": filepath.Join(base, "config"), "XDG_CACHE_HOME": filepath.Join(base, "cache"), "XDG_DATA_HOME": filepath.Join(base, "data"), "XDG_STATE_HOME": filepath.Join(base, "state"), "TMPDIR": filepath.Join(base, "tmp"), "NN_METADATA_BASE": base} {
		t.Setenv(key, value)
	}
	engine := filepath.Join(base, "fake-model")
	script := "#!/bin/sh\nprintf x >> \"$NN_METADATA_BASE/calls\"\n/bin/cat > \"$NN_METADATA_BASE/request\"\n"
	if imageCapable {
		script += "/bin/cat \"$1\" > \"$NN_METADATA_BASE/image\"\n"
	}
	script += "printf '%s' '" + strings.ReplaceAll(answer, "'", "'\\''") + "'\n"
	if err := os.WriteFile(engine, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	command := "[" + config.TOMLString(engine)
	if imageCapable {
		command += ",\"{image}\""
	}
	command += "]"
	cfgText := "[vault]\nroot=" + config.TOMLString(filepath.Join(base, "vault")) + "\n[notify]\nenabled=false\n[ai]\nprofile=\"metadata\"\n[ai.profiles.metadata]\nengine=\"command\"\ncommand=" + command + "\ntimeout=\"15s\"\n[ai.consent]\nmetadata=\"ask\"\n"
	if err := os.WriteFile(os.Getenv("NN_CONFIG"), []byte(cfgText), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, _, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	v, err := vault.Open(cfg)
	if err != nil {
		t.Fatal(err)
	}
	env := &app.Env{Cfg: cfg, Vault: v}
	plan, err := Prepare(&env.Cfg, "title", ai.Overrides{AI: true}, "wait", onceAsker{}, io.Discard)
	if err != nil || plan == nil {
		t.Fatalf("prepare: %v", err)
	}
	return env, plan, base
}

func metadataImage(t *testing.T) []byte {
	t.Helper()
	var b bytes.Buffer
	if err := png.Encode(&b, image.NewRGBA(image.Rect(0, 0, 1, 1))); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func TestWaitPreservesInputAndUsesTrailingKeywords(t *testing.T) {
	for _, manual := range []string{"", "Manual title", "body heading"} {
		t.Run(manual, func(t *testing.T) {
			env, plan, base := metadataFixture(t, true, `{"title":"Generated title","tags":["existing","New Word","new-word","123","bad!"],"body":"MODEL BODY MUST NOT LAND"}`)
			if _, err := env.Vault.Create(vault.NewNote{Title: "Private old title", Body: "Private old body", Tags: []string{"Existing"}}); err != nil {
				t.Fatal(err)
			}
			body := "Original source #manual"
			title := manual
			if manual == "body heading" {
				body = "# Source heading\n\n" + body
				title = ""
			}
			img := metadataImage(t)
			var warnings bytes.Buffer
			note, err := Save(context.Background(), env, plan, Input{Note: vault.NewNote{Title: title, Body: body, Tags: []string{"manual"}, Images: []vault.Image{{Data: img, Ext: "png"}}}, Text: body, Image: img, Ext: "png", AllowTitle: true}, &warnings)
			if err != nil {
				t.Fatal(err)
			}
			wantTitle := "Generated title"
			if manual == "Manual title" {
				wantTitle = manual
			}
			if manual == "body heading" {
				wantTitle = "Source heading"
			}
			if note.Title != wantTitle || !strings.Contains(note.Body, body) || strings.Contains(note.Body, "MODEL BODY") || !strings.HasSuffix(note.Body, "]]\n\n#Existing #new-word\n") {
				t.Fatalf("note=%+v; warnings=%s", note, warnings.String())
			}
			if got, _ := os.ReadFile(filepath.Join(base, "image")); !bytes.Equal(got, img) {
				t.Fatal("original image not supplied")
			}
			sent, _ := os.ReadFile(filepath.Join(base, "request"))
			if !bytes.Contains(sent, []byte("Existing")) || bytes.Contains(sent, []byte("Private old")) {
				t.Fatalf("request leaked old note or omitted vocabulary: %s", sent)
			}
			calls, _ := os.ReadFile(filepath.Join(base, "calls"))
			if string(calls) != "x" {
				t.Fatalf("calls=%q", calls)
			}
		})
	}
}

func TestAppendSendsOnlyNewFragment(t *testing.T) {
	env, plan, base := metadataFixture(t, false, `{"title":"Wrong target title","tags":["existing","new"],"body":"Wrong replacement"}`)
	old, err := env.Vault.Create(vault.NewNote{Title: "Old private title", Body: "Old private body #existing"})
	if err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(env.Vault.Abs(old.Path))
	note, err := Save(context.Background(), env, plan, Input{Note: vault.NewNote{Body: "new supplied fragment"}, To: old.Path, Text: "new supplied fragment", AllowTitle: true}, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(env.Vault.Abs(old.Path))
	sent, _ := os.ReadFile(filepath.Join(base, "request"))
	if !bytes.HasPrefix(after, before) || note.Title != old.Title || !strings.HasSuffix(note.Body, "#new\n") || strings.Contains(note.Body, "Wrong") {
		t.Fatalf("append=%s", after)
	}
	if bytes.Contains(sent, []byte("Old private")) || !bytes.Contains(sent, []byte("new supplied fragment")) {
		t.Fatalf("request=%s", sent)
	}
}

func TestOptionalImageTextFallback(t *testing.T) {
	for _, text := range []string{"", "Recognized OCR text"} {
		t.Run(text, func(t *testing.T) {
			env, plan, base := metadataFixture(t, false, `{"title":"OCR title","tags":["ocr"],"body":"ignored"}`)
			img := metadataImage(t)
			var stderr bytes.Buffer
			note, err := Save(context.Background(), env, plan, Input{Note: vault.NewNote{Body: text, Images: []vault.Image{{Data: img, Ext: "png"}}}, Text: text, Image: img, Ext: "png", AllowTitle: true}, &stderr)
			if err != nil || len(note.Embeds) != 1 {
				t.Fatalf("note=%+v err=%v", note, err)
			}
			_, err = os.Stat(filepath.Join(base, "calls"))
			if text == "" && !errors.Is(err, os.ErrNotExist) {
				t.Fatal("image-only unsupported profile invoked")
			}
			if text != "" && (err != nil || note.Title != "OCR title") {
				t.Fatalf("OCR fallback not used: %v %+v", err, note)
			}
			if !strings.Contains(stderr.String(), "takes no image") {
				t.Fatal("missing capability disclosure")
			}
		})
	}
}

func TestFailureRetainsTextAndOriginalImage(t *testing.T) {
	env, plan, base := metadataFixture(t, true, "")
	img := metadataImage(t)
	var stderr bytes.Buffer
	note, err := Save(context.Background(), env, plan, Input{Note: vault.NewNote{Body: "Original full text", Images: []vault.Image{{Data: img, Ext: "png"}}}, Text: "Original full text", Image: img, Ext: "png", AllowTitle: true}, &stderr)
	if err != nil || !strings.Contains(note.Body, "Original full text") || !strings.Contains(note.Body, "Original retained") {
		t.Fatalf("note=%+v err=%v", note, err)
	}
	dirs, _ := filepath.Glob(filepath.Join(base, "cache", "nn", "ai", "title-*"))
	if len(dirs) != 1 {
		t.Fatalf("recovery dirs=%v", dirs)
	}
	text, _ := os.ReadFile(filepath.Join(dirs[0], "original.txt"))
	data, _ := os.ReadFile(filepath.Join(dirs[0], "original.png"))
	if string(text) != "Original full text" || !bytes.Equal(data, img) {
		t.Fatal("original data lost")
	}
	if _, err := os.Stat(filepath.Join(dirs[0], "status.json")); err != nil {
		t.Fatal(err)
	}
}

func TestBackgroundTitleRealWorker(t *testing.T) {
	binary := filepath.Join(t.TempDir(), "nn")
	build := exec.Command("go", "build", "-o", binary, "../../cmd/nn")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %v %s", err, out)
	}
	env, plan, base := metadataFixture(t, false, `{"title":"Background title","tags":["background"],"body":"ignored model rewrite"}`)
	plan.Mode = "background"
	hook := "printf '%s\\n' \"$NN_ACTION\" >> \"$NN_METADATA_BASE/hooks\""
	env.Cfg.Hooks.PostSave = hook
	configBytes, _ := os.ReadFile(env.Cfg.Path)
	if err := os.WriteFile(env.Cfg.Path, append(configBytes, []byte("\n[hooks]\npost_save="+config.TOMLString(hook)+"\n")...), 0600); err != nil {
		t.Fatal(err)
	}
	previous := workerExecutable
	workerExecutable = func() (string, error) { return binary, nil }
	t.Cleanup(func() { workerExecutable = previous })
	ctx, cancel := context.WithCancel(context.Background())
	callbackCalls := 0
	img := vault.Image{Data: metadataImage(t), Ext: "png"}
	note, err := Save(ctx, env, plan, Input{Note: vault.NewNote{Body: "New text", FilenameHint: "stable-file", Images: []vault.Image{img, img}}, Text: "New text", AllowTitle: true, AfterSave: func(note *vault.Note, paths []string) {
		callbackCalls++
		if len(paths) != 2 || paths[0] != paths[1] || len(note.Embeds) != 1 || paths[0] != note.Embeds[0] {
			t.Errorf("background asset receipt=%v note=%+v", paths, note)
		}
		if _, err := os.Stat(filepath.Join(base, "calls")); !errors.Is(err, os.ErrNotExist) {
			t.Error("model ran before initial save hook")
		}
		env.PostSave(ctx, note.Path, "create")
	}}, io.Discard)
	cancel()
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		n, err := env.Vault.Load(note.Path)
		hooks, _ := os.ReadFile(filepath.Join(base, "hooks"))
		if err == nil && strings.Contains(n.Body, "#background") && strings.Contains(string(hooks), "append\n") {
			if callbackCalls != 1 || string(hooks) != "create\nappend\n" {
				t.Fatalf("callback calls=%d hooks=%q", callbackCalls, hooks)
			}
			if n.Title != "Background title" || n.Path != "nn/stable-file.md" || strings.Contains(n.Body, "ignored") {
				t.Fatalf("background=%+v", n)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("background did not complete: %+v %v", n, err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	calls, _ := os.ReadFile(filepath.Join(base, "calls"))
	if string(calls) != "x" {
		t.Fatalf("calls=%q", calls)
	}
	data, _ := os.ReadFile(env.Cfg.Path)
	if strings.Contains(string(data), "always") {
		t.Fatal("once consent persisted")
	}
}

func TestMetadataCacheFailureStillSavesAndCallsHook(t *testing.T) {
	env, plan, base := metadataFixture(t, false, `{"title":"unused","tags":[],"body":""}`)
	blocked := filepath.Join(base, "blocked-cache")
	if err := os.WriteFile(blocked, []byte("ordinary file"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_CACHE_HOME", blocked)
	var stderr bytes.Buffer
	calls := 0
	note, err := Save(context.Background(), env, plan, Input{Note: vault.NewNote{Body: "Must survive cache failure"}, Text: "Must survive cache failure", AllowTitle: true, AfterSave: func(*vault.Note, []string) { calls++ }}, &stderr)
	if err != nil || note == nil || !strings.Contains(note.Body, "Must survive cache failure") || calls != 1 {
		t.Fatalf("note=%+v err=%v hooks=%d", note, err, calls)
	}
	if _, err := os.Stat(filepath.Join(base, "calls")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("cache failure started model")
	}
	if !strings.Contains(stderr.String(), "saving without AI metadata") {
		t.Fatalf("warning=%s", &stderr)
	}
}

func TestTaskBindingAndMetadataHelpers(t *testing.T) {
	env, plan, _ := metadataFixture(t, false, `{"title":"title","tags":[],"body":"body"}`)
	dir, err := NewCache("title")
	if err != nil {
		t.Fatal(err)
	}
	if err := Start(context.Background(), dir, Job{Task: "shot"}, plan.Approval, ai.Request{Text: "new"}); err == nil {
		t.Fatal("mismatched task accepted")
	}
	if _, err := os.Stat(filepath.Join(dir, "job.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("mismatched job persisted")
	}
	if got := Keywords([]string{" Existing ", "New Word", "new-word", "123", "C++"}, []string{"Existing"}); !reflect.DeepEqual(got, []string{"Existing", "new-word"}) {
		t.Fatalf("keywords=%v", got)
	}
	if Title(" # Title\nwith\tspace # ") != "Title with space" || Title("bad\x00title") != "" {
		t.Fatal("title normalization failed")
	}
	if plan, err := Prepare(&env.Cfg, "title", ai.Overrides{NoAI: true}, "", nil, io.Discard); err != nil || plan != nil {
		t.Fatalf("no-ai=%+v %v", plan, err)
	}
	if plan, err := Prepare(&env.Cfg, "title", ai.Overrides{}, "", nil, io.Discard); err != nil || plan != nil {
		t.Fatalf("flag policy=%+v %v", plan, err)
	}
	call, err := plan.Approval.Call()
	if err != nil {
		t.Fatal(err)
	}
	call.Profile.Command[0] = "mutated"
	original, err := plan.Approval.Call()
	if err != nil || original.Profile.Command[0] == "mutated" {
		t.Fatal("approval call escaped mutable state")
	}
	if ai.TakesImage("title") {
		t.Fatal("title incorrectly requires image support")
	}
	var fields map[string]any
	data, _ := json.Marshal(Job{Task: "title"})
	if err := json.Unmarshal(data, &fields); err != nil || fields["Task"] != "title" {
		t.Fatal("task missing from envelope")
	}
}
