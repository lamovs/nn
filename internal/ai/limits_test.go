package ai

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/png"
	"strings"
	"testing"

	"github.com/lamovs/nn/internal/config"
)

func TestRequestLimitsBeforeSpawn(t *testing.T) {
	f := newFake(t)
	for _, req := range []Request{
		{Text: "T", System: strings.Repeat("s", MaxSystemBytes+1)},
		{Text: strings.Repeat("t", MaxTextBytes+1)},
		{Image: make([]byte, MaxImageBytes+1)},
		{Image: pngImage[:len(pngImage)-5]},
	} {
		if _, err := Run(context.Background(), issue(claudeCall("", "")), req); err == nil {
			t.Fatal("invalid request accepted")
		}
	}
	var large bytes.Buffer
	if err := png.Encode(&large, image.NewRGBA(image.Rect(0, 0, MaxImageDimension+1, 1))); err != nil {
		t.Fatal(err)
	}
	if err := ValidateShotImage(large.Bytes()); err == nil {
		t.Fatal("oversize dimension accepted")
	}
	if err := ValidateRequest(Request{System: strings.Repeat("s", MaxSystemBytes), Text: strings.Repeat("t", MaxTextBytes), Image: pngImage}); err != nil {
		t.Fatal(err)
	}
	if f.has("argv") {
		t.Fatal("invalid request started engine")
	}
}

func TestCommandExpansionBounded(t *testing.T) {
	f := newFake(t)
	for _, args := range [][]string{{"fake-model", "{prompt}"}, {"fake-model", strings.Repeat("{prompt}", 100)}} {
		if _, err := Run(context.Background(), issue(commandCall(args...)), Request{Text: strings.Repeat("x", MaxTextBytes)}); err == nil {
			t.Fatal("huge arguments accepted")
		}
	}
	if f.has("argv") {
		t.Fatal("oversize arguments started engine")
	}
}

func TestResolveExplicitNever(t *testing.T) {
	cfg := config.Default()
	task := cfg.AI.Tasks["shot"]
	task.Run = "never"
	cfg.AI.Tasks["shot"] = task
	if _, err := Resolve(cfg, "shot", Overrides{AI: true}); err == nil || !strings.Contains(err.Error(), "ai.tasks.shot.run") {
		t.Fatalf("explicit never: %v", err)
	}
	if _, err := Resolve(cfg, "shot", Overrides{}); err != nil {
		t.Fatalf("doctor resolve: %v", err)
	}
	if _, err := Resolve(cfg, "shot", Overrides{AI: true, NoAI: true}); !errors.Is(err, ErrOff) {
		t.Fatalf("no-ai: %v", err)
	}
}

func TestClaudeEchoBudget(t *testing.T) {
	f := newFake(t)
	// Padding remains part of the original file sent to the adapter.
	img := append(bytes.Clone(pngImage), make([]byte, MaxImageBytes-len(pngImage))...)
	line, err := claudeMessage("OCR", img, "image/png")
	if err != nil {
		t.Fatal(err)
	}
	f.put("stdout", string(line)+`{"type":"result","subtype":"success","structured_output":{"title":"T","tags":[],"body":"B"}}`)
	if _, err := Run(context.Background(), issue(claudeCall("", "")), Request{Text: "OCR", Image: img}); err != nil {
		t.Fatalf("echo counted as output: %v", err)
	}
	f.put("stdout", `{"type":"result","structured_output":{"title":"T","tags":[],"body":"`+strings.Repeat("x", maxOutput)+`"}}`)
	if _, err := Run(context.Background(), issue(claudeCall("", "")), Request{Text: "OCR", Image: img}); !errors.Is(err, ErrOutputLimit) {
		t.Fatalf("answer escaped output limit: %v", err)
	}
}

func TestClaudeOnlyDiscountsMatchingInputOnce(t *testing.T) {
	input := []byte("{\"type\":\"user\",\"message\":{\"role\":\"user\",\"content\":[]}}\n")
	reordered := []byte("{\"uuid\":\"id\",\"message\":{\"content\":[],\"role\":\"user\"},\"type\":\"user\"}\n")
	if got := stripClaudeInputEcho(append(append([]byte{}, reordered...), reordered...), input); !bytes.Equal(got, reordered) {
		t.Fatal("did not discount precisely one matching echo")
	}
	different := []byte("{\"type\":\"user\",\"message\":{\"role\":\"user\",\"content\":[\"different\"]}}\n")
	if got := stripClaudeInputEcho(different, input); !bytes.Equal(got, different) {
		t.Fatal("discounted a different user message")
	}
}
