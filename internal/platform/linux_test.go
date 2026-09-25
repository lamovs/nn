package platform

import (
	"context"
	"errors"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/lamovs/nn/internal/config"
)

const pngData = "\x89PNG\r\n\x1a\nfake"

func TestDetectSession(t *testing.T) {
	tests := []struct {
		name string
		env  map[string]string
		want session
	}{
		{"hyprland", map[string]string{"XDG_SESSION_TYPE": "wayland", "HYPRLAND_INSTANCE_SIGNATURE": "abc"}, session{wayland: true, desktop: desktopWlroots}},
		{"sway socket", map[string]string{"WAYLAND_DISPLAY": "wayland-1", "SWAYSOCK": "/run/sway.sock"}, session{wayland: true, desktop: desktopWlroots}},
		{"river", map[string]string{"XDG_SESSION_TYPE": "wayland", "XDG_CURRENT_DESKTOP": "river"}, session{wayland: true, desktop: desktopWlroots}},
		{"kde wayland", map[string]string{"XDG_SESSION_TYPE": "wayland", "XDG_CURRENT_DESKTOP": "KDE", "DISPLAY": ":0"}, session{wayland: true, desktop: desktopKDE}},
		{"ubuntu gnome x11", map[string]string{"XDG_SESSION_TYPE": "x11", "XDG_CURRENT_DESKTOP": "ubuntu:GNOME", "DISPLAY": ":0"}, session{x11: true, desktop: desktopGNOME}},
		{"bare x11", map[string]string{"DISPLAY": ":0", "XDG_CURRENT_DESKTOP": "i3"}, session{x11: true}},
		{"tty", map[string]string{"XDG_SESSION_TYPE": "tty"}, session{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			newFakeSystem(t, "linux", tt.env)
			if got := detectSession(); got != tt.want {
				t.Errorf("detectSession = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestLinuxScreenshotWlroots(t *testing.T) {
	f := newFakeSystem(t, "linux", map[string]string{"XDG_SESSION_TYPE": "wayland", "HYPRLAND_INSTANCE_SIGNATURE": "x"}, "grim", "slurp")
	var shotPath string
	f.handle = func(c call) (string, string, error) {
		switch c.name {
		case "slurp":
			return "10,20 300x200\n", "", nil
		case "grim":
			shotPath = c.args[len(c.args)-1]
			writeArg(t, c, pngData)
		}
		return "", "", nil
	}
	data, ext, err := Screenshot(context.Background(), config.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != pngData || ext != "png" {
		t.Errorf("got %q %q", data, ext)
	}
	if got := f.calls[1].args; !slices.Equal(got[:2], []string{"-g", "10,20 300x200"}) {
		t.Errorf("grim args = %q", got)
	}
	if _, err := os.Stat(shotPath); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("temp screenshot left behind: %v", err)
	}
}

func TestLinuxScreenshotSlurpCancelled(t *testing.T) {
	f := newFakeSystem(t, "linux", map[string]string{"SWAYSOCK": "/s", "WAYLAND_DISPLAY": "w"}, "grim", "slurp")
	f.handle = func(c call) (string, string, error) {
		return "", "selection cancelled\n", exitError(t, 1)
	}
	if _, _, err := Screenshot(context.Background(), config.Config{}); !errors.Is(err, ErrCancelled) {
		t.Errorf("err = %v", err)
	}
	if len(f.calls) != 1 {
		t.Errorf("grim ran after a cancelled selection: %q", f.commands())
	}
}

func TestLinuxScreenshotDesktops(t *testing.T) {
	tests := []struct {
		name string
		env  map[string]string
		bins []string
		want string
	}{
		{"kde", map[string]string{"XDG_SESSION_TYPE": "wayland", "XDG_CURRENT_DESKTOP": "KDE"}, []string{"spectacle"}, "spectacle -r -b -n -o"},
		{"gnome", map[string]string{"XDG_SESSION_TYPE": "wayland", "XDG_CURRENT_DESKTOP": "GNOME"}, []string{"gnome-screenshot"}, "gnome-screenshot -a -f"},
		{"x11 maim", map[string]string{"DISPLAY": ":0"}, []string{"maim", "import"}, "maim -s"},
		{"x11 import fallback", map[string]string{"DISPLAY": ":0"}, []string{"import"}, "import"},
		{"kde x11 without spectacle", map[string]string{"XDG_SESSION_TYPE": "x11", "XDG_CURRENT_DESKTOP": "KDE", "DISPLAY": ":0"}, []string{"maim"}, "maim -s"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFakeSystem(t, "linux", tt.env, tt.bins...)
			f.handle = func(c call) (string, string, error) {
				writeArg(t, c, pngData)
				return "", "", nil
			}
			if _, _, err := Screenshot(context.Background(), config.Config{}); err != nil {
				t.Fatal(err)
			}
			got := f.calls[0].String()
			path := f.calls[0].args[len(f.calls[0].args)-1]
			if got != tt.want+" "+path || !strings.HasSuffix(path, ".png") {
				t.Errorf("ran %q, want %q <file>.png", got, tt.want)
			}
		})
	}
}

func TestLinuxScreenshotCancelAndErrors(t *testing.T) {
	env := map[string]string{"DISPLAY": ":0"}

	f := newFakeSystem(t, "linux", env, "maim")
	f.handle = func(c call) (string, string, error) {
		return "", "Selection was cancelled by keystroke or right-click.\n", exitError(t, 1)
	}
	if _, _, err := Screenshot(context.Background(), config.Config{}); !errors.Is(err, ErrCancelled) {
		t.Errorf("maim cancel: err = %v", err)
	}

	f.handle = func(c call) (string, string, error) {
		return "", "maim: Failed to open X display\n", exitError(t, 1)
	}
	_, _, err := Screenshot(context.Background(), config.Config{})
	if err == nil || errors.Is(err, ErrCancelled) || !strings.Contains(err.Error(), "Failed to open X display") {
		t.Errorf("maim failure: err = %v", err)
	}

	f.handle = nil // exit 0 without writing the file
	if _, _, err := Screenshot(context.Background(), config.Config{}); !errors.Is(err, ErrCancelled) {
		t.Errorf("no file: err = %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := Screenshot(ctx, config.Config{}); !errors.Is(err, ErrCancelled) || !errors.Is(err, context.Canceled) {
		t.Errorf("cancelled ctx: err = %v", err)
	}
}

func TestLinuxScreenshotUnsupported(t *testing.T) {
	newFakeSystem(t, "linux", map[string]string{})
	_, _, err := Screenshot(context.Background(), config.Config{})
	if !errors.Is(err, ErrUnsupported) || !strings.Contains(err.Error(), "nn add --clip") {
		t.Errorf("no session: err = %v", err)
	}

	newFakeSystem(t, "linux", map[string]string{"HYPRLAND_INSTANCE_SIGNATURE": "x", "WAYLAND_DISPLAY": "w"}, "slurp")
	_, _, err = Screenshot(context.Background(), config.Config{})
	if !errors.Is(err, ErrUnsupported) || !strings.Contains(err.Error(), "grim + slurp is not installed") {
		t.Errorf("missing grim: err = %v", err)
	}
}

func TestLinuxScreenshotToolPinned(t *testing.T) {
	f := newFakeSystem(t, "linux", map[string]string{"XDG_SESSION_TYPE": "wayland", "XDG_CURRENT_DESKTOP": "GNOME"}, "maim", "gnome-screenshot")
	f.handle = func(c call) (string, string, error) {
		writeArg(t, c, pngData)
		return "", "", nil
	}
	if _, _, err := Screenshot(context.Background(), config.Config{Shot: config.Shot{Tool: "maim"}}); err != nil {
		t.Fatal(err)
	}
	if got := f.calls[0].name; got != "maim" {
		t.Errorf("ran %q, want maim", got)
	}
}

func TestLinuxScreenshotToolGrimIsGrimPlusSlurp(t *testing.T) {
	f := newFakeSystem(t, "linux", map[string]string{"DISPLAY": ":0"}, "grim", "slurp")
	f.handle = func(c call) (string, string, error) {
		switch c.name {
		case "slurp":
			return "0,0 10x10\n", "", nil
		case "grim":
			writeArg(t, c, pngData)
		}
		return "", "", nil
	}
	if _, _, err := Screenshot(context.Background(), config.Config{Shot: config.Shot{Tool: "grim"}}); err != nil {
		t.Fatal(err)
	}
	if len(f.calls) != 2 || f.calls[0].name != "slurp" || f.calls[1].name != "grim" {
		t.Errorf("calls = %v, want slurp then grim", f.commands())
	}
}

func TestLinuxScreenshotToolPinnedMissingNamesTheKey(t *testing.T) {
	newFakeSystem(t, "linux", map[string]string{"DISPLAY": ":0"})
	_, _, err := Screenshot(context.Background(), config.Config{Shot: config.Shot{Tool: "maim"}})
	if !errors.Is(err, ErrUnsupported) || !strings.Contains(err.Error(), "shot.tool = maim") || !strings.Contains(err.Error(), "maim is not installed") {
		t.Errorf("err = %v", err)
	}
}

// TestNamedShotToolsMatchSchemaEnum guards against namedShotTools and the
// shot.tool schema enum drifting apart.
func TestNamedShotToolsMatchSchemaEnum(t *testing.T) {
	k, err := config.Lookup("shot.tool")
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range k.Enum {
		if v == "auto" {
			continue
		}
		if _, ok := namedShotTools[v]; !ok {
			t.Errorf("schema enum has %q, missing from namedShotTools", v)
		}
	}
	for name := range namedShotTools {
		if !slices.Contains(k.Enum, name) {
			t.Errorf("namedShotTools has %q, missing from the schema enum", name)
		}
	}
}

func TestLinuxClipboardImageWayland(t *testing.T) {
	f := newFakeSystem(t, "linux", map[string]string{"WAYLAND_DISPLAY": "w"}, "wl-paste")
	f.handle = func(c call) (string, string, error) {
		if slices.Contains(c.args, "--list-types") {
			return "text/plain;charset=utf-8\nimage/png\nimage/jpeg\n", "", nil
		}
		return pngData, "", nil
	}
	data, ext, err := ClipboardImage(context.Background())
	if err != nil || string(data) != pngData || ext != "png" {
		t.Fatalf("got %q %q %v", data, ext, err)
	}
	if want := []string{"wl-paste --list-types", "wl-paste --type image/png"}; !slices.Equal(f.commands(), want) {
		t.Errorf("ran %q", f.commands())
	}

	f.calls = nil
	f.handle = func(c call) (string, string, error) {
		if slices.Contains(c.args, "--list-types") {
			return "image/jpeg\n", "", nil
		}
		return "jpegdata", "", nil
	}
	if _, ext, err := ClipboardImage(context.Background()); err != nil || ext != "jpg" {
		t.Errorf("jpeg: ext=%q err=%v", ext, err)
	}

	f.handle = func(c call) (string, string, error) { return "text/plain\n", "", nil }
	if _, _, err := ClipboardImage(context.Background()); !errors.Is(err, ErrNoImage) {
		t.Errorf("text only: err = %v", err)
	}

	f.handle = func(c call) (string, string, error) { return "", "Nothing is copied\n", exitError(t, 1) }
	if _, _, err := ClipboardImage(context.Background()); !errors.Is(err, ErrNoImage) {
		t.Errorf("empty clipboard: err = %v", err)
	}
}

func TestLinuxClipboardImageX11(t *testing.T) {
	f := newFakeSystem(t, "linux", map[string]string{"DISPLAY": ":0"}, "xclip")
	f.handle = func(c call) (string, string, error) { return pngData, "", nil }
	data, ext, err := ClipboardImage(context.Background())
	if err != nil || string(data) != pngData || ext != "png" {
		t.Fatalf("got %q %q %v", data, ext, err)
	}
	if got := f.commands()[0]; got != "xclip -selection clipboard -t image/png -o" {
		t.Errorf("ran %q", got)
	}

	f.handle = func(c call) (string, string, error) {
		return "", "Error: target image/png not available\n", exitError(t, 1)
	}
	if _, _, err := ClipboardImage(context.Background()); !errors.Is(err, ErrNoImage) {
		t.Errorf("no png: err = %v", err)
	}

	newFakeSystem(t, "linux", map[string]string{})
	if _, _, err := ClipboardImage(context.Background()); !errors.Is(err, ErrUnsupported) {
		t.Errorf("no tools: err = %v", err)
	}
}

func TestLinuxCopyText(t *testing.T) {
	tests := []struct {
		name string
		env  map[string]string
		bins []string
		want string
	}{
		{"wayland", map[string]string{"WAYLAND_DISPLAY": "w", "DISPLAY": ":0"}, []string{"wl-copy", "xclip"}, "wl-copy"},
		{"xclip", map[string]string{"DISPLAY": ":0"}, []string{"xclip", "xsel"}, "xclip -selection clipboard -in"},
		{"xsel", map[string]string{"DISPLAY": ":0"}, []string{"xsel"}, "xsel --clipboard --input"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFakeSystem(t, "linux", tt.env, tt.bins...)
			if err := CopyText(context.Background(), "\u043f\u0440\u0438\u0432\u0435\u0442"); err != nil {
				t.Fatal(err)
			}
			c := f.calls[0]
			if c.String() != tt.want || c.stdin != "\u043f\u0440\u0438\u0432\u0435\u0442" || !c.disc {
				t.Errorf("ran %q stdin=%q discard=%v", c.String(), c.stdin, c.disc)
			}
		})
	}

	newFakeSystem(t, "linux", map[string]string{"WAYLAND_DISPLAY": "w"})
	if err := CopyText(context.Background(), "x"); !errors.Is(err, ErrUnsupported) {
		t.Errorf("no tools: err = %v", err)
	}
}

func TestLinuxOpenURL(t *testing.T) {
	f := newFakeSystem(t, "linux", map[string]string{}, "xdg-open")
	uri := ObsidianURI("/home/me/notes/nn/a.md")
	if err := OpenURL(context.Background(), uri); err != nil {
		t.Fatal(err)
	}
	if got := f.commands(); !slices.Equal(got, []string{"xdg-open " + uri}) {
		t.Errorf("ran %q", got)
	}

	newFakeSystem(t, "linux", map[string]string{})
	if err := OpenURL(context.Background(), uri); !errors.Is(err, ErrUnsupported) {
		t.Errorf("no xdg-open: err = %v", err)
	}
}

func TestScreenAccessUnknownOnLinux(t *testing.T) {
	newFakeSystem(t, "linux", map[string]string{})
	if granted, known := ScreenAccess(context.Background()); granted || known {
		t.Errorf("got %v %v", granted, known)
	}
}
