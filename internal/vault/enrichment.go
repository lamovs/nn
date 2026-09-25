package vault

import (
	"strings"
	"unicode"
)

// Enrichment adds generated metadata without changing a note's path or
// frontmatter; AllowTitle is false when the capture already supplied a title.
type Enrichment struct {
	Body, Title string
	Tags        []string
	AllowTitle  bool
}

// NormalizeKeywords returns safe, case-insensitively unique hashtag values.
func NormalizeKeywords(tags []string) []string {
	var out []string
	for _, tag := range tags {
		tag = strings.TrimLeft(strings.TrimSpace(tag), "#")
		tag = strings.ToLower(strings.Join(strings.Fields(tag), "-"))
		tag = strings.TrimRight(tag, "/")
		valid := validTag(tag)
		for _, r := range tag {
			if !isTagRune(r) {
				valid = false
				break
			}
		}
		if valid {
			out = append(out, tag)
		}
	}
	return dedupeFold(out)
}

// HasTitle reports explicit metadata or a heading, excluding the filename
// fallback used to display a note.
func HasTitle(n *Note) bool {
	return n != nil && (len(n.Aliases) > 0 || strings.TrimSpace(stringField(n.Fields, "title")) != "" || firstH1(n.Body) != "")
}

// AppendEnrichment rereads the note under its append lock, so it sees any
// title or tags added since the caller last loaded it.
func (v *Vault) AppendEnrichment(rel string, e Enrichment) (*Note, error) {
	result, err := v.appendNote(rel, "", nil, func(src string) string {
		return enrichmentAddition(src, e)
	})
	return result.Note, err
}

func enrichmentAddition(src string, e Enrichment) string {
	fields, body, _, _ := ParseFrontmatter(src)
	current := &Note{Fields: fields, Body: body, Aliases: fieldList(fields, false, "aliases", "alias")}
	tags := append(fieldList(fields, true, "tags", "tag"), InlineTags(body)...)
	var blocks []string
	title := strings.Join(strings.Fields(e.Title), " ")
	if strings.ContainsFunc(title, unicode.IsControl) {
		title = ""
	}
	if e.AllowTitle && title != "" && !HasTitle(current) {
		blocks = append(blocks, "# "+title)
	}
	if text := strings.TrimSpace(e.Body); text != "" {
		blocks = append(blocks, text)
		tags = append(tags, InlineTags(text)...)
	}
	addition := strings.Join(blocks, "\n\n")
	if footer := keywordFooter(e.Tags, tags); footer != "" {
		addition = appendMetadata(addition, footer)
	}
	return addition
}

func keywordFooter(tags, existing []string) string {
	seen := map[string]bool{}
	for _, tag := range existing {
		seen[strings.ToLower(strings.TrimPrefix(strings.TrimSpace(tag), "#"))] = true
	}
	var words []string
	for _, tag := range tags {
		canonical := NormalizeKeywords([]string{tag})
		if len(canonical) == 0 || seen[canonical[0]] {
			continue
		}
		seen[canonical[0]] = true
		tag = strings.TrimLeft(strings.TrimSpace(tag), "#")
		tag = strings.TrimRight(strings.Join(strings.Fields(tag), "-"), "/")
		words = append(words, "#"+tag)
	}
	return strings.Join(words, " ")
}

func appendMetadata(src, metadata string) string {
	newline := "\n"
	if strings.Contains(src, "\r\n") {
		newline = "\r\n"
	}
	metadata = strings.ReplaceAll(strings.ReplaceAll(metadata, "\r\n", "\n"), "\n", newline)
	if src == "" {
		return strings.TrimRight(metadata, "\r\n") + newline
	}
	var open *fence
	_, body, _, _ := ParseFrontmatter(src)
	for _, line := range splitLines(body) {
		if open != nil {
			if open.closedBy(line) {
				open = nil
			}
		} else if f, _, ok := openFence(line); ok {
			open = &f
		}
	}
	separator := newline + newline
	if strings.HasSuffix(src, newline+newline) {
		separator = ""
	} else if strings.HasSuffix(src, newline) {
		separator = newline
	}
	if open != nil {
		separator += strings.Repeat(string(open.char), open.n) + newline + newline
	}
	return src + separator + strings.TrimRight(metadata, "\r\n") + newline
}
