package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"runtime"
	"strings"
	"syscall"
	"unicode/utf8"

	"github.com/lamovs/nn/internal/ai"
	"github.com/lamovs/nn/internal/app"
	"github.com/lamovs/nn/internal/cli"
	"github.com/lamovs/nn/internal/config"
	"github.com/lamovs/nn/internal/output"
)

func init() {
	register(command{help: cli.Help{
		Verb: "ai", Summary: "transform piped text with an AI instruction",
		Examples: []cli.Example{
			{Cmd: `cat draft.md | nn ai "Reduce to five bullet points"`, What: "print the transformed text"},
			{Cmd: `nn ai "Translate into English" < draft.txt > translated.txt`, What: "read a file and redirect the result"},
			{Cmd: `cat draft.md | nn ai -- -keep the original structure`, What: "protect instruction words starting with a dash"},
		},
		Sections: []cli.HelpSection{
			{Title: "Input and output", Items: []string{
				"Give the instruction as command-line words and the text on stdin through a pipe or file. Blank, invalid UTF-8, NUL-containing or oversized input is rejected without truncation.",
				"Only the transformed text is printed to stdout, preserving the model's whitespace and final newline, including an empty result. Errors go to stderr. A vault is not required; notes, links, state and configuration are not changed.",
			}},
			{Title: "AI and limits", Items: []string{
				"--ai[=PROFILE] selects a profile; --model MODEL and --effort low|medium|high|max override it. The filter task explicitly requests AI. ai.tasks.filter.prompt_file replaces its built-in prompt.",
				"The command waits regardless of ai.mode. Configure consent beforehand with nn setup ai; piped text is never read as consent. One profile timeout covers reading stdin and the model call.",
				"Stdin is limited to 256 KiB, and the serialized instruction plus input must also fit the shared 256 KiB request limit. JSON escaping counts toward that limit.",
			}},
			{Title: "Exit status", Items: []string{"0: transformed text; 2: invalid input, missing consent, timeout or model failure; 130: cancelled. Failures before output leave stdout empty."}},
		},
		SeeAlso: []string{"ask", "setup"},
	}, run: cmdAI})
}

// filterAsker never falls back to the process's own stdin for approval.
type filterAsker struct{}

func (filterAsker) Interactive() bool { return false }
func (filterAsker) Ask(string) (string, error) {
	return "", errors.New("configure AI consent with nn setup ai")
}

var filterIsTerminal = cli.IsTerminal

func cmdAI(inv *invocation) int {
	opt, err := parseAskOptions(inv.refinements)
	if err != nil {
		return inv.misuse("%v", err)
	}
	instruction := strings.Join(inv.data, " ")
	if strings.TrimSpace(instruction) == "" {
		return inv.misuse("an instruction is required")
	}
	if !utf8.ValidString(instruction) || strings.ContainsRune(instruction, 0) {
		return inv.misuse("instruction must be UTF-8 text without NUL bytes")
	}
	if len(instruction) > ai.MaxTextBytes {
		return inv.misuse("instruction exceeds %d bytes", ai.MaxTextBytes)
	}
	if filterIsTerminal(inv.stdin) {
		return inv.misuse("read text from a pipe or file, for example: cat draft.md | nn ai \"Summarize\"")
	}
	fail := func(err error) int {
		if inv.interrupted() || errors.Is(err, context.Canceled) {
			fmt.Fprintln(inv.stderr, "nn: ai: cancelled")
			return output.ExitInterrupted
		}
		if errors.Is(err, context.DeadlineExceeded) {
			return inv.fail(errors.New("timed out while reading stdin or waiting for the model"))
		}
		return inv.fail(err)
	}
	if err := inv.ctx.Err(); err != nil {
		return fail(err)
	}
	cfg, problems, err := config.Load()
	var p config.Problem
	missingRoot := errors.As(err, &p) && ((p.Kind == config.KindRequired && p.Key == "vault.root") || (p.Kind == config.KindMoved && p.Key == "root" && cfg.Vault.Root == ""))
	if err != nil && !missingRoot {
		return fail(err)
	}
	app.WarnConfig(inv.stderr, problems)
	call, err := ai.Resolve(cfg, "filter", opt)
	if err != nil {
		return fail(err)
	}
	ctx, cancel := context.WithTimeout(inv.ctx, call.Profile.Timeout)
	defer cancel()
	prompt, err := ai.Prompt(cfg, "filter")
	if err != nil {
		return fail(err)
	}
	data, err := readFilterInput(ctx, inv.stdin)
	if err != nil {
		return fail(err)
	}
	if !utf8.Valid(data) || bytes.IndexByte(data, 0) >= 0 {
		return fail(errors.New("stdin must be UTF-8 text without NUL bytes"))
	}
	if strings.TrimSpace(string(data)) == "" {
		return fail(errors.New("stdin must contain nonblank text"))
	}
	payload, err := json.Marshal(struct {
		Instruction string `json:"instruction"`
		Input       string `json:"input"`
	}{instruction, string(data)})
	if err != nil {
		return fail(err)
	}
	req := ai.Request{System: prompt, Text: string(payload)}
	if err := ai.ValidateRequest(req); err != nil {
		return fail(err)
	}
	decision, approval, err := ai.Approve(&cfg, call, "the instruction and text from stdin", filterAsker{}, inv.stderr)
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
	if result.Filter == nil {
		return fail(errors.New("model did not return a filter result"))
	}
	if _, err := io.WriteString(inv.stdout, result.Filter.Text); err != nil {
		return fail(err)
	}
	return output.ExitOK
}

// readFilterInput polls file readiness since inherited stdin may not support Go read deadlines.
func readFilterInput(ctx context.Context, input io.Reader) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	var r io.Reader
	switch v := input.(type) {
	case *os.File:
		r = filterFileReader{ctx: ctx, file: v}
	case *strings.Reader, *bytes.Reader, *bytes.Buffer:
		r = v
	case *io.PipeReader:
		stop := context.AfterFunc(ctx, func() { _ = v.CloseWithError(ctx.Err()) })
		defer stop()
		r = v
	default:
		return nil, errors.New("stdin must be a file or pipe")
	}
	data, err := io.ReadAll(io.LimitReader(r, ai.MaxTextBytes+1))
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if err != nil {
		return nil, fmt.Errorf("read stdin: %w", err)
	}
	if len(data) > ai.MaxTextBytes {
		return nil, fmt.Errorf("stdin exceeds %d bytes", ai.MaxTextBytes)
	}
	return data, nil
}

type filterFileReader struct {
	ctx  context.Context
	file *os.File
}

func (r filterFileReader) Read(p []byte) (int, error) {
	for {
		if err := r.ctx.Err(); err != nil {
			return 0, err
		}
		ready, err := filterFileReady(int(r.file.Fd()))
		if errors.Is(err, syscall.EINTR) {
			continue
		}
		if err != nil {
			return 0, err
		}
		if !ready {
			continue
		}
		n, err := syscall.Read(int(r.file.Fd()), p)
		runtime.KeepAlive(r.file)
		if n < 0 {
			n = 0
		}
		if errors.Is(err, syscall.EINTR) || errors.Is(err, syscall.EAGAIN) {
			continue
		}
		if n == 0 && err == nil {
			return 0, io.EOF
		}
		return n, err
	}
}
