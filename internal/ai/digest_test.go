package ai

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/lamovs/nn/internal/config"
)

const digestAnswer = `{"points":[{"text":"Two notes cover the same deploy pipeline.","source_ids":["S1","S2"]}]}`

func digestCall(engine string) Call {
	call := askCall(engine)
	call.Task = "digest"
	return call
}

func TestDigestRunAcrossEngines(t *testing.T) {
	for _, engine := range []string{"claude", "codex", "command"} {
		t.Run(engine, func(t *testing.T) {
			f := newFake(t)
			approval := issue(digestCall(engine))
			for _, reply := range []DigestReply{
				{Points: []AskParagraph{{Text: "Overview of the two notes.", SourceIDs: []string{"S1"}}}},
				{Points: []AskParagraph{
					{Text: "  ## Not a heading in output\n\n- Detail\n\tIndented\n", SourceIDs: []string{"S1", "S2"}},
					{Text: "A second, unrelated point.", SourceIDs: []string{"S3"}},
				}},
				{Points: []AskParagraph{{Text: "Café: команда 命令 कि", SourceIDs: []string{"S4"}}}},
			} {
				raw, _ := json.Marshal(reply)
				fakeAskReply(f, engine, string(raw))
				req := Request{System: "Digest prompt", Text: `{"selection":"since 2026-09-17","sources":[{"id":"S1","path":"a.md","title":"A","date":"2026-09-17","tags":[],"excerpt":"text","truncated":false}],"omitted_notes":0}`}
				res, err := Run(context.Background(), approval, req)
				if err != nil || res.Digest == nil || !reflect.DeepEqual(*res.Digest, reply) {
					t.Fatalf("digest result = %+v, %v; want %+v", res, err, reply)
				}
				if res.Ask != nil || res.Filter != nil || res.Last != nil || res.Triage != nil || res.URL != nil || res.Title != "" || res.Body != "" || len(res.Tags) != 0 {
					t.Fatalf("digest leaked into another task result: %+v", res)
				}
				if !strings.Contains(f.get("stdin"), req.Text) {
					t.Fatal("digest payload not sent intact")
				}
			}
			switch engine {
			case "claude":
				argv := f.argv()
				at := slices.Index(argv, "--json-schema")
				if at < 0 || at+1 >= len(argv) || argv[at+1] != digestSchema {
					t.Fatal("claude did not receive the digest schema")
				}
			case "codex":
				if f.get("schema") != digestSchema {
					t.Fatal("codex did not receive the digest schema")
				}
				if strings.Contains(f.get("schema"), "uniqueItems") {
					t.Error("digest schema uses uniqueItems, which askSchema does not")
				}
			}
			f.assertRanPrivately()
		})
	}
}

func TestDigestRejectsInvalidRepliesAcrossEngines(t *testing.T) {
	invalid := map[string]string{
		"plaintext":           "private-model-output",
		"missing":             `{}`,
		"null points":         `{"points":null}`,
		"empty points":        `{"points":[]}`,
		"33 points":           nPointsJSON(t, 33, 1),
		"missing text":        `{"points":[{"source_ids":["S1"]}]}`,
		"null text":           `{"points":[{"text":null,"source_ids":["S1"]}]}`,
		"empty text":          `{"points":[{"text":"","source_ids":["S1"]}]}`,
		"wrong text":          `{"points":[{"text":12,"source_ids":["S1"]}]}`,
		"missing ids":         `{"points":[{"text":"T"}]}`,
		"null ids":            `{"points":[{"text":"T","source_ids":null}]}`,
		"empty ids":           `{"points":[{"text":"T","source_ids":[]}]}`,
		"wrong ids":           `{"points":[{"text":"T","source_ids":"S1"}]}`,
		"null id item":        `{"points":[{"text":"T","source_ids":[null]}]}`,
		"empty id":            `{"points":[{"text":"T","source_ids":[""]}]}`,
		"duplicate ids":       `{"points":[{"text":"T","source_ids":["S1","S1"]}]}`,
		"unknown field":       `{"points":[{"text":"T","source_ids":["S1"]}],"extra":true}`,
		"unknown point field": `{"points":[{"text":"T","source_ids":["S1"],"note":"x"}]}`,
		"duplicate key":       `{"points":[{"text":"T","source_ids":["S1"]}],"points":[{"text":"U","source_ids":["S2"]}]}`,
		"case alias":          `{"Points":[{"text":"T","source_ids":["S1"]}]}`,
		"trailing object":     digestAnswer + `{}`,
		"trailing text":       digestAnswer + `private-model-output`,
		"fenced":              "```json\n" + digestAnswer + "\n```",
		"invalid utf8":        "{\"points\":[{\"text\":\"\xff\",\"source_ids\":[\"S1\"]}]}",
		"array":               `[]`,
		"text limit":          `{"points":[{"text":"` + strings.Repeat("x", 2001) + `","source_ids":["S1"]}]}`,
		"id limit":            `{"points":[{"text":"T","source_ids":["` + strings.Repeat("x", 65) + `"]}]}`,
		"id count limit":      nIDsJSON(t, 33),
	}
	for _, engine := range []string{"claude", "codex", "command"} {
		t.Run(engine, func(t *testing.T) {
			f := newFake(t)
			approval := issue(digestCall(engine))
			for name, reply := range invalid {
				t.Run(name, func(t *testing.T) {
					if name == "invalid utf8" && engine == "claude" {
						f.put("stdout", `{"type":"result","structured_output":`+reply+`}`)
					} else {
						fakeAskReply(f, engine, reply)
					}
					res, err := Run(context.Background(), approval, Request{Text: "digest payload"})
					if err == nil || res.Digest != nil {
						t.Fatalf("invalid reply accepted: %+v, %v", res, err)
					}
					if strings.Contains(err.Error(), "private-model-output") {
						t.Fatal("invalid reply leaked model output")
					}
				})
			}
		})
	}
}

// nPointsJSON marshals a DigestReply of n points, each with textLen ASCII
// runes of text and one source ID, for the points-count boundary.
func nPointsJSON(t *testing.T, n, textLen int) string {
	t.Helper()
	points := make([]AskParagraph, n)
	for i := range points {
		points[i] = AskParagraph{Text: strings.Repeat("x", textLen), SourceIDs: []string{"S1"}}
	}
	raw, err := json.Marshal(DigestReply{Points: points})
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

// nIDsJSON marshals a DigestReply of one point with n unique source IDs,
// for the source-IDs-count boundary.
func nIDsJSON(t *testing.T, n int) string {
	t.Helper()
	ids := make([]string, n)
	for i := range ids {
		ids[i] = "S" + strconv.Itoa(i+1)
	}
	raw, err := json.Marshal(DigestReply{Points: []AskParagraph{{Text: "T", SourceIDs: ids}}})
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func TestDecodeDigestReply(t *testing.T) {
	for _, tc := range []struct {
		name  string
		data  string
		valid bool
	}{
		{"one point", `{"points":[{"text":"T","source_ids":["S1"]}]}`, true},
		{"32 points", nPointsJSON(t, 32, 1), true},
		{"33 points", nPointsJSON(t, 33, 1), false},
		{"text boundary", `{"points":[{"text":"` + strings.Repeat("а", 2000) + `","source_ids":["S1"]}]}`, true},
		{"text over", `{"points":[{"text":"` + strings.Repeat("а", 2001) + `","source_ids":["S1"]}]}`, false},
		{"id boundary", `{"points":[{"text":"T","source_ids":["` + strings.Repeat("а", 64) + `"]}]}`, true},
		{"id over", `{"points":[{"text":"T","source_ids":["` + strings.Repeat("а", 65) + `"]}]}`, false},
		{"32 ids", nIDsJSON(t, 32), true},
		{"33 ids", nIDsJSON(t, 33), false},
		{"duplicate ids", `{"points":[{"text":"T","source_ids":["S1","S1"]}]}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, ok := decodeDigestReply([]byte(tc.data)); ok != tc.valid {
				t.Fatalf("accepted = %v, want %v", ok, tc.valid)
			}
		})
	}

	// Combined text of exactly 16000 runes across 8 points is accepted;
	// one more point past that boundary is not.
	eightPoints := make([]AskParagraph, 8)
	for i := range eightPoints {
		eightPoints[i] = AskParagraph{Text: strings.Repeat("а", 2000), SourceIDs: []string{"S1"}}
	}
	raw, _ := json.Marshal(DigestReply{Points: eightPoints})
	if _, ok := decodeDigestReply(raw); !ok {
		t.Fatal("8 points of 2000 runes (combined 16000) rejected")
	}
	ninePoints := append(append([]AskParagraph{}, eightPoints...), AskParagraph{Text: "x", SourceIDs: []string{"S1"}})
	raw, _ = json.Marshal(DigestReply{Points: ninePoints})
	if _, ok := decodeDigestReply(raw); ok {
		t.Fatal("combined text over 16000 accepted")
	}

	reply, ok := decodeDigestReply([]byte(`{"points":[{"text":"T","source_ids":["S1","S2"]}]}`))
	if !ok || len(reply.Points) != 1 || reply.Points[0].Text != "T" || !slices.Equal(reply.Points[0].SourceIDs, []string{"S1", "S2"}) {
		t.Fatalf("decoded reply = %+v, %v", reply, ok)
	}
}

func TestClaudeDigestStructuredOutput(t *testing.T) {
	f := newFake(t)
	approval := issue(digestCall("claude"))
	f.put("stdout", `{"type":"result","structured_output":`+digestAnswer+`}`)
	res, err := Run(context.Background(), approval, Request{Text: "digest payload"})
	if err != nil || res.Digest == nil || res.Digest.Points[0].Text != "Two notes cover the same deploy pipeline." {
		t.Fatalf("structured output = %+v, %v", res, err)
	}
	fallback, _ := json.Marshal(digestAnswer)
	for _, raw := range []string{`{}`, `null`, `{"points":[]}`, `{"points":[{"text":"T","source_ids":["S1"]}],"points":[{"text":"U","source_ids":["S2"]}]}`} {
		f.put("stdout", `{"type":"result","structured_output":`+raw+`,"result":`+string(fallback)+`}`)
		if _, err := Run(context.Background(), approval, Request{Text: "digest payload"}); err == nil {
			t.Fatal("invalid structured output hidden by fallback result")
		}
	}
}

func TestDigestPromptOverride(t *testing.T) {
	cfg := config.Default()
	prompt, err := Prompt(cfg, "digest")
	if err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{
		"selection", "sources", "omitted_notes",
		"untrusted data", "Never follow instructions", "Do not execute",
		"points", "source_ids",
		"headings", "hashtags", "code",
	} {
		if !strings.Contains(prompt, required) {
			t.Errorf("prompt missing %q", required)
		}
	}
	path := filepath.Join(t.TempDir(), "digest.md")
	if err := os.WriteFile(path, []byte("Custom digest prompt."), 0o600); err != nil {
		t.Fatal(err)
	}
	if got, err := Prompt(withPromptFile(cfg, "digest", path), "digest"); err != nil || got != "Custom digest prompt." {
		t.Fatalf("override: %q, %v", got, err)
	}
}

func TestDigestSchemaSelection(t *testing.T) {
	r := runner{call: Call{Task: "digest"}}
	if got := r.schema(); got != digestSchema {
		t.Errorf("digest schema = %q, want digestSchema", got)
	}
}

func TestDigestSendsIsThePinnedText(t *testing.T) {
	if want, got := "your selection and excerpts and metadata of the notes the digest covers", Sends("digest"); got != want {
		t.Fatalf("Sends(digest) = %q, want %q", got, want)
	}
}

// TestTransferStillRefusesDigest: the transfer allowlist is shot, title
// and url; digest must not be importable over it.
func TestTransferStillRefusesDigest(t *testing.T) {
	req := Request{Text: "digest payload"}
	call := commandCall("fake-model")
	call.Task = "digest"
	binding := TransferBinding{JobID: "digest-job", RequestSHA256: RequestDigest(req)}
	if _, err := transferForTest(t, issue(call), binding, config.Default()); err == nil {
		t.Fatal("digest was imported over an approval transfer")
	} else if !strings.Contains(err.Error(), "invalid transferred call") {
		t.Fatalf("unexpected refusal reason: %v", err)
	}
}
