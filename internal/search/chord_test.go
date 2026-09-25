package search

import (
	"slices"
	"strings"
	"testing"
)

var glyphs = strings.NewReplacer(
	"{cmd}", string(rune(0x2318)),
	"{shift}", string(rune(0x21e7)),
	"{alt}", string(rune(0x2325)),
	"{ctrl}", string(rune(0x2303)),
	"{enter}", string(rune(0x21a9)),
	"{return}", string(rune(0x23ce)),
	"{tab}", string(rune(0x21e5)),
	"{esc}", string(rune(0x238b)),
	"{bs}", string(rune(0x232b)),
	"{del}", string(rune(0x2326)),
	"{space}", string(rune(0x2423)),
	"{left}", string(rune(0x2190)),
	"{up}", string(rune(0x2191)),
	"{right}", string(rune(0x2192)),
	"{down}", string(rune(0x2193)),
	"{nbsp}", string(rune(0xa0)),
)

func g(s string) string { return glyphs.Replace(s) }

func TestParseChord(t *testing.T) {
	tests := []struct {
		in   string
		want string // Chord.String(), "" for not a chord
	}{
		{"Cmd+Shift+4", "shift+cmd+4"},
		{"Command-Shift-4", "shift+cmd+4"},
		{"cmd shift 4", "shift+cmd+4"},
		{"Shift+Cmd+4", "shift+cmd+4"},
		{"CMD+SHIFT+4", "shift+cmd+4"},
		{"{cmd}{shift}4", "shift+cmd+4"},
		{"{shift}{cmd}4", "shift+cmd+4"},
		{"{cmd} {shift} 4", "shift+cmd+4"},
		{"Cmd+{shift}+4", "shift+cmd+4"},
		{"{cmd} shift 4", "shift+cmd+4"},
		{"Ctrl + Alt + T", "ctrl+alt+t"},
		{"{ctrl}{alt}T", "ctrl+alt+t"},
		{"control option t", "ctrl+alt+t"},
		{"Ctrl-Alt-Del", "ctrl+alt+delete"},
		{"ctrl+c", "ctrl+c"},
		{"  ctrl+c  ", "ctrl+c"},
		{"ctrl{nbsp}+{nbsp}c", "ctrl+c"},
		{"{cmd} + K", "cmd+k"},
		{"{cmd}K", "cmd+k"},
		{"{cmd} K", "cmd+k"},
		{"cmd k", "cmd+k"},
		{"Shift+Tab", "shift+tab"},
		{"shift+{tab}", "shift+tab"},
		{"Alt+F4", "alt+f4"},
		{"Ctrl+Shift+F12", "ctrl+shift+f12"},
		{"cmd+F24", "cmd+f24"},
		{"cmd+,", "cmd+,"},
		{"Cmd++", "cmd++"},
		{"Ctrl+-", "ctrl+-"},
		{"Opt+Left", "alt+left"},
		{"option+{right}", "alt+right"},
		{"{cmd}{up}", "cmd+up"},
		{"{cmd}{down}", "cmd+down"},
		{"alt+{bs}", "alt+backspace"},
		{"cmd+{del}", "cmd+delete"},
		{"ctrl+{space}", "ctrl+space"},
		{"{ctrl}{esc}", "ctrl+esc"},
		{"Win+E", "super+e"},
		{"Meta+Space", "super+space"},
		{"Super+Enter", "super+enter"},
		{"cmd+return", "cmd+enter"},
		{"{cmd}{enter}", "cmd+enter"},
		{"{cmd}{return}", "cmd+enter"},
		{"control+option+command+shift+k", "ctrl+alt+shift+cmd+k"},
		{"shift+shift+a", "shift+a"},

		// Cyrillic keys; yo and ye stay distinct (different physical keys).
		{"Ctrl+\u0428", "ctrl+\u0448"}, // Ctrl+Sha, uppercase
		{"ctrl+\u0448", "ctrl+\u0448"}, // ctrl+sha, lowercase
		{"CTRL+\u0428", "ctrl+\u0448"},
		{"ctrl-\u0448", "ctrl+\u0448"},
		{"ctrl \u0448", "ctrl+\u0448"},
		{"cmd+\u0439", "cmd+\u0439"},   // a second letter (short i)
		{"ctrl+\u0451", "ctrl+\u0451"}, // yo
		{"ctrl+\u0435", "ctrl+\u0435"}, // ye, a different key from yo
		{"ctrl+\u0448\u0448", ""},      // two letters glued: not a single key

		{"", ""},
		{"cmd", ""},
		{"{cmd}", ""},
		{"Ctrl+", ""},
		{"Alt+Shift", ""},
		{"shift work", ""},
		{"a-b", ""},
		{"C++", ""},
		{"win-win", ""},
		{"super mario", ""},
		{"command line", ""},
		{"meta-analysis", ""},
		{"ctrl+abc", ""},
		{"cmd+F25", ""},
		{"cmd+F0", ""},
		{"cmd+F07", ""},
		{"cmdk", ""},
		{"k+cmd", ""},
		{"4", ""},
		{"cmd+k extra", ""},
		{"cmd--k", ""},
		{"shift-a-b", ""},
		{"ctrl+\u0444oo", ""}, // gluing rejected for Cyrillic keys too
	}
	for _, tt := range tests {
		in := g(tt.in)
		c, ok := ParseChord(in)
		got := ""
		if ok {
			got = c.String()
		}
		if got != tt.want {
			t.Errorf("ParseChord(%q) = %q, %v; want %q", in, got, ok, tt.want)
		}
	}
}

func TestParseChordCanonicalMods(t *testing.T) {
	c, ok := ParseChord("super+cmd+shift+alt+ctrl+x")
	if !ok {
		t.Fatal("not parsed")
	}
	want := []string{"ctrl", "alt", "shift", "cmd", "super"}
	if !slices.Equal(c.Mods, want) || c.Key != "x" {
		t.Errorf("got %+v, want mods %v key x", c, want)
	}
}

func TestFindChords(t *testing.T) {
	tests := []struct {
		in   string
		want []string
	}{
		{"Press Cmd+Shift+4 to capture", []string{"shift+cmd+4"}},
		{"Use Command-Shift-4 or Command-Shift-5", []string{"shift+cmd+4", "shift+cmd+5"}},
		{"Screenshot: cmd shift 4", []string{"shift+cmd+4"}},
		{"Ctrl + Alt + T opens a terminal", []string{"ctrl+alt+t"}},
		{"control alt t", []string{"ctrl+alt+t"}},
		{"{cmd}{shift}4 and {cmd}K", []string{"shift+cmd+4", "cmd+k"}},
		{"{cmd} K", []string{"cmd+k"}},
		{"press shift enter to send", []string{"shift+enter"}},
		{"alt tab", []string{"alt+tab"}},
		{"Ctrl-Alt-Del", []string{"ctrl+alt+delete"}},
		{"(Ctrl+C)", []string{"ctrl+c"}},
		{"Cmd+C/Cmd+V", []string{"cmd+c", "cmd+v"}},
		{"Alt+F4, then Enter", []string{"alt+f4"}},
		{"Нажми Ctrl+C и всё", []string{"ctrl+c"}},
		{"{ctrl}{alt}T|{cmd}{enter}", []string{"ctrl+alt+t", "cmd+enter"}},
		{"Ctrl+\u0428 - shortcut", []string{"ctrl+\u0448"}},
		{"Ctrl-\u0428 switches layout", []string{"ctrl+\u0448"}},

		{"Option A is better", nil},
		{"Option 1: use docker", nil},
		{"cmd k", nil},
		{"shift work schedule", nil},
		{"a-b testing", nil},
		{"C++ and C#", nil},
		{"win-win situation", nil},
		{"non-shift-a", nil},
		{"meta-analysis", nil},
		{"the command line", nil},
		{"shift-a-b", nil},
		{"foo_ctrl+c", nil},
		{"xcmd+k", nil},
		{"cmd+kx", nil},
		{"Ctrl+Shift switches layout", nil},
		{"super mario", nil},
		{"фCtrl+C", nil},
		{"4cmd+k", nil},
		{"ctrl \u0448 layout switch", nil},
		{"Ctrl+\u0428\u0448 volume", nil},
	}
	for _, tt := range tests {
		in := g(tt.in)
		var got []string
		for _, c := range FindChords(in) {
			got = append(got, c.String())
		}
		if !slices.Equal(got, tt.want) {
			t.Errorf("FindChords(%q) = %q, want %q", in, got, tt.want)
		}
	}
}

func TestScanChordsRanges(t *testing.T) {
	line := g("Press Cmd+Shift+4, then {cmd}K.")
	var got []string
	scanChords(line, false, func(_ chordKey, a, b int) bool {
		got = append(got, line[a:b])
		return true
	})
	want := []string{"Cmd+Shift+4", g("{cmd}K")}
	if !slices.Equal(got, want) {
		t.Errorf("chord spans = %q, want %q", got, want)
	}
}
