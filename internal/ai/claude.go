package ai

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"reflect"
	"strings"
	"unicode/utf8"
)

const claudeEffortEnv = "CLAUDE_CODE_EFFORT_LEVEL"

// claude runs claude -p with no tools, no MCP servers, no settings files,
// no saved session. Text goes on stdin. --bare isolates more but skips the
// keychain, breaking subscription login.
func (r *runner) claude(ctx context.Context) (Result, error) {
	p := r.call.Profile
	args := []string{"-p", "--system-prompt", r.req.System}
	if p.Model != "" {
		args = append(args, "--model", p.Model)
	}
	if p.Effort != "" {
		args = append(args, "--effort", engineEffort(engineClaude, p.Effort))
	}
	args = append(args, "--json-schema", r.schema())
	stdin := []byte(r.req.Text)
	stdoutEcho := 0
	if r.image != nil {
		args = append(args, "--input-format", "stream-json", "--output-format", "stream-json", "--verbose")
		line, err := claudeMessage(r.req.Text, r.req.Image, r.image.mediaType)
		if err != nil {
			return Result{}, err
		}
		stdin = line
		stdoutEcho = len(line) + 4096
	} else {
		args = append(args, "--output-format", "json")
	}
	args = append(args, "--tools", "", "--strict-mcp-config", "--no-session-persistence", "--setting-sources", "")

	stdout, stderr, err := run(ctx, process{
		name:       engineClaude,
		path:       r.path,
		args:       args,
		stdin:      stdin,
		env:        claudeEnv(os.Environ(), engineEffort(engineClaude, p.Effort)),
		dir:        r.work,
		stdoutEcho: stdoutEcho,
	}, p.Timeout)
	var exit *exec.ExitError
	if err != nil && !errors.As(err, &exit) {
		return Result{}, err
	}
	if r.image != nil {
		stdout = stripClaudeInputEcho(stdout, stdin)
		if len(stdout) > maxOutput {
			return Result{}, fmt.Errorf("claude: %w", ErrOutputLimit)
		}
	}

	if (r.call.Task == "filter" || r.call.Task == "last" || r.call.Task == "triage" || r.call.Task == "url" || r.call.Task == "digest") && !utf8.Valid(stdout) {
		return Result{}, r.invalidReply()
	}
	reply, found := claudeResult(stdout)
	switch {
	case found && (reply.IsError || reply.Subtype != "" && reply.Subtype != "success"):
		if claudeAuthFailed(reply.Result) {
			return Result{}, &AuthError{Engine: engineClaude}
		}
		msg := reply.Result
		if msg == "" {
			msg = reply.Subtype
		}
		return Result{}, fmt.Errorf("claude failed: %s", excerpt(msg))
	case err != nil:
		return Result{}, failure(engineClaude, err, lastLine(stderr))
	case !found:
		return Result{}, errors.New("claude printed no result")
	}
	result, ok := r.decodeResult(reply.StructuredOutput)
	if !ok && (r.call.Task != "ask" && r.call.Task != "filter" && r.call.Task != "last" && r.call.Task != "triage" && r.call.Task != "url" && r.call.Task != "digest" || len(bytes.TrimSpace(reply.StructuredOutput)) == 0) {
		// The same JSON is in result, should structured_output be missing.
		result, ok = r.decodeResult([]byte(reply.Result))
	}
	if !ok {
		return Result{}, errors.New("claude's answer is not the JSON it was asked for")
	}
	result.Usage = reply.Usage
	return result, nil
}

// stripClaudeInputEcho discounts only one exact copy of the submitted message.
func stripClaudeInputEcho(stdout, stdin []byte) []byte {
	var input struct {
		Message any `json:"message"`
	}
	if json.Unmarshal(stdin, &input) != nil {
		return stdout
	}
	var out bytes.Buffer
	removed := false
	for line := range bytes.SplitAfterSeq(stdout, []byte("\n")) {
		var event struct {
			Type    string `json:"type"`
			Message any    `json:"message"`
		}
		if !removed && json.Unmarshal(line, &event) == nil && event.Type == "user" && reflect.DeepEqual(event.Message, input.Message) {
			removed = true
			continue
		}
		out.Write(line)
	}
	return out.Bytes()
}

// claudeEnv: the shell's own effort must not stand in for the profile's.
func claudeEnv(env []string, effort string) []string {
	out := make([]string, 0, len(env)+1)
	for _, kv := range env {
		if !strings.HasPrefix(kv, claudeEffortEnv+"=") {
			out = append(out, kv)
		}
	}
	if effort != "" {
		out = append(out, claudeEffortEnv+"="+effort)
	}
	return out
}

func claudeMessage(text string, image []byte, mediaType string) ([]byte, error) {
	type source struct {
		Type      string `json:"type"`
		MediaType string `json:"media_type"`
		Data      string `json:"data"`
	}
	type block struct {
		Type   string  `json:"type"`
		Text   string  `json:"text,omitempty"`
		Source *source `json:"source,omitempty"`
	}
	type message struct {
		Role    string  `json:"role"`
		Content []block `json:"content"`
	}
	content := []block{{Type: "image", Source: &source{
		Type:      "base64",
		MediaType: mediaType,
		Data:      base64.StdEncoding.EncodeToString(image),
	}}}
	if text != "" {
		content = append(content, block{Type: "text", Text: text})
	}
	line, err := json.Marshal(struct {
		Type            string  `json:"type"`
		Message         message `json:"message"`
		ParentToolUseID *string `json:"parent_tool_use_id"`
	}{Type: "user", Message: message{Role: "user", Content: content}})
	if err != nil {
		return nil, err
	}
	return append(line, '\n'), nil
}

// claudeReply ignores unknown keys and wrong-typed ones: claude's output
// grows from version to version.
type claudeReply struct {
	Type             string          `json:"type"`
	Subtype          string          `json:"subtype"`
	IsError          bool            `json:"is_error"`
	Result           string          `json:"result"`
	StructuredOutput json.RawMessage `json:"structured_output"`
	Usage            Usage           `json:"usage"`
}

func claudeResult(stdout []byte) (claudeReply, bool) {
	if reply, ok := decodeClaudeReply(stdout); ok {
		return reply, true
	}
	var last claudeReply
	found := false
	for line := range bytes.SplitSeq(stdout, []byte("\n")) {
		if reply, ok := decodeClaudeReply(line); ok {
			last, found = reply, true
		}
	}
	return last, found
}

func decodeClaudeReply(data []byte) (claudeReply, bool) {
	data = bytes.TrimSpace(data)
	if len(data) == 0 || data[0] != '{' {
		return claudeReply{}, false
	}
	var reply claudeReply
	err := json.Unmarshal(data, &reply)
	var typeErr *json.UnmarshalTypeError
	if err != nil && !errors.As(err, &typeErr) {
		return claudeReply{}, false
	}
	return reply, reply.Type == "result"
}

var claudeAuthSigns = []string{"not logged in", "/login", "invalid api key", "oauth token has expired"}

func claudeAuthFailed(result string) bool {
	lower := strings.ToLower(result)
	for _, sign := range claudeAuthSigns {
		if strings.Contains(lower, sign) {
			return true
		}
	}
	return false
}
