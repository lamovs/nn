package ai

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// codexIsolation disables everything codex exec could do besides answering.
var codexIsolation = []string{
	"--disable", "shell_tool",
	"--disable", "apps",
	"--disable", "multi_agent",
	"--disable", "view_image",
	"--disable", "image_generation",
	"-c", `web_search="disabled"`,
	"-c", "tools.experimental_request_user_input.enabled=false",
	"-c", "skills.include_instructions=false",
	"-c", "include_environment_context=false",
}

const codexLoginHint = "if codex is not logged in, it retries until stopped: run codex login"

// codex runs codex exec read-only, ephemeral, without config.toml; the
// prompt goes on stdin. It still reads AGENTS.md - no flag turns that off.
func (r *runner) codex(ctx context.Context) (Result, error) {
	p := r.call.Profile
	schema, err := r.writeFile("schema.json", []byte(r.schema()))
	if err != nil {
		return Result{}, err
	}
	out := filepath.Join(r.dir, "answer.json")

	args := []string{"exec"}
	if p.Model != "" {
		args = append(args, "-m", p.Model)
	}
	if p.Effort != "" {
		args = append(args, "-c", "model_reasoning_effort="+engineEffort(engineCodex, p.Effort))
	}
	// --color never: codex's own "ERROR:" line must read as written.
	args = append(args, "--ephemeral", "-s", "read-only", "--skip-git-repo-check", "--ignore-user-config", "--strict-config", "--color", "never")
	args = append(args, codexIsolation...)
	args = append(args, "--output-schema", schema, "-o", out, "-C", r.work)
	if r.image != nil {
		image, err := r.writeImage()
		if err != nil {
			return Result{}, err
		}
		// "=" keeps the trailing "-" meaning the prompt, not another --image.
		args = append(args, "--image="+image)
	}
	args = append(args, "-")

	prompt := []byte(joinPrompt(r.req.System, r.req.Text))
	_, stderr, err := run(ctx, process{
		name:        engineCodex,
		path:        r.path,
		args:        args,
		stdin:       prompt,
		echo:        len(prompt),
		dir:         r.work,
		timeoutHint: codexLoginHint,
	}, p.Timeout)
	var exit *exec.ExitError
	switch {
	case errors.As(err, &exit) && codexAuthFailed(stderr, prompt):
		return Result{}, &AuthError{Engine: engineCodex}
	case errors.As(err, &exit):
		return Result{}, failure(engineCodex, err, codexError(stderr, prompt))
	case err != nil:
		return Result{}, err
	}

	file, err := os.Open(out)
	if err != nil {
		return Result{}, errors.New("codex wrote no answer")
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, maxOutput+1))
	if len(data) > maxOutput {
		return Result{}, fmt.Errorf("codex: %w", ErrOutputLimit)
	}
	if err != nil || len(bytes.TrimSpace(data)) == 0 {
		return Result{}, errors.New("codex wrote no answer")
	}
	result, ok := r.decodeResult(data)
	if !ok {
		return Result{}, errors.New("codex's answer is not the JSON it was asked for")
	}
	return result, nil
}

// codexError: past the prompt echo, an incomplete or unrecognized echo
// must never expose the prompt.
func codexError(stderr, prompt []byte) string {
	var offset int
	for _, line := range bytes.SplitAfter(stderr, []byte("\n")) {
		offset += len(line)
		if !bytes.Equal(bytes.TrimSuffix(bytes.TrimSuffix(line, []byte("\n")), []byte("\r")), []byte("user")) {
			continue
		}
		echo := bytes.TrimPrefix(prompt, []byte{0xEF, 0xBB, 0xBF})
		tail := stderr[offset:]
		if len(echo) == 0 || !bytes.HasPrefix(tail, echo) {
			return ""
		}
		tail = tail[len(echo):]
		if len(tail) > 0 && !bytes.HasSuffix(echo, []byte("\n")) && tail[0] != '\n' && !bytes.HasPrefix(tail, []byte("\r\n")) {
			return ""
		}
		lines := strings.Split(strings.TrimSpace(string(tail)), "\n")
		if n := len(lines); n >= 2 && strings.TrimSpace(lines[n-2]) == "tokens used" && codexTokenCount(strings.TrimSpace(lines[n-1])) {
			lines = lines[:n-2]
		}
		if line := lastLine([]byte(strings.Join(lines, "\n"))); strings.HasPrefix(line, "ERROR:") {
			return line
		}
		return ""
	}
	return lastLine(stderr)
}

func codexTokenCount(line string) bool {
	if line == "" || line[0] < '0' || line[0] > '9' || line[len(line)-1] < '0' || line[len(line)-1] > '9' {
		return false
	}
	for _, c := range line {
		if (c < '0' || c > '9') && c != ',' {
			return false
		}
	}
	return true
}

// codexAuthFailed: a 401 anywhere else in stderr may be in the prompt.
func codexAuthFailed(stderr, prompt []byte) bool {
	line := codexError(stderr, prompt)
	return strings.HasPrefix(line, "ERROR:") && strings.Contains(strings.ToLower(line), "401 unauthorized")
}
