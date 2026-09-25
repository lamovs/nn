package ai

import (
	"encoding/json"
	"unicode/utf8"
)

// DigestReply: the caller checks that every ID identifies a note it
// actually supplied.
type DigestReply struct {
	Points []AskParagraph `json:"points"` // text + source_ids, same shape as ask
}

const digestSchema = `{"type":"object","properties":{"points":{"type":"array","minItems":1,"maxItems":32,"items":{"type":"object","properties":{"text":{"type":"string","minLength":1,"maxLength":2000},"source_ids":{"type":"array","minItems":1,"maxItems":32,"items":{"type":"string","minLength":1,"maxLength":64}}},"required":["text","source_ids"],"additionalProperties":false}}},"required":["points"],"additionalProperties":false}`

// decodeDigestReply: visibility after ask.Literal, and whether IDs belong
// to the sent set, is checked by Run in internal/digest, not here.
func decodeDigestReply(data []byte) (DigestReply, bool) {
	fields, ok := strictObject(data, "points")
	if !ok {
		return DigestReply{}, false
	}
	var raw []json.RawMessage
	if json.Unmarshal(fields["points"], &raw) != nil || len(raw) == 0 || len(raw) > 32 {
		return DigestReply{}, false
	}
	reply := DigestReply{Points: make([]AskParagraph, 0, len(raw))}
	total := 0
	for _, item := range raw {
		fields, ok := strictObject(item, "text", "source_ids")
		var point AskParagraph
		if !ok || json.Unmarshal(fields["text"], &point.Text) != nil ||
			json.Unmarshal(fields["source_ids"], &point.SourceIDs) != nil ||
			utf8.RuneCountInString(point.Text) == 0 || utf8.RuneCountInString(point.Text) > 2000 ||
			len(point.SourceIDs) == 0 || len(point.SourceIDs) > 32 {
			return DigestReply{}, false
		}
		seen := make(map[string]bool, len(point.SourceIDs))
		for _, id := range point.SourceIDs {
			if n := utf8.RuneCountInString(id); n == 0 || n > 64 || seen[id] {
				return DigestReply{}, false
			}
			seen[id] = true
		}
		total += utf8.RuneCountInString(point.Text)
		if total > 16000 {
			return DigestReply{}, false
		}
		reply.Points = append(reply.Points, point)
	}
	return reply, true
}
