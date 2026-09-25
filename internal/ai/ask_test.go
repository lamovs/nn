package ai

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/lamovs/nn/internal/config"
)

const askAnswer = `{"action":"answer","query":"","paragraphs":[{"text":"Clear the build cache.","source_ids":["S1"]}],"missing":""}`

func askCall(engine string) Call {
	var call Call
	switch engine {
	case "claude":
		call = claudeCall("", "")
	case "codex":
		call = codexCall("", "")
	default:
		call = commandCall("fake-model")
	}
	call.Task = "ask"
	return call
}

func fakeAskReply(f *fake, engine, reply string) {
	switch engine {
	case "claude":
		// Encode as a string so malformed JSON can reach the task decoder.
		encoded, _ := json.Marshal(reply)
		f.put("stdout", `{"type":"result","result":`+string(encoded)+`,"usage":{"input_tokens":12,"output_tokens":4}}`)
	case "codex":
		f.put("out", reply)
	default:
		f.put("stdout", reply)
	}
}

func TestAskRunAcrossEngines(t *testing.T) {
	for _, engine := range []string{"claude", "codex", "command"} {
		t.Run(engine, func(t *testing.T) {
			f := newFake(t)
			approval := issue(askCall(engine))
			for _, reply := range []string{
				`{"action":"search","query":"docker cache","paragraphs":[],"missing":""}`,
				askAnswer,
				`{"action":"insufficient","query":"","paragraphs":[],"missing":"No matching evidence."}`,
			} {
				fakeAskReply(f, engine, reply)
				res, err := Run(context.Background(), approval, Request{System: "Ask prompt", Text: "Question and excerpts"})
				if err != nil {
					t.Fatal(err)
				}
				if res.Ask == nil || res.Body != "" || res.Title != "" || len(res.Tags) != 0 {
					t.Fatalf("result = %+v", res)
				}
				got, _ := json.Marshal(res.Ask)
				if string(got) != reply {
					t.Errorf("reply = %s, want %s", got, reply)
				}
				if engine == "claude" && res.Usage != (Usage{InputTokens: 12, OutputTokens: 4}) {
					t.Errorf("usage = %+v", res.Usage)
				}
			}
			switch engine {
			case "claude":
				argv := f.argv()
				at := slices.Index(argv, "--json-schema")
				if at < 0 || at+1 >= len(argv) || argv[at+1] != askSchema {
					t.Fatalf("wrong ask schema in argv: %q", argv)
				}
			case "codex":
				if f.get("schema") != askSchema {
					t.Fatal("codex did not receive ask schema")
				}
			}
			f.assertRanPrivately()
		})
	}
}

func TestAskRejectsInvalidRepliesAcrossEngines(t *testing.T) {
	invalid := map[string]string{
		"plaintext":               "The answer is known.",
		"capture":                 `{"title":"T","tags":[],"body":"B"}`,
		"missing field":           `{"action":"insufficient","query":"","paragraphs":[]}`,
		"unknown field":           strings.TrimSuffix(askAnswer, "}") + `,"url":"file:///secret"}`,
		"unknown paragraph field": strings.Replace(askAnswer, `"source_ids"`, `"url":"file:///secret","source_ids"`, 1),
		"null field":              strings.Replace(askAnswer, `"query":""`, `"query":null`, 1),
		"null paragraphs":         `{"action":"insufficient","query":"","paragraphs":null,"missing":"No evidence."}`,
		"duplicate field":         strings.Replace(askAnswer, `"action":"answer"`, `"action":"search","action":"answer"`, 1),
		"case alias":              strings.Replace(askAnswer, `"query"`, `"Query"`, 1),
		"trailing junk":           askAnswer + " nonsense",
		"trailing object":         askAnswer + "{}",
		"wrong branch":            strings.Replace(askAnswer, `"query":""`, `"query":"another search"`, 1),
		"uncited answer":          strings.Replace(askAnswer, `["S1"]`, `[]`, 1),
		"null source":             strings.Replace(askAnswer, `["S1"]`, `[null]`, 1),
		"duplicate source":        strings.Replace(askAnswer, `["S1"]`, `["S1","S1"]`, 1),
		"empty answer":            `{"action":"answer","query":"","paragraphs":[],"missing":""}`,
		"blank query":             `{"action":"search","query":"  ","paragraphs":[],"missing":""}`,
		"missing explanation":     `{"action":"insufficient","query":"","paragraphs":[],"missing":""}`,
		"long query":              `{"action":"search","query":"` + strings.Repeat("a", 201) + `","paragraphs":[],"missing":""}`,
		"long paragraph":          strings.Replace(askAnswer, "Clear the build cache.", strings.Repeat("x", 4001), 1),
		"long missing":            `{"action":"insufficient","query":"","paragraphs":[],"missing":"` + strings.Repeat("x", 2001) + `"}`,
	}
	for _, engine := range []string{"claude", "codex", "command"} {
		t.Run(engine, func(t *testing.T) {
			f := newFake(t)
			approval := issue(askCall(engine))
			for name, reply := range invalid {
				t.Run(name, func(t *testing.T) {
					fakeAskReply(f, engine, reply)
					res, err := Run(context.Background(), approval, Request{Text: "question"})
					if err == nil || res.Ask != nil {
						t.Fatalf("invalid reply accepted: result=%+v err=%v", res, err)
					}
				})
			}
		})
	}
}

func TestClaudeAskStructuredOutput(t *testing.T) {
	f := newFake(t)
	f.put("stdout", `{"type":"result","structured_output":`+askAnswer+`}`)
	if result, err := Run(context.Background(), issue(askCall("claude")), Request{Text: "Q"}); err != nil || result.Ask == nil {
		t.Fatalf("structured reply: %+v, %v", result, err)
	}
	encoded, _ := json.Marshal(askAnswer)
	f.put("stdout", `{"type":"result","structured_output":{"action":"answer"},"result":`+string(encoded)+`}`)
	if _, err := Run(context.Background(), issue(askCall("claude")), Request{Text: "Q"}); err == nil {
		t.Fatal("invalid structured reply hidden by fallback result")
	}
}

func TestAskReplyBoundsAndPartialEvidence(t *testing.T) {
	partial := `{"action":"insufficient","query":"","paragraphs":[{"text":"A supported fact.","source_ids":["S1"]}],"missing":"No date is given."}`
	if _, ok := decodeAskReply([]byte(partial)); !ok {
		t.Fatal("partial supported answer rejected")
	}
	query := `{"action":"search","query":"` + strings.Repeat("\u044f", 200) + `","paragraphs":[],"missing":""}`
	if _, ok := decodeAskReply([]byte(query)); !ok {
		t.Fatal("200-rune query rejected")
	}
	paragraph := `{"text":"` + strings.Repeat("x", 4000) + `","source_ids":["S1"]}`
	for _, count := range []int{4, 5, 33} {
		raw := `{"action":"answer","query":"","paragraphs":[` + strings.TrimSuffix(strings.Repeat(paragraph+",", count), ",") + `],"missing":""}`
		if _, ok := decodeAskReply([]byte(raw)); ok != (count == 4) {
			t.Errorf("paragraph count %d: accepted=%v", count, ok)
		}
	}
}

func TestAskPromptOverride(t *testing.T) {
	cfg := config.Default()
	prompt, err := Prompt(cfg, "ask")
	if err != nil || !strings.Contains(prompt, "untrusted data") || !strings.Contains(prompt, "source_ids") {
		t.Fatalf("ask built-in prompt: %q, %v", prompt, err)
	}
	path := filepath.Join(t.TempDir(), "ask.md")
	if err := os.WriteFile(path, []byte("Custom ask prompt."), 0o600); err != nil {
		t.Fatal(err)
	}
	if got, err := Prompt(withPromptFile(cfg, "ask", path), "ask"); err != nil || got != "Custom ask prompt." {
		t.Fatalf("ask prompt override: %q, %v", got, err)
	}
}
