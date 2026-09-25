package platform

import (
	"context"
	"errors"
	"slices"
	"testing"
)

func TestClipboardTextBackendsPreserveBytes(t *testing.T) {
	for _, tc := range []struct {
		os   string
		env  map[string]string
		bins []string
		want string
	}{
		{"darwin", map[string]string{"LC_ALL": "C"}, nil, "pbpaste -Prefer txt"},
		{"linux", map[string]string{"WAYLAND_DISPLAY": "w"}, []string{"wl-paste"}, "wl-paste --no-newline --type text"},
		{"linux", map[string]string{"DISPLAY": ":0"}, []string{"xclip", "xsel"}, "xclip -selection clipboard -out"},
		{"linux", map[string]string{"DISPLAY": ":0"}, []string{"xsel"}, "xsel --clipboard --output"},
	} {
		t.Run(tc.want, func(t *testing.T) {
			f := newFakeSystem(t, tc.os, tc.env, tc.bins...)
			f.handle = func(call) (string, string, error) { return "  Текст\nsecond line\n\n", "", nil }
			text, err := ClipboardText(context.Background())
			if err != nil || string(text) != "  Текст\nsecond line\n\n" {
				t.Fatalf("text=%q err=%v", text, err)
			}
			if !slices.Equal(f.commands(), []string{tc.want}) {
				t.Fatalf("commands=%v", f.commands())
			}
			if tc.os == "darwin" && !slices.Contains(f.calls[0].env, "LC_ALL=en_US.UTF-8") {
				t.Fatal("pbpaste locale not UTF-8")
			}
		})
	}
}

func TestClipboardImageFallbackClassification(t *testing.T) {
	t.Run("xsel only", func(t *testing.T) {
		newFakeSystem(t, "linux", map[string]string{"DISPLAY": ":0"}, "xsel")
		if _, _, err := ClipboardImage(context.Background()); !errors.Is(err, ErrImageUnavailable) {
			t.Fatalf("missing image backend=%v", err)
		}
	})
	t.Run("automatic vs explicit helper", func(t *testing.T) {
		f := newFakeSystem(t, "darwin", map[string]string{})
		previous := findHelper
		findHelper = func() (string, error) { return "", errors.New("helper absent") }
		t.Cleanup(func() { findHelper = previous })
		if _, _, err := ClipboardImage(context.Background()); !errors.Is(err, ErrImageUnavailable) {
			t.Fatalf("automatic helper=%v", err)
		}
		f.env["NN_VISION_HELPER"] = "/explicit/missing"
		if _, _, err := ClipboardImage(context.Background()); err == nil || errors.Is(err, ErrImageUnavailable) {
			t.Fatalf("explicit helper=%v", err)
		}
	})
	for _, msg := range []string{"Cannot open display", "Failed to connect to a Wayland server", ""} {
		t.Run(msg, func(t *testing.T) {
			f := newFakeSystem(t, "linux", map[string]string{"DISPLAY": ":0"}, "xclip")
			exit := exitError(t, 1)
			f.handle = func(call) (string, string, error) { return "", msg, exit }
			if _, _, err := ClipboardImage(context.Background()); err == nil || errors.Is(err, ErrNoImage) || errors.Is(err, ErrImageUnavailable) {
				t.Fatalf("real failure classified as fallback: %v", err)
			}
		})
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	newFakeSystem(t, "linux", map[string]string{"WAYLAND_DISPLAY": "w"}, "wl-paste")
	if _, _, err := ClipboardImage(ctx); !errors.Is(err, ErrCancelled) {
		t.Fatalf("cancel=%v", err)
	}
}
