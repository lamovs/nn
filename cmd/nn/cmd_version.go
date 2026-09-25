package main

import (
	"fmt"
	"io"

	"github.com/lamovs/nn/internal/cli"
	"github.com/lamovs/nn/internal/output"
)

func init() {
	register(command{
		help: cli.Help{
			Verb:    "version",
			Summary: "print the nn version",
			Examples: []cli.Example{
				{Cmd: "nn version", What: "the version this binary was built at"},
				{Cmd: "nn --version", What: "the same thing"},
			},
		},
		run: func(inv *invocation) int {
			return cmdVersion(inv.stdout, inv.stderr, inv.args)
		},
	})
}

func cmdVersion(stdout, stderr io.Writer, args []string) int {
	if len(args) != 0 {
		fmt.Fprintln(stderr, quoteWord("nn: version: unknown argument ", args[0]))
		fmt.Fprint(stderr, "run \"nn help version\" for examples\n")
		return output.ExitError
	}
	fmt.Fprintf(stdout, "nn version %s\n", version)
	return output.ExitOK
}
