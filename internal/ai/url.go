package ai

// URLReply: same JSON shape and limits as LastReply.
type URLReply struct {
	Title string   `json:"title"`
	Tags  []string `json:"tags"`
	Body  string   `json:"body"`
}

func decodeURLReply(data []byte) (URLReply, bool) {
	reply, ok := decodeLastReply(data)
	if !ok {
		return URLReply{}, false
	}
	return URLReply{Title: reply.Title, Tags: reply.Tags, Body: reply.Body}, true
}
