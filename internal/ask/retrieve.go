package ask

import (
	"context"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/lamovs/nn/internal/search"
)

const maxTerms = 12

var stopWords = wordSet("a an and are as at be been but by can could did do does for from had has have how i if in is it me my of on or our should that the their there these they this to was we were what when where which who why will with would you your " +
	"а без бы был была были было в во вот все где для до его ее если есть еще же за зачем и из или их к как какая какие какой когда кто ли мне мой мы на надо нам нас не него нее нет но ну о об он она они оно от по почему при про с со так такое такие такой там то того тоже только тут ты у уже что чтобы это эта эти этот я")

func wordSet(s string) map[string]bool {
	m := map[string]bool{}
	for _, word := range strings.Fields(s) {
		m[word] = true
	}
	return m
}

func terms(text string) []string {
	text = strings.Map(func(r rune) rune {
		if r == 'ё' || r == 'Ё' {
			return 'е'
		}
		return unicode.ToLower(r)
	}, text)
	parts := strings.FieldsFunc(text, func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsNumber(r) && r != '_' && r != '-' && r != '+' && r != '.' && r != '/' && r != '#'
	})
	seen := map[string]bool{}
	var result []string
	for _, word := range parts {
		word = strings.TrimPrefix(strings.Trim(word, "._-/"), "#")
		if strings.IndexFunc(word, func(r rune) bool { return unicode.IsLetter(r) || unicode.IsNumber(r) }) < 0 || stopWords[word] || seen[word] {
			continue
		}
		seen[word] = true
		result = append(result, word)
		if len(result) == maxTerms {
			break
		}
	}
	return result
}

type candidate struct {
	doc      *search.Doc
	hits     []search.Hit
	coverage map[string]bool
	score    float64
}

func (s *Session) candidates(ctx context.Context, query string) ([]*candidate, error) {
	words := terms(query)
	if len(words) == 0 {
		return nil, nil
	}
	queries := []string{strings.Join(words, " ")}
	if len(words) > 1 {
		queries = append(queries, words...)
	}
	byPath := map[string]*candidate{}
	for _, q := range queries {
		results, err := search.Search(ctx, s.docs, search.Query{Text: q, NoLayoutFallback: s.opts.NoLayoutFallback}, nil, s.now)
		if err != nil {
			return nil, err
		}
		for _, result := range results {
			c := byPath[result.Doc.Path]
			if c == nil {
				c = &candidate{doc: result.Doc, coverage: map[string]bool{}, score: result.Score}
				byPath[result.Doc.Path] = c
			}
			c.score = max(c.score, result.Score)
			for _, word := range strings.Fields(q) {
				c.coverage[word] = true
			}
			room := max(0, 96-len(c.hits)) // best hits arrive first
			c.hits = append(c.hits, result.Hits[:min(room, len(result.Hits))]...)
		}
	}
	result := make([]*candidate, 0, len(byPath))
	for _, c := range byPath {
		result = append(result, c)
	}
	sort.Slice(result, func(i, j int) bool {
		a, b := result[i], result[j]
		if len(a.coverage) != len(b.coverage) {
			return len(a.coverage) > len(b.coverage)
		}
		if a.score != b.score {
			return a.score > b.score
		}
		return a.doc.Path < b.doc.Path
	})
	return result, nil
}

func (s *Session) retrieve(ctx context.Context, query string, opportunities int) (bool, error) {
	found, err := s.candidates(ctx, query)
	if err != nil {
		return false, err
	}
	noteShare := share(s.opts.Limits.Notes-len(s.paths), opportunities)
	startChars := s.chars
	ceiling := startChars + share(s.opts.Limits.Chars-startChars, opportunities)
	addedNotes := 0
	progress := false
	for index, c := range found {
		if err := ctx.Err(); err != nil {
			return false, err
		}
		if !s.paths[c.doc.Path] && addedNotes >= noteShare {
			continue
		}
		remaining := ceiling - s.chars
		if remaining <= 0 {
			break
		}
		slots := max(1, min(len(found)-index, noteShare-addedNotes))
		allowance := min(remaining, max(400, remaining/slots))
		wasKnown := s.paths[c.doc.Path]
		added, err := s.admit(ctx, c, min(s.opts.Limits.Chars, s.chars+allowance))
		if err != nil {
			return false, err
		}
		if added && !wasKnown {
			addedNotes++
		}
		progress = progress || added
	}
	return progress, nil
}

func share(remaining, opportunities int) int {
	result := remaining / opportunities
	if remaining%opportunities != 0 {
		result++
	}
	return result
}

func metadata(doc *search.Doc) source {
	src := source{Path: doc.Path, Title: prefix(doc.Title, 240), Tags: []string{}}
	for _, tag := range doc.Tags[:min(16, len(doc.Tags))] {
		src.Tags = append(src.Tags, prefix(tag, 64))
	}
	return src
}

func (s *Session) hasRoom() bool {
	remaining := s.opts.Limits.Chars - s.chars
	for _, doc := range s.docs {
		if !s.paths[doc.Path] && len(s.paths) >= s.opts.Limits.Notes {
			continue
		}
		cost := 0
		if !s.paths[doc.Path] {
			cost = contentChars(metadata(doc))
		}
		if cost+32 <= remaining {
			return true
		}
	}
	return false
}

func contentChars(src source) int {
	n := utf8.RuneCountInString(src.Path) + utf8.RuneCountInString(src.Title) + utf8.RuneCountInString(src.Excerpt)
	for _, tag := range src.Tags {
		n += utf8.RuneCountInString(tag)
	}
	return n
}

// prefix truncates without copying or splitting the entire input into runes.
func prefix(s string, limit int) string {
	if limit <= 0 {
		return ""
	}
	count := 0
	for offset := range s {
		if count == limit {
			return s[:offset]
		}
		count++
	}
	return s
}
