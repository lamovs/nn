package main

import (
	"github.com/lamovs/nn/internal/cli"
	"github.com/lamovs/nn/internal/output"
)

func init() {
	register(command{
		help: cli.Help{
			Verb:    "help",
			Summary: "show what a command does, with examples",
			Examples: []cli.Example{
				{Cmd: "nn help", What: "list every command"},
				{Cmd: "nn help add", What: "the examples for one command"},
				{Cmd: "nn add --help", What: "the same thing, from where you are"},
			},
		},
		run: cmdHelp,
	})
}

func cmdHelp(inv *invocation) int {
	if len(inv.refinements) > 0 {
		return inv.misuseWord("unknown option ", inv.refinements[0])
	}
	switch len(inv.data) {
	case 0:
		if err := writeIndex(inv.stdout, inv.outPalette()); err != nil {
			return inv.fail(err)
		}
		return output.ExitOK
	case 1:
		c, ok := lookup(inv.data[0])
		if !ok {
			return inv.misuseWord("no command called ", inv.data[0])
		}
		if err := cli.WriteLines(inv.stdout, c.help.Render(inv.outPalette())); err != nil {
			return inv.fail(err)
		}
		return output.ExitOK
	default:
		return inv.misuse("one command at a time")
	}
}
