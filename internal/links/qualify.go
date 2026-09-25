package links

import (
	"errors"
	"path/filepath"
	"strings"
	"unicode"
)

// QualifiedLink returns a wikilink to target as an explicit "./" or "../"
// path from from's directory, so Obsidian cannot pick a different note by
// basename or alias.
func QualifiedLink(g *Graph, notePaths []string, from, target string) (string, error) {
	if !safeWikiPath(target) {
		return "", errors.New("path cannot be represented safely in wiki syntax")
	}
	count := 0
	for _, p := range notePaths {
		if strings.EqualFold(p, target) {
			count++
		}
	}
	if count != 1 {
		return "", errors.New("case-ambiguous target")
	}
	rel, err := filepath.Rel(filepath.Dir(filepath.FromSlash(from)), filepath.FromSlash(target))
	if err != nil {
		return "", err
	}
	rel = filepath.ToSlash(rel)
	if !strings.HasPrefix(rel, "../") {
		rel = "./" + rel
	}
	if !safeWikiPath(rel) {
		return "", errors.New("unsafe qualified path")
	}
	rendered := "[[" + rel + "]]"
	parsed := Parse(rendered, 1)
	if len(parsed) != 1 || parsed[0].Embed || parsed[0].Target != rel || parsed[0].Heading != "" || parsed[0].Block != "" || parsed[0].Display != "" {
		return "", errors.New("qualified link markup does not preserve its target")
	}
	resolved, ok := g.Resolve(from, parsed[0].Target)
	if !ok || resolved != target {
		return "", errors.New("qualified link does not resolve to the intended target")
	}
	return rendered, nil
}

func safeWikiPath(p string) bool {
	return strings.TrimSpace(p) == p && !strings.ContainsAny(p, "[]#^|`\\\r\n") && strings.IndexFunc(p, func(r rune) bool { return unicode.IsControl(r) || unicode.Is(unicode.Cf, r) }) < 0
}
