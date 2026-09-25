package ai

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/lamovs/nn/internal/config"
)

const lastAnswer = `{"title":"Build cache","tags":["docker"],"body":"Removes the build cache. No output was supplied."}`

func lastCall(engine string) Call {
	call := askCall(engine)
	call.Task = "last"
	return call
}

func TestLastRunAcrossEngines(t *testing.T) {
	for _, engine := range []string{"claude", "codex", "command"} {
		t.Run(engine, func(t *testing.T) {
			f := newFake(t)
			approval := issue(lastCall(engine))
			for _, reply := range []LastReply{
				{Title: "Build cache", Tags: []string{"docker"}, Body: "Removes the build cache. No output was supplied."},
				{Tags: []string{}, Body: "  ## Details\n\n- Flag\n\tExample\n"},
				{Tags: []string{}, Body: "Cafe\u0301: \u043a\u043e\u043c\u0430\u043d\u0434\u0430 \u547d\u4ee4 \u0915\u093f"},
				{Tags: []string{}, Body: "\u2705\ufe0f \U0001f44d\U0001f3fd"},
				{Tags: []string{}, Body: "\u2801\u2800\u2803"},
				{Tags: []string{}, Body: "\u2800e\u0301\u2800"},
			} {
				raw, _ := json.Marshal(reply)
				fakeAskReply(f, engine, string(raw))
				req := Request{System: "Last prompt", Text: `{"command":"docker builder prune","output":"","output_provided":false}`}
				res, err := Run(context.Background(), approval, req)
				if err != nil || res.Last == nil || !reflect.DeepEqual(*res.Last, reply) {
					t.Fatalf("last result = %+v, %v; want %+v", res, err, reply)
				}
				if res.Ask != nil || res.Filter != nil || res.Title != "" || res.Body != "" || len(res.Tags) != 0 {
					t.Fatalf("last leaked into another task result: %+v", res)
				}
				if engine == "claude" && res.Usage != (Usage{InputTokens: 12, OutputTokens: 4}) {
					t.Errorf("usage = %+v", res.Usage)
				}
				if !strings.Contains(f.get("stdin"), req.Text) {
					t.Fatal("command and output envelope not sent intact")
				}
			}
			switch engine {
			case "claude":
				argv := f.argv()
				at := slices.Index(argv, "--json-schema")
				if at < 0 || at+1 >= len(argv) || argv[at+1] != lastSchema {
					t.Fatal("claude did not receive last schema")
				}
			case "codex":
				if f.get("schema") != lastSchema {
					t.Fatal("codex did not receive last schema")
				}
			}
			f.assertRanPrivately()
		})
	}
}

func TestLastRejectsInvalidRepliesAcrossEngines(t *testing.T) {
	invalid := map[string]string{
		"plaintext":       "private-model-output",
		"partial capture": `{"body":"private-model-output"}`,
		"filter":          `{"text":"private-model-output"}`,
		"ask":             askAnswer,
		"missing body":    `{"title":"","tags":[]}`,
		"null title":      `{"title":null,"tags":[],"body":"B"}`,
		"null tags":       `{"title":"","tags":null,"body":"B"}`,
		"null tag item":   `{"title":"","tags":[null],"body":"B"}`,
		"null body":       `{"title":"","tags":[],"body":null}`,
		"wrong title":     `{"title":12,"tags":[],"body":"B"}`,
		"wrong tags":      `{"title":"","tags":"shell","body":"B"}`,
		"wrong tag item":  `{"title":"","tags":[{}],"body":"B"}`,
		"wrong body":      `{"title":"","tags":[],"body":[]}`,
		"empty body":      `{"title":"T","tags":[],"body":""}`,
		"blank body":      `{"title":"T","tags":[],"body":" \n\t"}`,
		"invisible body":  `{"title":"T","tags":[],"body":"\u0001\u200b\u202e \n"}`,
		"grapheme joiner": `{"title":"T","tags":[],"body":"\u034f"}`,
		"variation only":  `{"title":"T","tags":[],"body":"\ufe0f"}`,
		"marks only":      `{"title":"T","tags":[],"body":"\u0301\u20dd\u093e"}`,
		"mixed invisible": `{"title":"T","tags":[],"body":"\u034f\ufe0f\u0001\u200b\u202e \n\t"}`,
		"invisible base":  `{"title":"T","tags":[],"body":"\u115f\u3164"}`,
		"braille blank":   `{"title":"T","tags":[],"body":"\u2800"}`,
		"braille blanks":  `{"title":"T","tags":[],"body":"\u2800\u2800\u2800"}`,
		"blank mix":       `{"title":"T","tags":[],"body":"\u2800\u034f\ufe0f\u200b\u0001\u115f \t\n\u2800"}`,
		"unknown":         `{"title":"","tags":[],"body":"B","extra":true}`,
		"duplicate":       `{"title":"","tags":[],"body":"B","body":"C"}`,
		"case alias":      `{"Title":"","tags":[],"body":"B"}`,
		"case duplicate":  `{"title":"","tags":[],"body":"B","Body":"C"}`,
		"trailing object": lastAnswer + `{}`,
		"trailing text":   lastAnswer + `private-model-output`,
		"fenced":          "```json\n" + lastAnswer + "\n```",
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
			approval := issue(lastCall(engine))
			for name, reply := range invalid {
				t.Run(name, func(t *testing.T) {
					if name == "invalid utf8" && engine == "claude" {
						f.put("stdout", `{"type":"result","structured_output":`+reply+`}`)
					} else {
						fakeAskReply(f, engine, reply)
					}
					res, err := Run(context.Background(), approval, Request{Text: "command"})
					if err == nil || res.Last != nil {
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

func TestLastReplyBoundsAndVisibleBody(t *testing.T) {
	for _, tc := range []struct {
		name  string
		reply LastReply
		valid bool
	}{
		{"title boundary", LastReply{Title: strings.Repeat("\u0430", 240), Tags: []string{}, Body: "B"}, true},
		{"title too long", LastReply{Title: strings.Repeat("\u0430", 241), Tags: []string{}, Body: "B"}, false},
		{"tag boundary", LastReply{Tags: []string{strings.Repeat("\u0430", 64)}, Body: "B"}, true},
		{"tag too long", LastReply{Tags: []string{strings.Repeat("\u0430", 65)}, Body: "B"}, false},
		{"tags boundary", LastReply{Tags: make([]string, 8), Body: "B"}, true},
		{"too many tags", LastReply{Tags: make([]string, 9), Body: "B"}, false},
		{"body boundary", LastReply{Tags: []string{}, Body: strings.Repeat("\u0430", 16000)}, true},
		{"body too long", LastReply{Tags: []string{}, Body: strings.Repeat("\u0430", 16001)}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw, _ := json.Marshal(tc.reply)
			if _, ok := decodeLastReply(raw); ok != tc.valid {
				t.Fatalf("accepted = %v, want %v", ok, tc.valid)
			}
		})
	}
	reply, ok := decodeLastReply([]byte(`{"title":"\u202eTitle\n","tags":["tag\u0007"],"body":"\u001bText\u200b\n\tline\r\n"}`))
	if !ok || reply.Title != "Title" || len(reply.Tags) != 1 || reply.Tags[0] != "tag" || reply.Body != "Text\n\tline\n" {
		t.Fatalf("sanitized last reply = %+v, %v", reply, ok)
	}
}

func TestClaudeLastStructuredOutput(t *testing.T) {
	f := newFake(t)
	approval := issue(lastCall("claude"))
	f.put("stdout", `{"type":"result","structured_output":`+lastAnswer+`}`)
	res, err := Run(context.Background(), approval, Request{Text: "command"})
	if err != nil || res.Last == nil || res.Last.Title != "Build cache" {
		t.Fatalf("structured output = %+v, %v", res, err)
	}
	fallback, _ := json.Marshal(lastAnswer)
	for _, raw := range []string{`{}`, `null`, `{"title":"","tags":[],"body":null}`, `{"title":"","tags":[],"body":"B","body":"C"}`} {
		f.put("stdout", `{"type":"result","structured_output":`+raw+`,"result":`+string(fallback)+`}`)
		if _, err := Run(context.Background(), approval, Request{Text: "command"}); err == nil {
			t.Fatal("invalid structured output hidden by fallback result")
		}
	}
	// Invalid UTF-8 in the outer result string must not become replacement text.
	f.put("stdout", "{\"type\":\"result\",\"result\":\"{\\\"title\\\":\\\"\\\",\\\"tags\\\":[],\\\"body\\\":\\\"\xff\\\"}\"}")
	if _, err := Run(context.Background(), approval, Request{Text: "command"}); err == nil {
		t.Fatal("invalid UTF-8 fallback accepted")
	}
}

func TestLastOutputLimitAcrossEngines(t *testing.T) {
	for _, engine := range []string{"claude", "codex", "command"} {
		t.Run(engine, func(t *testing.T) {
			f := newFake(t)
			fakeAskReply(f, engine, `{"title":"","tags":[],"body":"`+strings.Repeat("x", maxOutput)+`"}`)
			res, err := Run(context.Background(), issue(lastCall(engine)), Request{Text: "command"})
			if !errors.Is(err, ErrOutputLimit) || res.Last != nil {
				t.Fatalf("output limit: %+v, %v", res, err)
			}
		})
	}
}

func TestLastProfileAndConsentCompatibility(t *testing.T) {
	for _, engine := range []string{"claude", "codex", "command"} {
		t.Run(engine, func(t *testing.T) {
			f := newFake(t)
			cfg := config.Default()
			cfg.AI.Profiles["explain"] = lastCall(engine).Profile
			task := cfg.AI.Tasks["last"]
			task.Profile = "explain"
			cfg.AI.Tasks["last"] = task
			call, err := Resolve(cfg, "last", Overrides{AI: true, Model: "chosen", Effort: "low"})
			if err != nil || call.Name != "explain" || call.Profile.Model != "chosen" || call.Profile.Effort != "low" || call.Profile.Timeout <= 0 {
				t.Fatalf("resolve: %+v, %v", call, err)
			}
			cfg.AI.Consent[ConsentKey(call.Name, call.Profile)] = ConsentAlways
			decision, approval, err := Approve(&cfg, call, Sends("last"), nil, io.Discard)
			if err != nil || decision != Allowed {
				t.Fatalf("approve: %v, %v", decision, err)
			}
			fakeAskReply(f, engine, lastAnswer)
			res, err := Run(context.Background(), approval, Request{Text: "command"})
			if err != nil || res.Last == nil {
				t.Fatalf("run: %+v, %v", res, err)
			}
			call.Profile.Timeout = time.Millisecond
			f.put("sleep", "1")
			if _, err := Run(context.Background(), issue(call), Request{Text: "command"}); err == nil {
				t.Fatal("last ignored profile timeout")
			}
		})
	}
}

func TestLastPromptOverride(t *testing.T) {
	cfg := config.Default()
	prompt, err := Prompt(cfg, "last")
	if err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{"command", "output_provided", "untrusted data", "Do not execute", "Never\ninvent", "title", "tags", "body"} {
		if !strings.Contains(prompt, required) {
			t.Errorf("prompt missing %q", required)
		}
	}
	path := filepath.Join(t.TempDir(), "last.md")
	if err := os.WriteFile(path, []byte("Custom last prompt."), 0o600); err != nil {
		t.Fatal(err)
	}
	if got, err := Prompt(withPromptFile(cfg, "last", path), "last"); err != nil || got != "Custom last prompt." {
		t.Fatalf("override: %q, %v", got, err)
	}
}
