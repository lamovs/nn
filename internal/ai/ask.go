package ai

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"unicode/utf8"
)

// AskReply: the caller checks that SourceIDs identify excerpts it actually supplied.
type AskReply struct {
	Action     string         `json:"action"`
	Query      string         `json:"query"`
	Paragraphs []AskParagraph `json:"paragraphs"`
	Missing    string         `json:"missing"`
}

type AskParagraph struct {
	Text      string   `json:"text"`
	SourceIDs []string `json:"source_ids"`
}

const askSchema = `{"type":"object","properties":{"action":{"type":"string","enum":["answer","search","insufficient"]},"query":{"type":"string","maxLength":200},"paragraphs":{"type":"array","maxItems":32,"items":{"type":"object","properties":{"text":{"type":"string","minLength":1,"maxLength":4000},"source_ids":{"type":"array","minItems":1,"maxItems":32,"items":{"type":"string","minLength":1,"maxLength":64}}},"required":["text","source_ids"],"additionalProperties":false}},"missing":{"type":"string","maxLength":2000}},"required":["action","query","paragraphs","missing"],"additionalProperties":false}`

func (r *runner) schema() string {
	if r.call.Task == "triage" {
		return triageSchema
	}
	if r.call.Task == "last" {
		return lastSchema
	}
	if r.call.Task == "ask" {
		return askSchema
	}
	if r.call.Task == "filter" {
		return filterSchema
	}
	if r.call.Task == "url" {
		return lastSchema
	}
	if r.call.Task == "digest" {
		return digestSchema
	}
	return answerSchema
}

func (r *runner) decodeResult(data []byte) (Result, bool) {
	if r.call.Task == "triage" {
		reply, ok := decodeTriageReply(data)
		if !ok {
			return Result{}, false
		}
		return Result{Triage: &reply}, true
	}
	if r.call.Task == "last" {
		reply, ok := decodeLastReply(data)
		if !ok {
			return Result{}, false
		}
		return Result{Last: &reply}, true
	}
	if r.call.Task == "filter" {
		reply, ok := decodeFilterReply(data)
		if !ok {
			return Result{}, false
		}
		return Result{Filter: &reply}, true
	}
	if r.call.Task == "ask" {
		reply, ok := decodeAskReply(data)
		if !ok {
			return Result{}, false
		}
		return Result{Ask: &reply}, true
	}
	if r.call.Task == "url" {
		reply, ok := decodeURLReply(data)
		if !ok {
			return Result{}, false
		}
		return Result{URL: &reply}, true
	}
	if r.call.Task == "digest" {
		reply, ok := decodeDigestReply(data)
		if !ok {
			return Result{}, false
		}
		return Result{Digest: &reply}, true
	}
	answer, ok := decodeAnswer(data)
	return Result{Answer: answer}, ok
}

func decodeAskReply(data []byte) (AskReply, bool) {
	fields, ok := strictObject(data, "action", "query", "paragraphs", "missing")
	if !ok {
		return AskReply{}, false
	}
	var reply AskReply
	if json.Unmarshal(fields["action"], &reply.Action) != nil ||
		json.Unmarshal(fields["query"], &reply.Query) != nil ||
		json.Unmarshal(fields["missing"], &reply.Missing) != nil {
		return AskReply{}, false
	}
	var paragraphs []json.RawMessage
	if json.Unmarshal(fields["paragraphs"], &paragraphs) != nil || len(paragraphs) > 32 {
		return AskReply{}, false
	}
	reply.Paragraphs = make([]AskParagraph, 0, len(paragraphs))
	total := utf8.RuneCountInString(reply.Missing)
	if total > 2000 || utf8.RuneCountInString(reply.Query) > 200 {
		return AskReply{}, false
	}
	for _, raw := range paragraphs {
		fields, ok := strictObject(raw, "text", "source_ids")
		var paragraph AskParagraph
		if !ok || json.Unmarshal(fields["text"], &paragraph.Text) != nil ||
			json.Unmarshal(fields["source_ids"], &paragraph.SourceIDs) != nil ||
			strings.TrimSpace(paragraph.Text) == "" || utf8.RuneCountInString(paragraph.Text) > 4000 ||
			len(paragraph.SourceIDs) == 0 || len(paragraph.SourceIDs) > 32 {
			return AskReply{}, false
		}
		seen := make(map[string]bool, len(paragraph.SourceIDs))
		for _, id := range paragraph.SourceIDs {
			if strings.TrimSpace(id) == "" || utf8.RuneCountInString(id) > 64 || seen[id] {
				return AskReply{}, false
			}
			seen[id] = true
		}
		total += utf8.RuneCountInString(paragraph.Text)
		if total > 16000 {
			return AskReply{}, false
		}
		reply.Paragraphs = append(reply.Paragraphs, paragraph)
	}
	switch reply.Action {
	case "answer":
		ok = reply.Query == "" && reply.Missing == "" && len(reply.Paragraphs) > 0
	case "search":
		ok = strings.TrimSpace(reply.Query) != "" && reply.Missing == "" && len(reply.Paragraphs) == 0
	case "insufficient":
		ok = reply.Query == "" && strings.TrimSpace(reply.Missing) != ""
	default:
		ok = false
	}
	return reply, ok
}

// strictObject: exactly the named keys, no nulls, duplicates, unknowns, or trailing JSON.
func strictObject(data []byte, keys ...string) (map[string]json.RawMessage, bool) {
	if !utf8.Valid(data) {
		return nil, false
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	first, err := decoder.Token()
	if err != nil || first != json.Delim('{') {
		return nil, false
	}
	fields := make(map[string]json.RawMessage, len(keys))
	allowed := make(map[string]bool, len(keys))
	for _, key := range keys {
		allowed[key] = true
	}
	for decoder.More() {
		token, err := decoder.Token()
		key, ok := token.(string)
		if err != nil || !ok || !allowed[key] || fields[key] != nil {
			return nil, false
		}
		var raw json.RawMessage
		if decoder.Decode(&raw) != nil || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
			return nil, false
		}
		fields[key] = raw
	}
	if last, err := decoder.Token(); err != nil || last != json.Delim('}') || len(fields) != len(keys) {
		return nil, false
	}
	if _, err := decoder.Token(); err != io.EOF {
		return nil, false
	}
	return fields, true
}

func (r *runner) invalidReply() error {
	return fmt.Errorf("%s's answer is not the JSON it was asked for", r.name())
}
