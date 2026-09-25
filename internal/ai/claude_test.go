package ai

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"
)

const claudeOK = `{"type":"result","subtype":"success","is_error":false,"result":"{\"title\":\"x\"}",` +
	`"structured_output":{"title":"Fix vet shadow","tags":["go","vet"],"body":"Rename the inner err."},` +
	`"usage":{"input_tokens":486,"output_tokens":31,"cache_read_input_tokens":0},` +
	`"total_cost_usd":0.0075,"modelUsage":{"claude-sonnet":{"inputTokens":486}},"fast_mode_state":"off"}` + "\n"

func TestClaudeText(t *testing.T) {
	f := newFake(t)
	f.put("stdout", claudeOK)
	t.Setenv(claudeEffortEnv, "low")

	res, err := Run(context.Background(), issue(claudeCall("sonnet", "max")), Request{System: "You title notes.", Text: "secret note text"})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"-p", "--system-prompt", "You title notes.",
		"--model", "sonnet", "--effort", "max",
		"--json-schema", answerSchema,
		"--output-format", "json",
		"--tools", "", "--strict-mcp-config", "--no-session-persistence", "--setting-sources", "",
	}
	if got := f.argv(); !slices.Equal(got, want) {
		t.Errorf("argv = %q\nwant   %q", got, want)
	}
	if got := f.get("stdin"); got != "secret note text" {
		t.Errorf("stdin = %q", got)
	}
	if got := f.get("effort_env"); got != "set:max" {
		t.Errorf("%s = %q, want the profile's max", claudeEffortEnv, got)
	}
	f.assertRanPrivately()
	if res.Title != "Fix vet shadow" || !slices.Equal(res.Tags, []string{"go", "vet"}) || res.Body != "Rename the inner err." {
		t.Errorf("answer = %+v", res.Answer)
	}
	if res.Usage != (Usage{InputTokens: 486, OutputTokens: 31}) {
		t.Errorf("usage = %+v", res.Usage)
	}
}

func TestClaudeDefaultsLeaveTheEngineAlone(t *testing.T) {
	f := newFake(t)
	f.put("stdout", claudeOK)
	t.Setenv(claudeEffortEnv, "max")

	if _, err := Run(context.Background(), issue(claudeCall("", "")), Request{System: "S", Text: "T"}); err != nil {
		t.Fatal(err)
	}
	argv := f.argv()
	for _, flag := range []string{"--model", "--effort"} {
		if slices.Contains(argv, flag) {
			t.Errorf("argv has %s: %q", flag, argv)
		}
	}
	if got := f.get("effort_env"); got != "unset" {
		t.Errorf("%s = %q, want it unset", claudeEffortEnv, got)
	}
}

func TestClaudeImage(t *testing.T) {
	f := newFake(t)
	f.put("stdout", `{"type":"system","subtype":"init","session_id":"s"}`+"\n"+
		`{"type":"assistant","message":{"content":[{"type":"text","text":"{\"type\":\"result\"}"}]}}`+"\n"+
		`not json at all`+"\n"+
		`{"type":"result","subtype":"success","is_error":false,"result":"",`+
		`"structured_output":{"title":"Red square","tags":["image"],"body":"A red square."},"usage":{"input_tokens":470}}`+"\n")

	res, err := Run(context.Background(), issue(claudeCall("", "high")), Request{System: "Describe.", Text: "OCR: nothing", Image: pngImage})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"-p", "--system-prompt", "Describe.",
		"--effort", "high",
		"--json-schema", answerSchema,
		"--input-format", "stream-json", "--output-format", "stream-json", "--verbose",
		"--tools", "", "--strict-mcp-config", "--no-session-persistence", "--setting-sources", "",
	}
	if got := f.argv(); !slices.Equal(got, want) {
		t.Errorf("argv = %q\nwant   %q", got, want)
	}

	stdin := f.get("stdin")
	if strings.Count(stdin, "\n") != 1 || !strings.HasSuffix(stdin, "\n") {
		t.Fatalf("stdin is not one JSON line: %q", stdin)
	}
	var msg struct {
		Type    string `json:"type"`
		Message struct {
			Role    string `json:"role"`
			Content []struct {
				Type   string `json:"type"`
				Text   string `json:"text"`
				Source struct {
					Type      string `json:"type"`
					MediaType string `json:"media_type"`
					Data      string `json:"data"`
				} `json:"source"`
			} `json:"content"`
		} `json:"message"`
		ParentToolUseID *string `json:"parent_tool_use_id"`
	}
	if err := json.Unmarshal([]byte(stdin), &msg); err != nil {
		t.Fatalf("stdin: %v", err)
	}
	if msg.Type != "user" || msg.Message.Role != "user" || len(msg.Message.Content) != 2 || !strings.Contains(stdin, `"parent_tool_use_id":null`) {
		t.Fatalf("message = %+v", msg)
	}
	image, text := msg.Message.Content[0], msg.Message.Content[1]
	data, _ := base64.StdEncoding.DecodeString(image.Source.Data)
	if image.Type != "image" || image.Source.Type != "base64" || image.Source.MediaType != "image/png" || string(data) != string(pngImage) {
		t.Errorf("image block = %+v", image)
	}
	if text.Type != "text" || text.Text != "OCR: nothing" {
		t.Errorf("text block = %+v", text)
	}
	if res.Title != "Red square" || res.Usage.InputTokens != 470 {
		t.Errorf("result = %+v", res)
	}
	f.assertRanPrivately()
}

func TestClaudeImageWithoutText(t *testing.T) {
	f := newFake(t)
	f.put("stdout", claudeOK)
	if _, err := Run(context.Background(), issue(claudeCall("", "")), Request{System: "Describe.", Image: pngImage}); err != nil {
		t.Fatal(err)
	}
	if stdin := f.get("stdin"); strings.Contains(stdin, `"type":"text"`) {
		t.Errorf("stdin has a text block: %s", stdin)
	}
}

func TestClaudeReadsOnlyTheKeysItNeeds(t *testing.T) {
	f := newFake(t)
	f.put("stdout", `{"type":"result","subtype":"success","is_error":false,"duration_ms":"soon","usage":7,`+
		`"result":"{\"title\":\"From result\",\"tags\":[],\"body\":\"b\"}"}`)
	res, err := Run(context.Background(), issue(claudeCall("", "")), Request{Text: "T"})
	if err != nil || res.Title != "From result" || res.Body != "b" {
		t.Errorf("res = %+v, err = %v", res, err)
	}
}

func TestClaudeNotLoggedIn(t *testing.T) {
	f := newFake(t)
	f.put("stdout", `{"type":"result","subtype":"success","is_error":true,"result":"Not logged in - Please run /login","api_error_status":null}`+"\n")
	f.put("exit", "1")
	_, err := Run(context.Background(), issue(claudeCall("", "")), Request{Text: "T"})
	var auth *AuthError
	if !errors.As(err, &auth) || auth.Engine != "claude" || !strings.Contains(err.Error(), "/login") {
		t.Errorf("err = %v, want a claude AuthError", err)
	}
}

func TestClaudeErrorIsQuotedBriefly(t *testing.T) {
	f := newFake(t)
	long := "API Error: \x1b[31mbad request\x1b[0m\n" + strings.Repeat("private note ", 100)
	reply, _ := json.Marshal(map[string]any{"type": "result", "subtype": "error_during_execution", "is_error": true, "result": long})
	f.put("stdout", string(reply))
	f.put("exit", "1")
	_, err := Run(context.Background(), issue(claudeCall("", "")), Request{Text: "T"})
	if err == nil {
		t.Fatal("want an error")
	}
	msg := err.Error()
	if len(msg) > maxExcerpt+40 || strings.ContainsAny(msg, "\x1b\n") || !strings.Contains(msg, "API Error") {
		t.Errorf("message = %q", msg)
	}
}

func TestClaudeFailures(t *testing.T) {
	for _, tc := range []struct {
		name, stdout, stderr, exit, want string
	}{
		{"exit without a result", "", "Error: unknown option --tools\n", "2", "claude failed (exit status 2): Error: unknown option --tools"},
		{"no result at all", `{"type":"system"}`, "", "0", "claude printed no result"},
		{"answer not the schema", `{"type":"result","subtype":"success","is_error":false,"result":"just prose"}`, "", "0", "not the JSON it was asked for"},
		{"empty answer", `{"type":"result","subtype":"success","is_error":false,"structured_output":{"title":"","tags":[],"body":""}}`, "", "0", "claude gave an empty answer"},
		{"error subtype", `{"type":"result","subtype":"error_max_structured_output_retries","is_error":false}`, "", "0", "claude failed: error_max_structured_output_retries"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFake(t)
			f.put("stdout", tc.stdout)
			f.put("stderr", tc.stderr)
			f.put("exit", tc.exit)
			_, err := Run(context.Background(), issue(claudeCall("", "")), Request{Text: "T"})
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err = %v, want %q", err, tc.want)
			}
			var auth *AuthError
			if errors.As(err, &auth) {
				t.Errorf("err = %v is not a login problem", err)
			}
		})
	}
}
