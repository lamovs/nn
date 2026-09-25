package ocr

import (
	"slices"
	"strings"
)

// langPairs maps tesseract codes to the BCP 47 codes Apple Vision uses.
var langPairs = [][2]string{
	{"eng", "en-US"},
	{"rus", "ru-RU"},
	{"ukr", "uk-UA"},
	{"fra", "fr-FR"},
	{"ita", "it-IT"},
	{"deu", "de-DE"},
	{"spa", "es-ES"},
	{"por", "pt-BR"},
	{"chi_sim", "zh-Hans"},
	{"chi_tra", "zh-Hant"},
	{"kor", "ko-KR"},
	{"jpn", "ja-JP"},
	{"tha", "th-TH"},
	{"vie", "vi-VT"},
	{"ara", "ar-SA"},
	{"tur", "tr-TR"},
	{"ind", "id-ID"},
	{"ces", "cs-CZ"},
	{"dan", "da-DK"},
	{"nld", "nl-NL"},
	{"nor", "no-NO"},
	{"msa", "ms-MY"},
	{"pol", "pl-PL"},
	{"ron", "ro-RO"},
	{"swe", "sv-SE"},
}

func splitLangs(values ...string) []string {
	var out []string
	for _, v := range values {
		for part := range strings.FieldsFuncSeq(v, func(r rune) bool {
			return r == '+' || r == ',' || r == ' ' || r == '\t'
		}) {
			out = appendUnique(out, part)
		}
	}
	return out
}

// visionLangs passes unrecognized codes through unchanged.
func visionLangs(langs []string) []string {
	var out []string
	for _, lang := range splitLangs(langs...) {
		out = appendUnique(out, toVisionLang(lang))
	}
	return out
}

// tesseractLangs passes unrecognized codes through unchanged.
func tesseractLangs(langs []string) []string {
	var out []string
	for _, lang := range splitLangs(langs...) {
		out = appendUnique(out, toTesseractLang(lang))
	}
	return out
}

// sameLangs compares sets regardless of order or engine spelling; an empty
// want matches anything (the engine's own defaults).
func sameLangs(have, want []string) bool {
	if len(want) == 0 {
		return true
	}
	a, b := tesseractLangs(have), tesseractLangs(want)
	slices.Sort(a)
	slices.Sort(b)
	return slices.Equal(a, b)
}

func toVisionLang(lang string) string {
	norm := strings.ReplaceAll(lang, "_", "-")
	for _, p := range langPairs {
		switch {
		case strings.EqualFold(lang, p[0]), strings.EqualFold(norm, p[1]):
			return p[1]
		case strings.EqualFold(norm, primarySubtag(p[1])):
			return p[1]
		}
	}
	return lang
}

func toTesseractLang(lang string) string {
	norm := strings.ReplaceAll(lang, "_", "-")
	for _, p := range langPairs {
		switch {
		case lang == p[0]:
			return p[0]
		case strings.EqualFold(norm, p[1]):
			return p[0]
		case !strings.Contains(norm, "-") && len(norm) == 2 && strings.EqualFold(norm, primarySubtag(p[1])):
			return p[0]
		}
	}
	return lang
}

func primarySubtag(tag string) string {
	head, _, _ := strings.Cut(tag, "-")
	return head
}

func appendUnique(list []string, s string) []string {
	for _, have := range list {
		if have == s {
			return list
		}
	}
	return append(list, s)
}
