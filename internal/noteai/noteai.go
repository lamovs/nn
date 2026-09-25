// Package noteai applies shared title and keyword metadata to captured notes.
package noteai

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"unicode"

	"github.com/lamovs/nn/internal/ai"
	"github.com/lamovs/nn/internal/app"
	"github.com/lamovs/nn/internal/config"
	"github.com/lamovs/nn/internal/vault"
)

type Plan struct {
	Approval   ai.Approval
	Mode, Task string
}

// Prepare resolves policy and obtains consent before any model request.
func Prepare(cfg *config.Config, task string, overrides ai.Overrides, mode string, asker ai.Asker, stderr io.Writer) (*Plan, error) {
	if task != "title" && task != "shot" && task != "url" {
		return nil, fmt.Errorf("unsupported metadata task %q", task)
	}
	if mode != "" && !slices.Contains([]string{"auto", "wait", "background"}, mode) {
		return nil, errors.New("--ai-mode: want auto, wait or background")
	}
	// url has no run key: only consent and --no-ai decide.
	if overrides.NoAI || task != "url" && !overrides.Explicit() && cfg.AI.Tasks[task].Run != "always" {
		return nil, nil
	}
	call, err := ai.Resolve(*cfg, task, overrides)
	if err != nil {
		return nil, err
	}
	decision, approval, err := ai.Approve(cfg, call, ai.Sends(task), asker, stderr)
	if err != nil {
		return nil, err
	}
	if decision != ai.Allowed {
		if decision == ai.Unasked {
			fmt.Fprintf(stderr, "nn: %s: AI needs consent; enable it with nn setup ai\n", task)
		}
		return nil, nil
	}
	if mode == "" {
		mode = cfg.AI.Mode
	}
	if mode == "auto" {
		mode = "background"
		if asker != nil && asker.Interactive() {
			mode = "wait"
		}
	}
	if mode == "" {
		mode = "background"
	}
	return &Plan{Approval: approval, Mode: mode, Task: task}, nil
}

type Input struct {
	Note       vault.NewNote
	To, Text   string
	Image      []byte
	Ext        string
	AllowTitle bool
	AfterSave  func(*vault.Note, []string)
}

var appendEnrichment = (*vault.Vault).AppendEnrichment

// Save: model body is never used for a title request.
func Save(ctx context.Context, env *app.Env, plan *Plan, in Input, stderr io.Writer) (*vault.Note, error) {
	var imagePaths []string
	finish := func(note *vault.Note, err error) (*vault.Note, error) {
		if err == nil && in.AfterSave != nil {
			in.AfterSave(note, slices.Clone(imagePaths))
		}
		return note, err
	}
	save := func(n vault.NewNote) (*vault.Note, error) {
		var result vault.WriteResult
		var err error
		if in.To != "" {
			result, err = env.Vault.AppendWithAssets(in.To, n.Body, n.Images)
		} else {
			result, err = env.Vault.CreateWithAssets(n)
		}
		if err == nil {
			imagePaths = slices.Clone(result.Images)
		}
		return result.Note, err
	}
	if plan == nil {
		return finish(save(in.Note))
	}
	if plan.Task != "title" {
		return nil, errors.New("note save requires the title task")
	}
	call, err := plan.Approval.Call()
	if err != nil {
		return nil, err
	}
	if call.Task != plan.Task {
		return nil, errors.New("metadata task does not match approval")
	}
	image := in.Image
	if len(image) > 0 && !ai.AcceptsImage(call.Profile) {
		if strings.TrimSpace(in.Text) == "" {
			fmt.Fprintln(stderr, "nn: warning: AI profile takes no image and no new text is available; saved without AI metadata")
			return finish(save(in.Note))
		}
		image = nil
		fmt.Fprintln(stderr, "nn: warning: AI profile takes no image; sending only new text or OCR")
	}
	if strings.TrimSpace(in.Text) == "" && len(image) == 0 {
		fmt.Fprintln(stderr, "nn: warning: no new text or image for AI metadata; saved without AI metadata")
		return finish(save(in.Note))
	}
	dir, err := NewCache("title")
	if err != nil {
		fmt.Fprintf(stderr, "nn: warning: AI metadata cache: %v; saving without AI metadata\n", err)
		return finish(save(in.Note))
	}
	if err := os.WriteFile(filepath.Join(dir, "original.txt"), []byte(in.Note.Body), 0600); err != nil {
		_ = os.RemoveAll(dir)
		fmt.Fprintf(stderr, "nn: warning: AI metadata cache: %v; saving without AI metadata\n", err)
		return finish(save(in.Note))
	}
	imageName := ""
	if len(in.Image) > 0 {
		if !slices.Contains([]string{"png", "jpg", "jpeg", "gif", "webp"}, in.Ext) {
			return nil, fmt.Errorf("unsupported image extension %q", in.Ext)
		}
		imageName = "original." + in.Ext
		if err := os.WriteFile(filepath.Join(dir, imageName), in.Image, 0600); err != nil {
			fmt.Fprintf(stderr, "nn: warning: AI image cache: %v; text retained at %s; saving without AI metadata\n", err, dir)
			return finish(save(in.Note))
		}
	}
	fallback := func(cause error) (*vault.Note, error) {
		WriteStatus(dir, "Metadata failed; original retained")
		fmt.Fprintf(stderr, "nn: warning: note metadata: %v; recovery cache: %s\n", cause, dir)
		note, err := save(in.Note)
		if err != nil {
			return nil, fmt.Errorf("save note: %w; original retained at %s", err, dir)
		}
		if updated, err := appendEnrichment(env.Vault, note.Path, vault.Enrichment{Body: Recovery("title", dir)}); err == nil {
			note = updated
		}
		return finish(note, nil)
	}
	req, vocabulary, err := Request(ctx, env, "title", in.Text, image)
	if err != nil {
		return fallback(err)
	}
	if err := ctx.Err(); err != nil {
		return fallback(err)
	}
	allowTitle := in.AllowTitle && in.To == "" && strings.TrimSpace(in.Note.Title) == "" && !vault.HasTitle(&vault.Note{Body: in.Note.Body})
	if plan.Mode == "wait" {
		result, err := ai.Run(ctx, plan.Approval, req)
		if err != nil {
			return fallback(err)
		}
		answer, _ := json.Marshal(result.Answer)
		if err := os.WriteFile(filepath.Join(dir, "answer.json"), answer, 0600); err != nil {
			WriteStatus(dir, "Could not retain metadata answer before save")
		}
		base := ApplyNew(in.Note, result.Answer, vocabulary, allowTitle)
		note, err := save(base)
		if err != nil {
			return nil, fmt.Errorf("save note: %w; original and answer retained at %s", err, dir)
		}
		if in.To != "" {
			initial := note
			note, err = appendEnrichment(env.Vault, note.Path, vault.Enrichment{Tags: Keywords(result.Tags, vocabulary)})
			if err != nil {
				if in.AfterSave != nil {
					in.AfterSave(initial, slices.Clone(imagePaths))
				}
				return initial, fmt.Errorf("append metadata: %w; new content saved at %s; answer retained at %s", err, initial.Path, dir)
			}
		}
		_ = os.RemoveAll(dir)
		return finish(note, nil)
	}
	note, err := save(in.Note)
	if err != nil {
		return nil, fmt.Errorf("save note: %w; original retained at %s", err, dir)
	}
	if len(image) == 0 {
		imageName = ""
	}
	if in.AfterSave != nil {
		in.AfterSave(note, slices.Clone(imagePaths))
	}
	j := Job{Version: 1, ID: filepath.Base(dir), Task: "title", AllowTitle: allowTitle, Config: env.Cfg.Path, Root: env.Vault.Root, Inbox: env.Vault.Inbox, Note: note.Path, Image: imageName, System: req.System, Text: req.Text}
	if err := Start(ctx, dir, j, plan.Approval, req); err != nil {
		WriteStatus(dir, "Could not start metadata worker; original retained")
		fmt.Fprintf(stderr, "nn: warning: note metadata: %v; recovery cache: %s\n", err, dir)
		if updated, e := appendEnrichment(env.Vault, note.Path, vault.Enrichment{Body: Recovery("title", dir)}); e == nil {
			note = updated
		}
	} else {
		fmt.Fprintf(stderr, "nn: title: metadata running in background; recovery cache: %s\n", dir)
	}
	return note, nil
}

func Request(ctx context.Context, env *app.Env, task, text string, image []byte) (ai.Request, []string, error) {
	prompt, err := ai.Prompt(env.Cfg, task)
	if err != nil {
		return ai.Request{}, nil, err
	}
	vocabulary, err := Vocabulary(ctx, env)
	if err != nil {
		return ai.Request{}, nil, err
	}
	names, _ := json.Marshal(vocabulary)
	req := ai.Request{System: prompt, Text: "Existing vault tag names (reuse appropriate names; new useful keywords are allowed): " + string(names) + "\n\nNew capture text or OCR:\n" + text, Image: image}
	if task == "shot" && len(image) > 0 {
		if err := ai.ValidateShotImage(image); err != nil {
			return ai.Request{}, nil, err
		}
	}
	return req, vocabulary, ai.ValidateRequest(req)
}

func Vocabulary(ctx context.Context, env *app.Env) ([]string, error) {
	notes, err := env.Notes(ctx)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	var tags []string
	for _, n := range notes {
		for _, tag := range n.Tags {
			if key := strings.ToLower(tag); !seen[key] {
				seen[key] = true
				tags = append(tags, tag)
			}
		}
	}
	slices.Sort(tags)
	return tags, nil
}

func Keywords(tags, vocabulary []string) []string {
	spellings := map[string]string{}
	for _, tag := range vocabulary {
		if normalized := vault.NormalizeKeywords([]string{tag}); len(normalized) == 1 {
			spellings[normalized[0]] = tag
		}
	}
	result := vault.NormalizeKeywords(tags)
	for i, tag := range result {
		if spelling, ok := spellings[tag]; ok {
			result[i] = spelling
		}
	}
	return result
}

func Title(title string) string {
	title = strings.Trim(strings.Join(strings.Fields(title), " "), "# `")
	if strings.ContainsFunc(title, unicode.IsControl) {
		return ""
	}
	runes := []rune(title)
	if len(runes) > 160 {
		title = string(runes[:160])
	}
	return title
}

// ApplyNew never replaces the body or a manual title.
func ApplyNew(note vault.NewNote, answer ai.Answer, vocabulary []string, allowTitle bool) vault.NewNote {
	if allowTitle && strings.TrimSpace(note.Title) == "" && !vault.HasTitle(&vault.Note{Body: note.Body}) {
		note.Title = Title(answer.Title)
	}
	note.TrailingTags = append(slices.Clone(note.TrailingTags), Keywords(answer.Tags, vocabulary)...)
	return note
}

func AnswerBody(answer ai.Answer) string {
	body := strings.TrimSpace(answer.Body)
	if body == "" {
		body = strings.TrimSpace(answer.Title)
	}
	return body
}

func NewCache(task string) (string, error) {
	if task != "title" && task != "shot" && task != "url" {
		return "", errors.New("invalid metadata cache task")
	}
	root := os.Getenv("XDG_CACHE_HOME")
	if root == "" {
		var err error
		root, err = os.UserCacheDir()
		if err != nil {
			return "", err
		}
	}
	root = filepath.Join(root, "nn", "ai")
	if err := os.MkdirAll(root, 0700); err != nil {
		return "", err
	}
	return os.MkdirTemp(root, task+"-")
}

func Recovery(task, path string) string {
	prefix := "Note metadata"
	switch task {
	case "shot":
		prefix = "Screenshot analysis"
	case "url":
		prefix = "Link summary"
	}
	return "\n\n" + prefix + " did not finish. Original retained at:\n\n    " + strings.ReplaceAll(path, "\n", "\n    ") + "\n"
}

func WriteStatus(dir, status string) { writeStatus(dir, status) }
