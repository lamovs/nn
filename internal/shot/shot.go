// Package shot saves screenshots and hands their analysis to the shared AI core.
package shot

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/lamovs/nn/internal/ai"
	"github.com/lamovs/nn/internal/app"
	"github.com/lamovs/nn/internal/config"
	"github.com/lamovs/nn/internal/noteai"
	"github.com/lamovs/nn/internal/ocr"
	"github.com/lamovs/nn/internal/vault"
)

// Plan is one authorized screenshot analysis. Prepare happens before capture.
type Plan = noteai.Plan

func Prepare(cfg *config.Config, overrides ai.Overrides, mode string, asker ai.Asker, stderr io.Writer) (*Plan, error) {
	return noteai.Prepare(cfg, "shot", overrides, mode, asker, stderr)
}

type Input struct {
	Data     []byte
	Ext      string
	Title    string
	Tags     []string
	OCR      bool
	Engine   ocr.Engine
	Langs    []string
	CopyText func([]ocr.Line)
}

// Save: an analysis failure becomes a saved OCR note with a recovery path, not a lost capture.
func Save(ctx context.Context, env *app.Env, plan *Plan, in Input, stderr io.Writer) (*vault.Note, error) {
	if !slices.Contains([]string{"png", "jpg", "jpeg", "gif", "webp"}, in.Ext) {
		return nil, fmt.Errorf("unsupported screenshot extension %q", in.Ext)
	}
	dir, err := newCache()
	if err != nil {
		return nil, err
	}
	imageName := "original." + in.Ext
	imagePath := filepath.Join(dir, imageName)
	if err := os.WriteFile(imagePath, in.Data, 0600); err != nil {
		os.RemoveAll(dir)
		return nil, err
	}
	var recognized *ocr.Result
	var text string
	if in.OCR {
		recognized, err = ocr.Ensure(ctx, dir, imageName, in.Engine, in.Langs)
		if err != nil {
			fmt.Fprintf(stderr, "nn: warning: OCR: %v\n", err)
		} else {
			lines := make([]string, len(recognized.Lines))
			for i, line := range recognized.Lines {
				lines[i] = line.Text
			}
			text = strings.Join(lines, "\n")
			if in.CopyText != nil {
				in.CopyText(recognized.Lines)
			}
		}
	}
	where, repo := vault.Context("")
	now := time.Now()
	if env.Now != nil {
		now = env.Now()
	}
	explicitTitle := in.Title
	if in.Title == "" {
		for _, line := range strings.Split(text, "\n") {
			if title := strings.Join(strings.Fields(line), " "); title != "" {
				runes := []rune(title)
				if len(runes) > 80 {
					runes = runes[:80]
				}
				in.Title = string(runes)
				break
			}
		}
		if in.Title == "" {
			in.Title = "shot-" + now.Format("2006-01-02-150405")
		}
	}
	base := vault.NewNote{Title: explicitTitle, FilenameHint: in.Title, Tags: in.Tags, Body: ocrBody(text), Via: "shot", Where: where, Repo: repo, Now: now}
	if env.Cfg.AI.Tasks["shot"].Image == "embed" {
		base.Images = []vault.Image{{Data: in.Data, Ext: in.Ext}}
	}
	create := func(n vault.NewNote) (*vault.Note, error) {
		written, err := env.Vault.CreateWithAssets(n)
		if err != nil {
			return nil, fmt.Errorf("save screenshot note: %w; original retained at %s", err, imagePath)
		}
		note := written.Note
		if recognized != nil && len(written.Images) > 0 {
			if e := ocr.SaveSidecar(env.Vault.Root, written.Images[0], recognized); e != nil {
				fmt.Fprintf(stderr, "nn: warning: OCR cache: %v\n", e)
			}
		}
		env.PostSave(ctx, note.Path, "create")
		return note, nil
	}
	fallback := func(cause error) (*vault.Note, error) {
		fmt.Fprintf(stderr, "nn: warning: screenshot analysis: %v; original retained at %s\n", cause, imagePath)
		base.Body += recovery(imagePath)
		return create(base)
	}
	req, vocabulary, err := noteai.Request(ctx, env, "shot", text, in.Data)
	if err != nil {
		return fallback(err)
	}
	if err := ctx.Err(); err != nil {
		return fallback(err)
	}
	if plan.Mode == "wait" {
		result, err := ai.Run(ctx, plan.Approval, req)
		if err != nil {
			return fallback(err)
		}
		if answerBody(result.Answer) == "" {
			return fallback(fmt.Errorf("model returned no analysis text"))
		}
		base.Body = answerBody(result.Answer)
		base = noteai.ApplyNew(base, result.Answer, vocabulary, explicitTitle == "")
		note, err := create(base)
		if err == nil {
			os.RemoveAll(dir)
		} else {
			_ = os.WriteFile(filepath.Join(dir, "answer.md"), []byte(base.Body), 0600)
		}
		return note, err
	}
	note, err := create(base)
	if err != nil {
		return nil, err
	}
	job := job{Version: 1, Task: "shot", AllowTitle: explicitTitle == "", ID: filepath.Base(dir), Config: env.Cfg.Path, Root: env.Vault.Root, Inbox: env.Vault.Inbox, Note: note.Path, Image: imageName, System: req.System, Text: req.Text}
	if err := startWorker(ctx, dir, job, plan.Approval, req); err != nil {
		fmt.Fprintf(stderr, "nn: warning: screenshot analysis: %v; original retained at %s\n", err, imagePath)
		if updated, e := env.Vault.Append(note.Path, recovery(imagePath), nil); e == nil {
			note = updated
			env.PostSave(ctx, note.Path, "append")
		} else {
			writeStatus(dir, "Could not append recovery path; original retained")
		}
	} else {
		fmt.Fprintf(stderr, "nn: shot: analysis running in background; recovery cache: %s\n", dir)
	}
	return note, nil
}

func newCache() (string, error) { return noteai.NewCache("shot") }
func ocrBody(text string) string {
	if strings.TrimSpace(text) == "" {
		return "Screenshot captured. No text recognized."
	}
	// Indenting keeps OCR content as data rather than Markdown instructions.
	return "## Recognized text\n\n    " + strings.ReplaceAll(strings.TrimRight(text, "\n"), "\n", "\n    ")
}
func recovery(path string) string {
	return "\n\nScreenshot analysis did not finish. Original retained at:\n\n    " + strings.ReplaceAll(path, "\n", "\n    ") + "\n"
}
func answerBody(answer ai.Answer) string { return noteai.AnswerBody(answer) }
