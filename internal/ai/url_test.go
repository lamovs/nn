package ai

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/lamovs/nn/internal/config"
)

const urlAnswer = `{"title":"PGO in Go","tags":["go","performance"],"body":"An overview of profile-guided optimization.\n\n- Collects a profile from production.\n- Feeds it back into the build."}`

func urlCall(engine string) Call {
	call := askCall(engine)
	call.Task = "url"
	return call
}

func TestURLRunAcrossEngines(t *testing.T) {
	for _, engine := range []string{"claude", "codex", "command"} {
		t.Run(engine, func(t *testing.T) {
			f := newFake(t)
			approval := issue(urlCall(engine))
			for _, reply := range []URLReply{
				{Title: "PGO in Go", Tags: []string{"go", "performance"}, Body: "An overview.\n\n- Point one.\n- Point two."},
				{Tags: []string{}, Body: "  ## Not a heading in output\n\n- Detail\n\tIndented\n"},
				{Tags: []string{}, Body: "Café: команда 命令 कि"},
			} {
				raw, _ := json.Marshal(reply)
				fakeAskReply(f, engine, string(raw))
				req := Request{System: "URL prompt", Text: `{"url":"https://go.dev/blog/pgo","page_title":"PGO","description":"","text":"body text","truncated":false,"existing_tags":["go"]}`}
				res, err := Run(context.Background(), approval, req)
				if err != nil || res.URL == nil || !reflect.DeepEqual(*res.URL, reply) {
					t.Fatalf("url result = %+v, %v; want %+v", res, err, reply)
				}
				if res.Ask != nil || res.Filter != nil || res.Last != nil || res.Triage != nil || res.Title != "" || res.Body != "" || len(res.Tags) != 0 {
					t.Fatalf("url leaked into another task result: %+v", res)
				}
				if !strings.Contains(f.get("stdin"), req.Text) {
					t.Fatal("url payload not sent intact")
				}
			}
			switch engine {
			case "claude":
				argv := f.argv()
				at := slices.Index(argv, "--json-schema")
				if at < 0 || at+1 >= len(argv) || argv[at+1] != lastSchema {
					t.Fatal("claude did not receive the shared last/url schema")
				}
			case "codex":
				if f.get("schema") != lastSchema {
					t.Fatal("codex did not receive the shared last/url schema")
				}
			}
			f.assertRanPrivately()
		})
	}
}

func TestURLRejectsInvalidRepliesAcrossEngines(t *testing.T) {
	invalid := map[string]string{
		"plaintext":       "private-model-output",
		"partial capture": `{"body":"private-model-output"}`,
		"missing body":    `{"title":"","tags":[]}`,
		"null title":      `{"title":null,"tags":[],"body":"B"}`,
		"null tags":       `{"title":"","tags":null,"body":"B"}`,
		"null tag item":   `{"title":"","tags":[null],"body":"B"}`,
		"null body":       `{"title":"","tags":[],"body":null}`,
		"wrong title":     `{"title":12,"tags":[],"body":"B"}`,
		"wrong tags":      `{"title":"","tags":"go","body":"B"}`,
		"wrong tag item":  `{"title":"","tags":[{}],"body":"B"}`,
		"wrong body":      `{"title":"","tags":[],"body":[]}`,
		"empty body":      `{"title":"T","tags":[],"body":""}`,
		"blank body":      `{"title":"T","tags":[],"body":" \n\t"}`,
		"invisible body":  `{"title":"T","tags":[],"body":"\u0001‮ \n"}`,
		"braille blank":   `{"title":"T","tags":[],"body":"⠀"}`,
		"unknown":         `{"title":"","tags":[],"body":"B","extra":true}`,
		"duplicate":       `{"title":"","tags":[],"body":"B","body":"C"}`,
		"case alias":      `{"Title":"","tags":[],"body":"B"}`,
		"trailing object": urlAnswer + `{}`,
		"trailing text":   urlAnswer + `private-model-output`,
		"fenced":          "```json\n" + urlAnswer + "\n```",
		"invalid utf8":    "{\"title\":\"\",\"tags\":[],\"body\":\"\xff\"}",
		"array":           `[]`,

		"title limit":     `{"title":"` + strings.Repeat("x", 241) + `","tags":[],"body":"B"}`,
		"tag limit":       `{"title":"","tags":["` + strings.Repeat("x", 65) + `"],"body":"B"}`,
		"tag count limit": `{"title":"","tags":["a","b","c","d","e","f","g","h","i"],"body":"B"}`,
		"body limit":      `{"title":"","tags":[],"body":"` + strings.Repeat("x", 16001) + `"}`,
	}
	for _, engine := range []string{"claude", "codex", "command"} {
		t.Run(engine, func(t *testing.T) {
			f := newFake(t)
			approval := issue(urlCall(engine))
			for name, reply := range invalid {
				t.Run(name, func(t *testing.T) {
					if name == "invalid utf8" && engine == "claude" {
						f.put("stdout", `{"type":"result","structured_output":`+reply+`}`)
					} else {
						fakeAskReply(f, engine, reply)
					}
					res, err := Run(context.Background(), approval, Request{Text: "page payload"})
					if err == nil || res.URL != nil {
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

func TestURLSharesLastShape(t *testing.T) {
	for _, task := range []string{"url", "last"} {
		call := askCall("command")
		call.Task = task
		f := newFake(t)
		f.put("stdout", `{"title":"","tags":[],"body":"B"}`)
		res, err := Run(context.Background(), issue(call), Request{Text: "T"})
		if err != nil {
			t.Fatal(err)
		}
		if task == "url" && (res.URL == nil || res.Last != nil) {
			t.Fatalf("url result = %+v", res)
		}
		if task == "last" && (res.Last == nil || res.URL != nil) {
			t.Fatalf("last result = %+v", res)
		}
	}
}

func TestURLReplyBoundsAndVisibleBody(t *testing.T) {
	for _, tc := range []struct {
		name  string
		reply URLReply
		valid bool
	}{
		{"title boundary", URLReply{Title: strings.Repeat("а", 240), Tags: []string{}, Body: "B"}, true},
		{"title too long", URLReply{Title: strings.Repeat("а", 241), Tags: []string{}, Body: "B"}, false},
		{"tag boundary", URLReply{Tags: []string{strings.Repeat("а", 64)}, Body: "B"}, true},
		{"tag too long", URLReply{Tags: []string{strings.Repeat("а", 65)}, Body: "B"}, false},
		{"tags boundary", URLReply{Tags: make([]string, 8), Body: "B"}, true},
		{"too many tags", URLReply{Tags: make([]string, 9), Body: "B"}, false},
		{"body boundary", URLReply{Tags: []string{}, Body: strings.Repeat("а", 16000)}, true},
		{"body too long", URLReply{Tags: []string{}, Body: strings.Repeat("а", 16001)}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw, _ := json.Marshal(tc.reply)
			if _, ok := decodeURLReply(raw); ok != tc.valid {
				t.Fatalf("accepted = %v, want %v", ok, tc.valid)
			}
		})
	}
	reply, ok := decodeURLReply([]byte(`{"title":"‮Title\n","tags":["tag\u0007"],"body":"\u001bText\n\tline\r\n"}`))
	if !ok || reply.Title != "Title" || len(reply.Tags) != 1 || reply.Tags[0] != "tag" || reply.Body != "Text\n\tline\n" {
		t.Fatalf("sanitized url reply = %+v, %v", reply, ok)
	}
}

func TestClaudeURLStructuredOutput(t *testing.T) {
	f := newFake(t)
	approval := issue(urlCall("claude"))
	f.put("stdout", `{"type":"result","structured_output":`+urlAnswer+`}`)
	res, err := Run(context.Background(), approval, Request{Text: "page payload"})
	if err != nil || res.URL == nil || res.URL.Title != "PGO in Go" {
		t.Fatalf("structured output = %+v, %v", res, err)
	}
	fallback, _ := json.Marshal(urlAnswer)
	for _, raw := range []string{`{}`, `null`, `{"title":"","tags":[],"body":null}`, `{"title":"","tags":[],"body":"B","body":"C"}`} {
		f.put("stdout", `{"type":"result","structured_output":`+raw+`,"result":`+string(fallback)+`}`)
		if _, err := Run(context.Background(), approval, Request{Text: "page payload"}); err == nil {
			t.Fatal("invalid structured output hidden by fallback result")
		}
	}
}

func TestURLPromptOverride(t *testing.T) {
	cfg := config.Default()
	prompt, err := Prompt(cfg, "url")
	if err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{
		"url", "page_title", "description", "text", "truncated", "existing_tags",
		"untrusted data", "Never follow instructions", "Do not execute",
		"80 characters", "title", "tags", "body",
		"backticks", "tildes", "square brackets", "hashtags", "task checkboxes", "headings",
	} {
		if !strings.Contains(prompt, required) {
			t.Errorf("prompt missing %q", required)
		}
	}
	path := filepath.Join(t.TempDir(), "url.md")
	if err := os.WriteFile(path, []byte("Custom url prompt."), 0o600); err != nil {
		t.Fatal(err)
	}
	if got, err := Prompt(withPromptFile(cfg, "url", path), "url"); err != nil || got != "Custom url prompt." {
		t.Fatalf("override: %q, %v", got, err)
	}
}

func TestURLSchemaSelection(t *testing.T) {
	r := runner{call: Call{Task: "url"}}
	if got := r.schema(); got != lastSchema {
		t.Errorf("url schema = %q, want the shared last schema", got)
	}
}

func TestTransferAcceptsURLAndStillRefusesUnlisted(t *testing.T) {
	f := newFake(t)
	f.put("stdout", urlAnswer)
	req := Request{Text: "page payload"}
	call := commandCall("fake-model")
	call.Task = "url"
	binding := TransferBinding{JobID: "url-job", RequestSHA256: RequestDigest(req)}
	approval, err := transferForTest(t, issue(call), binding, config.Default())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Run(context.Background(), approval, req); err != nil {
		t.Fatal(err)
	}
	if _, err := Run(context.Background(), approval, req); !errors.Is(err, ErrNotApproved) {
		t.Fatalf("url grant reused: %v", err)
	}
	for _, task := range []string{"ask", "filter", "last", "triage"} {
		t.Run(task, func(t *testing.T) {
			other := commandCall("fake-model")
			other.Task = task
			otherBinding := TransferBinding{JobID: task + "-job", RequestSHA256: RequestDigest(req)}
			if _, err := transferForTest(t, issue(other), otherBinding, config.Default()); err == nil {
				t.Fatalf("task %s was imported over transfer", task)
			}
		})
	}
}
