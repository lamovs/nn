package search

import "testing"

func TestSwapLayout(t *testing.T) {
	tests := []struct {
		in, want string
		ok       bool
	}{
		{"ghbdtn", "привет", true},
		{"привет", "ghbdtn", true},
		{"вщслук", "docker", true},
		{"docker", "вщслук", true},
		{"Ghbdtn", "Привет", true},
		{"ПРИВЕТ", "GHBDTN", true},
		{"'nj", "это", true},
		{"ёжик", "`;br", true},
		{"`;br", "ёжик", true},
		{"вщслук.сщьзщыу", "docker/compose", true},
		{"docker/compose", "вщслук.сщьзщыу", true},
		{"ghbdtn мир", "привет мир", true},
		{"123", "123", false},
		{"", "", false},
	}
	for _, tt := range tests {
		got, ok := SwapLayout(tt.in)
		if got != tt.want || ok != tt.ok {
			t.Errorf("SwapLayout(%q) = %q, %v; want %q, %v", tt.in, got, ok, tt.want, tt.ok)
		}
	}
}

func TestSwapLayoutRoundTrip(t *testing.T) {
	for _, s := range []string{"qwertyuiop[]asdfghjkl;'zxcvbnm,./`", "QWERTYUIOP{}ASDFGHJKL:\"ZXCVBNM<>?~"} {
		there, ok := SwapLayout(s)
		if !ok {
			t.Fatalf("SwapLayout(%q) not converted", s)
		}
		back, _ := SwapLayout(there)
		if back != s {
			t.Errorf("round trip %q -> %q -> %q", s, there, back)
		}
	}
}
