package shot

import (
	"bytes"
	"context"
	"encoding/json"
	"image"
	"image/png"
	"io"
	"os"
	"os/exec"
	"path/filepath"
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

func realPNG(t *testing.T) []byte {
	t.Helper()
	var b bytes.Buffer
	if err := png.Encode(&b, image.NewRGBA(image.Rect(0, 0, 2, 2))); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

// Exercises the real private command and engine adapter; only the model is fake.
func TestBackgroundRealWorkerOnce(t *testing.T) {
	dir := t.TempDir()
	// Build before HOME moves: a module cache under the temp HOME is read-only
	// and breaks TempDir cleanup.
	binary := filepath.Join(dir, "nn")
	build := exec.Command("go", "build", "-o", binary, "../../cmd/nn")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	home := filepath.Join(dir, "home")
	if err := os.MkdirAll(home, 0700); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"HOME", "XDG_CONFIG_HOME", "XDG_CACHE_HOME", "XDG_DATA_HOME", "XDG_STATE_HOME", "TMPDIR"} {
		t.Setenv(key, home)
	}
	root := filepath.Join(dir, "vault")
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("NN_ROOT", root)
	cfgPath := filepath.Join(dir, "config.toml")
	t.Setenv("NN_CONFIG", cfgPath)
	t.Setenv("NN_AI", "")
	engine := filepath.Join(dir, "fake-engine")
	marker := filepath.Join(dir, "sent-image")
	t.Setenv("SHOT_IMAGE_MARKER", marker)
	script := "#!/bin/sh\ncat \"$1\" > \"$SHOT_IMAGE_MARKER\"\ncat >/dev/null\nsleep 0.1\nprintf '%s' '{\"title\":\"Changed title\",\"tags\":[\"invented\"],\"body\":\"Finished analysis\"}'\n"
	if err := os.WriteFile(engine, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	configText := "[vault]\nroot = " + config.TOMLString(root) + "\n[notify]\nenabled=false\n[ai.profiles.local]\nengine=\"command\"\ncommand=[" + config.TOMLString(engine) + ", \"{image}\"]\n[ai.consent]\nlocal=\"ask\"\n"
	if err := os.WriteFile(cfgPath, []byte(configText), 0600); err != nil {
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
	note, err := v.Create(vault.NewNote{Title: "Original title", Body: "Recognized text", Tags: []string{"manual"}})
	if err != nil {
		t.Fatal(err)
	}
	original, err := os.ReadFile(v.Abs(note.Path))
	if err != nil {
		t.Fatal(err)
	}
	call, err := ai.Resolve(cfg, "shot", ai.Overrides{AI: true, Profile: "local"})
	if err != nil {
		t.Fatal(err)
	}
	decision, approval, err := ai.Approve(&cfg, call, ai.Sends("shot"), onceAsker{}, io.Discard)
	if err != nil || decision != ai.Allowed {
		t.Fatalf("approve: %v %v", decision, err)
	}
	jobDir := filepath.Join(dir, "shot-real")
	if err := os.Mkdir(jobDir, 0700); err != nil {
		t.Fatal(err)
	}
	imageData := realPNG(t)
	req := ai.Request{System: "Analyze", Text: "OCR text", Image: imageData}
	j := job{Version: 1, ID: "shot-real", Config: cfg.Path, Root: root, Inbox: v.Inbox, Note: note.Path, Image: "original.png", System: req.System, Text: req.Text}
	data, _ := json.Marshal(j)
	if err := os.WriteFile(filepath.Join(jobDir, "job.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(jobDir, j.Image), imageData, 0600); err != nil {
		t.Fatal(err)
	}
	// Config profile changes after approval must not substitute another engine.
	changed := strings.Replace(configText, config.TOMLString(engine), config.TOMLString(filepath.Join(dir, "must-not-run")), 1)
	if err := os.WriteFile(cfgPath, []byte(changed), 0600); err != nil {
		t.Fatal(err)
	}
	grantR, grantW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	readyR, readyW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer grantR.Close()
	defer grantW.Close()
	defer readyR.Close()
	defer readyW.Close()
	releaseR, releaseW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer releaseR.Close()
	defer releaseW.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, binary, "__ai", jobDir)
	command.ExtraFiles = []*os.File{grantR, readyW, releaseR}
	var stderr bytes.Buffer
	command.Stderr = &stderr
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	grantR.Close()
	readyW.Close()
	releaseR.Close()
	if err := ai.WriteTransfer(grantW, approval, ai.TransferBinding{JobID: j.ID, RequestSHA256: ai.RequestDigest(req)}); err != nil {
		t.Fatal(err)
	}
	grantW.Close()
	_ = readyR.SetReadDeadline(time.Now().Add(5 * time.Second))
	ack := make([]byte, 6)
	if _, err := io.ReadFull(readyR, ack); err != nil {
		_ = command.Process.Kill()
		_ = command.Wait()
		t.Fatalf("ACK: %v; %s", err, &stderr)
	}
	if string(ack) != "READY\n" {
		t.Fatalf("ack %q", ack)
	}
	if _, err := io.WriteString(releaseW, "G"); err != nil {
		t.Fatal(err)
	}
	releaseW.Close()
	// An edit made while the model runs must survive its append.
	if _, err := v.Append(note.Path, "User edit during analysis", nil); err != nil {
		t.Fatal(err)
	}
	if err := command.Wait(); err != nil {
		t.Fatalf("worker: %v; %s", err, &stderr)
	}
	after, err := os.ReadFile(v.Abs(note.Path))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(after, bytes.TrimRight(original, "\n")) || !bytes.Contains(after, []byte("User edit during analysis")) || !bytes.Contains(after, []byte("Finished analysis")) {
		t.Fatalf("note not preserved: %s", after)
	}
	if bytes.Contains(after, []byte("Changed title")) || !bytes.HasSuffix(after, []byte("#invented\n")) {
		t.Fatalf("worker changed manual title or lost new keyword: %s", after)
	}
	got, err := os.ReadFile(marker)
	if err != nil || !bytes.Equal(got, imageData) {
		t.Fatalf("original image not sent: %v", err)
	}
	if _, err := os.Stat(jobDir); !os.IsNotExist(err) {
		t.Fatalf("cache remains: %v", err)
	}
	current, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(current), "always") {
		t.Fatal("once was persisted")
	}
	// Exercise the complete Save -> startWorker -> real nn __ai path too.
	if err := os.WriteFile(cfgPath, []byte(configText), 0600); err != nil {
		t.Fatal(err)
	}
	previous := workerExecutable
	workerExecutable = func() (string, error) { return binary, nil }
	t.Cleanup(func() { workerExecutable = previous })
	plan, err := Prepare(&cfg, ai.Overrides{AI: true, Profile: "local"}, "background", onceAsker{}, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	parentCtx, stopParent := context.WithCancel(context.Background())
	saved, err := Save(parentCtx, &app.Env{Cfg: cfg, Vault: v}, plan, Input{Data: imageData, Ext: "png", Title: "Save integration", Tags: []string{"manual"}}, io.Discard)
	stopParent()
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		finished, err := v.Load(saved.Path)
		if err == nil && strings.Contains(finished.Body, "Finished analysis") {
			if finished.Title != "Save integration" {
				t.Fatalf("title changed: %q", finished.Title)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("Save background did not complete: note=%+v err=%v", finished, err)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
