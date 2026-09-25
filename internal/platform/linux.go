package platform

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/lamovs/nn/internal/config"
)

const (
	desktopWlroots = "wlroots"
	desktopKDE     = "kde"
	desktopGNOME   = "gnome"
)

const clipHint = "take a screenshot to the clipboard with your system tool, then run: nn add --clip"

type session struct {
	wayland bool
	x11     bool
	desktop string // desktopWlroots, desktopKDE, desktopGNOME or ""
}

func (s session) String() string {
	var parts []string
	switch s.desktop {
	case desktopWlroots:
		parts = append(parts, "wlroots compositor")
	case desktopKDE:
		parts = append(parts, "KDE")
	case desktopGNOME:
		parts = append(parts, "GNOME")
	}
	switch {
	case s.wayland:
		parts = append(parts, "Wayland")
	case s.x11:
		parts = append(parts, "X11")
	default:
		parts = append(parts, "no graphical session")
	}
	return strings.Join(parts, ", ")
}

// wlrootsDesktops implement wlr-screencopy, which grim needs.
var wlrootsDesktops = []string{"hyprland", "sway", "river", "wayfire", "labwc", "niri"}

func detectSession() session {
	var s session
	sessionType := strings.ToLower(getenv("XDG_SESSION_TYPE"))
	s.wayland = sessionType == "wayland" || getenv("WAYLAND_DISPLAY") != ""
	s.x11 = sessionType == "x11" || (getenv("DISPLAY") != "" && !s.wayland)

	desktops := strings.Split(strings.ToLower(getenv("XDG_CURRENT_DESKTOP")), ":")
	switch {
	case getenv("HYPRLAND_INSTANCE_SIGNATURE") != "" || getenv("SWAYSOCK") != "" || anyIn(desktops, wlrootsDesktops):
		s.desktop = desktopWlroots
	case anyIn(desktops, []string{"kde"}):
		s.desktop = desktopKDE
	case anyIn(desktops, []string{"gnome"}):
		s.desktop = desktopGNOME
	}
	return s
}

func anyIn(have, want []string) bool {
	for _, h := range have {
		for _, w := range want {
			if h == w {
				return true
			}
		}
	}
	return false
}

type shotTool struct {
	name     string
	bins     []string // programs that must be in PATH
	packages []string
	capture  func(ctx context.Context, path string) ([]byte, error)
}

func (t shotTool) installed() bool {
	for _, bin := range t.bins {
		if !have(bin) {
			return false
		}
	}
	return true
}

var (
	grimTool = shotTool{
		name:     "grim + slurp",
		bins:     []string{"slurp", "grim"},
		packages: []string{"grim", "slurp"},
		capture: func(ctx context.Context, path string) ([]byte, error) {
			geometry, stderr, err := runCommand(ctx, command{name: "slurp", processGroup: true})
			if ctx.Err() != nil {
				return nil, cancelled(ctx)
			}
			var exit *exec.ExitError
			if err != nil && !errors.As(err, &exit) {
				return nil, commandError(ctx, "slurp", err, stderr)
			}
			// slurp exits non-zero when the selection is dismissed.
			region := strings.TrimSpace(string(geometry))
			if err != nil || region == "" {
				return nil, ErrCancelled
			}
			return captureToFile(ctx, command{name: "grim", args: []string{"-g", region, path}}, path)
		},
	}
	spectacleTool = shotTool{
		name:     "spectacle",
		bins:     []string{"spectacle"},
		packages: []string{"spectacle"},
		capture: func(ctx context.Context, path string) ([]byte, error) {
			return captureToFile(ctx, command{name: "spectacle", args: []string{"-r", "-b", "-n", "-o", path}}, path)
		},
	}
	gnomeScreenshotTool = shotTool{
		name:     "gnome-screenshot",
		bins:     []string{"gnome-screenshot"},
		packages: []string{"gnome-screenshot"},
		capture: func(ctx context.Context, path string) ([]byte, error) {
			return captureToFile(ctx, command{name: "gnome-screenshot", args: []string{"-a", "-f", path}}, path)
		},
	}
	maimTool = shotTool{
		name:     "maim",
		bins:     []string{"maim"},
		packages: []string{"maim"},
		capture: func(ctx context.Context, path string) ([]byte, error) {
			return captureToFile(ctx, command{name: "maim", args: []string{"-s", path}}, path)
		},
	}
	importTool = shotTool{
		name:     "import (ImageMagick)",
		bins:     []string{"import"},
		packages: []string{"imagemagick"},
		capture: func(ctx context.Context, path string) ([]byte, error) {
			return captureToFile(ctx, command{name: "import", args: []string{path}}, path)
		},
	}
)

var namedShotTools = map[string]shotTool{
	"grim":             grimTool,
	"spectacle":        spectacleTool,
	"gnome-screenshot": gnomeScreenshotTool,
	"maim":             maimTool,
	"import":           importTool,
}

func shotTools(s session, tool string) []shotTool {
	if named, ok := namedShotTools[tool]; ok {
		return []shotTool{named}
	}
	var tools []shotTool
	switch s.desktop {
	case desktopWlroots:
		tools = append(tools, grimTool)
	case desktopKDE:
		tools = append(tools, spectacleTool)
	case desktopGNOME:
		tools = append(tools, gnomeScreenshotTool)
	}
	switch {
	case s.x11 && !s.wayland:
		tools = append(tools, maimTool, importTool)
	case s.wayland && s.desktop == "":
		// An unrecognized compositor may still support wlr-screencopy.
		tools = append(tools, grimTool)
	}
	return tools
}

func linuxScreenshot(ctx context.Context, tool string) ([]byte, string, error) {
	s := detectSession()
	tools := shotTools(s, tool)
	if len(tools) == 0 {
		return nil, "", fmt.Errorf("%w: no screenshot tool for %s; %s", ErrUnsupported, s, clipHint)
	}
	for _, t := range tools {
		if !t.installed() {
			continue
		}
		data, err := withTempDir(func(dir string) ([]byte, error) {
			return t.capture(ctx, filepath.Join(dir, "shot.png"))
		})
		if err != nil {
			return nil, "", err
		}
		return data, "png", nil
	}
	if _, ok := namedShotTools[tool]; ok {
		return nil, "", fmt.Errorf("%w: shot.tool = %s: %s is not installed; %s",
			ErrUnsupported, tool, tools[0].name, clipHint)
	}
	return nil, "", fmt.Errorf("%w: %s is not installed (%s); %s",
		ErrUnsupported, tools[0].name, s, clipHint)
}

var clipboardImageTypes = [][2]string{
	{"image/png", "png"},
	{"image/jpeg", "jpg"},
	{"image/webp", "webp"},
	{"image/gif", "gif"},
}

func linuxClipboardImage(ctx context.Context) ([]byte, string, error) {
	s := detectSession()
	switch {
	case s.wayland && have("wl-paste"):
		types, stderr, err := runCommand(ctx, command{name: "wl-paste", args: []string{"--list-types"}})
		if err != nil {
			return nil, "", noImageError(ctx, err, stderr)
		}
		mime, ext := pickImageType(types)
		if mime == "" {
			return nil, "", ErrNoImage
		}
		data, stderr, err := runCommand(ctx, command{name: "wl-paste", args: []string{"--type", mime}})
		if err != nil {
			return nil, "", commandError(ctx, "wl-paste", err, stderr)
		}
		if len(data) == 0 {
			return nil, "", ErrNoImage
		}
		return data, ext, nil

	case getenv("DISPLAY") != "" && have("xclip"):
		data, stderr, err := runCommand(ctx, command{
			name: "xclip",
			args: []string{"-selection", "clipboard", "-t", "image/png", "-o"},
		})
		if err != nil {
			return nil, "", noImageError(ctx, err, stderr)
		}
		if len(data) == 0 {
			return nil, "", ErrNoImage
		}
		return data, "png", nil
	}
	return nil, "", fmt.Errorf("%w: %w: no clipboard image tool for %s (install wl-clipboard or xclip)", ErrImageUnavailable, ErrUnsupported, s)
}

func noImageError(ctx context.Context, err error, stderr []byte) error {
	if ctx.Err() != nil {
		return cancelled(ctx)
	}
	var exit *exec.ExitError
	if !errors.As(err, &exit) {
		return err
	}
	msg := strings.TrimSpace(string(stderr))
	lower := strings.ToLower(msg)
	if strings.Contains(lower, "nothing is copied") || strings.Contains(lower, "target image/png not available") || strings.Contains(lower, "selection owner is not present") {
		return fmt.Errorf("%w: %s", ErrNoImage, msg)
	}
	if msg != "" {
		return fmt.Errorf("clipboard image: %s: %w", msg, err)
	}
	return err
}

func pickImageType(list []byte) (mime, ext string) {
	var offered []string
	sc := bufio.NewScanner(bytes.NewReader(list))
	for sc.Scan() {
		offered = append(offered, strings.TrimSpace(sc.Text()))
	}
	for _, t := range clipboardImageTypes {
		for _, o := range offered {
			if o == t[0] {
				return t[0], t[1]
			}
		}
	}
	return "", ""
}

func linuxCopyText(ctx context.Context, s string) error {
	sess := detectSession()
	var c command
	switch {
	case sess.wayland && have("wl-copy"):
		c = command{name: "wl-copy"}
	case getenv("DISPLAY") != "" && have("xclip"):
		c = command{name: "xclip", args: []string{"-selection", "clipboard", "-in"}}
	case getenv("DISPLAY") != "" && have("xsel"):
		c = command{name: "xsel", args: []string{"--clipboard", "--input"}}
	default:
		return fmt.Errorf("%w: no clipboard tool for %s (install wl-clipboard, xclip or xsel)", ErrUnsupported, sess)
	}
	c.stdin = []byte(s)
	c.discard = true
	if _, stderr, err := runCommand(ctx, c); err != nil {
		return commandError(ctx, c.name, err, stderr)
	}
	return nil
}

func linuxOpenURL(ctx context.Context, url string) error {
	if !have("xdg-open") {
		return fmt.Errorf("%w: xdg-open is not installed (package xdg-utils)", ErrUnsupported)
	}
	if err := startCommand(ctx, command{name: "xdg-open", args: []string{url}}); err != nil {
		if errors.Is(err, ErrCancelled) {
			return err
		}
		return fmt.Errorf("xdg-open %s: %w", url, err)
	}
	return nil
}

// notifyBody escapes "\" against notify-send's g_strcompress and "&"/"<" against markup; the summary gets neither.
var notifyBody = strings.NewReplacer(`\`, `\\`, "&", "&amp;", "<", "&lt;", ">", "&gt;")

func linuxNotify(ctx context.Context, title, body string) error {
	if !have("notify-send") {
		return nil
	}
	c := command{name: "notify-send", args: []string{"--", title, notifyBody.Replace(body)}}
	if _, stderr, err := runCommand(ctx, c); err != nil {
		return commandError(ctx, c.name, err, stderr)
	}
	return nil
}

func omarchy() bool {
	if have("omarchy-version") {
		return true
	}
	home, err := userHomeDir()
	return err == nil && dirExists(filepath.Join(home, ".local", "share", "omarchy"))
}

func linuxChecks(ctx context.Context, cfg config.Config) []Check {
	d := detectDistro()
	s := detectSession()
	var checks []Check
	if omarchy() {
		checks = append(checks, Check{
			Name:   "Omarchy",
			OK:     true,
			Detail: "detected: grim, slurp, wl-clipboard and tesseract ship with it",
		})
	}
	checks = append(checks,
		ocrCheck(ctx, cfg, d),
		linuxShotCheck(s, d, cfg.Shot.Tool),
		linuxClipboardCheck(s, d),
		linuxOpenCheck(d),
	)
	if home, err := userHomeDir(); err == nil {
		configHome := getenv("XDG_CONFIG_HOME")
		if !filepath.IsAbs(configHome) {
			configHome = filepath.Join(home, ".config")
		}
		checks = append(checks, obsidianCheck(cfg.Vault.Root, []string{
			filepath.Join(configHome, "obsidian", "obsidian.json"),
			filepath.Join(home, ".var", "app", "md.obsidian.Obsidian", "config", "obsidian", "obsidian.json"),
			filepath.Join(home, "snap", "obsidian", "current", ".config", "obsidian", "obsidian.json"),
		}))
	}
	return checks
}

func linuxShotCheck(s session, d distro, tool string) Check {
	c := Check{Name: "screenshot"}
	tools := shotTools(s, tool)
	if len(tools) == 0 {
		c.Detail = s.String()
		c.Fix = []string{clipHint}
		return c
	}
	for _, t := range tools {
		if t.installed() {
			c.OK = true
			c.Detail = fmt.Sprintf("%s (%s)", t.name, s)
			return c
		}
	}
	if _, pinned := namedShotTools[tool]; pinned {
		c.Detail = fmt.Sprintf("shot.tool = %s: %s is not installed (%s)", tool, tools[0].name, s)
		c.Fix = append(d.install(tools[0].packages...), "or: nn config shot.tool auto")
		return c
	}
	c.Detail = fmt.Sprintf("%s is not installed (%s)", tools[0].name, s)
	c.Fix = d.install(tools[0].packages...)
	return c
}

func linuxClipboardCheck(s session, d distro) Check {
	c := Check{Name: "clipboard"}
	switch {
	case s.wayland:
		if have("wl-copy") && have("wl-paste") {
			c.OK = true
			c.Detail = "wl-clipboard"
			return c
		}
		c.Detail = "wl-copy and wl-paste not found"
		c.Fix = d.install("wl-clipboard")
	case getenv("DISPLAY") != "":
		switch {
		case have("xclip"):
			c.OK = true
			c.Detail = "xclip"
		case have("xsel"):
			c.Detail = "xsel copies text but cannot read images; install xclip"
			c.Fix = d.install("xclip")
		default:
			c.Detail = "xclip not found"
			c.Fix = d.install("xclip")
		}
	default:
		c.Detail = s.String()
	}
	return c
}

func linuxOpenCheck(d distro) Check {
	if have("xdg-open") {
		return Check{Name: "open links", OK: true, Detail: "xdg-open"}
	}
	return Check{Name: "open links", Detail: "xdg-open not found", Fix: d.install("xdg-utils")}
}
