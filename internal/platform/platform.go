// Package platform is the per-OS surface nn needs: screenshots, clipboard,
// opening URLs, and "nn doctor" checks.
package platform

import (
	"context"
	"errors"
	"fmt"
	"os"
	"runtime"
	"slices"
	"strings"
	"time"

	"github.com/lamovs/nn/internal/config"
)

var (
	ErrCancelled        = errors.New("cancelled")
	ErrNoImage          = errors.New("no image available")
	ErrImageUnavailable = errors.New("clipboard image backend unavailable")
	ErrUnsupported      = errors.New("unsupported platform")
)

var (
	goos        = runtime.GOOS
	getenv      = os.Getenv
	userHomeDir = os.UserHomeDir
)

// Screenshot reports a dismissed selection as ErrCancelled.
func Screenshot(ctx context.Context, cfg config.Config) (data []byte, ext string, err error) {
	switch goos {
	case "darwin":
		return darwinScreenshot(ctx)
	case "linux":
		return linuxScreenshot(ctx, cfg.Shot.Tool)
	}
	return nil, "", unsupportedOS()
}

// ClipboardImage reports ErrNoImage when the clipboard holds no image.
func ClipboardImage(ctx context.Context) ([]byte, string, error) {
	switch goos {
	case "darwin":
		return darwinClipboardImage(ctx)
	case "linux":
		return linuxClipboardImage(ctx)
	}
	return nil, "", unsupportedOS()
}

// ClipboardText reads plain text without trimming or adding newlines.
func ClipboardText(ctx context.Context) ([]byte, error) {
	var c command
	switch goos {
	case "darwin":
		c = command{name: "pbpaste", args: []string{"-Prefer", "txt"}, env: []string{"LC_ALL=en_US.UTF-8", "LANG=en_US.UTF-8", "LC_CTYPE=en_US.UTF-8"}}
	case "linux":
		s := detectSession()
		switch {
		case s.wayland && have("wl-paste"):
			c = command{name: "wl-paste", args: []string{"--no-newline", "--type", "text"}}
		case getenv("DISPLAY") != "" && have("xclip"):
			c = command{name: "xclip", args: []string{"-selection", "clipboard", "-out"}}
		case getenv("DISPLAY") != "" && have("xsel"):
			c = command{name: "xsel", args: []string{"--clipboard", "--output"}}
		default:
			return nil, fmt.Errorf("%w: no clipboard text tool for %s", ErrUnsupported, s)
		}
	default:
		return nil, unsupportedOS()
	}
	data, stderr, err := runCommand(ctx, c)
	if err != nil {
		return nil, commandError(ctx, c.name, err, stderr)
	}
	return data, nil
}

func CopyText(ctx context.Context, s string) error {
	switch goos {
	case "darwin":
		return darwinCopyText(ctx, s)
	case "linux":
		return linuxCopyText(ctx, s)
	}
	return unsupportedOS()
}

func OpenURL(ctx context.Context, url string) error {
	switch goos {
	case "darwin":
		return darwinOpenURL(ctx, url)
	case "linux":
		return linuxOpenURL(ctx, url)
	}
	return unsupportedOS()
}

var notifyTimeout = 2 * time.Second

// Notify passes title and body to the OS tool as arguments, never as part of a script.
func Notify(ctx context.Context, cfg config.Config, title, body string) error {
	if !cfg.Notify.Enabled {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, notifyTimeout)
	defer cancel()
	var err error
	switch goos {
	case "darwin":
		err = darwinNotify(ctx, title, body)
	case "linux":
		err = linuxNotify(ctx, title, body)
	}
	// A notification that ran out of time was not cancelled by the user.
	if errors.Is(err, ErrCancelled) && errors.Is(context.Cause(ctx), context.DeadlineExceeded) {
		return fmt.Errorf("notification: no answer within %s", notifyTimeout)
	}
	return err
}

func ObsidianURI(absPath string) string {
	return "obsidian://open?path=" + encodeURIComponent(absPath)
}

// ScreenAccess's known is false when the answer cannot be determined.
func ScreenAccess(ctx context.Context) (granted bool, known bool) {
	if goos == "darwin" {
		return darwinScreenAccess(ctx)
	}
	return false, false
}

type Check struct {
	Name   string
	OK     bool
	Detail string
	Fix    []string
}

func Checks(ctx context.Context, cfg config.Config) []Check {
	var checks []Check
	switch goos {
	case "darwin":
		checks = darwinChecks(ctx, cfg)
	case "linux":
		checks = linuxChecks(ctx, cfg)
	default:
		return []Check{{Name: "platform", Detail: unsupportedOS().Error()}}
	}
	if !cfg.Obsidian.Check {
		checks = slices.DeleteFunc(checks, func(c Check) bool { return c.Name == "Obsidian" })
	}
	return checks
}

func unsupportedOS() error {
	return errors.New(ErrUnsupported.Error() + ": " + goos)
}

// encodeURIComponent matches JavaScript's, which is what Obsidian decodes.
func encodeURIComponent(s string) string {
	const hex = "0123456789ABCDEF"
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case 'a' <= c && c <= 'z', 'A' <= c && c <= 'Z', '0' <= c && c <= '9',
			c == '-', c == '_', c == '.', c == '~':
			b.WriteByte(c)
		default:
			b.WriteByte('%')
			b.WriteByte(hex[c>>4])
			b.WriteByte(hex[c&15])
		}
	}
	return b.String()
}
