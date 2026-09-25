package search

import (
	"unicode"
	"unicode/utf8"
)

const (
	cyrYe      = '\u0435'
	cyrYeUpper = '\u0415'
	cyrYo      = '\u0451'
	cyrYoUpper = '\u0401'
	cyrI       = '\u0438'
	cyrIUpper  = '\u0418'

	combDiaeresis = '\u0308' // ye + this is a decomposed yo
	combBreve     = '\u0306' // i + this is a decomposed short i
)

// foldTable caches foldSlow up to Cyrillic (0x530).
var foldTable [0x530]rune

func init() {
	for r := range foldTable {
		foldTable[r] = foldSlow(rune(r))
	}
}

func foldRune(r rune) rune {
	if r < rune(len(foldTable)) {
		return foldTable[r]
	}
	return foldSlow(r)
}

func foldSlow(r rune) rune {
	lowest := r
	for f := unicode.SimpleFold(r); f != r; f = unicode.SimpleFold(f) {
		lowest = min(lowest, f)
	}
	folded := unicode.ToLower(lowest)
	if folded == cyrYo {
		folded = cyrYe
	}
	return folded
}

var asciiNorm [2][utf8.RuneSelf]byte

func init() {
	for b := range utf8.RuneSelf {
		c := byte(b)
		switch c {
		case '\t', '\n', '\v', '\f', '\r':
			c = ' '
		}
		asciiNorm[0][b] = c
		if 'A' <= c && c <= 'Z' {
			c += 'a' - 'A'
		}
		asciiNorm[1][b] = c
	}
}

// appendNorm folds, composes decomposed yo/short i, collapses whitespace.
func appendNorm(dst []byte, offs []int32, s string, cs, withOffs bool) ([]byte, []int32) {
	table := &asciiNorm[1]
	if cs {
		table = &asciiNorm[0]
	}
	inSpace := false
	for i := 0; i < len(s); {
		if c := s[i]; c < utf8.RuneSelf {
			c = table[c]
			if c == ' ' {
				if inSpace {
					i++
					continue
				}
				inSpace = true
			} else {
				inSpace = false
			}
			dst = append(dst, c)
			if withOffs {
				offs = append(offs, int32(i))
			}
			i++
			continue
		}

		r, size := decodeRune(s, i)
		if isSpaceRune(r) {
			if !inSpace {
				dst = append(dst, ' ')
				if withOffs {
					offs = append(offs, int32(i))
				}
				inSpace = true
			}
			i += size
			continue
		}
		// A combining mark after ye or i (folded) completes a decomposed
		// yo or short i; both base letters encode as 0xd0 plus one byte.
		if n := len(dst); n >= 2 && dst[n-2] == 0xd0 {
			last := dst[n-1]
			if r == combDiaeresis && (last == byte(0x80|cyrYe&0x3f) || last == byte(0x80|cyrYeUpper&0x3f)) {
				i += size
				continue
			}
			if r == combBreve && (last == byte(0x80|cyrI&0x3f) || last == byte(0x80|cyrIUpper&0x3f)) {
				dst[n-1]++
				i += size
				continue
			}
		}

		if cs {
			switch r {
			case cyrYo:
				r = cyrYe
			case cyrYoUpper:
				r = cyrYeUpper
			}
		} else {
			r = foldRune(r)
		}
		inSpace = false
		at := len(dst)
		switch {
		case r < utf8.RuneSelf:
			dst = append(dst, byte(r))
		case r < 0x800:
			dst = append(dst, 0xc0|byte(r>>6), 0x80|byte(r)&0x3f)
		default:
			dst = utf8.AppendRune(dst, r)
		}
		if withOffs {
			for range len(dst) - at {
				offs = append(offs, int32(i))
			}
		}
		i += size
	}
	return dst, offs
}

func decodeRune(s string, i int) (rune, int) {
	if b := s[i]; b >= 0xc2 && b < 0xe0 && i+1 < len(s) && s[i+1]&0xc0 == 0x80 {
		return rune(b&0x1f)<<6 | rune(s[i+1]&0x3f), 2
	}
	return utf8.DecodeRuneInString(s[i:])
}

func isSpaceRune(r rune) bool {
	return r == 0x85 || r == 0xa0 || r >= 0x1680 && unicode.IsSpace(r)
}

func normString(s string, cs bool) []byte {
	dst, _ := appendNorm(nil, nil, s, cs, false)
	return dst
}

func sourceRange(offs []int32, srcLen, a, b int) [2]int {
	end := srcLen
	if b < len(offs) {
		end = int(offs[b])
	}
	return [2]int{int(offs[a]), end}
}

func isWordRune(r rune) bool {
	if r < utf8.RuneSelf {
		return 'a' <= r && r <= 'z' || 'A' <= r && r <= 'Z' || '0' <= r && r <= '9' || r == '_'
	}
	return unicode.IsLetter(r) || unicode.IsDigit(r)
}

func atWordStart(b []byte, i int) bool {
	if i == 0 {
		return true
	}
	r, _ := utf8.DecodeLastRune(b[:i])
	return !isWordRune(r)
}

func atWordEnd(b []byte, i int) bool {
	if i >= len(b) {
		return true
	}
	r, _ := utf8.DecodeRune(b[i:])
	return !isWordRune(r)
}
