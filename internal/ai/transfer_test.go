package ai

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/lamovs/nn/internal/config"
)

func transferForTest(t *testing.T, approval Approval, binding TransferBinding, cfg config.Config) (Approval, error) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	done := make(chan error, 1)
	go func() { defer w.Close(); done <- WriteTransfer(w, approval, binding) }()
	imported, readErr := ReadTransfer(r, cfg, binding)
	if err := <-done; err != nil {
		return Approval{}, err
	}
	return imported, readErr
}

func TestTransferConsumesAndBinds(t *testing.T) {
	f := newFake(t)
	f.put("stdout", `{"title":"ok","tags":[],"body":"body"}`)
	req := Request{Text: "private", Image: pngImage}
	call := commandCall("fake-model", "--image={image}")
	original := issue(call)
	call.Profile.Command[0] = "changed"
	binding := TransferBinding{JobID: "one-job", RequestSHA256: RequestDigest(req)}
	imported, err := transferForTest(t, original, binding, config.Default())
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := original.allowed(); ok {
		t.Fatal("original still allows a run")
	}
	if _, err := Run(context.Background(), imported, req); err != nil {
		t.Fatal(err)
	}
	if _, err := Run(context.Background(), imported, req); !errors.Is(err, ErrNotApproved) {
		t.Fatalf("second run: %v", err)
	}
}

func TestTransferChangedRequestAndNever(t *testing.T) {
	req := Request{Text: "private"}
	binding := TransferBinding{JobID: "job", RequestSHA256: RequestDigest(req)}
	f := newFake(t)
	imported, err := transferForTest(t, issue(claudeCall("", "")), binding, config.Default())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Run(context.Background(), imported, Request{Text: "other"}); !errors.Is(err, ErrNotApproved) {
		t.Fatalf("changed request: %v", err)
	}
	if f.has("argv") {
		t.Fatal("changed request started engine")
	}
	for _, policy := range []string{"consent", "run"} {
		cfg := config.Default()
		if policy == "consent" {
			cfg.AI.Consent["claude"] = "never"
		} else {
			task := cfg.AI.Tasks["shot"]
			task.Run = "never"
			cfg.AI.Tasks["shot"] = task
		}
		if _, err := transferForTest(t, issue(claudeCall("", "")), binding, cfg); err == nil || !strings.Contains(err.Error(), "never") {
			t.Fatalf("%s: %v", policy, err)
		}
	}
}

func TestTransferRejectsInvalidTransportAndGrant(t *testing.T) {
	binding := TransferBinding{JobID: "job", RequestSHA256: RequestDigest(Request{Text: "T"})}
	file, err := os.CreateTemp(t.TempDir(), "grant")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if err := WriteTransfer(file, issue(claudeCall("", "")), binding); err == nil {
		t.Fatal("accepted regular file")
	}
	if _, err := transferForTest(t, Approval{}, binding, config.Default()); !errors.Is(err, ErrNotApproved) {
		t.Fatalf("zero grant: %v", err)
	}
	for _, data := range []string{`{}`, `{"Version":99}`, `{"Version":1,"Extra":true}`, strings.Repeat("x", maxTransferBytes+1)} {
		r, w, err := os.Pipe()
		if err != nil {
			t.Fatal(err)
		}
		done := make(chan struct{})
		go func() { defer close(done); defer w.Close(); _, _ = w.Write([]byte(data)) }()
		_, err = ReadTransfer(r, config.Default(), binding)
		r.Close()
		<-done
		if err == nil {
			t.Fatal("invalid envelope accepted")
		}
	}
}

func TestRequestDigestUnambiguous(t *testing.T) {
	a := RequestDigest(Request{System: "ab", Text: "c"})
	b := RequestDigest(Request{System: "a", Text: "bc"})
	if a == b {
		t.Fatal("field boundaries ignored")
	}
}

func TestTransferTitleOnlyAndTaskNever(t *testing.T) {
	f := newFake(t)
	f.put("stdout", `{"title":"title","tags":["new"],"body":""}`)
	req := Request{Text: "new text without image"}
	binding := TransferBinding{JobID: "title-job", RequestSHA256: RequestDigest(req)}
	call := commandCall("fake-model")
	call.Task = "title"
	approval, err := transferForTest(t, issue(call), binding, config.Default())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Run(context.Background(), approval, req); err != nil {
		t.Fatal(err)
	}
	if _, err := Run(context.Background(), approval, req); !errors.Is(err, ErrNotApproved) {
		t.Fatalf("title grant reused: %v", err)
	}
	cfg := config.Default()
	task := cfg.AI.Tasks["title"]
	task.Run = "never"
	cfg.AI.Tasks["title"] = task
	if _, err := transferForTest(t, issue(call), binding, cfg); err == nil {
		t.Fatal("title never ignored")
	}
	call.Task = "ask"
	if _, err := transferForTest(t, issue(call), binding, config.Default()); err == nil {
		t.Fatal("unsupported task imported")
	}
}

func TestTransferRejectsOtherJobAndRevokedGrant(t *testing.T) {
	cfg := config.Default()
	req := Request{Text: "T"}
	binding := TransferBinding{JobID: "first", RequestSHA256: RequestDigest(req)}
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	original := issue(claudeCall("", ""))
	done := make(chan error, 1)
	go func() { defer w.Close(); done <- WriteTransfer(w, original, binding) }()
	expected := binding
	expected.JobID = "second"
	if _, err := ReadTransfer(r, cfg, expected); err == nil {
		t.Fatal("other job accepted")
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if _, ok := original.allowed(); ok {
		t.Fatal("export did not consume")
	}

	revoked := issue(claudeCall("", ""))
	remember(&cfg, "claude", ConsentNever)
	if _, err := transferForTest(t, revoked, binding, config.Default()); !errors.Is(err, ErrNotApproved) {
		t.Fatalf("revoked exported: %v", err)
	}
}

func TestTransferChangedImageConsumesGrant(t *testing.T) {
	req := Request{Text: "T", Image: pngImage}
	binding := TransferBinding{JobID: "image", RequestSHA256: RequestDigest(req)}
	imported, err := transferForTest(t, issue(claudeCall("", "")), binding, config.Default())
	if err != nil {
		t.Fatal(err)
	}
	changed := req
	changed.Image = append(append([]byte{}, pngImage...), 0)
	if _, err := Run(context.Background(), imported, changed); !errors.Is(err, ErrNotApproved) {
		t.Fatalf("changed image: %v", err)
	}
	if _, err := Run(context.Background(), imported, req); !errors.Is(err, ErrNotApproved) {
		t.Fatalf("mismatch retry allowed: %v", err)
	}
}
