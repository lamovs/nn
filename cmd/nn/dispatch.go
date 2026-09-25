package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"

	"github.com/lamovs/nn/internal/cli"
	"github.com/lamovs/nn/internal/config"
	"github.com/lamovs/nn/internal/output"
	"github.com/lamovs/nn/internal/shot"
)

const endOfOptions = "--"

func runText(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if len(args) > 0 && args[0] == "__ai" {
		if err := shot.Worker(ctx, args[1:]); err != nil {
			fmt.Fprintf(stderr, "nn: AI worker: %v\n", err)
			return output.ExitError
		}
		return output.ExitOK
	}
	if len(args) == 0 {

		writeIndex(stdout, resolveColor(cli.ColorAuto, false, helpColorMode(), stdout))
		return output.ExitOK
	}

	if len(args) == 1 {
		switch args[0] {
		case "--version":
			return cmdVersion(stdout, stderr, nil)
		case "--help", "-h":
			writeIndex(stdout, resolveColor(cli.ColorAuto, false, helpColorMode(), stdout))
			return output.ExitOK
		}
	}

	verb, rest := args[0], args[1:]
	c, known := lookup(verb)
	if !known {
		if path, ok := lookupExternal(verb); ok {
			return runExternal(ctx, path, rest, stdin, stdout, stderr)
		}
		switch {
		case verb == endOfOptions:

			fmt.Fprintf(stderr, "nn: %s\n", endOfOptionsBeforeTheVerb())
		case strings.HasPrefix(verb, "-"):
			fmt.Fprintln(stderr, quoteWord("nn: unknown flag ", verb))
		default:
			fmt.Fprintln(stderr, quoteWord("nn: unknown command ", verb))
		}
		fmt.Fprint(stderr, "run \"nn\" with no arguments for a list of commands\n")
		return output.ExitError
	}

	data, refinements := splitArgs(rest)
	inv := &invocation{
		ctx:         ctx,
		verb:        c.help.Verb,
		stdin:       stdin,
		stdout:      stdout,
		stderr:      stderr,
		args:        rest,
		data:        data,
		refinements: refinements,
	}

	if wantsHelp(refinements) {
		cli.WriteLines(stdout, c.help.Render(inv.outPalette()))
		return output.ExitOK
	}
	if slices.Contains(refinements, endOfOptions) {
		return inv.misuse("%s", endOfOptionsTooLate(inv.verb))
	}
	return c.run(inv)
}

const (
	endOfOptionsRefusal = `"--" goes in front of the data it protects, and everything after it is ` +
		`read as data - so nothing can follow it as an option, as in:`
	endOfOptionsExample = "  nn add -- -5 min plank"

	endOfOptionsFirst = `"--" goes after the command and in front of the data it protects, so a ` +
		`command line cannot begin with it, as in:`
)

func endOfOptionsTooLate(verb string) string {
	wrapped := cli.Wrap(endOfOptionsRefusal, cli.Width-len("nn: "+verb+": "))
	return strings.Join(append(wrapped, endOfOptionsExample), "\n")
}

func endOfOptionsBeforeTheVerb() string {
	wrapped := cli.Wrap(endOfOptionsFirst, cli.Width-len("nn: "))
	return strings.Join(append(wrapped, endOfOptionsExample), "\n")
}

func splitArgs(args []string) (data, refinements []string) {
	for i, a := range args {
		if a == endOfOptions {
			return append(data, args[i+1:]...), nil
		}
		if a != "-" && strings.HasPrefix(a, "-") {
			return data, args[i:]
		}
		data = append(data, a)
	}
	return data, nil
}

func lookupExternal(verb string) (string, bool) {
	if verb == "" || strings.ContainsAny(verb, "/\\") {
		return "", false
	}
	path, err := exec.LookPath("nn-" + verb)
	if err != nil {
		return "", false
	}
	return path, true
}

func runExternal(ctx context.Context, path string, args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	cmd := exec.CommandContext(ctx, path, args...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = stdin, stdout, stderr
	cmd.Env = externalEnv()

	err := cmd.Run()
	if err == nil {
		return output.ExitOK
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		if code := exitErr.ExitCode(); code >= 0 {
			return code
		}
		return output.ExitInterrupted
	}
	fmt.Fprintf(stderr, "nn: %s: %v\n", filepath.Base(path), err)
	return output.ExitError
}

func externalEnv() []string {
	env := os.Environ()
	filtered := env[:0]
	for _, kv := range env {
		if strings.HasPrefix(kv, "NN_ROOT=") || strings.HasPrefix(kv, "NN_INBOX=") || strings.HasPrefix(kv, "NN_CONFIG=") {
			continue
		}
		filtered = append(filtered, kv)
	}
	if cfg, _, err := config.Load(); err == nil {
		filtered = append(filtered, "NN_ROOT="+cfg.Vault.Root, "NN_INBOX="+cfg.Vault.Inbox, "NN_CONFIG="+cfg.Path)
	}
	return filtered
}
