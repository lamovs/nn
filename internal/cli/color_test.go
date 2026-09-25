package cli

import "testing"

// Priority chain: --color > NO_COLOR/TERM=dumb > output.color > TTY.
func TestColorEnabledPriority(t *testing.T) {
	tests := []struct {
		name           string
		mode, fileMode ColorMode
		isTTY, noColor bool
		term           string
		want           bool
	}{
		{"flag always wins over NO_COLOR", ColorAlways, ColorNever, false, true, "", true},
		{"flag always wins even off a tty", ColorAlways, ColorAuto, false, false, "", true},
		{"flag never wins over file always", ColorNever, ColorAlways, true, false, "", false},
		{"NO_COLOR beats file always", ColorAuto, ColorAlways, true, true, "", false},
		{"TERM=dumb beats file always", ColorAuto, ColorAlways, true, false, "dumb", false},
		{"file always wins with no flag and no NO_COLOR", ColorAuto, ColorAlways, false, false, "", true},
		{"file never wins with no flag and no NO_COLOR", ColorAuto, ColorNever, true, false, "", false},
		{"file auto falls back to tty detection (on)", ColorAuto, ColorAuto, true, false, "", true},
		{"file auto falls back to tty detection (off)", ColorAuto, ColorAuto, false, false, "", false},
		{"flag always wins over TERM=dumb", ColorAlways, ColorAuto, false, false, "dumb", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := colorEnabled(tt.mode, tt.fileMode, tt.isTTY, tt.noColor, tt.term); got != tt.want {
				t.Errorf("colorEnabled(%q, %q, tty=%v, noColor=%v, term=%q) = %v, want %v",
					tt.mode, tt.fileMode, tt.isTTY, tt.noColor, tt.term, got, tt.want)
			}
		})
	}
}
