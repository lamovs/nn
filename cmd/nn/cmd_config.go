package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/lamovs/nn/internal/app"
	"github.com/lamovs/nn/internal/cli"
	"github.com/lamovs/nn/internal/config"
	"github.com/lamovs/nn/internal/output"
	"github.com/lamovs/nn/internal/state"
)

func init() {
	register(command{
		help: cli.Help{
			Verb:    "config",
			Summary: "show or change configuration",
			Examples: []cli.Example{
				{Cmd: "nn config", What: "lists every key with its value and where the value came from"},
				{Cmd: "nn config search.limit", What: "prints one key's value"},
				{Cmd: "nn config vault.root ~/Notes", What: "writes the vault location to the config file"},
				{Cmd: "nn config ls.sort title", What: "changes one key, leaving the rest of the file as it was"},
				{Cmd: "nn config --defaults", What: "prints a complete config.toml with every default, documented"},
				{Cmd: "nn config --json", What: "lists every key as JSON: key, value, source"},
			},
			Sections: []cli.HelpSection{
				{Title: "Notes", Items: []string{
					"The source of a value is default, file, or env NAME when an environment variable overrides the file.",
					"Lists, such as ocr.langs, are edited in the file by hand.",
					"A key the file sets in a form nn does not edit (a dotted key, an inline table, a multi-line string) is not written: the line to add by hand goes to stderr, and the exit code is 1.",
					"Reading works before vault.root is set: the values are shown and the missing root goes to stderr. A file that does not parse stops it.",
					"A config file that is a symlink stays one: the file it points at is edited, and keeps its permissions.",
					"--json and --tsv are for reading; setting a key or --defaults takes neither.",
					"A value is written without the spaces around it: nn config ls.sort \" title \" writes \"title\".",
				}},
			},
			SeeAlso: []string{"doctor", "setup"},
		},
		run: cmdConfig,
	})
}

func cmdConfig(inv *invocation) int {
	outOpt, rest, err := output.ParseFlags(inv.refinements)
	if err != nil {
		return inv.misuse("%v", err)
	}
	defaults := false
	for _, r := range rest {
		if r != "--defaults" {
			return inv.misuseWord("unknown option ", r)
		}
		defaults = true
	}

	if form := formFlag(outOpt); form != "" {
		switch {
		case defaults:
			return inv.misuse("%s does not apply to --defaults", form)
		case len(inv.data) > 1:
			return inv.misuse("%s does not apply to setting a key", form)
		}
	}

	switch {
	case defaults:
		if len(inv.data) > 0 {
			return inv.misuse("--defaults takes no key")
		}
		if _, err := inv.stdout.Write(config.DefaultFile()); err != nil {
			return inv.fail(err)
		}
		return output.ExitOK
	case len(inv.data) == 0:
		cfg, ok := loadConfig(inv)
		if !ok {
			return output.ExitError
		}
		return emitConfigEntries(inv, outOpt, cfg)
	case len(inv.data) == 1:
		return getConfigKey(inv, outOpt, inv.data[0])
	default:
		// The value is every word after the key, so a path with spaces needs no quotes.
		return setConfigKey(inv, inv.data[0], strings.Join(inv.data[1:], " "))
	}
}

// formFlag names the output-form flag outOpt asks for, or "" for none.
func formFlag(outOpt output.Options) string {
	switch {
	case outOpt.JSON:
		return "--json"
	case outOpt.TSV:
		return "--tsv"
	case outOpt.Paths:
		return "--paths"
	case outOpt.NUL:
		return "-0"
	case outOpt.Format != "":
		return "--format"
	}
	return ""
}

// loadConfig, unlike every other command, does not stop on a missing vault.root: it shows values and warns instead.
func loadConfig(inv *invocation) (config.Config, bool) {
	cfg, problems, err := config.Load()
	var fatal config.Problem
	if errors.As(err, &fatal) && fatal.Kind != config.KindSyntax {
		app.WarnConfig(inv.stderr, problems)
		fmt.Fprintf(inv.stderr, "nn: warning: config: %v\n", err)
		return cfg, true
	}
	if err != nil {
		inv.fail(err)
		return cfg, false
	}
	app.WarnConfig(inv.stderr, problems)
	return cfg, true
}

func getConfigKey(inv *invocation, outOpt output.Options, key string) int {
	if _, err := config.Lookup(key); err != nil {
		return inv.misuse("%v", err)
	}
	cfg, ok := loadConfig(inv)
	if !ok {
		return output.ExitError
	}
	entry, err := cfg.Get(key)
	var notSet *config.NotSetError
	if errors.As(err, &notSet) {
		fmt.Fprintf(inv.stderr, "nn: %s: %v\n", inv.verb, err)
		return output.ExitNotFound
	}
	if err != nil {
		return inv.fail(err)
	}
	err = output.Emit(inv.stdout, outOpt, []config.Entry{entry}, output.Spec[config.Entry]{
		Text: func(w io.Writer, rows []config.Entry) error {
			for _, r := range rows {
				for _, line := range plainValueLines(r.Value) {
					if _, err := fmt.Fprintln(w, line); err != nil {
						return err
					}
				}
			}
			return nil
		},
		TSVHeader: configTSVHeader,
		TSV:       configTSV,
	})
	if err != nil {
		return inv.fail(err)
	}
	return output.ExitOK
}

func setConfigKey(inv *invocation, key, value string) int {
	written, err := config.Assignment(key, value)
	if err != nil {
		return inv.fail(err)
	}
	err = config.Set(key, value)
	var manual *config.ManualEditError
	switch {
	case errors.As(err, &manual):
		fmt.Fprintf(inv.stderr, "nn: %s: %v\n", inv.verb, err)
		fmt.Fprintf(inv.stderr, "set it by hand, under [%s]:\n%s\n", manual.Section, manual.Line)
		return 1
	case err != nil:
		return inv.fail(err)
	}

	if k, _ := config.Lookup(key); k.Env != "" && strings.TrimSpace(os.Getenv(k.Env)) != "" {
		fmt.Fprintf(inv.stderr, "nn: %s: %s is set and overrides %s\n", inv.verb, k.Env, key)
	}
	// A config that still cannot load (vault.root not set yet) is only a warning here; the write already happened.
	_, problems, err := config.Load()
	if err != nil {
		fmt.Fprintf(inv.stderr, "nn: warning: config: %v\n", err)
	} else {
		app.WarnConfig(inv.stderr, problems)
	}
	fmt.Fprintln(inv.stdout, written)
	return output.ExitOK
}

var configTSVHeader = []string{"key", "value", "source"}

func configTSV(e config.Entry) []string {
	return []string{e.Key, strings.Join(plainValueLines(e.Value), ","), string(e.Source)}
}

func emitConfigEntries(inv *invocation, outOpt output.Options, cfg config.Config) int {
	entries := cfg.Entries()
	err := output.Emit(inv.stdout, outOpt, entries, output.Spec[config.Entry]{
		Text: func(w io.Writer, rows []config.Entry) error {
			statePath, _ := state.Path()
			lines := []string{"# config file: " + cfg.Path}
			if statePath != "" {
				lines = append(lines, "# state file: "+statePath)
			}
			width := 0
			for _, r := range rows {
				width = max(width, len(r.Key))
			}
			for _, r := range rows {
				lines = append(lines, fmt.Sprintf("%-*s = %s  # %s", width, r.Key, r.TOML(), r.Source))
			}
			return cli.WriteLines(w, lines)
		},
		TSVHeader: configTSVHeader,
		TSV:       configTSV,
	})
	if err != nil {
		return inv.fail(err)
	}
	return output.ExitOK
}

// plainValueLines renders a value for a script: a string as it is, a list one item per line.
func plainValueLines(v any) []string {
	switch x := v.(type) {
	case []string:
		return x
	case float64:
		return []string{fmt.Sprint(x)}
	}
	return []string{fmt.Sprint(v)}
}
