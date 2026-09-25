package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"syscall"
	"unicode/utf8"

	"github.com/lamovs/nn/internal/ai"
	"github.com/lamovs/nn/internal/app"
	"github.com/lamovs/nn/internal/capture"
	"github.com/lamovs/nn/internal/cli"
	"github.com/lamovs/nn/internal/config"
	"github.com/lamovs/nn/internal/noteai"
	"github.com/lamovs/nn/internal/output"
	"github.com/lamovs/nn/internal/vault"
)

func init() {
	register(command{help: cli.Help{
		Verb: "last", Summary: "explain a shell command provided on stdin",
		Examples: []cli.Example{
			{Cmd: `nn-last --ai`, What: "explain the previous command from the current zsh session"},
			{Cmd: `nn-last --ai --output error.log --save`, What: "include an explicit output file and save the analysis"},
			{Cmd: `printf '%s' 'docker builder prune' | nn last`, What: "provide command text directly"},
		},
		Sections: []cli.HelpSection{
			{Title: "Input and output", Items: []string{
				"Command text comes only from stdin. nn does not read shell history, execute commands or collect their output. --output FILE adds a regular UTF-8 text file; an empty file is distinct from omitted output.",
				"The analysis goes to stdout. Without output, the model cannot establish why a command failed. A vault is required only for --save, which creates one note with the analysis and literal source text; its path goes to stderr.",
				"nn-last without arguments retains its existing behavior of saving the previous command as a code note. New helper options require --ai or --ai=PROFILE.",
			}},
			{Title: "AI and limits", Items: []string{
				"--ai[=PROFILE], --model MODEL and --effort low|medium|high|max select the last task profile. ai.tasks.last.prompt_file replaces the built-in prompt. The command waits regardless of ai.mode.",
				"Configure consent beforehand with nn setup ai. Likely secrets are rejected unless --allow-secret is explicit; that flag does not bypass engine consent.",
				"Input must be UTF-8 without NUL bytes, and the command must be nonblank. Each input and their complete serialized request are bounded by 256 KiB, without truncation. One profile timeout covers reads and the model call.",
			}},
			{Title: "Exit status", Items: []string{"0: analysis, optionally saved; 2: input, consent, model or save failure; 130: cancelled. A save failure preserves the successful analysis on stdout; earlier failures leave it empty."}},
		}, SeeAlso: []string{"ai", "add", "setup"},
	}, run: cmdLast})
}

type lastOptions struct {
	ai                                ai.Overrides
	output                            string
	outputProvided, save, allowSecret bool
}

func parseLastOptions(args []string) (lastOptions, error) {
	opt := lastOptions{}
	var modelArgs []string
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch arg {
		case "--save":
			opt.save = true
		case "--allow-secret":
			opt.allowSecret = true
		default:
			key, value, assigned := strings.Cut(arg, "=")
			if key == "--output" {
				if !assigned {
					var ok bool
					value, ok = flagValue(args, &i)
					if !ok {
						return opt, errors.New("--output needs a file")
					}
				}
				if value == "" {
					return opt, errors.New("--output needs a file")
				}
				opt.output, opt.outputProvided = value, true
			} else {
				modelArgs = append(modelArgs, arg)
				if (key == "--model" || key == "--effort") && !assigned && i+1 < len(args) {
					i++
					modelArgs = append(modelArgs, args[i])
				}
			}
		}
	}
	var err error
	opt.ai, err = parseAskOptions(modelArgs)
	return opt, err
}

type lastPayload struct {
	Command        string `json:"command"`
	Output         string `json:"output"`
	OutputProvided bool   `json:"output_provided"`
}

func cmdLast(inv *invocation) int {
	opt, err := parseLastOptions(inv.refinements)
	if err != nil {
		return inv.misuse("%v", err)
	}
	if len(inv.data) != 0 {
		return inv.misuse("provide command text only on stdin")
	}
	if filterIsTerminal(inv.stdin) {
		return inv.misuse("provide command text through nn-last --ai, a pipe or a file")
	}
	fail := func(err error) int {
		if inv.interrupted() || errors.Is(err, context.Canceled) {
			fmt.Fprintln(inv.stderr, "nn: last: cancelled")
			return output.ExitInterrupted
		}
		if errors.Is(err, context.DeadlineExceeded) {
			return inv.fail(errors.New("timed out while reading input or waiting for the model"))
		}
		return inv.fail(err)
	}
	if err := inv.ctx.Err(); err != nil {
		return fail(err)
	}
	var cfg config.Config
	var env *app.Env
	if opt.save {
		env, err = app.Open()
		if err != nil {
			return fail(err)
		}
		cfg = env.Cfg
	} else {
		var problems []config.Problem
		cfg, problems, err = config.Load()
		var p config.Problem
		missingRoot := errors.As(err, &p) && ((p.Kind == config.KindRequired && p.Key == "vault.root") || (p.Kind == config.KindMoved && p.Key == "root" && cfg.Vault.Root == ""))
		if err != nil && !missingRoot {
			return fail(err)
		}
		app.WarnConfig(inv.stderr, problems)
	}
	call, err := ai.Resolve(cfg, "last", opt.ai)
	if err != nil {
		return fail(err)
	}
	ctx, cancel := context.WithTimeout(inv.ctx, call.Profile.Timeout)
	defer cancel()
	prompt, err := ai.Prompt(cfg, "last")
	if err != nil {
		return fail(err)
	}
	command, err := readFilterInput(ctx, inv.stdin)
	if err != nil {
		return fail(err)
	}
	if err := validateLastText(command, true); err != nil {
		return fail(err)
	}
	data := lastPayload{Command: string(command), OutputProvided: opt.outputProvided}
	if opt.outputProvided {
		data.Output, err = readLastOutput(ctx, opt.output)
		if err != nil {
			return fail(err)
		}
	}
	if !opt.allowSecret && (len(capture.FindSecrets(data.Command)) > 0 || len(capture.FindSecrets(data.Output)) > 0) {
		return fail(errors.New("input may contain credentials; review it and use --allow-secret to allow sending it"))
	}
	payload, err := json.Marshal(data)
	if err != nil {
		return fail(err)
	}
	req := ai.Request{System: prompt, Text: string(payload)}
	if err := ai.ValidateRequest(req); err != nil {
		return fail(err)
	}
	decision, approval, err := ai.Approve(&cfg, call, "the previous command you provide and its optional output", filterAsker{}, inv.stderr)
	if err != nil {
		return fail(err)
	}
	if decision != ai.Allowed {
		return fail(errors.New("AI needs consent; configure it beforehand with nn setup ai"))
	}
	result, err := ai.Run(ctx, approval, req)
	if err != nil {
		return fail(err)
	}
	if err := ctx.Err(); err != nil {
		return fail(err)
	}
	if result.Last == nil {
		return fail(errors.New("model did not return a last result"))
	}
	if _, err := io.WriteString(inv.stdout, result.Last.Body); err != nil {
		return fail(err)
	}
	if opt.save {
		if err := saveLast(ctx, inv, env, data, result.Last); err != nil {
			return fail(err)
		}
	}
	return output.ExitOK
}

func validateLastText(data []byte, command bool) error {
	if !utf8.Valid(data) || bytes.IndexByte(data, 0) >= 0 {
		return errors.New("input must be UTF-8 text without NUL bytes")
	}
	if command && strings.TrimSpace(string(data)) == "" {
		return errors.New("stdin must contain a nonblank command")
	}
	return nil
}

func readLastOutput(ctx context.Context, path string) (string, error) {
	// Open nonblocking before fstat: a FIFO path must never wait for a writer.
	file, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return "", errors.New("cannot open --output file")
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return "", errors.New("--output must be a regular text file")
	}
	data, err := readFilterInput(ctx, file)
	if err != nil {
		return "", fmt.Errorf("read --output: %w", err)
	}
	if err := validateLastText(data, false); err != nil {
		return "", fmt.Errorf("--output: %w", err)
	}
	return string(data), nil
}

func lastFence(text, language string) string {
	longest, run := 0, 0
	for _, r := range text {
		if r == '`' {
			run++
			if run > longest {
				longest = run
			}
		} else {
			run = 0
		}
	}
	if longest < 2 {
		longest = 2
	}
	fence := strings.Repeat("`", longest+1)
	// Always add a separator newline; all source bytes remain inside the fence.
	return fence + language + "\n" + text + "\n" + fence
}

func saveLast(ctx context.Context, inv *invocation, env *app.Env, data lastPayload, reply *ai.LastReply) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	vocabulary, err := noteai.Vocabulary(ctx, env)
	if err != nil {
		return fmt.Errorf("save analysis: %w", err)
	}
	title := noteai.Title(reply.Title)
	if title == "" {
		title = "Command analysis"
	}
	body := "## Command\n\n" + lastFence(data.Command, "sh")
	if data.OutputProvided {
		body += "\n\n## Supplied output\n\n" + lastFence(data.Output, "text")
	}
	body += "\n\n## Analysis\n\n" + reply.Body
	if err := ctx.Err(); err != nil {
		return err
	}
	note, err := env.Vault.Create(vault.NewNote{Title: title, Body: body, TrailingTags: noteai.Keywords(reply.Tags, vocabulary), Via: "stdin", Now: envNow(env)})
	if err != nil {
		return fmt.Errorf("save analysis: %w", err)
	}
	env.PostSave(ctx, note.Path, "create")
	_, err = fmt.Fprintln(inv.stderr, note.Path)
	return err
}
