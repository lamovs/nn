package search

import (
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

type Chord struct {
	Mods []string
	Key  string
}

func (c Chord) String() string {
	parts := append(append([]string(nil), c.Mods...), c.Key)
	return strings.Join(parts, "+")
}

const (
	modCtrl uint8 = 1 << iota
	modAlt
	modShift
	modCmd
	modSuper
)

var modNames = [...]string{"ctrl", "alt", "shift", "cmd", "super"}

var modWords = map[string]uint8{
	"ctrl": modCtrl, "control": modCtrl,
	"alt": modAlt, "opt": modAlt, "option": modAlt,
	"shift": modShift,
	"cmd":   modCmd, "command": modCmd,
	"super": modSuper, "win": modSuper, "meta": modSuper,
}

// Mac modifier glyphs.
const (
	glyphCtrl  = 0x2303 // up arrowhead
	glyphAlt   = 0x2325 // option key
	glyphShift = 0x21e7 // upwards white arrow
	glyphCmd   = 0x2318 // place of interest sign
)

func glyphMod(r rune) uint8 {
	switch r {
	case glyphCtrl:
		return modCtrl
	case glyphAlt:
		return modAlt
	case glyphShift:
		return modShift
	case glyphCmd:
		return modCmd
	}
	return 0
}

func glyphKey(r rune) string {
	switch r {
	case 0x21a9, 0x23ce: // leftwards arrow with hook, return symbol
		return "enter"
	case 0x21e5: // rightwards arrow to bar
		return "tab"
	case 0x238b: // broken circle with northwest arrow
		return "esc"
	case 0x232b: // erase to the left
		return "backspace"
	case 0x2326: // erase to the right
		return "delete"
	case 0x2423: // open box
		return "space"
	case 0x2190:
		return "left"
	case 0x2191:
		return "up"
	case 0x2192:
		return "right"
	case 0x2193:
		return "down"
	}
	return ""
}

type keyClass uint8

const (
	keyChar keyClass = iota
	keyArrow
	keyNamed
	keyGlyph
	keyF
)

func (c keyClass) distinctive() bool {
	return c == keyNamed || c == keyGlyph || c == keyF
}

var keyWords = map[string]string{
	"enter": "enter", "return": "enter",
	"tab": "tab",
	"esc": "esc", "escape": "esc",
	"backspace": "backspace",
	"delete":    "delete", "del": "delete",
	"space": "space",
}

var arrowWords = map[string]string{
	"left": "left", "right": "right", "up": "up", "down": "down",
}

const symbolKeys = "`~!@#$%^&*()-_=+[]{}\\|;:'\",.<>/?"

const nbsp = "\xc2\xa0"

var oneChar [utf8.RuneSelf]string

var fKeys [25]string

func init() {
	for b := range utf8.RuneSelf {
		switch {
		case 'a' <= b && b <= 'z', '0' <= b && b <= '9', strings.IndexByte(symbolKeys, byte(b)) >= 0:
			oneChar[b] = string(rune(b))
		case 'A' <= b && b <= 'Z':
			oneChar[b] = string(rune(b + 'a' - 'A'))
		}
	}
	for n := 1; n < len(fKeys); n++ {
		fKeys[n] = "f" + strconv.Itoa(n)
	}
}

type chordKey struct {
	mods uint8
	key  string
}

func (k chordKey) chord() Chord {
	c := Chord{Key: k.key}
	for i, name := range modNames {
		if k.mods&(1<<i) != 0 {
			c.Mods = append(c.Mods, name)
		}
	}
	return c
}

// ParseChord reports whether s as a whole is a shortcut, in canonical form.
func ParseChord(s string) (Chord, bool) {
	k, ok := parseChord(s)
	if !ok {
		return Chord{}, false
	}
	return k.chord(), true
}

func parseChord(s string) (chordKey, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return chordKey{}, false
	}
	k, end, ok := parseChordAt(s, 0, true)
	if !ok || end != len(s) {
		return chordKey{}, false
	}
	return k, true
}

func FindChords(line string) []Chord {
	var out []Chord
	scanChords(line, false, func(k chordKey, _, _ int) bool {
		out = append(out, k.chord())
		return true
	})
	return out
}

func scanChords(s string, lenient bool, fn func(k chordKey, start, end int) bool) {
	for i := 0; i < len(s); {
		b := s[i]
		if b < utf8.RuneSelf {
			if !isLetterByte(b) {
				i++
				continue
			}
			if chordStartOK(s, i) {
				if k, end, ok := parseChordAt(s, i, lenient); ok {
					if !fn(k, i, end) {
						return
					}
					i = end
					continue
				}
			}
			for i < len(s) && isAlnumByte(s[i]) {
				i++
			}
			continue
		}
		r, size := utf8.DecodeRuneInString(s[i:])
		if glyphMod(r) != 0 {
			if k, end, ok := parseChordAt(s, i, lenient); ok {
				if !fn(k, i, end) {
					return
				}
				i = end
				continue
			}
		}
		i += size
	}
}

func chordStartOK(s string, i int) bool {
	if i == 0 {
		return true
	}
	if b := s[i-1]; b < utf8.RuneSelf {
		return !isAlnumByte(b) && b != '_' && b != '-' && b != '+'
	}
	r, _ := utf8.DecodeLastRuneInString(s[:i])
	return !unicode.IsLetter(r) && !unicode.IsDigit(r)
}

type sepKind uint8

const (
	sepNone sepKind = iota
	sepPlus
	sepHyphen
	sepSpace
)

func parseChordAt(s string, p int, lenient bool) (chordKey, int, bool) {
	mod, glyph, pos, ok := readMod(s, p)
	if !ok {
		return chordKey{}, 0, false
	}
	mods := mod
	nMods, nGlyphs := 1, 0
	if glyph {
		nGlyphs++
	}
	prevGlyph, spaced := glyph, false
	for {
		sep, next := readSep(s, pos)
		if sep == sepNone && !prevGlyph && !glyphAt(s, next) {
			return chordKey{}, 0, false
		}
		if sep == sepSpace {
			spaced = true
		}
		if m, g, end, ok := readMod(s, next); ok {
			mods |= m
			nMods++
			if g {
				nGlyphs++
			}
			prevGlyph, pos = g, end
			continue
		}
		key, class, end, ok := readKey(s, next)
		if !ok || !chordEndOK(s, end) {
			return chordKey{}, 0, false
		}
		if spaced && !lenient && nMods < 2 && nGlyphs < nMods && !class.distinctive() {
			return chordKey{}, 0, false
		}
		return chordKey{mods: mods, key: key}, end, true
	}
}

func readMod(s string, p int) (mod uint8, glyph bool, end int, ok bool) {
	if p >= len(s) {
		return 0, false, 0, false
	}
	if s[p] >= utf8.RuneSelf {
		r, size := utf8.DecodeRuneInString(s[p:])
		mod = glyphMod(r)
		return mod, true, p + size, mod != 0
	}
	end = p
	for end < len(s) && isLetterByte(s[end]) {
		end++
	}
	if end-p < len("alt") || end-p > len("command") || !wordEnds(s, end) {
		return 0, false, 0, false
	}
	var buf [8]byte
	mod, ok = modWords[string(lowerInto(buf[:0], s[p:end]))]
	return mod, false, end, ok
}

func readSep(s string, p int) (sepKind, int) {
	e := skipSpaces(s, p)
	if e < len(s) && s[e] == '+' {
		return sepPlus, skipSpaces(s, e+1)
	}
	if e > p {
		return sepSpace, e
	}
	if e < len(s) && s[e] == '-' {
		return sepHyphen, e + 1
	}
	return sepNone, p
}

func skipSpaces(s string, p int) int {
	for p < len(s) {
		switch {
		case s[p] == ' ' || s[p] == '\t':
			p++
		case strings.HasPrefix(s[p:], nbsp):
			p += len(nbsp)
		default:
			return p
		}
	}
	return p
}

func readKey(s string, p int) (key string, class keyClass, end int, ok bool) {
	if p >= len(s) {
		return "", 0, 0, false
	}
	b := s[p]
	if b >= utf8.RuneSelf {
		r, size := utf8.DecodeRuneInString(s[p:])
		if key = glyphKey(r); key != "" {
			return key, keyGlyph, p + size, true
		}
		// toQwerty (layout.go) also maps the Cyrillic letter on the same
		// physical key, so a chord can be typed in either layout.
		if _, ok := toQwerty[r]; ok {
			return string(unicode.ToLower(r)), keyChar, p + size, true
		}
		return "", 0, 0, false
	}
	if !isAlnumByte(b) {
		if key = oneChar[b]; key != "" {
			return key, keyChar, p + 1, true
		}
		return "", 0, 0, false
	}

	end = p
	for end < len(s) && isAlnumByte(s[end]) {
		end++
	}
	word := s[p:end]
	switch {
	case len(word) == 1:
		return oneChar[b], keyChar, end, true
	case len(word) > len("backspace"):
		return "", 0, 0, false
	case (b == 'f' || b == 'F') && isFKeyNumber(word[1:]):
		n := int(word[1] - '0')
		if len(word) == 3 {
			n = n*10 + int(word[2]-'0')
		}
		return fKeys[n], keyF, end, true
	}
	var buf [16]byte
	lower := lowerInto(buf[:0], word)
	if key = keyWords[string(lower)]; key != "" {
		return key, keyNamed, end, true
	}
	if key = arrowWords[string(lower)]; key != "" {
		return key, keyArrow, end, true
	}
	return "", 0, 0, false
}

func isFKeyNumber(digits string) bool {
	switch len(digits) {
	case 1:
		return '1' <= digits[0] && digits[0] <= '9'
	case 2:
		return digits[0] == '1' && '0' <= digits[1] && digits[1] <= '9' ||
			digits[0] == '2' && '0' <= digits[1] && digits[1] <= '4'
	}
	return false
}

func chordEndOK(s string, end int) bool {
	if end >= len(s) {
		return true
	}
	r, size := utf8.DecodeRuneInString(s[end:])
	if unicode.IsLetter(r) || unicode.IsDigit(r) {
		return false
	}
	if r == '-' || r == '+' {
		next, _ := utf8.DecodeRuneInString(s[end+size:])
		return !unicode.IsLetter(next) && !unicode.IsDigit(next)
	}
	return true
}

func wordEnds(s string, end int) bool {
	if end >= len(s) {
		return true
	}
	if b := s[end]; b < utf8.RuneSelf {
		return !isAlnumByte(b)
	}
	r, _ := utf8.DecodeRuneInString(s[end:])
	return !unicode.IsLetter(r) && !unicode.IsDigit(r)
}

func glyphAt(s string, p int) bool {
	if p >= len(s) || s[p] < utf8.RuneSelf {
		return false
	}
	r, _ := utf8.DecodeRuneInString(s[p:])
	return glyphMod(r) != 0 || glyphKey(r) != ""
}

func lowerInto(dst []byte, s string) []byte {
	for i := 0; i < len(s); i++ {
		b := s[i]
		if 'A' <= b && b <= 'Z' {
			b += 'a' - 'A'
		}
		dst = append(dst, b)
	}
	return dst
}

func isLetterByte(b byte) bool {
	return 'a' <= b && b <= 'z' || 'A' <= b && b <= 'Z'
}

func isAlnumByte(b byte) bool {
	return isLetterByte(b) || '0' <= b && b <= '9'
}
