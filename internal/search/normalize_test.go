package search

import (
	"math/rand/v2"
	"strings"
	"testing"
	"unicode"
	"unicode/utf8"
)

func refNorm(s string, cs bool) string {
	var out []rune
	inSpace := false
	for _, r := range s {
		if unicode.IsSpace(r) {
			if !inSpace {
				out = append(out, ' ')
				inSpace = true
			}
			continue
		}
		if n := len(out); n > 0 {
			if r == combDiaeresis && (out[n-1] == cyrYe || out[n-1] == cyrYeUpper) {
				continue
			}
			if r == combBreve && (out[n-1] == cyrI || out[n-1] == cyrIUpper) {
				out[n-1]++
				continue
			}
		}
		inSpace = false
		switch {
		case !cs:
			r = foldSlow(r)
		case r == cyrYo:
			r = cyrYe
		case r == cyrYoUpper:
			r = cyrYeUpper
		}
		out = append(out, r)
	}
	return string(out)
}

func TestAppendNormMatchesReference(t *testing.T) {
	pool := []string{
		"a", "Z", "q", "7", " ", "  ", "\t", "\n", "-", "_",
		string(rune(0xa0)), string(rune(0x2003)), string(rune(0x85)),
		"е", "Е", "ё", "Ё", "и", "И", "й", "ж", "Я",
		string(rune(combDiaeresis)), string(rune(combBreve)),
		string(rune(0x212a)), string(rune(0x17f)), "ß", string(rune(0x1e9e)),
		string(rune(0x3a3)), string(rune(0x3c2)), string(rune(0x130)),
		string(rune(0x1f600)), "\xff", "\xd0", "\xe2\x8c",
	}
	rng := rand.New(rand.NewPCG(3, 4))
	check := func(s string) {
		t.Helper()
		for _, cs := range []bool{false, true} {
			got, offs := appendNorm(nil, nil, s, cs, true)
			if want := refNorm(s, cs); string(got) != want {
				t.Fatalf("appendNorm(%q, cs=%v) = %q, want %q", s, cs, got, want)
			}
			if plain, _ := appendNorm(nil, nil, s, cs, false); string(plain) != string(got) {
				t.Fatalf("appendNorm(%q) differs with and without offsets", s)
			}
			if len(offs) != len(got) {
				t.Fatalf("appendNorm(%q): %d offsets for %d bytes", s, len(offs), len(got))
			}
			for i, o := range offs {
				if o < 0 || int(o) >= len(s) || (i > 0 && o < offs[i-1]) {
					t.Fatalf("appendNorm(%q): bad offset %d at %d: %v", s, o, i, offs)
				}
				if !utf8.RuneStart(s[o]) && utf8.ValidString(s) {
					t.Fatalf("appendNorm(%q): offset %d is not a rune start", s, o)
				}
			}
		}
	}
	for _, s := range []string{"", "Hello  World", "\tЁлка\n", "е" + string(rune(combDiaeresis)), "И" + string(rune(combBreve)) + string(rune(combBreve))} {
		check(s)
	}
	for range 5000 {
		var sb strings.Builder
		for range rng.IntN(12) {
			sb.WriteString(pool[rng.IntN(len(pool))])
		}
		check(sb.String())
	}
}

func TestSourceRangeWholeString(t *testing.T) {
	s := "  Ёжик" + string(rune(combBreve)) + "  "
	norm, offs := appendNorm(nil, nil, s, false, true)
	if got := sourceRange(offs, len(s), 0, len(norm)); got != [2]int{0, len(s)} {
		t.Errorf("sourceRange = %v, want [0 %d]", got, len(s))
	}
}
