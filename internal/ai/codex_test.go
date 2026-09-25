package ai

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestCodexArgs(t *testing.T) {
	f := newFake(t)
	f.put("out", `{"title":"Red square","tags":["image"],"body":"A red square."}`)

	res, err := Run(context.Background(), issue(codexCall("gpt-6", "max")), Request{System: "Describe.", Text: "OCR: none", Image: pngImage})
	if err != nil {
		t.Fatal(err)
	}
	argv := f.argv()
	if len(argv) < 5 {
		t.Fatalf("argv = %q", argv)
	}
	at := func(flag string) string {
		i := slices.Index(argv, flag)
		if i < 0 || i+1 >= len(argv) {
			t.Fatalf("argv has no %s: %q", flag, argv)
		}
		return argv[i+1]
	}
	schema, out, work := at("--output-schema"), at("-o"), at("-C")
	image := strings.TrimPrefix(argv[len(argv)-2], "--image=")
	want := []string{
		"exec", "-m", "gpt-6", "-c", "model_reasoning_effort=xhigh",
		"--ephemeral", "-s", "read-only", "--skip-git-repo-check", "--ignore-user-config", "--strict-config", "--color", "never",
		"--disable", "shell_tool", "--disable", "apps", "--disable", "multi_agent",
		"--disable", "view_image", "--disable", "image_generation",
		"-c", `web_search="disabled"`,
		"-c", "tools.experimental_request_user_input.enabled=false",
		"-c", "skills.include_instructions=false",
		"-c", "include_environment_context=false",
		"--output-schema", schema, "-o", out, "-C", work, "--image=" + image, "-",
	}
	if !slices.Equal(argv, want) {
		t.Errorf("argv = %q\nwant   %q", argv, want)
	}

	private := filepath.Dir(work)
	for _, p := range []string{schema, out, image} {
		if filepath.Dir(p) != private {
			t.Errorf("%s is not in the private directory %s", p, private)
		}
	}
	if filepath.Ext(image) != ".png" {
		t.Errorf("image %s, want a .png name for a PNG", image)
	}
	if mode := f.get("private_dir"); !strings.HasPrefix(mode, "drwx------") {
		t.Errorf("private directory: %s", mode)
	}
	for _, name := range []string{"schema_mode", "image_mode"} {
		if mode := f.get(name); !strings.HasPrefix(mode, "-rw-------") {
			t.Errorf("%s: %s", name, mode)
		}
	}
	if got := f.get("schema"); got != answerSchema {
		t.Errorf("schema = %s", got)
	}
	if got := f.get("image"); got != string(pngImage) {
		t.Errorf("image = %q", got)
	}
	if got := f.get("stdin"); got != "Describe.\n\nOCR: none" {
		t.Errorf("stdin = %q", got)
	}
	if got, cwd := strings.TrimSpace(f.get("codex_dir")), strings.TrimSpace(f.get("cwd")); got != cwd {
		t.Errorf("-C %s, but codex ran in %s", got, cwd)
	}
	f.assertRanPrivately()
	if _, err := os.Stat(private); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("private directory still there: %v", err)
	}
	if res.Title != "Red square" || !slices.Equal(res.Tags, []string{"image"}) || res.Body != "A red square." {
		t.Errorf("answer = %+v", res.Answer)
	}
}

func TestCodexDefaults(t *testing.T) {
	f := newFake(t)
	f.put("out", `{"title":"t","tags":[],"body":""}`)
	if _, err := Run(context.Background(), issue(codexCall("", "")), Request{Text: "only text"}); err != nil {
		t.Fatal(err)
	}
	argv := f.argv()
	if argv[1] != "--ephemeral" || argv[len(argv)-1] != "-" {
		t.Errorf("argv = %q", argv)
	}
	for _, arg := range argv {
		if arg == "-m" || strings.HasPrefix(arg, "model_reasoning_effort") || strings.HasPrefix(arg, "--image") {
			t.Errorf("argv has %s: %q", arg, argv)
		}
	}
	if got := f.get("stdin"); got != "only text" {
		t.Errorf("stdin = %q", got)
	}
}

func TestEngineEffort(t *testing.T) {
	for _, tc := range []struct{ engine, effort, want string }{
		{"claude", "max", "max"}, {"claude", "low", "low"},
		{"codex", "max", "xhigh"}, {"codex", "high", "high"}, {"codex", "", ""},
		{"command", "max", "max"},
	} {
		if got := engineEffort(tc.engine, tc.effort); got != tc.want {
			t.Errorf("engineEffort(%s, %s) = %q, want %q", tc.engine, tc.effort, got, tc.want)
		}
	}
}

func TestCodexNotLoggedIn(t *testing.T) {
	f := newFake(t)
	f.put("stderr", "user\nT\nreconnecting 1/5\nreconnecting 5/5\nERROR: unexpected status 401 Unauthorized: Missing bearer or basic authentication in header\n")
	f.put("exit", "1")
	_, err := Run(context.Background(), issue(codexCall("", "")), Request{Text: "T"})
	var auth *AuthError
	if !errors.As(err, &auth) || auth.Engine != "codex" || !strings.Contains(err.Error(), "codex login") {
		t.Errorf("err = %v, want a codex AuthError", err)
	}

	f = newFake(t)
	f.put("sleep", "30")
	call := codexCall("", "")
	call.Profile.Timeout = 300 * time.Millisecond
	start := time.Now()
	_, err = Run(context.Background(), issue(call), Request{Text: "T"})
	var timeout *TimeoutError
	if !errors.As(err, &timeout) || !errors.Is(err, context.DeadlineExceeded) || !strings.Contains(err.Error(), "within 300ms") || !strings.Contains(err.Error(), "codex login") {
		t.Errorf("err = %v, want a timeout that names codex login", err)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Errorf("the timeout took %v to stop codex", elapsed)
	}
}

// TestCodexFailures: a failure quotes only codex's own error line - after
// the echoed prompt is cut out - never the prompt itself, however much it
// resembles a 401 or an ERROR line.
func TestCodexFailures(t *testing.T) {
	const (
		note      = "Title this.\n\nmy private note: the API returns 401 Unauthorized after a token refresh"
		errorNote = "my private note\nERROR: unexpected status 401 Unauthorized in my private log"
	)
	echo := func(prompt string) string { return "OpenAI Codex\n--------\nuser\n" + prompt + "\n" }
	for _, tc := range []struct {
		name, text, out, stderr, exit, want string
	}{
		{"exit with an error", "T", "", "session started\nprompt: my private note\nERROR: model not found\n", "1", "codex failed (exit status 1): ERROR: model not found"},
		{"early override error", "T", "", "Error parsing -c overrides: invalid override key\n", "1", "codex failed (exit status 1): Error parsing -c overrides: invalid override key"},
		{"early config error", "T", "", "Error loading config.toml:\nunknown field `tools.experimental_request_user_input`\n", "1", "codex failed (exit status 1): unknown field `tools.experimental_request_user_input`"},
		{"early schema error", "T", "", "Output schema file /tmp/x/schema.json is not valid JSON: EOF while parsing\n", "1", "codex failed (exit status 1): Output schema file /tmp/x/schema.json is not valid JSON: EOF while parsing"},
		{"tokens after error", "T", "", echo("T") + "ERROR: stream disconnected before completion\ntokens used\n8,812\n", "1", "codex failed (exit status 1): ERROR: stream disconnected before completion"},
		{"BOM removed from echo", "\ufeff" + errorNote, "", echo(errorNote), "137", "codex failed: exit status 137"},
		{"incomplete echo", errorNote + "\nmore text", "", echo(errorNote), "137", "codex failed: exit status 137"},
		{"a 401 in the prompt", note, "", echo(note) + "\nERROR: model not found\n", "1", "codex failed (exit status 1): ERROR: model not found"},
		{"killed right after the prompt", note, "", echo(note), "137", "codex failed: exit status 137"},
		{"an error last in the prompt", errorNote, "", echo(errorNote), "137", "codex failed: exit status 137"},
		{"an error last in the prompt, then codex's own", errorNote, "", echo(errorNote) + "ERROR: model not found\n", "1", "codex failed (exit status 1): ERROR: model not found"},
		{"a 401 last, prompt not in stderr", "T", "", "user\nmy private note: why 401 Unauthorized?\n", "1", "codex failed: exit status 1"},
		{"no answer file", "T", "", "", "0", "codex wrote no answer"},
		{"answer not JSON", "T", "Here is a title.", "", "0", "not the JSON it was asked for"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFake(t)
			if tc.out != "" {
				f.put("out", tc.out)
			}
			f.put("stderr", tc.stderr)
			f.put("exit", tc.exit)
			_, err := Run(context.Background(), issue(codexCall("", "")), Request{Text: tc.text})
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err = %v, want %q", err, tc.want)
			}
			if err != nil && strings.Contains(err.Error(), "private") {
				t.Errorf("the message quotes the prompt: %v", err)
			}
			var auth *AuthError
			if errors.As(err, &auth) {
				t.Errorf("err = %v is not a login problem", err)
			}
			var exit *exec.ExitError
			if tc.exit != "0" && (!errors.As(err, &exit) || strconv.Itoa(exit.ExitCode()) != tc.exit) {
				t.Errorf("err = %v does not carry exit status %s", err, tc.exit)
			}
		})
	}
}

// TestCodexErrorBoundary keeps diagnostics separate from prompt text even
// when the prompt resembles errors or the token-count trailer.
func TestCodexErrorBoundary(t *testing.T) {
	const private = "private note\nERROR: 401 Unauthorized in private log"
	for _, tc := range []struct {
		name, prompt, stderr, want string
		auth                       bool
	}{
		{"early diagnostic contains prompt", "Error", "Error loading config.toml:\ninvalid key\n", "invalid key", false},
		{"early plain 401 is not authentication", "T", "connection refused: 401 Unauthorized\n", "connection refused: 401 Unauthorized", false},
		{"early ERROR 401", "T", "ERROR: 401 Unauthorized\n", "ERROR: 401 Unauthorized", true},
		{"marker without echo", "T", "user\nERROR: 401 Unauthorized in private log\n", "", false},
		{"marker at end", "T", "ERROR: early message\nuser", "", false},
		{"empty prompt after marker", "", "user\nERROR: private log\n", "", false},
		{"prompt only", private, "user\n" + private + "\n", "", false},
		{"partial prompt", private + "\nlast line", "user\n" + private + "\n", "", false},
		{"prompt found later", private, "user\nother text\n" + private + "\nERROR: 401 Unauthorized\n", "", false},
		{"prompt prefix is not complete echo", "private", "user\nprivate note\nERROR: 401 Unauthorized in private log\n", "", false},
		{"BOM normalization", "\ufeff" + private, "user\n" + private + "\n", "", false},
		{"BOM plus real error", "\ufeff" + private, "user\n" + private + "\nERROR: model unavailable\n", "ERROR: model unavailable", false},
		{"BOM plus real authentication error", "\ufeff" + private, "user\n" + private + "\nERROR: 401 Unauthorized\ntokens used\n0\n", "ERROR: 401 Unauthorized", true},
		{"tokens in prompt", "ERROR: private log\ntokens used\n8,812", "user\nERROR: private log\ntokens used\n8,812\n", "", false},
		{"real error after tokens in prompt", "tokens used\n8,812", "user\ntokens used\n8,812\nERROR: model unavailable\ntokens used\n100\n", "ERROR: model unavailable", false},
		{"last error with trailer", "T", "user\nT\nERROR: first\nERROR: last\n\ntokens used\n8,812\n\n", "ERROR: last", false},
		{"unknown trailing line", "T", "user\nT\nERROR: 401 Unauthorized\nmore output\n", "", false},
		{"trailer must have a count", "T", "user\nT\nERROR: 401 Unauthorized\ntokens used\nunknown\n", "", false},
		{"trailing prompt newline", "T\n", "user\nT\n\nERROR: model unavailable\n", "ERROR: model unavailable", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stderr, prompt := []byte(tc.stderr), []byte(tc.prompt)
			if got := codexError(stderr, prompt); got != tc.want {
				t.Errorf("codexError = %q, want %q", got, tc.want)
			}
			if got := codexAuthFailed(stderr, prompt); got != tc.auth {
				t.Errorf("codexAuthFailed = %v, want %v", got, tc.auth)
			}
		})
	}
}

func TestCodexPromptEchoIsNotOutput(t *testing.T) {
	f := newFake(t)
	f.put("echo", "")
	f.put("out", `{"title":"t","tags":[],"body":""}`)
	big := strings.Repeat("x", MaxTextBytes)
	if _, err := Run(context.Background(), issue(codexCall("", "")), Request{Text: big}); err != nil {
		t.Errorf("a prompt of %d bytes: err = %v", len(big), err)
	}
	if got := f.get("stdin"); len(got) != len(big) {
		t.Errorf("codex got %d bytes of the prompt, want %d", len(got), len(big))
	}

	f = newFake(t)
	f.put("echo", "")
	f.put("stderr", strings.Repeat("y", maxOutput+1))
	f.put("sleep", "30")
	start := time.Now()
	_, err := Run(context.Background(), issue(codexCall("", "")), Request{Text: "T"})
	if !errors.Is(err, ErrOutputLimit) {
		t.Errorf("more than %d bytes of its own: err = %v, want ErrOutputLimit", maxOutput, err)
	}
	if elapsed := time.Since(start); elapsed > 10*time.Second {
		t.Errorf("over the limit took %v: codex was waited for", elapsed)
	}
}
