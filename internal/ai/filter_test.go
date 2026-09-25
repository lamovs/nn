package ai

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/lamovs/nn/internal/config"
)

func filterCall(engine string) Call {
	call := askCall(engine)
	call.Task = "filter"
	return call
}

func TestFilterRunAcrossEngines(t *testing.T) {
	for _, engine := range []string{"claude", "codex", "command"} {
		t.Run(engine, func(t *testing.T) {
			f := newFake(t)
			approval := issue(filterCall(engine))
			for _, text := range []string{"Transformed.", "", " \t\n  result\r\n\n", `{"title":"a JSON result","tags":[],"body":"data"}`} {
				raw, err := json.Marshal(FilterReply{Text: text})
				if err != nil {
					t.Fatal(err)
				}
				fakeAskReply(f, engine, string(raw))
				req := Request{System: "Filter prompt", Text: `{"instruction":"Transform","input":"source"}`}
				res, err := Run(context.Background(), approval, req)
				if err != nil || res.Filter == nil {
					t.Fatalf("filter result missing: %+v, %v", res, err)
				}
				if res.Filter.Text != text || res.Ask != nil || res.Body != "" || res.Title != "" || len(res.Tags) != 0 {
					t.Fatalf("result = %+v, filter text = %q, want %q", res, res.Filter.Text, text)
				}
				if engine == "claude" && res.Usage != (Usage{InputTokens: 12, OutputTokens: 4}) {
					t.Errorf("usage = %+v", res.Usage)
				}
				if !strings.Contains(f.get("stdin"), req.Text) {
					t.Fatal("serialized instruction and input not sent intact")
				}
			}
			switch engine {
			case "claude":
				argv := f.argv()
				at := slices.Index(argv, "--json-schema")
				if at < 0 || at+1 >= len(argv) || argv[at+1] != filterSchema {
					t.Fatal("claude did not receive filter schema")
				}
			case "codex":
				if f.get("schema") != filterSchema {
					t.Fatal("codex did not receive filter schema")
				}
			}
			f.assertRanPrivately()
		})
	}
}

func TestFilterRejectsInvalidRepliesAcrossEngines(t *testing.T) {
	const secret = "private-model-output"
	invalid := map[string]string{
		"plaintext":        secret,
		"capture":          `{"title":"` + secret + `","tags":[],"body":"B"}`,
		"ask":              askAnswer,
		"missing":          `{}`,
		"null":             `{"text":null}`,
		"number":           `{"text":42}`,
		"array":            `{"text":[]}`,
		"object":           `{"text":{}}`,
		"unknown":          `{"text":"` + secret + `","extra":true}`,
		"duplicate":        `{"text":"` + secret + `","text":""}`,
		"case alias":       `{"Text":"` + secret + `"}`,
		"case duplicate":   `{"text":"` + secret + `","Text":""}`,
		"trailing object":  `{"text":"` + secret + `"}{}`,
		"trailing text":    `{"text":""}` + secret,
		"fenced":           "```json\n{\"text\":\"" + secret + "\"}\n```",
		"invalid utf8":     "{\"text\":\"\xff\"}",
		"top level string": `"` + secret + `"`,
	}
	for _, engine := range []string{"claude", "codex", "command"} {
		t.Run(engine, func(t *testing.T) {
			f := newFake(t)
			approval := issue(filterCall(engine))
			for name, reply := range invalid {
				t.Run(name, func(t *testing.T) {
					if name == "invalid utf8" && engine == "claude" {
						f.put("stdout", `{"type":"result","structured_output":`+reply+`}`)
					} else {
						fakeAskReply(f, engine, reply)
					}
					res, err := Run(context.Background(), approval, Request{Text: "source"})
					if err == nil || res.Filter != nil {
						t.Fatalf("invalid reply accepted: %+v, %v", res, err)
					}
					if strings.Contains(err.Error(), secret) {
						t.Fatal("invalid reply leaked model output")
					}
				})
			}
		})
	}
}

func TestClaudeFilterStructuredOutput(t *testing.T) {
	f := newFake(t)
	approval := issue(filterCall("claude"))
	f.put("stdout", `{"type":"result","structured_output":{"text":"  exact\n"}}`)
	res, err := Run(context.Background(), approval, Request{Text: "source"})
	if err != nil || res.Filter == nil || res.Filter.Text != "  exact\n" {
		t.Fatalf("structured output = %+v, %v", res, err)
	}
	for _, raw := range []string{`{}`, `null`, `{"text":null}`, `{"text":"x","text":"y"}`} {
		f.put("stdout", `{"type":"result","structured_output":`+raw+`,"result":"{\"text\":\"fallback\"}"}`)
		if _, err := Run(context.Background(), approval, Request{Text: "source"}); err == nil {
			t.Fatal("invalid structured output hidden by fallback result")
		}
	}
}

func TestFilterOutputLimitAcrossEngines(t *testing.T) {
	for _, engine := range []string{"claude", "codex", "command"} {
		t.Run(engine, func(t *testing.T) {
			f := newFake(t)
			fakeAskReply(f, engine, `{"text":"`+strings.Repeat("x", maxOutput)+`"}`)
			res, err := Run(context.Background(), issue(filterCall(engine)), Request{Text: "source"})
			if !errors.Is(err, ErrOutputLimit) || res.Filter != nil {
				t.Fatalf("output limit: %+v, %v", res, err)
			}
		})
	}
}

func TestFilterProfileAndConsentCompatibility(t *testing.T) {
	for _, engine := range []string{"claude", "codex", "command"} {
		t.Run(engine, func(t *testing.T) {
			f := newFake(t)
			cfg := config.Default()
			cfg.AI.Profiles["transform"] = filterCall(engine).Profile
			task := cfg.AI.Tasks["filter"]
			task.Profile = "transform"
			cfg.AI.Tasks["filter"] = task
			call, err := Resolve(cfg, "filter", Overrides{AI: true, Model: "chosen", Effort: "low"})
			if err != nil || call.Name != "transform" || call.Profile.Model != "chosen" || call.Profile.Effort != "low" || call.Profile.Timeout <= 0 {
				t.Fatalf("resolve: %+v, %v", call, err)
			}
			cfg.AI.Consent[ConsentKey(call.Name, call.Profile)] = ConsentAlways
			decision, approval, err := Approve(&cfg, call, Sends("filter"), nil, io.Discard)
			if err != nil || decision != Allowed {
				t.Fatalf("approve: %v, %v", decision, err)
			}
			fakeAskReply(f, engine, `{"text":"approved"}`)
			res, err := Run(context.Background(), approval, Request{Text: "source"})
			if err != nil || res.Filter == nil || res.Filter.Text != "approved" {
				t.Fatalf("run: %+v, %v", res, err)
			}
			call.Profile.Timeout = time.Millisecond
			f.put("sleep", "1")
			if _, err := Run(context.Background(), issue(call), Request{Text: "source"}); err == nil {
				t.Fatal("filter ignored profile timeout")
			}
		})
	}
}

func TestFilterPromptOverride(t *testing.T) {
	cfg := config.Default()
	prompt, err := Prompt(cfg, "filter")
	if err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{"instruction", "input", "untrusted data", "machine envelope", "text", "Do not use tools"} {
		if !strings.Contains(prompt, required) {
			t.Errorf("prompt missing %q", required)
		}
	}
	path := filepath.Join(t.TempDir(), "filter.md")
	if err := os.WriteFile(path, []byte("Custom filter prompt."), 0o600); err != nil {
		t.Fatal(err)
	}
	if got, err := Prompt(withPromptFile(cfg, "filter", path), "filter"); err != nil || got != "Custom filter prompt." {
		t.Fatalf("override: %q, %v", got, err)
	}
}
