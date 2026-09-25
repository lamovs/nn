package ai

import (
	"bytes"
	"encoding/json"
	"strings"
	"unicode"
	"unicode/utf8"
)

// LastReply: Body has terminal control and format characters removed.
type LastReply struct {
	Title string   `json:"title"`
	Tags  []string `json:"tags"`
	Body  string   `json:"body"`
}

const lastSchema = `{"type":"object","properties":{"title":{"type":"string","maxLength":240},"tags":{"type":"array","maxItems":8,"items":{"type":"string","maxLength":64}},"body":{"type":"string","minLength":1,"maxLength":16000}},"required":["title","tags","body"],"additionalProperties":false}`

func decodeLastReply(data []byte) (LastReply, bool) {
	fields, ok := strictObject(data, "title", "tags", "body")
	if !ok {
		return LastReply{}, false
	}
	var reply LastReply
	var tags []json.RawMessage
	if json.Unmarshal(fields["title"], &reply.Title) != nil ||
		json.Unmarshal(fields["body"], &reply.Body) != nil ||
		json.Unmarshal(fields["tags"], &tags) != nil || len(tags) > 8 ||
		utf8.RuneCountInString(reply.Title) > 240 || utf8.RuneCountInString(reply.Body) > 16000 {
		return LastReply{}, false
	}
	reply.Tags = make([]string, 0, len(tags))
	for _, raw := range tags {
		var tag string
		if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) || json.Unmarshal(raw, &tag) != nil || utf8.RuneCountInString(tag) > 64 {
			return LastReply{}, false
		}
		reply.Tags = append(reply.Tags, lastText(tag, false))
	}
	reply.Title = lastText(reply.Title, false)
	reply.Body = lastText(reply.Body, true)
	if !lastVisibleBody(reply.Body) {
		return LastReply{}, false
	}
	return reply, true
}

// lastVisibleBody requires at least one visible character; marks alone don't count.
func lastVisibleBody(text string) bool {
	return strings.ContainsFunc(text, func(r rune) bool {
		// Braille's blank cell is a symbol, not Unicode whitespace.
		if r == '\u2800' || unicode.Is(unicode.Other_Default_Ignorable_Code_Point, r) {
			return false
		}
		return unicode.IsLetter(r) || unicode.IsNumber(r) || unicode.IsPunct(r) || unicode.IsSymbol(r)
	})
}

func lastText(text string, multiline bool) string {
	return strings.Map(func(r rune) rune {
		if multiline && (r == '\n' || r == '\t') {
			return r
		}
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			return -1
		}
		return r
	}, text)
}
