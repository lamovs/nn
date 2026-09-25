package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/lamovs/nn/internal/cli"
	"github.com/lamovs/nn/internal/install"
	"github.com/lamovs/nn/internal/output"
)

func init() {
	register(command{
		help: cli.Help{
			Verb:    "setup",
			Summary: "print zsh integration, install the binary, or set up AI consent",
			Examples: []cli.Example{
				{Cmd: "nn setup zsh", What: "prints the zsh snippet to add to ~/.zshrc"},
				{Cmd: "nn setup zsh >> ~/.zshrc", What: "appends it directly"},
				{Cmd: "./nn setup install", What: "installs nn into ~/.local/bin (and nn-vision, if bundled)"},
				{Cmd: "nn setup ai", What: "shows claude, codex and your command profiles, and asks which of them nn may send to"},
			},
			Sections: []cli.HelpSection{
				{Title: "zsh", Items: []string{
					"Adds nn-last (capture the previous shell command as a code note), a ZLE widget for nn snip, and completion for commands, tags and notes.",
					"No key binding is set; the printed fragment has a commented-out bindkey line to uncomment and choose your own.",
				}},
				{Title: "ai", Items: []string{
					"Lists claude, codex and every profile with engine command: where its program is, its consent (ai.consent.KEY) and what nn sends it.",
					"Asks about each program it finds: always, never, or skip to leave the consent as it is; an empty answer skips too. It asks again after an answer it does not know, three times in all, and then leaves the consent as it is. A program it does not find is not asked about.",
					"Without a terminal it asks nothing: each program found whose consent is not always yet gets how to allow it - the nn config command, or for a profile name TOML has to quote, the line to add by hand - and the exit code is 2.",
					"A consent it cannot write in place goes to stderr as the line to add by hand; the rest are still asked, and the exit code is 1.",
					"Works before vault.root is set, like nn config.",
				}},
			},
			SeeAlso: []string{"doctor", "config"},
		},
		run: cmdSetup,
	})
}

func cmdSetup(inv *invocation) int {
	if len(inv.refinements) > 0 {
		return inv.misuseWord("unknown option ", inv.refinements[0])
	}
	if len(inv.data) == 0 {
		cli.WriteLines(inv.stdout, commands["setup"].help.Render(inv.outPalette()))
		return output.ExitOK
	}
	if len(inv.data) > 1 {
		return inv.misuse("use \"nn help setup\" for available options")
	}
	switch inv.data[0] {
	case "zsh":
		fmt.Fprint(inv.stdout, zshIntegration)
		return output.ExitOK
	case "install":
		return cmdSetupInstall(inv)
	case "ai":
		return cmdSetupAI(inv)
	default:
		return inv.misuseWord("unknown setup target ", inv.data[0])
	}
}

func cmdSetupInstall(inv *invocation) int {
	home, err := userHomeDir()
	if err != nil {
		return inv.fail(errors.New("cannot determine the home directory"))
	}
	exe, err := executablePath()
	if err != nil {
		return inv.fail(errors.New("cannot determine the installed executable"))
	}
	if real, err := filepath.EvalSymlinks(exe); err == nil {
		exe = real
	}

	target := filepath.Join(home, ".local", "bin", "nn")
	if err := copyExecutableFile(exe, target); err != nil {
		return inv.fail(fmt.Errorf("install nn: %w", err))
	}
	fmt.Fprintf(inv.stdout, "installed %s\n", target)

	if share, ok := installResources(); ok {
		helperSrc := filepath.Join(share, "nn-vision")
		if info, statErr := os.Stat(helperSrc); statErr == nil && !info.IsDir() {
			helperDst := filepath.Join(home, ".local", "share", "nn", "nn-vision")
			if err := copyExecutableFile(helperSrc, helperDst); err != nil {
				fmt.Fprintf(inv.stderr, "nn: warning: install nn-vision helper: %v\n", err)
			} else {
				fmt.Fprintf(inv.stdout, "installed %s\n", helperDst)
			}
		}
	}
	fmt.Fprintln(inv.stdout, `if "nn" is not found, add ~/.local/bin to your PATH`)
	return output.ExitOK
}

var (
	userHomeDir      = os.UserHomeDir
	executablePath   = os.Executable
	installResources = install.Resources
)

// copyExecutableFile writes dst via a temp file and rename, for an atomic install.
func copyExecutableFile(src, dst string) error {
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	dir := filepath.Dir(dst)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".nn-install-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)

	_, writeErr := tmp.Write(data)
	chmodErr := tmp.Chmod(0o755)
	closeErr := tmp.Close()
	if err := errors.Join(writeErr, chmodErr, closeErr); err != nil {
		return err
	}
	return os.Rename(tmpPath, dst)
}

const zshIntegration = `# nn zsh integration. Add to ~/.zshrc, or source directly:
#   source /path/to/nn.zsh

# Capture the last shell command as a code note, or with --ai explain it via nn last.
nn-last() {
  local cmd arg event rest listing boundary oldest candidate
  local use_ai=0
  if (( $# == 0 )); then
    cmd=$(fc -ln -1)
    nn add "$cmd" --code=sh
    return $?
  fi
  for arg in "$@"; do
    if [[ $arg == --ai || $arg == --ai=* ]]; then
      use_ai=1
    fi
  done
  if (( ! use_ai )); then
    print -u2 -r -- 'nn-last: options require --ai or --ai=PROFILE'
    return 2
  fi
  builtin setopt LOCAL_OPTIONS || return 2
  builtin unsetopt ALL_EXPORT || return 2
  builtin local +x cmd arg event rest listing boundary oldest candidate use_ai || return 2
  if ! builtin zmodload zsh/parameter; then
    print -u2 -r -- 'nn-last: raw shell history is unavailable'
    return 2
  fi
  # fc displays escaped text; only use numeric event metadata from its listing.
  listing=$(builtin fc -l -1 -1 2>/dev/null) || {
    print -u2 -r -- 'nn-last: no previous local command is available'
    return 2
  }
  IFS=$' \t' builtin read -r boundary rest <<< "$listing"
  if [[ $boundary != <1-> ]]; then
    print -u2 -r -- 'nn-last: no previous local command is available'
    return 2
  fi
  oldest=$boundary
  for candidate in "${(@k)history}"; do
    if [[ $candidate == <1-> ]] && (( candidate < oldest )); then
      oldest=$candidate
    fi
  done
  listing=$(builtin fc -l -I -L "$oldest" "$boundary" 2>/dev/null) || {
    print -u2 -r -- 'nn-last: no previous local command is available'
    return 2
  }
  event=0
  while IFS=$' \t' builtin read -r candidate rest; do
    if [[ $candidate == <1-> ]] && (( candidate <= boundary && candidate > event )); then
      event=$candidate
    fi
  done <<< "$listing"
  if [[ $event == 0 || ${+history[$event]} != 1 ]]; then
    print -u2 -r -- 'nn-last: no previous local command is available'
    return 2
  fi
  cmd="${history[$event]}"
  builtin print -rn -- "$cmd" | nn last "$@"
}

# Insert the result of "nn snip" at the cursor, for a keybinding.
nn-snip-widget() {
  local snippet
  snippet=$(nn snip)
  if [[ -n $snippet ]]; then
    LBUFFER+=$snippet
  fi
  zle reset-prompt
}
zle -N nn-snip-widget

# Bind nn-snip-widget to a key of your choice, for example:
# bindkey '^X^N' nn-snip-widget

# Tab completion: commands, tags and notes.
_nn() {
  local -a commands
  commands=(
    add:'capture a new note' a:'alias for add'
    shot:'capture a screen region' edit:'open a note' e:'alias for edit'
    url:'save a web link with an AI summary'
    snip:'print a code snippet' ai:'transform piped text'
    last:'explain a supplied shell command'
    s:'search notes' ask:'answer questions from notes' digest:'summarize selected notes'
    triage:'review inbox additions'
    ls:'list notes' show:'print a note'
    cat:'print a note body' code:'print code blocks' open:'open in Obsidian'
    links:'outgoing links' backlinks:'incoming links' graph:'link graph'
    tags:'list tags' stats:'usage stats'
    ocr:'recognize text in images'
    doctor:'check setup' setup:'shell integration, install and AI consent'
    config:'show or change configuration' help:'command help'
    version:'print the version'
  )

  if (( CURRENT == 2 )); then
    _describe -t commands 'nn command' commands
    return
  fi

  case ${words[2]} in
    add|a)
      _arguments \
        '--ai[suggest title and keyword tags]' '--no-ai[skip metadata]' \
        '--ai-mode[metadata delivery]:mode:(background wait auto)' \
        '--model[override model]:model:' \
        '--effort[override reasoning effort]:effort:(low medium high max)' \
        '--title[note title]:title:' '--to[append to note]:note:' \
        '--clip[read clipboard image or text]' '--preview[preview only]' \
        '--ocr[recognize text]' '--no-ocr[skip text recognition]' \
        '--new[create a new note]' '--allow-secret[allow discovered secrets]' \
        '--code[wrap code]' '-e[edit draft]' '-T[template]:template:' \
        '*-t[note tag]:tag:'
      ;;
    shot)
      _arguments \
        '--ai[analyze screenshot with the default profile]' \
        '--no-ai[skip screenshot analysis]' \
        '--ai-mode[analysis delivery]:mode:(background wait auto)' \
        '--model[override model]:model:' \
        '--effort[override reasoning effort]:effort:(low medium high max)' \
        '--title[note title]:title:' \
        '--ocr[recognize text]' '--no-ocr[skip text recognition]' \
        '--copy-text[copy recognized text]' '--no-copy-text[do not copy text]' \
        '*-t[note tag]:tag:'
      ;;
    url)
      _arguments \
        '--title[note title]:title:' \
        '--ai[select an AI profile]' '--no-ai[save the link without a summary]' \
        '--ai-mode[summary delivery]:mode:(background wait auto)' \
        '--model[override model]:model:' \
        '--effort[override reasoning effort]:effort:(low medium high max)' \
        '--allow-secret[allow sending discovered secrets]' \
        '--allow-private[allow fetching private network addresses]' \
        '*-t[note tag]:tag:'
      ;;
    last)
      _arguments \
        '--ai[select an AI profile]' \
        '--model[override model]:model:' \
        '--effort[override reasoning effort]:effort:(low medium high max)' \
        '--output[include output from a text file]:file:_files' \
        '--save[save the analysis as a note]' \
        '--allow-secret[allow sending discovered credentials]'
      ;;
    triage)
      _arguments \
        '--apply[select additions and confirm their exact diff]' \
        '--ai[select an AI profile]' \
        '--model[override model]:model:' \
        '--effort[override reasoning effort]:effort:(low medium high max)'
      ;;
    ask)
      _arguments \
        '--ai[select an AI profile]' \
        '--model[override model]:model:' \
        '--effort[override reasoning effort]:effort:(low medium high max)' \
        '--save[save the answer as a note]'
      ;;
    ai)
      _arguments \
        '--ai[select an AI profile]' \
        '--model[override model]:model:' \
        '--effort[override reasoning effort]:effort:(low medium high max)'
      ;;
    digest)
      if [[ ${words[CURRENT]} == -t || ${words[CURRENT-1]} == -t ]]; then
        local -a tags
        tags=(${(f)"$(nn tags --names 2>/dev/null)"})
        _describe -t tags 'tag' tags
      else
        _arguments \
          '*-t[filter by tag]:tag:' \
          '--since[period start]:since:' \
          '--until[period end]:until:' \
          '--inbox[only inbox notes]' \
          '--here[only notes captured here]' \
          '--notes[note limit]:notes:' \
          '--chars[character limit]:chars:' \
          '--save[save the digest as a note]' \
          '--allow-secret[allow sending discovered secrets]' \
          '--ai[select an AI profile]' \
          '--model[override model]:model:' \
          '--effort[override reasoning effort]:effort:(low medium high max)'
      fi
      ;;
    edit|e|show|cat|code|open|links|backlinks|graph)
      local -a notes
      notes=(${(f)"$(nn ls --paths -n all 2>/dev/null)"})
      _describe -t notes 'note' notes
      ;;
    s|ls|snip)
      if [[ ${words[CURRENT]} == -t || ${words[CURRENT-1]} == -t ]]; then
        local -a tags
        tags=(${(f)"$(nn tags --names 2>/dev/null)"})
        _describe -t tags 'tag' tags
      fi
      ;;
  esac
}
compdef _nn nn
`
