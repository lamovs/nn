package platform

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/lamovs/nn/internal/config"
	"github.com/lamovs/nn/internal/ocr"
)

func TestObsidianURI(t *testing.T) {
	got := ObsidianURI("/Users/me/My Vault/\u0437\u0430\u043c\u0435\u0442\u043a\u0430 & more+1.md")
	want := "obsidian://open?path=%2FUsers%2Fme%2FMy%20Vault%2F%D0%B7%D0%B0%D0%BC%D0%B5%D1%82%D0%BA%D0%B0%20%26%20more%2B1.md"
	if got != want {
		t.Errorf("got  %s\nwant %s", got, want)
	}
}

func TestDistroFromOSRelease(t *testing.T) {
	tests := []struct {
		release string
		family  string
	}{
		{"NAME=\"Arch Linux\"\nID=arch\n", familyArch},
		{"ID=endeavouros\nID_LIKE=arch\n", familyArch},
		{"PRETTY_NAME=\"Ubuntu 24.04 LTS\"\nID=ubuntu\nID_LIKE=debian\n", familyDebian},
		{"ID=linuxmint\nID_LIKE=\"ubuntu debian\"\n", familyDebian},
		{"ID=fedora\n", familyFedora},
		{"ID=\"opensuse-tumbleweed\"\nID_LIKE=\"opensuse suse\"\n", familyOpenSUSE},
		{"ID=alpine\n", familyAlpine},
		{"ID=nixos\n", familyNixOS},
		{"ID=gentoo\n", ""},
	}
	for _, tt := range tests {
		d := distroFromOSRelease(parseOSRelease(strings.NewReader(tt.release)))
		if d.family != tt.family {
			t.Errorf("%q: family %q, want %q", tt.release, d.family, tt.family)
		}
	}
}

func TestInstallCommands(t *testing.T) {
	tools := []string{"tesseract", "tesseract-lang:rus", "tesseract-lang:eng"}
	tests := []struct {
		family string
		want   string
	}{
		{familyArch, "sudo pacman -S --needed tesseract tesseract-data-rus tesseract-data-eng"},
		{familyDebian, "sudo apt install tesseract-ocr tesseract-ocr-rus tesseract-ocr-eng"},
		{familyFedora, "sudo dnf install tesseract tesseract-langpack-rus tesseract-langpack-eng"},
		{familyOpenSUSE, "sudo zypper install tesseract-ocr tesseract-ocr-traineddata-rus tesseract-ocr-traineddata-eng"},
		{familyAlpine, "sudo apk add tesseract-ocr tesseract-ocr-data-rus tesseract-ocr-data-eng"},
		{familyNixOS, "nix-env -iA nixos.tesseract"},
	}
	for _, tt := range tests {
		got := distro{family: tt.family}.install(tools...)
		if len(got) == 0 || got[0] != tt.want {
			t.Errorf("%s: got %q, want %q", tt.family, got, tt.want)
		}
	}

	shot := map[string]string{
		familyDebian:   "sudo apt install kde-spectacle",
		familyNixOS:    "nix-env -iA nixos.kdePackages.spectacle",
		familyOpenSUSE: "sudo zypper install spectacle",
	}
	for family, want := range shot {
		if got := (distro{family: family}).install("spectacle"); got[0] != want {
			t.Errorf("%s spectacle: got %q", family, got)
		}
	}
	if got := (distro{}).install("grim", "slurp"); got[0] != "install with your package manager: grim, slurp" {
		t.Errorf("unknown distro: %q", got)
	}
}

type fakeEngine struct {
	name string
	err  error
}

func (e fakeEngine) Name() string                    { return e.name }
func (e fakeEngine) Check(ctx context.Context) error { return e.err }
func (e fakeEngine) Recognize(ctx context.Context, p string, l []string) (*ocr.Result, error) {
	return nil, e.err
}

func stubOCR(t *testing.T, e ocr.Engine, langs []string) {
	t.Helper()
	prevEngine, prevLangs := defaultEngine, defaultLangs
	defaultEngine = func(config.Config) ocr.Engine { return e }
	defaultLangs = func(config.Config) []string { return langs }
	t.Cleanup(func() { defaultEngine, defaultLangs = prevEngine, prevLangs })
}

func stubLinuxHost(t *testing.T, release string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "os-release")
	if err := os.WriteFile(path, []byte(release), 0o644); err != nil {
		t.Fatal(err)
	}
	prevPaths, prevHome := osReleasePaths, userHomeDir
	osReleasePaths = []string{path}
	home := filepath.Join(dir, "home")
	userHomeDir = func() (string, error) { return home, nil }
	t.Cleanup(func() { osReleasePaths, userHomeDir = prevPaths, prevHome })
	return home
}

func findCheck(t *testing.T, checks []Check, prefix string) Check {
	t.Helper()
	for _, c := range checks {
		if strings.HasPrefix(c.Name, prefix) {
			return c
		}
	}
	t.Fatalf("no %q check in %+v", prefix, checks)
	return Check{}
}

func TestLinuxChecksOmarchyMissingRussian(t *testing.T) {
	home := stubLinuxHost(t, "NAME=\"Arch Linux\"\nID=arch\n")
	if err := os.MkdirAll(filepath.Join(home, ".local", "share", "omarchy"), 0o755); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	writeObsidianConfig(t, filepath.Join(home, ".config", "obsidian", "obsidian.json"), root)

	newFakeSystem(t, "linux",
		map[string]string{"XDG_SESSION_TYPE": "wayland", "HYPRLAND_INSTANCE_SIGNATURE": "x", "WAYLAND_DISPLAY": "wayland-1"},
		"grim", "slurp", "wl-copy", "wl-paste", "xdg-open", "tesseract")
	stubOCR(t, fakeEngine{"tesseract", &ocr.LangsError{Engine: "tesseract", Missing: []string{"rus"}, Available: []string{"eng", "osd"}}},
		[]string{"rus", "eng"})

	checks := Checks(context.Background(), config.Config{Vault: config.Vault{Root: root}, Obsidian: config.Obsidian{Check: true}})
	var names []string
	for _, c := range checks {
		names = append(names, c.Name)
	}
	want := []string{"Omarchy", "OCR (tesseract)", "screenshot", "clipboard", "open links", "Obsidian"}
	if !slices.Equal(names, want) {
		t.Fatalf("checks = %q", names)
	}
	ocrc := findCheck(t, checks, "OCR")
	if ocrc.OK || !slices.Equal(ocrc.Fix, []string{"sudo pacman -S --needed tesseract-data-rus"}) {
		t.Errorf("OCR check = %+v", ocrc)
	}
	for _, name := range []string{"Omarchy", "screenshot", "clipboard", "open links", "Obsidian"} {
		if c := findCheck(t, checks, name); !c.OK {
			t.Errorf("%s check = %+v", name, c)
		}
	}
}

func TestLinuxChecksDebianGnomeMissingTools(t *testing.T) {
	stubLinuxHost(t, "ID=ubuntu\nID_LIKE=debian\n")
	newFakeSystem(t, "linux", map[string]string{"XDG_SESSION_TYPE": "x11", "XDG_CURRENT_DESKTOP": "ubuntu:GNOME", "DISPLAY": ":0"})
	stubOCR(t, fakeEngine{"tesseract", fmt.Errorf("tesseract: %w", ocr.ErrNotInstalled)}, []string{"rus", "eng"})

	checks := Checks(context.Background(), config.Config{Vault: config.Vault{Root: t.TempDir()}, Obsidian: config.Obsidian{Check: true}})
	for name, want := range map[string][]string{
		"OCR":        {"sudo apt install tesseract-ocr tesseract-ocr-rus tesseract-ocr-eng"},
		"screenshot": {"sudo apt install gnome-screenshot"},
		"clipboard":  {"sudo apt install xclip"},
		"open links": {"sudo apt install xdg-utils"},
	} {
		c := findCheck(t, checks, name)
		if c.OK || !slices.Equal(c.Fix, want) {
			t.Errorf("%s check = %+v, want fix %q", name, c, want)
		}
	}
	if c := findCheck(t, checks, "Obsidian"); c.OK || len(c.Fix) != 2 {
		t.Errorf("Obsidian check = %+v", c)
	}
}

func TestLinuxChecksShotToolPinned(t *testing.T) {
	stubLinuxHost(t, "ID=ubuntu\nID_LIKE=debian\n")
	newFakeSystem(t, "linux", map[string]string{"XDG_SESSION_TYPE": "x11", "XDG_CURRENT_DESKTOP": "ubuntu:GNOME", "DISPLAY": ":0"}, "gnome-screenshot")
	stubOCR(t, fakeEngine{"tesseract", nil}, []string{"rus", "eng"})

	checks := Checks(context.Background(), config.Config{
		Vault: config.Vault{Root: t.TempDir()},
		Shot:  config.Shot{Tool: "maim"},
	})
	c := findCheck(t, checks, "screenshot")
	want := []string{"sudo apt install maim", "or: nn config shot.tool auto"}
	if c.OK || !strings.Contains(c.Detail, "shot.tool = maim") || !strings.Contains(c.Detail, "maim is not installed") || !slices.Equal(c.Fix, want) {
		t.Errorf("screenshot check = %+v, want maim pinned and missing", c)
	}
}

func TestObsidianCheckDisabledByConfig(t *testing.T) {
	stubLinuxHost(t, "ID=ubuntu\nID_LIKE=debian\n")
	newFakeSystem(t, "linux", map[string]string{"XDG_SESSION_TYPE": "x11", "XDG_CURRENT_DESKTOP": "ubuntu:GNOME", "DISPLAY": ":0"},
		"gnome-screenshot", "xclip", "xdg-open", "tesseract")
	stubOCR(t, fakeEngine{"tesseract", nil}, []string{"rus", "eng"})

	checks := Checks(context.Background(), config.Config{Vault: config.Vault{Root: t.TempDir()}, Obsidian: config.Obsidian{Check: false}})
	for _, c := range checks {
		if c.Name == "Obsidian" {
			t.Errorf("Obsidian check present despite obsidian.check = false: %+v", c)
		}
	}
}

func TestObsidianCheckDisabledByConfigDarwin(t *testing.T) {
	home := t.TempDir()
	prevHome := userHomeDir
	userHomeDir = func() (string, error) { return home, nil }
	t.Cleanup(func() { userHomeDir = prevHome })

	stubHelper(t, "", errors.New("nn-vision helper not found"))
	newFakeSystem(t, "darwin", map[string]string{}, "pbcopy")
	stubOCR(t, fakeEngine{"vision", fmt.Errorf("%w: helper not found", ocr.ErrNotInstalled)}, []string{"ru-RU", "en-US"})

	checks := Checks(context.Background(), config.Config{Vault: config.Vault{Root: t.TempDir()}, Obsidian: config.Obsidian{Check: false}})
	for _, c := range checks {
		if c.Name == "Obsidian" {
			t.Errorf("Obsidian check present despite obsidian.check = false: %+v", c)
		}
	}
}

func TestLinuxChecksNixOSLanguages(t *testing.T) {
	stubLinuxHost(t, "ID=nixos\n")
	newFakeSystem(t, "linux", map[string]string{})
	stubOCR(t, fakeEngine{"tesseract", &ocr.LangsError{Engine: "tesseract", Missing: []string{"rus"}}}, []string{"rus", "eng"})
	c := findCheck(t, Checks(context.Background(), config.Config{Vault: config.Vault{Root: "/v"}}), "OCR")
	if len(c.Fix) != 1 || !strings.Contains(c.Fix[0], `enableLanguages = [ "rus" "eng" ]`) {
		t.Errorf("fix = %q", c.Fix)
	}
}

func TestDarwinChecks(t *testing.T) {
	home := t.TempDir()
	prevHome := userHomeDir
	userHomeDir = func() (string, error) { return home, nil }
	t.Cleanup(func() { userHomeDir = prevHome })

	stubHelper(t, "", errors.New("nn-vision helper not found"))
	newFakeSystem(t, "darwin", map[string]string{}, "pbcopy")
	stubOCR(t, fakeEngine{"vision", fmt.Errorf("%w: helper not found", ocr.ErrNotInstalled)}, []string{"ru-RU", "en-US"})

	root := t.TempDir()
	writeObsidianConfig(t, filepath.Join(home, "Library", "Application Support", "obsidian", "obsidian.json"), "/elsewhere")
	checks := Checks(context.Background(), config.Config{Vault: config.Vault{Root: root}, Obsidian: config.Obsidian{Check: true}})

	helper := findCheck(t, checks, "nn-vision")
	if helper.OK || len(helper.Fix) == 0 {
		t.Errorf("helper check = %+v", helper)
	}
	if c := findCheck(t, checks, "OCR (vision)"); c.OK || len(c.Fix) != 0 {
		t.Errorf("OCR check = %+v, want no duplicate fix", c)
	}
	if c := findCheck(t, checks, "screen recording"); c.OK || !strings.Contains(c.Detail, "helper") {
		t.Errorf("screen recording check = %+v", c)
	}
	if c := findCheck(t, checks, "clipboard"); !c.OK {
		t.Errorf("clipboard check = %+v", c)
	}
	if c := findCheck(t, checks, "Obsidian"); c.OK || !strings.Contains(c.Detail, "not an Obsidian vault") {
		t.Errorf("Obsidian check = %+v", c)
	}
}

func TestObsidianCheckSymlinkedRoot(t *testing.T) {
	dir := t.TempDir()
	real := filepath.Join(dir, "notes")
	link := filepath.Join(dir, "link")
	if err := os.Mkdir(real, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	cfgPath := filepath.Join(dir, "obsidian.json")
	writeObsidianConfig(t, cfgPath, real+"/")
	if c := obsidianCheck(link, []string{filepath.Join(dir, "missing.json"), cfgPath}); !c.OK {
		t.Errorf("check = %+v", c)
	}
}

func writeObsidianConfig(t *testing.T, path, vault string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	body := fmt.Sprintf(`{"vaults":{"a1b2c3":{"path":%q,"ts":1757980000000,"open":true}}}`, vault)
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}
