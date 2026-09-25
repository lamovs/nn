package ai

import (
	"errors"
	"io"
	"testing"

	"github.com/lamovs/nn/internal/config"
)

func TestCheckApprovalFreshPolicyAndImmutableCall(t *testing.T) {
	cfg := config.Default()
	cfg.AI.Consent["claude"] = ConsentAlways
	call, err := Resolve(cfg, "shot", Overrides{AI: true})
	if err != nil {
		t.Fatal(err)
	}
	_, approval, err := Approve(&cfg, call, Sends("shot"), nil, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if err := CheckApproval(cfg, approval); err != nil {
		t.Fatal(err)
	}
	if err := CheckApproval(cfg, approval); err != nil {
		t.Fatalf("check consumed grant: %v", err)
	}
	cfg.AI.Profile = "codex"
	cfg.AI.Consent["codex"] = ConsentNever
	if err := CheckApproval(cfg, approval); err != nil {
		t.Fatalf("check re-resolved profile: %v", err)
	}
	cfg.AI.Consent["claude"] = ConsentNever
	var denied *ConsentError
	if err := CheckApproval(cfg, approval); !errors.As(err, &denied) || denied.Key != "claude" {
		t.Fatalf("consent not rechecked: %v", err)
	}
	cfg.AI.Consent["claude"] = ConsentAlways
	task := cfg.AI.Tasks["shot"]
	task.Run = "never"
	cfg.AI.Tasks["shot"] = task
	if err := CheckApproval(cfg, approval); err == nil {
		t.Fatal("run=never not checked")
	}
	if err := CheckApproval(cfg, Approval{}); !errors.Is(err, ErrNotApproved) {
		t.Fatalf("zero grant accepted: %v", err)
	}
}
