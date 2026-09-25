package search

import (
	"strings"
	"unicode"
)

// QWERTY keys, unshifted and shifted, in the same order as ycukenKeys.
const (
	qwertyKeys  = "qwertyuiop[]asdfghjkl;'zxcvbnm,./`"
	qwertyShift = "QWERTYUIOP{}ASDFGHJKL:\"ZXCVBNM<>?~"
)

// ycukenKeys are the same physical keys in the Russian (PC) layout.
var ycukenKeys = []rune{
	0x439, 0x446, 0x443, 0x43a, 0x435, 0x43d, 0x433, 0x448, 0x449, 0x437, 0x445, 0x44a,
	0x444, 0x44b, 0x432, 0x430, 0x43f, 0x440, 0x43e, 0x43b, 0x434, 0x436, 0x44d,
	0x44f, 0x447, 0x441, 0x43c, 0x438, 0x442, 0x44c, 0x431, 0x44e, '.',
	0x451,
}

var toYcuken, toQwerty map[rune]rune

func init() {
	toYcuken = make(map[rune]rune, 2*len(ycukenKeys))
	toQwerty = make(map[rune]rune, 2*len(ycukenKeys))
	for i, cyr := range ycukenKeys {
		shifted := unicode.ToUpper(cyr)
		if cyr == '.' {
			shifted = ','
		}
		for _, pair := range [2][2]rune{{rune(qwertyKeys[i]), cyr}, {rune(qwertyShift[i]), shifted}} {
			toYcuken[pair[0]] = pair[1]
			toQwerty[pair[1]] = pair[0]
		}
	}
}

// SwapLayout converts toward whichever script has more letters in s.
func SwapLayout(s string) (string, bool) {
	var latin, cyrillic int
	for _, r := range s {
		switch {
		case r < 0x80 && unicode.IsLetter(r):
			latin++
		case unicode.Is(unicode.Cyrillic, r):
			if _, ok := toQwerty[r]; ok {
				cyrillic++
			}
		}
	}
	if latin == 0 && cyrillic == 0 {
		return s, false
	}
	table := toYcuken
	if cyrillic > latin {
		table = toQwerty
	}
	changed := false
	out := strings.Map(func(r rune) rune {
		if to, ok := table[r]; ok {
			changed = true
			return to
		}
		return r
	}, s)
	return out, changed
}
