package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/lamovs/nn/internal/ai"
	"github.com/lamovs/nn/internal/app"
	"github.com/lamovs/nn/internal/cli"
	"github.com/lamovs/nn/internal/config"
	"github.com/lamovs/nn/internal/ocr"
	"github.com/lamovs/nn/internal/output"
	"github.com/lamovs/nn/internal/platform"
	"github.com/lamovs/nn/internal/shot"
	"github.com/lamovs/nn/internal/vault"
)

func init() {
	def := config.Default()
	register(command{
		help: cli.Help{
			Verb:    "shot",
			Summary: "capture a screen region into a new note with OCR",
			Examples: []cli.Example{
				{Cmd: "nn shot -t docker", What: "select a screen region and save it as a tagged note"},
				{Cmd: "nn shot --ai", What: "analyze the original screenshot in the background"},
				{Cmd: "nn shot --ai=codex --ai-mode wait", What: "wait for a finished analysis note"},
				{Cmd: "nn shot --copy-text", What: "also put the recognized text on the clipboard"},
				{Cmd: "nn shot --title \"error dialog\"", What: "gives the note a title instead of one derived from the image"},
			},
			Sections: []cli.HelpSection{
				{Title: "AI analysis", Items: []string{
					"--ai[=PROFILE] requests analysis; --no-ai disables it. --model MODEL and --effort low|medium|high|max override the profile for this capture.",
					"--ai-mode background (the default) saves OCR immediately and appends the answer later. wait saves the finished note; auto waits on an interactive terminal and otherwise uses background.",
					"AI receives the original image and OCR text after consent. ai.tasks.shot.run controls automatic use: always, flag or never. Explicit --ai with never is an error before capture.",
					"On analysis failure, the OCR note and original screenshot are preserved. ai.tasks.shot.image=embed keeps the image in assets; discard retains its private recovery copy until analysis is saved.",
				}},
				{Title: "Cancelling", Items: []string{
					"Dismissing the selection (Escape, for example) exits 0 with a note on stderr; nothing is saved.",
				}},
				{Title: "OCR and clipboard", Items: []string{
					fmt.Sprintf("OCR runs by default (default: capture.ocr, %v); --no-ocr skips it, --ocr runs it even when capture.ocr is false. The two cannot be combined; --no-ocr also turns off copying recognized text, even when shot.copy_text is on in the config.", def.Capture.OCR),
					fmt.Sprintf("--copy-text defaults from shot.copy_text (default: %v); --no-copy-text turns it off even when the config default is on. The two cannot be combined. --copy-text needs OCR to have text to copy, so it turns OCR on even when capture.ocr is off, unless --no-ocr is also given, which is a usage error together with --copy-text.", def.Shot.CopyText),
				}},
				{Title: "Linux", Items: []string{
					fmt.Sprintf("shot.tool (default: %s) picks the screenshot tool: auto tries the session's usual order, a named tool (grim, spectacle, gnome-screenshot, maim, import) is used exclusively, or fails if it is not installed. Ignored on macOS.", def.Shot.Tool),
				}},
			},
			SeeAlso: []string{"add", "ocr"},
		},
		run: cmdShot,
	})
}

type shotOptions struct {
	overrides  ai.Overrides
	aiMode     string
	tags       []string
	title      string
	ocr        bool
	noOCR      bool
	copyText   bool
	noCopyText bool
}

func parseShotOptions(args []string) (shotOptions, error) {
	var opt shotOptions
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "-t":
			v, ok := flagValue(args, &i)
			if !ok {
				return opt, fmt.Errorf("-t needs a tag")
			}
			opt.tags = append(opt.tags, v)
		case "--title":
			v, ok := flagValue(args, &i)
			if !ok {
				return opt, fmt.Errorf("--title needs a value")
			}
			opt.title = v
		case "--ai":
			opt.overrides.AI = true
		case "--no-ai":
			opt.overrides.NoAI = true
		case "--model", "--effort", "--ai-mode":
			key := args[i]
			value, ok := flagValue(args, &i)
			if !ok || value == "" {
				return opt, fmt.Errorf("%s needs a value", key)
			}
			switch key {
			case "--model":
				opt.overrides.Model = value
			case "--effort":
				opt.overrides.Effort = value
			case "--ai-mode":
				opt.aiMode = value
			}
		case "--ocr":
			opt.ocr = true
		case "--no-ocr":
			opt.noOCR = true
		case "--copy-text":
			opt.copyText = true
		case "--no-copy-text":
			opt.noCopyText = true
		default:
			key, value, hasValue := strings.Cut(args[i], "=")
			if !hasValue {
				return opt, unknownOption{args[i]}
			}
			if value == "" {
				return opt, fmt.Errorf("%s needs a value", key)
			}
			switch key {
			case "--ai":
				opt.overrides.AI = true
				opt.overrides.Profile = value
			case "--model":
				opt.overrides.Model = value
			case "--effort":
				opt.overrides.Effort = value
			case "--ai-mode":
				opt.aiMode = value
			default:
				return opt, unknownOption{args[i]}
			}
		}
	}
	if opt.aiMode != "" && opt.aiMode != "auto" && opt.aiMode != "wait" && opt.aiMode != "background" {
		return opt, fmt.Errorf("--ai-mode: want auto, wait or background")
	}
	if opt.ocr && opt.noOCR {
		return opt, errors.New("--ocr and --no-ocr cannot be combined")
	}
	if opt.copyText && opt.noCopyText {
		return opt, errors.New("--copy-text and --no-copy-text cannot be combined")
	}
	if opt.noOCR && opt.copyText {
		return opt, errors.New("--no-ocr and --copy-text cannot be combined: there is no text to copy without OCR")
	}
	return opt, nil
}

func cmdShot(inv *invocation) int {
	outOpt, rest, err := output.ParseFlags(inv.refinements)
	if err != nil {
		return inv.misuse("%v", err)
	}
	opt, err := parseShotOptions(rest)
	if err != nil {
		var unknown unknownOption
		if errors.As(err, &unknown) {
			return inv.misuseWord("unknown option ", unknown.opt)
		}
		return inv.misuse("%v", err)
	}
	if len(inv.data) > 0 {
		return inv.misuse("shot takes no words; use --title to name the note")
	}

	env, err := app.Open()
	if err != nil {
		return inv.fail(err)
	}

	plan, err := shot.Prepare(&env.Cfg, opt.overrides, opt.aiMode, shotAsker{inv.ctx}, inv.stderr)
	if err != nil {
		return inv.fail(err)
	}

	data, ext, err := captureScreenshot(inv.ctx, env.Cfg)
	if err != nil {
		if errors.Is(err, platform.ErrCancelled) {
			fmt.Fprintln(inv.stderr, "nn: shot: cancelled")
			return output.ExitOK
		}
		return inv.fail(err)
	}

	var notes []*vault.Note
	if len(opt.tags) > 0 {
		if notes, err = env.Notes(inv.ctx); err != nil {
			return inv.fail(err)
		}
	}
	tags := adjustSimilarTags(inv, opt.tags, notes)

	if plan != nil {
		wantsCopy := wantCopyText(env.Cfg, opt.copyText, opt.noCopyText)
		input := shot.Input{Data: data, Ext: ext, Title: opt.title, Tags: tags,
			OCR: wantOCR(env.Cfg, opt.ocr, opt.noOCR) || (wantsCopy && !opt.noOCR), Engine: ocrEngineFor(env.Cfg), Langs: defaultOCRLangs(env.Cfg)}
		if wantsCopy {
			input.CopyText = func(lines []ocr.Line) { copyRecognizedText(inv.ctx, inv.stderr, lines) }
		}
		note, err := shot.Save(inv.ctx, env, plan, input, inv.stderr)
		if err != nil {
			return inv.fail(err)
		}
		return emitAddResult(inv, outOpt, note.Path, "create")
	}

	where, repo := vault.Context("")
	written, err := env.Vault.CreateWithAssets(vault.NewNote{
		Title: opt.title, Tags: tags, Via: "shot", Where: where, Repo: repo,
		Images: []vault.Image{{Data: data, Ext: ext}}, Now: envNow(env),
	})
	if err != nil {
		return inv.fail(err)
	}
	note := written.Note

	wantsCopyText := wantCopyText(env.Cfg, opt.copyText, opt.noCopyText)
	runOCR := wantOCR(env.Cfg, opt.ocr, opt.noOCR) || (wantsCopyText && !opt.noOCR)
	if runOCR {
		if embed := written.Images; len(embed) == 1 {
			result, oerr := ocr.Ensure(inv.ctx, env.Vault.Root, embed[0], ocrEngineFor(env.Cfg), defaultOCRLangs(env.Cfg))
			switch {
			case oerr != nil:
				fmt.Fprintf(inv.stderr, "nn: warning: ocr %s: %v\n", embed[0], oerr)
			case wantsCopyText:
				copyRecognizedText(inv.ctx, inv.stderr, result.Lines)
			}
		}
	}

	env.PostSave(inv.ctx, note.Path, "create")
	return emitAddResult(inv, outOpt, note.Path, "create")
}

func wantCopyText(cfg config.Config, copyText, noCopyText bool) bool {
	switch {
	case copyText:
		return true
	case noCopyText:
		return false
	default:
		return cfg.Shot.CopyText
	}
}

func copyRecognizedText(ctx context.Context, stderr io.Writer, lines []ocr.Line) {
	parts := make([]string, len(lines))
	for i, l := range lines {
		parts[i] = l.Text
	}
	text := strings.Join(parts, "\n")
	if text == "" {
		fmt.Fprintln(stderr, "nn: shot: no text recognized; clipboard left unchanged")
		return
	}
	if err := captureCopyText(ctx, text); err != nil {
		fmt.Fprintf(stderr, "nn: warning: copy text: %v\n", err)
	}
}

var (
	captureScreenshot = platform.Screenshot
	captureCopyText   = platform.CopyText
)

// Consent remains interactive only in the parent; once is transferred to the worker.
type shotAsker struct{ ctx context.Context }

func (shotAsker) Interactive() bool                     { return app.Interactive() }
func (a shotAsker) Ask(question string) (string, error) { return app.AskContext(a.ctx, question) }
