package main

import (
	"errors"
	"strconv"
	"strings"

	"github.com/lamovs/nn/internal/app"
	"github.com/lamovs/nn/internal/cli"
	"github.com/lamovs/nn/internal/output"
	"github.com/lamovs/nn/internal/vault"
)

func init() {
	register(command{
		help: cli.Help{
			Verb:    "edit",
			Alias:   "e",
			Summary: "open a note in your editor",
			Examples: []cli.Example{
				{Cmd: "nn edit docker cleanup", What: "resolves the best match and opens it in VISUAL or EDITOR"},
				{Cmd: "nn edit", What: "opens the most recently modified note in the inbox"},
				{Cmd: "nn edit docker-cleanup-notes -l 12", What: "opens at line 12, for editors that support it"},
			},
			SeeAlso: []string{"add", "show", "open"},
		},
		run: cmdEdit,
	})
}

func cmdEdit(inv *invocation) int {
	line := 0
	for i := 0; i < len(inv.refinements); i++ {
		if inv.refinements[i] != "-l" {
			return inv.misuseWord("unknown option ", inv.refinements[i])
		}
		if i+1 >= len(inv.refinements) {
			return inv.misuse("-l needs a line number")
		}
		i++
		n, err := strconv.Atoi(inv.refinements[i])
		if err != nil || n <= 0 {
			return inv.misuse("-l wants a positive line number, got %q", inv.refinements[i])
		}
		line = n
	}

	env, err := app.Open()
	if err != nil {
		return inv.fail(err)
	}

	ref := strings.TrimSpace(strings.Join(inv.data, " "))
	var rel string
	if ref == "" {
		rel, err = lastModifiedInInbox(inv, env)
		if err != nil {
			return inv.fail(err)
		}
	} else {
		rel, err = env.Resolve(inv.ctx, ref)
		if err != nil {
			return resolveFailure(inv, ref, err)
		}
	}

	if err := app.Edit(inv.ctx, env, env.Vault.Abs(rel), line); err != nil {
		return inv.fail(err)
	}
	env.RecordOpen(rel)
	return output.ExitOK
}

// lastModifiedInInbox is what "nn edit" opens when given no note.
func lastModifiedInInbox(inv *invocation, env *app.Env) (string, error) {
	notes, err := env.Notes(inv.ctx)
	if err != nil {
		return "", err
	}
	var best *vault.Note
	for _, n := range notes {
		if !n.InInbox {
			continue
		}
		if best == nil || n.Modified.After(best.Modified) {
			best = n
		}
	}
	if best == nil {
		return "", errors.New("the inbox has no notes yet")
	}
	return best.Path, nil
}
