package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"slices"
	"strconv"

	"github.com/lamovs/nn/internal/cli"
	"github.com/lamovs/nn/internal/config"
	"github.com/lamovs/nn/internal/output"
)

type command struct {
	help cli.Help

	run func(inv *invocation) int
}

var commands = map[string]command{}

var aliases = map[string]string{}

func register(c command) {
	if c.help.Verb == "" {
		panic("nn: a command was registered with no verb")
	}
	if _, dup := commands[c.help.Verb]; dup {
		panic("nn: two commands registered as " + c.help.Verb)
	}
	commands[c.help.Verb] = c

	if c.help.Alias == "" {
		return
	}
	if _, dup := commands[c.help.Alias]; dup {
		panic("nn: alias " + c.help.Alias + " collides with a command name")
	}
	if other, dup := aliases[c.help.Alias]; dup {
		panic("nn: alias " + c.help.Alias + " is already used by " + other)
	}
	aliases[c.help.Alias] = c.help.Verb
}

func lookup(verb string) (command, bool) {
	if c, ok := commands[verb]; ok {
		return c, true
	}
	if target, ok := aliases[verb]; ok {
		return commands[target], true
	}
	return command{}, false
}

type invocation struct {
	ctx    context.Context
	verb   string
	stdin  io.Reader
	stdout io.Writer
	stderr io.Writer

	args        []string
	data        []string
	refinements []string
}

func (inv *invocation) outPalette() cli.Palette {
	return resolveColor(cli.ColorAuto, false, helpColorMode(), inv.stdout)
}

func resolveColor(mode cli.ColorMode, colorSet bool, cfgColor cli.ColorMode, w io.Writer) cli.Palette {
	fileMode := cfgColor
	if colorSet {
		fileMode = cli.ColorAuto
	}
	return cli.PaletteFor(mode, fileMode, w)
}

func helpColorMode() cli.ColorMode {
	cfg, _, _ := config.Load()
	return cli.ColorMode(cfg.Output.Color)
}

func schemaEnum(key string) []string {
	k, err := config.Lookup(key)
	if err != nil {
		panic("nn: schemaEnum: " + err.Error())
	}
	if k.Type != config.TypeEnum {
		panic("nn: schemaEnum: " + key + " is not an enum")
	}
	return slices.Clone(k.Enum)
}

func (inv *invocation) fail(err error) int {
	fmt.Fprintf(inv.stderr, "nn: %s: %v\n", inv.verb, err)
	return output.ExitError
}

func (inv *invocation) misuse(format string, a ...any) int {
	fmt.Fprintf(inv.stderr, "nn: %s: %s\n", inv.verb, fmt.Sprintf(format, a...))
	fmt.Fprintf(inv.stderr, "run \"nn help %s\" for examples\n", inv.verb)
	return output.ExitError
}

func (inv *invocation) misuseWord(before, word string) int {
	return inv.misuse("%s%s", before, strconv.Quote(word))
}

func quoteWord(before, word string) string {
	return before + strconv.Quote(word)
}

func interrupted(ctx context.Context) bool {
	return errors.Is(ctx.Err(), context.Canceled)
}

func (inv *invocation) interrupted() bool { return interrupted(inv.ctx) }

const (
	indexIntro  = "nn: capture and search notes in your Obsidian vault"
	indexFooter = `Run "nn help <command>" for examples. Start with "nn add", then "nn s" to search.`
)

var indexOrder = []struct {
	title string
	verbs []string
}{
	{"Capture", []string{"add", "shot", "url", "edit", "snip"}},
	{"Find and read", []string{"s", "ask", "digest", "triage", "ls", "show", "cat", "code", "open"}},
	{"Links and tags", []string{"links", "backlinks", "graph", "tags", "stats"}},
	{"Images", []string{"ocr"}},
	{"Text processing", []string{"ai", "last"}},
	{"Setup and maintenance", []string{"doctor", "setup", "config", "help", "version"}},
}

func indexGroups() []cli.IndexGroup {
	placed := map[string]bool{}
	groups := make([]cli.IndexGroup, 0, len(indexOrder)+1)
	for _, g := range indexOrder {
		group := cli.IndexGroup{Title: g.title}
		for _, verb := range g.verbs {
			c, ok := commands[verb]
			if !ok {
				continue
			}
			placed[verb] = true
			group.Verbs = append(group.Verbs, c.help)
		}
		groups = append(groups, group)
	}

	var rest []string
	for verb := range commands {
		if !placed[verb] {
			rest = append(rest, verb)
		}
	}
	if len(rest) > 0 {

		slices.Sort(rest)
		group := cli.IndexGroup{Title: "Other"}
		for _, verb := range rest {
			group.Verbs = append(group.Verbs, commands[verb].help)
		}
		groups = append(groups, group)
	}
	return groups
}

func writeIndex(w io.Writer, p cli.Palette) error {
	return cli.WriteLines(w, cli.RenderIndex(indexIntro, indexGroups(), indexFooter, p))
}

var helpOptions = []string{"--help", "-h"}

func wantsHelp(refinements []string) bool {
	for _, r := range refinements {
		if slices.Contains(helpOptions, r) {
			return true
		}
	}
	return false
}
