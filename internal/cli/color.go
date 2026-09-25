package cli

import (
	"fmt"
	"io"
	"os"
	"slices"
	"strings"
)

// ColorMode: from --color, else output.color, else auto-detection.
type ColorMode string

const (
	ColorAuto ColorMode = "auto"

	ColorAlways ColorMode = "always"

	ColorNever ColorMode = "never"
)

var colorModeNames = []ColorMode{ColorAuto, ColorAlways, ColorNever}

func ParseColorMode(s string) (ColorMode, error) {
	m := ColorMode(strings.ToLower(strings.TrimSpace(s)))
	if slices.Contains(colorModeNames, m) {
		return m, nil
	}
	names := make([]string, 0, len(colorModeNames))
	for _, n := range colorModeNames {
		names = append(names, string(n))
	}
	return "", fmt.Errorf("unknown value %q (allowed: %s)", s, strings.Join(names, ", "))
}

func (m ColorMode) String() string { return string(m) }

const noColorVar = "NO_COLOR"

const (
	termVar  = "TERM"
	dumbTerm = "dumb"
)

// PaletteFor: mode > NO_COLOR/TERM=dumb > fileMode > TTY detection.
func PaletteFor(mode, fileMode ColorMode, w io.Writer) Palette {
	return NewPalette(colorEnabled(mode, fileMode, IsTerminal(w), noColorSet(), os.Getenv(termVar)))
}

func noColorSet() bool {
	return os.Getenv(noColorVar) != ""
}

func colorEnabled(mode, fileMode ColorMode, isTTY, noColor bool, term string) bool {
	switch mode {
	case ColorNever:
		return false
	case ColorAlways:
		return true
	}
	if noColor || term == dumbTerm {
		return false
	}
	switch fileMode {
	case ColorAlways:
		return true
	case ColorNever:
		return false
	}
	return isTTY
}

func IsTerminal(v any) bool {
	f, ok := v.(*os.File)
	if !ok {
		return false
	}
	return isTerminal(f)
}

type Palette struct{ on bool }

func NewPalette(on bool) Palette { return Palette{on: on} }

func (p Palette) Enabled() bool { return p.on }

const (
	sgrBold   = "\x1b[1m"
	sgrDim    = "\x1b[2m"
	sgrWeight = "\x1b[22m"

	sgrAccent      = "\x1b[33m"
	sgrDefaultText = "\x1b[39m"
)

func (p Palette) Bold(s string) string { return p.wrap(s, sgrBold, sgrWeight) }

func (p Palette) Dim(s string) string { return p.wrap(s, sgrDim, sgrWeight) }

func (p Palette) Accent(s string) string { return p.wrap(s, sgrAccent, sgrDefaultText) }

func (p Palette) wrap(s, on, off string) string {
	if !p.on || s == "" {
		return s
	}
	return on + s + off
}

type styler func(string) string

func PlainPalette() Palette { return Palette{} }
