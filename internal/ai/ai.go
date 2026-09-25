// Package ai hands a task to an AI engine, run in an empty temp
// directory, killed with its children on cancel or timeout. The content
// (Request.Text) goes over stdin, not argv, which other users on the
// machine can read; claude's system prompt and a command profile's
// {prompt} are the exceptions.
package ai

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode"

	"github.com/lamovs/nn/internal/config"
)

const (
	engineClaude = "claude"
	engineCodex  = "codex"
)

// A run needs Text, Image, or both.
type Request struct {
	System string
	Text   string
	Image  []byte // PNG, JPEG, GIF or WebP
}

type Answer struct {
	Title string   `json:"title"`
	Tags  []string `json:"tags"`
	Body  string   `json:"body"`
}

// Result: Usage is populated only when the engine reports tokens.
type Result struct {
	Answer
	Ask    *AskReply
	Digest *DigestReply
	Filter *FilterReply
	Last   *LastReply
	Triage *TriageReply
	URL    *URLReply
	Usage  Usage
}

type Usage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}

const answerSchema = `{"type":"object","properties":{"title":{"type":"string"},"tags":{"type":"array","items":{"type":"string"}},"body":{"type":"string"}},"required":["title","tags","body"],"additionalProperties":false}`

var (
	ErrOff              = errors.New("AI is off for this call")
	ErrNotInstalled     = errors.New("not installed")
	ErrImageUnsupported = errors.New("takes no image")
	ErrOutputLimit      = errors.New("output limit exceeded")
	ErrNoPrompt         = errors.New("no built-in prompt")

	// ErrNotApproved is an Approval that Approve did not grant.
	ErrNotApproved = errors.New("not approved")
)

type AuthError struct {
	Engine string
}

func (e *AuthError) Error() string {
	switch e.Engine {
	case engineClaude:
		return "claude is not logged in; run claude and log in with /login"
	case engineCodex:
		return "codex is not logged in; run: codex login"
	}
	return e.Engine + " is not logged in"
}

// TimeoutError unwraps to context.DeadlineExceeded.
type TimeoutError struct {
	Name    string
	Timeout time.Duration
	Hint    string // what may be behind it, "" when unknown
}

func (e *TimeoutError) Error() string {
	msg := fmt.Sprintf("%s did not answer within %s", e.Name, e.Timeout)
	if e.Hint != "" {
		msg += "; " + e.Hint
	}
	return msg
}

func (e *TimeoutError) Unwrap() error { return context.DeadlineExceeded }

// Run requires an Approval from Approve, not the zero value. Failure
// messages quote the engine only briefly: its output can echo what it was sent.
func Run(ctx context.Context, approval Approval, req Request) (Result, error) {
	call, ok := approval.allowed()
	if !ok {
		return Result{}, fmt.Errorf("ai: run %w: Approve gives the approval a run needs", ErrNotApproved)
	}
	if digest := approval.grant.requestDigest; digest != nil {
		_, active := grants.LoadAndDelete(approval.grant)
		if !active || *digest != RequestDigest(req) {
			return Result{}, fmt.Errorf("ai: transferred request %w", ErrNotApproved)
		}
	}
	if req.Text == "" && len(req.Image) == 0 {
		return Result{}, errors.New("ai: nothing to send")
	}
	if err := ValidateRequest(req); err != nil {
		return Result{}, err
	}
	switch call.Profile.Engine {
	case engineClaude, engineCodex, config.EngineCommand:
	default:
		return Result{}, fmt.Errorf("profile %s: unknown engine %q", call.Name, call.Profile.Engine)
	}
	if call.Profile.Timeout <= 0 {
		return Result{}, fmt.Errorf("profile %s: no timeout", call.Name)
	}
	r := &runner{call: call, req: req}
	if len(req.Image) > 0 {
		kind, ok := sniffImage(req.Image)
		if !ok {
			return Result{}, errors.New("ai: the image is not PNG, JPEG, GIF or WebP")
		}
		if !AcceptsImage(call.Profile) {
			return Result{}, fmt.Errorf("profile %s %w: its command has no {image}", call.Name, ErrImageUnsupported)
		}
		r.image = &kind
	}
	path, err := LookBinary(call.Profile)
	if err != nil {
		return Result{}, err
	}
	r.path = path

	dir, err := os.MkdirTemp("", "nn-ai-*")
	if err != nil {
		return Result{}, err
	}
	defer os.RemoveAll(dir)
	r.dir = dir
	r.work = filepath.Join(dir, "work")
	if err := os.Mkdir(r.work, 0o700); err != nil {
		return Result{}, err
	}

	var res Result
	switch call.Profile.Engine {
	case engineClaude:
		res, err = r.claude(ctx)
	case engineCodex:
		res, err = r.codex(ctx)
	default:
		res, err = r.command(ctx)
	}
	if err != nil {
		return Result{}, err
	}
	if res.Ask == nil && res.Digest == nil && res.Filter == nil && res.Last == nil && res.Triage == nil && res.URL == nil && res.Title == "" && len(res.Tags) == 0 && res.Body == "" {
		return Result{}, fmt.Errorf("%s gave an empty answer", r.name())
	}
	return res, nil
}

type runner struct {
	call  Call
	req   Request
	image *imageKind // nil: no image

	path string // absolute
	dir  string // private temp dir for the run's own files
	work string // empty dir inside dir the engine runs in
}

func (r *runner) name() string { return filepath.Base(Binary(r.call.Profile)) }

// writeFile: the run's private directory is readable by nn's user only.
func (r *runner) writeFile(name string, data []byte) (string, error) {
	path := filepath.Join(r.dir, name)
	return path, os.WriteFile(path, data, 0o600)
}

func (r *runner) writeImage() (string, error) {
	return r.writeFile("image."+r.image.ext, r.req.Image)
}

type imageKind struct {
	mediaType string
	ext       string
}

func sniffImage(data []byte) (imageKind, bool) {
	switch {
	case bytes.HasPrefix(data, []byte("\x89PNG\r\n\x1a\n")):
		return imageKind{"image/png", "png"}, true
	case bytes.HasPrefix(data, []byte{0xFF, 0xD8, 0xFF}):
		return imageKind{"image/jpeg", "jpg"}, true
	case bytes.HasPrefix(data, []byte("GIF87a")), bytes.HasPrefix(data, []byte("GIF89a")):
		return imageKind{"image/gif", "gif"}, true
	case len(data) >= 12 && string(data[:4]) == "RIFF" && string(data[8:12]) == "WEBP":
		return imageKind{"image/webp", "webp"}, true
	}
	return imageKind{}, false
}

func joinPrompt(system, text string) string {
	switch {
	case system == "":
		return text
	case text == "":
		return system
	}
	return system + "\n\n" + text
}

// decodeAnswer: at least one of title, tags and body must be present.
func decodeAnswer(data []byte) (Answer, bool) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(bytes.TrimSpace(data), &fields); err != nil || fields == nil {
		return Answer{}, false
	}
	var a Answer
	found := false
	for key, dst := range map[string]any{"title": &a.Title, "tags": &a.Tags, "body": &a.Body} {
		raw, ok := fields[key]
		if !ok {
			continue
		}
		if err := json.Unmarshal(raw, dst); err != nil {
			return Answer{}, false
		}
		found = true
	}
	return a, found
}

const maxExcerpt = 200

// excerpt strips control and bidi-control characters, which could make a
// quoted message read other than it is.
func excerpt(text string) string {
	clean := strings.Map(func(r rune) rune {
		if r < 0x20 && r != '\t' && r != '\n' || r >= 0x7f && r < 0xa0 || unicode.In(r, unicode.Bidi_Control, unicode.Cf) {
			return -1
		}
		return r
	}, text)
	line := []rune(strings.Join(strings.Fields(clean), " "))
	if len(line) > maxExcerpt {
		return string(line[:maxExcerpt]) + "..."
	}
	return string(line)
}

func lastLine(text []byte) string {
	lines := strings.Split(strings.TrimSpace(string(text)), "\n")
	return lines[len(lines)-1]
}

func failure(name string, err error, line string) error {
	if msg := excerpt(line); msg != "" {
		return fmt.Errorf("%s failed (%w): %s", name, err, msg)
	}
	return fmt.Errorf("%s failed: %w", name, err)
}
