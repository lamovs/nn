package main

import (
	"fmt"
	"strings"

	"github.com/lamovs/nn/internal/app"
	"github.com/lamovs/nn/internal/cli"
	"github.com/lamovs/nn/internal/output"
	"github.com/lamovs/nn/internal/platform"
)

func init() {
	register(command{
		help: cli.Help{
			Verb:    "open",
			Summary: "open a note in Obsidian",
			Examples: []cli.Example{
				{Cmd: "nn open docker cleanup", What: "opens the note through an obsidian:// URI"},
				{Cmd: "nn open docker-cleanup-notes --print", What: "prints the URI instead of opening it"},
			},
			SeeAlso: []string{"edit", "show"},
		},
		run: cmdOpen,
	})
}

func cmdOpen(inv *invocation) int {
	print := false
	for _, r := range inv.refinements {
		if r != "--print" {
			return inv.misuseWord("unknown option ", r)
		}
		print = true
	}
	ref := strings.TrimSpace(strings.Join(inv.data, " "))
	if ref == "" {
		return inv.misuse("a note is required")
	}

	env, err := app.Open()
	if err != nil {
		return inv.fail(err)
	}
	rel, err := env.Resolve(inv.ctx, ref)
	if err != nil {
		return resolveFailure(inv, ref, err)
	}

	uri := platform.ObsidianURI(env.Vault.Abs(rel))
	if print {
		fmt.Fprintln(inv.stdout, uri)
		return output.ExitOK
	}
	if err := openURL(inv.ctx, uri); err != nil {
		return inv.fail(err)
	}
	env.RecordOpen(rel)
	return output.ExitOK
}

var openURL = platform.OpenURL
