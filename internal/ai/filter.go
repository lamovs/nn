package ai

import "encoding/json"

// FilterReply holds the exact transformed text, including its whitespace.
// An empty string is a valid result.
type FilterReply struct {
	Text string `json:"text"`
}

const filterSchema = `{"type":"object","properties":{"text":{"type":"string"}},"required":["text"],"additionalProperties":false}`

func decodeFilterReply(data []byte) (FilterReply, bool) {
	fields, ok := strictObject(data, "text")
	if !ok {
		return FilterReply{}, false
	}
	var reply FilterReply
	if json.Unmarshal(fields["text"], &reply.Text) != nil {
		return FilterReply{}, false
	}
	return reply, true
}
