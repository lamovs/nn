package capture

import (
	"path"
	"sort"
	"strings"
	"unicode"

	"github.com/lamovs/nn/internal/vault"
)

type Similar struct {
	Path, Title string
	Score       float64
}

const shortBodyTokens = 8

// SimilarNotes scores notes by Dice coefficient of transliterated, stemmed word sets, best first, >= threshold.
func SimilarNotes(notes []*vault.Note, title, body string, threshold float64, limit int) []Similar {
	titleWords := wordSet(title)
	bodyWords := wordSet(body)
	namesFromBody := len(titleWords) == 0 && len(bodyWords) > 0 && len(bodyWords) <= shortBodyTokens
	if len(titleWords) == 0 && len(bodyWords) == 0 {
		return nil
	}

	var out []Similar
	for _, n := range notes {
		if n == nil {
			continue
		}
		score := 0.0
		if len(titleWords) > 0 || namesFromBody {
			names := append([]string{n.Title, strings.TrimSuffix(path.Base(n.Path), path.Ext(n.Path))}, n.Aliases...)
			for _, name := range names {
				nameWords := wordSet(name)
				if len(titleWords) > 0 {
					score = max(score, dice(titleWords, nameWords))
				}
				if namesFromBody {
					score = max(score, dice(bodyWords, nameWords))
				}
			}
		}
		if len(bodyWords) > 0 && score < 1 {
			score = max(score, dice(bodyWords, wordSet(n.Body)))
		}
		if score > 0 && score >= threshold {
			out = append(out, Similar{Path: n.Path, Title: n.Title, Score: score})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Score != out[j].Score {
			return out[i].Score > out[j].Score
		}
		return out[i].Path < out[j].Path
	})
	if limit >= 0 && len(out) > limit {
		out = out[:limit]
	}
	return out
}

var stopWords = map[string]bool{
	"the": true, "and": true, "or": true, "of": true, "to": true, "in": true, "on": true,
	"for": true, "with": true, "is": true, "it": true, "an": true, "be": true, "by": true,
	"at": true, "as": true, "from": true, "this": true, "that": true,
	// Russian, transliterated: na po ne chto kak dlya iz no eto ili pri ot do
	"na": true, "po": true, "ne": true, "chto": true, "kak": true, "dlya": true, "iz": true,
	"no": true, "eto": true, "ili": true, "pri": true, "ot": true, "do": true,
}

// endings are stripped, longest first, while at least three letters remain.
var endings = []string{
	"yami", "ami", "ogo", "ego", "omu", "emu", "ymi", "imi", "ing", "yah", "yam", "aya", "uyu",
	"oy", "ey", "iy", "yy", "oe", "ee", "ye", "ie", "ov", "ev", "ah", "am", "om", "em", "yu",
	"ya", "ed", "es", "a", "o", "e", "y", "i", "u", "s",
}

func wordSet(s string) map[string]bool {
	words := strings.FieldsFunc(vault.Transliterate(s), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
	set := make(map[string]bool, len(words))
	for _, w := range words {
		if len(w) < 2 || stopWords[w] {
			continue
		}
		set[stemWord(w)] = true
	}
	return set
}

func stemWord(w string) string {
	for _, end := range endings {
		if len(w)-len(end) >= 3 && strings.HasSuffix(w, end) {
			return w[:len(w)-len(end)]
		}
	}
	return w
}

func dice(a, b map[string]bool) float64 {
	if len(a) == 0 || len(b) == 0 {
		return 0
	}
	if len(a) > len(b) {
		a, b = b, a
	}
	common := 0
	for w := range a {
		if b[w] {
			common++
		}
	}
	return 2 * float64(common) / float64(len(a)+len(b))
}

type TagSuggestion struct {
	Tag, Existing string
	Count         int
}

// Relation strength between two tags; lower is stronger.
const (
	relNone = iota
	relSame
	relPrefix
	relTypo
)

// SimilarTags matches new tags against existing by prefix, transliteration, plural or Levenshtein <= 1.
func SimilarTags(existing map[string]int, tags []string) []TagSuggestion {
	var out []TagSuggestion
	for _, raw := range tags {
		tag := strings.TrimPrefix(strings.TrimSpace(raw), "#")
		t := strings.ToLower(tag)
		if t == "" {
			continue
		}
		own, exists := -1, false
		for name, count := range existing {
			if strings.ToLower(name) == t && count > own {
				own, exists = count, true
			}
		}

		var best TagSuggestion
		bestRel := relNone
		for name, count := range existing {
			e := strings.ToLower(strings.TrimPrefix(name, "#"))
			if e == t || e == "" {
				continue
			}
			rel := tagRelation(t, e)
			if rel == relNone {
				continue
			}
			if exists && !(count > own || count == own && e < t) {
				continue
			}
			better := bestRel == relNone || rel < bestRel ||
				rel == bestRel && (count > best.Count || count == best.Count && name < best.Existing)
			if better {
				best, bestRel = TagSuggestion{Tag: tag, Existing: name, Count: count}, rel
			}
		}
		if bestRel != relNone {
			out = append(out, best)
		}
	}
	return out
}

func tagRelation(a, b string) int {
	if hasCyrillic(a) != hasCyrillic(b) && latinKey(a) == latinKey(b) {
		return relSame
	}
	if pluralPair(a, b) {
		return relSame
	}
	if prefixPair(a, b) {
		return relPrefix
	}
	if min(runeLen(a), runeLen(b)) >= 4 && levenshtein(a, b) <= 1 {
		return relTypo
	}
	return relNone
}

func hasCyrillic(s string) bool {
	for _, r := range s {
		if r >= 0x0400 && r <= 0x04ff {
			return true
		}
	}
	return false
}

// latinSpellings folds common Latin spellings of Russian onto the vault's own transliteration.
var latinSpellings = strings.NewReplacer(
	"shch", "sch", "tch", "ch", "ts", "c", "tz", "c", "kh", "h", "yo", "e", "ye", "e",
	"ja", "ya", "ju", "yu", "jo", "e", "iy", "y", "yy", "y", "x", "ks", "w", "v", "j", "y",
)

func latinKey(s string) string {
	s = latinSpellings.Replace(vault.Transliterate(s))
	return strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' {
			return r
		}
		return -1
	}, s)
}

// cyrillicEndings: a ya o e y i soft-sign short-i u yu.
var cyrillicEndings = "\U00000430\U0000044f\U0000043e\U00000435\U0000044b\U00000438\U0000044c\U00000439\U00000443\U0000044e"

func pluralPair(a, b string) bool {
	if len(a) > len(b) {
		a, b = b, a
	}
	if hasCyrillic(a) && hasCyrillic(b) {
		sa, sb := trimLastRune(a, cyrillicEndings), trimLastRune(b, cyrillicEndings)
		return a != b && sa == sb && runeLen(sa) >= 3
	}
	if runeLen(a) < 3 {
		return false
	}
	return b == a+"s" || b == a+"es" || strings.HasSuffix(a, "y") && b == a[:len(a)-1]+"ies"
}

func trimLastRune(s, set string) string {
	for i, r := range s {
		if i+len(string(r)) == len(s) && strings.ContainsRune(set, r) {
			return s[:i]
		}
	}
	return s
}

// prefixSuffixes may follow a shorter tag for the prefix rule: go~golang.
var prefixSuffixes = map[string]bool{"lang": true, "-lang": true, "js": true, ".js": true, "-js": true}

func prefixPair(a, b string) bool {
	if len(a) > len(b) {
		a, b = b, a
	}
	if len(a) < 2 || a == b || !strings.HasPrefix(b, a) {
		return false
	}
	rest := b[len(a):]
	if prefixSuffixes[rest] {
		return true
	}
	digits := strings.TrimLeft(rest, "-.")
	if digits == "" {
		return false
	}
	for _, r := range digits {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func runeLen(s string) int {
	return len([]rune(s))
}

func levenshtein(a, b string) int {
	ra, rb := []rune(a), []rune(b)
	prev := make([]int, len(rb)+1)
	cur := make([]int, len(rb)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(ra); i++ {
		cur[0] = i
		for j := 1; j <= len(rb); j++ {
			cost := 1
			if ra[i-1] == rb[j-1] {
				cost = 0
			}
			cur[j] = min(prev[j]+1, cur[j-1]+1, prev[j-1]+cost)
		}
		prev, cur = cur, prev
	}
	return prev[len(rb)]
}
