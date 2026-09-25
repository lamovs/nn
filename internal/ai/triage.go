package ai

import (
	"bytes"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"unicode"
	"unicode/utf8"
)

// TriageReply: the caller verifies every identifier refers to an eligible
// supplied note.
type TriageReply struct {
	Action    string           `json:"action"`
	Query     string           `json:"query"`
	Proposals []TriageProposal `json:"proposals"`
}

// TriageProposal only proposes additive metadata; empty Title means no change.
type TriageProposal struct {
	NoteID string   `json:"note_id"`
	Title  string   `json:"title"`
	Tags   []string `json:"tags"`
	Links  []string `json:"links"`
	Topic  string   `json:"topic"`
	Reason string   `json:"reason"`
}

const triageSchema = `{"type":"object","properties":{"action":{"type":"string","enum":["search","propose"]},"query":{"type":"string","maxLength":200},"proposals":{"type":"array","maxItems":32,"items":{"type":"object","properties":{"note_id":{"type":"string","minLength":1,"maxLength":64},"title":{"type":"string","maxLength":240},"tags":{"type":"array","maxItems":8,"items":{"type":"string","minLength":1,"maxLength":64}},"links":{"type":"array","maxItems":8,"uniqueItems":true,"items":{"type":"string","minLength":1,"maxLength":64}},"topic":{"type":"string","minLength":1,"maxLength":240},"reason":{"type":"string","minLength":1,"maxLength":2000}},"required":["note_id","title","tags","links","topic","reason"],"additionalProperties":false}}},"required":["action","query","proposals"],"additionalProperties":false}`

// ValidateTriageReply: a typed caller must not bypass adapter sanitization.
func ValidateTriageReply(reply *TriageReply) error {
	if reply != nil {
		data, err := json.Marshal(reply)
		if err == nil {
			canonical, ok := decodeTriageReply(data)
			if ok && reflect.DeepEqual(canonical, *reply) {
				return nil
			}
		}
	}
	return errors.New("ai: invalid triage reply")
}

func decodeTriageReply(data []byte) (TriageReply, bool) {
	fields, ok := strictObject(data, "action", "query", "proposals")
	if !ok {
		return TriageReply{}, false
	}
	var reply TriageReply
	var proposals []json.RawMessage
	if json.Unmarshal(fields["action"], &reply.Action) != nil ||
		json.Unmarshal(fields["proposals"], &proposals) != nil || len(proposals) > 32 {
		return TriageReply{}, false
	}
	var total int
	reply.Query, total, ok = triageText(fields["query"], 200, false, true)
	if !ok {
		return TriageReply{}, false
	}
	reply.Proposals = make([]TriageProposal, 0, len(proposals))
	seen := make(map[string]bool, len(proposals))
	for _, raw := range proposals {
		proposal, size, ok := decodeTriageProposal(raw)
		if !ok || seen[proposal.NoteID] {
			return TriageReply{}, false
		}
		total += size
		if total > 16000 {
			return TriageReply{}, false
		}
		seen[proposal.NoteID] = true
		reply.Proposals = append(reply.Proposals, proposal)
	}
	switch reply.Action {
	case "search":
		ok = reply.Query != "" && len(reply.Proposals) == 0
	case "propose":
		ok = reply.Query == ""
	default:
		ok = false
	}
	return reply, ok
}

func decodeTriageProposal(data []byte) (TriageProposal, int, bool) {
	fields, ok := strictObject(data, "note_id", "title", "tags", "links", "topic", "reason")
	if !ok {
		return TriageProposal{}, 0, false
	}
	var proposal TriageProposal
	proposal.NoteID, ok = triageID(fields["note_id"])
	if !ok {
		return TriageProposal{}, 0, false
	}
	var rawTitle string
	if json.Unmarshal(fields["title"], &rawTitle) != nil || strings.ContainsAny(rawTitle, "\r\n") {
		return TriageProposal{}, 0, false
	}
	var total, size int
	proposal.Title, total, ok = triageText(fields["title"], 240, false, true)
	if !ok {
		return TriageProposal{}, 0, false
	}
	proposal.Topic, size, ok = triageText(fields["topic"], 240, false, false)
	if !ok {
		return TriageProposal{}, 0, false
	}
	total += size
	proposal.Reason, size, ok = triageText(fields["reason"], 2000, true, false)
	if !ok {
		return TriageProposal{}, 0, false
	}
	total += size
	var tags, links []json.RawMessage
	if json.Unmarshal(fields["tags"], &tags) != nil || len(tags) > 8 ||
		json.Unmarshal(fields["links"], &links) != nil || len(links) > 8 {
		return TriageProposal{}, 0, false
	}
	proposal.Tags = make([]string, 0, len(tags))
	for _, raw := range tags {
		tag, size, ok := triageText(raw, 64, false, false)
		if !ok {
			return TriageProposal{}, 0, false
		}
		total += size
		proposal.Tags = append(proposal.Tags, tag)
	}
	proposal.Links = make([]string, 0, len(links))
	seen := make(map[string]bool, len(links))
	for _, raw := range links {
		id, ok := triageID(raw)
		if !ok || seen[id] {
			return TriageProposal{}, 0, false
		}
		seen[id] = true
		proposal.Links = append(proposal.Links, id)
	}
	return proposal, total, true
}

func triageText(raw json.RawMessage, limit int, multiline, empty bool) (string, int, bool) {
	var text string
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) || json.Unmarshal(raw, &text) != nil {
		return "", 0, false
	}
	size := utf8.RuneCountInString(text)
	if size > limit {
		return "", 0, false
	}
	if text == "" && empty {
		return "", 0, true
	}
	text = lastText(text, multiline)
	return text, size, lastVisibleBody(text)
}

// triageID: never sanitize one into a different ID.
func triageID(raw json.RawMessage) (string, bool) {
	var id string
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) || json.Unmarshal(raw, &id) != nil ||
		utf8.RuneCountInString(id) > 64 || !lastVisibleBody(id) ||
		strings.ContainsFunc(id, func(r rune) bool { return unicode.IsControl(r) || unicode.Is(unicode.Cf, r) }) {
		return "", false
	}
	return id, true
}
