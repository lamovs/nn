package platform

import (
	"bufio"
	"io"
	"os"
	"strconv"
	"strings"
)

const (
	familyArch     = "arch"
	familyDebian   = "debian"
	familyFedora   = "fedora"
	familyOpenSUSE = "opensuse"
	familyAlpine   = "alpine"
	familyNixOS    = "nixos"
)

type distro struct {
	family string // one of the family constants, or "" when unknown
	name   string // PRETTY_NAME, for messages
}

var osReleasePaths = []string{"/etc/os-release", "/usr/lib/os-release"}

func detectDistro() distro {
	for _, path := range osReleasePaths {
		f, err := os.Open(path)
		if err != nil {
			continue
		}
		fields := parseOSRelease(f)
		f.Close()
		return distroFromOSRelease(fields)
	}
	return distro{}
}

func parseOSRelease(r io.Reader) map[string]string {
	fields := map[string]string{}
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		if unquoted, err := strconv.Unquote(value); err == nil {
			value = unquoted
		} else {
			value = strings.Trim(value, `"'`)
		}
		fields[key] = value
	}
	return fields
}

var familyIDs = map[string]string{
	"arch":                familyArch,
	"archarm":             familyArch,
	"manjaro":             familyArch,
	"endeavouros":         familyArch,
	"cachyos":             familyArch,
	"garuda":              familyArch,
	"omarchy":             familyArch,
	"debian":              familyDebian,
	"ubuntu":              familyDebian,
	"linuxmint":           familyDebian,
	"pop":                 familyDebian,
	"elementary":          familyDebian,
	"zorin":               familyDebian,
	"raspbian":            familyDebian,
	"kali":                familyDebian,
	"fedora":              familyFedora,
	"nobara":              familyFedora,
	"opensuse":            familyOpenSUSE,
	"opensuse-tumbleweed": familyOpenSUSE,
	"opensuse-leap":       familyOpenSUSE,
	"opensuse-slowroll":   familyOpenSUSE,
	"suse":                familyOpenSUSE,
	"sles":                familyOpenSUSE,
	"alpine":              familyAlpine,
	"postmarketos":        familyAlpine,
	"nixos":               familyNixOS,
}

func distroFromOSRelease(fields map[string]string) distro {
	d := distro{name: fields["PRETTY_NAME"]}
	if d.name == "" {
		d.name = fields["NAME"]
	}
	ids := append([]string{fields["ID"]}, strings.Fields(fields["ID_LIKE"])...)
	for _, id := range ids {
		if family, ok := familyIDs[strings.ToLower(id)]; ok {
			d.family = family
			break
		}
	}
	return d
}

// packageRenames lists packages whose name differs from Arch's.
var packageRenames = map[string]map[string]string{
	"tesseract": {
		familyDebian:   "tesseract-ocr",
		familyOpenSUSE: "tesseract-ocr",
		familyAlpine:   "tesseract-ocr",
	},
	"spectacle": {
		familyDebian: "kde-spectacle",
		familyNixOS:  "kdePackages.spectacle",
	},
	"imagemagick": {
		familyFedora:   "ImageMagick",
		familyOpenSUSE: "ImageMagick",
	},
}

// packageName is named after the tool's Arch package; empty means nixpkgs needs none.
func (d distro) packageName(tool string) string {
	if lang, ok := strings.CutPrefix(tool, "tesseract-lang:"); ok {
		switch d.family {
		case familyArch:
			return "tesseract-data-" + lang
		case familyDebian:
			return "tesseract-ocr-" + strings.ReplaceAll(strings.ToLower(lang), "_", "-")
		case familyFedora:
			return "tesseract-langpack-" + lang
		case familyOpenSUSE:
			return "tesseract-ocr-traineddata-" + lang
		case familyAlpine:
			return "tesseract-ocr-data-" + lang
		case familyNixOS:
			return ""
		}
		return "tesseract data for " + lang
	}
	if name, ok := packageRenames[tool][d.family]; ok {
		return name
	}
	return tool
}

func (d distro) install(tools ...string) []string {
	var pkgs []string
	for _, tool := range tools {
		if name := d.packageName(tool); name != "" {
			pkgs = appendUnique(pkgs, name)
		}
	}
	if len(pkgs) == 0 {
		return nil
	}
	list := strings.Join(pkgs, " ")
	switch d.family {
	case familyArch:
		return []string{"sudo pacman -S --needed " + list}
	case familyDebian:
		return []string{"sudo apt install " + list}
	case familyFedora:
		return []string{"sudo dnf install " + list}
	case familyOpenSUSE:
		return []string{"sudo zypper install " + list}
	case familyAlpine:
		return []string{"sudo apk add " + list}
	case familyNixOS:
		attrs := make([]string, len(pkgs))
		for i, p := range pkgs {
			attrs[i] = "nixos." + p
		}
		return []string{
			"nix-env -iA " + strings.Join(attrs, " "),
			"or add " + list + " to environment.systemPackages",
		}
	}
	return []string{"install with your package manager: " + strings.Join(pkgs, ", ")}
}

func appendUnique(list []string, s string) []string {
	for _, have := range list {
		if have == s {
			return list
		}
	}
	return append(list, s)
}
