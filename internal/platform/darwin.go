package platform

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"

	"github.com/lamovs/nn/internal/config"
	"github.com/lamovs/nn/internal/install"
)

const screencapture = "/usr/sbin/screencapture"

// nnVisionNoImage is nn-vision's exit code for an empty clipboard.
const nnVisionNoImage = 3

var findHelper = install.Helper

func darwinScreenshot(ctx context.Context) ([]byte, string, error) {
	data, err := withTempDir(func(dir string) ([]byte, error) {
		path := filepath.Join(dir, "shot.png")
		return captureToFile(ctx, command{name: screencapture, args: []string{"-i", "-x", path}}, path)
	})
	if err != nil {
		return nil, "", err
	}
	return data, "png", nil
}

func darwinClipboardImage(ctx context.Context) ([]byte, string, error) {
	helper, err := findHelper()
	if err != nil {
		if getenv("NN_VISION_HELPER") == "" {
			return nil, "", fmt.Errorf("%w: %v", ErrImageUnavailable, err)
		}
		return nil, "", err
	}
	data, err := withTempDir(func(dir string) ([]byte, error) {
		path := filepath.Join(dir, "clip.png")
		_, stderr, err := runCommand(ctx, command{name: helper, args: []string{"clipboard-image", path}})
		var exit *exec.ExitError
		switch {
		case err == nil:
		case ctx.Err() == nil && errors.As(err, &exit) && exit.ExitCode() == nnVisionNoImage:
			return nil, ErrNoImage
		default:
			return nil, commandError(ctx, "nn-vision", err, stderr)
		}
		data, err := os.ReadFile(path)
		if err == nil && len(data) == 0 {
			return nil, ErrNoImage
		}
		return data, err
	})
	if err != nil {
		return nil, "", err
	}
	return data, "png", nil
}

func darwinCopyText(ctx context.Context, s string) error {
	c := command{name: "pbcopy", stdin: []byte(s)}
	// pbcopy decodes stdin by the locale and mangles UTF-8 without one.
	if !utf8Locale() {
		c.env = []string{"LANG=en_US.UTF-8", "LC_CTYPE=en_US.UTF-8"}
	}
	if _, stderr, err := runCommand(ctx, c); err != nil {
		return commandError(ctx, "pbcopy", err, stderr)
	}
	return nil
}

func utf8Locale() bool {
	for _, key := range []string{"LC_ALL", "LC_CTYPE", "LANG"} {
		if v := getenv(key); v != "" {
			v = strings.ToLower(v)
			return strings.Contains(v, "utf-8") || strings.Contains(v, "utf8")
		}
	}
	return false
}

func darwinOpenURL(ctx context.Context, url string) error {
	if _, stderr, err := runCommand(ctx, command{name: "open", args: []string{url}}); err != nil {
		return commandError(ctx, "open", err, stderr)
	}
	return nil
}

// notifyScript takes title and body from argv, not pasted into the script,
// so a quote or backslash in either cannot break out of the AppleScript string.
var notifyScript = []string{
	"-e", "on run argv",
	"-e", "display notification (item 2 of argv) with title (item 1 of argv)",
	"-e", "end run",
}

func darwinNotify(ctx context.Context, title, body string) error {
	if !have("osascript") {
		return nil
	}
	args := append(slices.Clone(notifyScript), "--", title, body)
	if _, stderr, err := runCommand(ctx, command{name: "osascript", args: args}); err != nil {
		return commandError(ctx, "osascript", err, stderr)
	}
	return nil
}

func darwinScreenAccess(ctx context.Context) (granted bool, known bool) {
	helper, err := findHelper()
	if err != nil {
		return false, false
	}
	stdout, _, err := runCommand(ctx, command{name: helper, args: []string{"screen-access"}})
	if err != nil {
		return false, false
	}
	switch strings.TrimSpace(string(stdout)) {
	case "granted":
		return true, true
	case "denied":
		return false, true
	}
	return false, false
}

func darwinHelperCheck() (Check, bool) {
	helper, err := findHelper()
	if err != nil {
		return Check{
			Name:   "nn-vision helper",
			Detail: err.Error(),
			Fix: []string{
				"nn setup install",
				"or, from a source checkout: scripts/build-macos-helper ~/.local/share/nn",
			},
		}, false
	}
	return Check{Name: "nn-vision helper", OK: true, Detail: helper}, true
}

func darwinScreenAccessCheck(ctx context.Context, helperOK bool) Check {
	ctx, cancel := context.WithTimeout(ctx, checkTimeout)
	defer cancel()
	c := Check{Name: "screen recording"}
	granted, known := darwinScreenAccess(ctx)
	switch {
	case granted:
		c.OK = true
		c.Detail = "allowed"
	case known:
		c.Detail = "not allowed: nn shot captures only the desktop background"
		c.Fix = []string{
			"System Settings -> Privacy & Security -> Screen & System Audio Recording:",
			"allow your terminal app, then restart it",
		}
	case !helperOK:
		c.Detail = "unknown: needs the nn-vision helper"
	default:
		c.Detail = "unknown: nn-vision screen-access failed"
	}
	return c
}

func darwinChecks(ctx context.Context, cfg config.Config) []Check {
	helperCheck, helperOK := darwinHelperCheck()
	checks := []Check{helperCheck, ocrCheck(ctx, cfg, distro{})}

	shot := Check{Name: "screenshot", OK: fileExists(screencapture), Detail: screencapture}
	if !shot.OK {
		shot.Detail = screencapture + " is missing"
	}
	checks = append(checks, shot, darwinScreenAccessCheck(ctx, helperOK))

	clip := Check{Name: "clipboard", OK: have("pbcopy")}
	switch {
	case !clip.OK:
		clip.Detail = "pbcopy not found in PATH"
	case helperOK:
		clip.Detail = "text via pbcopy, images via nn-vision"
	default:
		clip.Detail = "text via pbcopy; images need the nn-vision helper"
	}
	checks = append(checks, clip)

	if home, err := userHomeDir(); err == nil {
		checks = append(checks, obsidianCheck(cfg.Vault.Root, []string{
			filepath.Join(home, "Library", "Application Support", "obsidian", "obsidian.json"),
		}))
	}
	return checks
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}
