package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"strings"

	"github.com/lamovs/nn/internal/app"
	"github.com/lamovs/nn/internal/cli"
	"github.com/lamovs/nn/internal/config"
	"github.com/lamovs/nn/internal/ocr"
	"github.com/lamovs/nn/internal/output"
	"github.com/lamovs/nn/internal/termimg"
	"github.com/lamovs/nn/internal/vault"
)

func init() {
	def := config.Default()
	register(command{
		help: cli.Help{
			Verb:    "show",
			Summary: "print a note with its images inline",
			Examples: []cli.Example{
				{Cmd: "nn show docker cleanup", What: "renders the note's frontmatter, body and images"},
				{Cmd: "nn show docker cleanup --ocr", What: "also prints each image's recognized text, under it"},
				{Cmd: "nn s hotkey --paths | nn show -", What: "shows the first piped path"},
			},
			Sections: []cli.HelpSection{
				{Title: "Images", Items: []string{
					"Inline images need a terminal that speaks the iTerm2 (iTerm2, WezTerm) or kitty (kitty, ghostty) graphics protocol; anything else prints [image: path].",
					fmt.Sprintf("show.images (default: %s) can be set to never, which behaves like --no-images by default; --images forces auto back on even then. --no-images always prints [image: path] instead of attempting inline graphics - useful together with --ocr, or when piping to a file. --images and --no-images cannot be combined.", def.Show.Images),
					fmt.Sprintf("--ocr's default is show.ocr (default: %v); --no-ocr turns it off even when the config default is on. --ocr and --no-ocr cannot be combined.", def.Show.OCR),
				}},
			},
			SeeAlso: []string{"cat", "edit", "s"},
		},
		run: cmdShow,
	})
}

type showOptions struct {
	ocr      bool
	noOCR    bool
	images   bool
	noImages bool
}

func parseShowOptions(args []string) (showOptions, []string, error) {
	var opt showOptions
	var words []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			words = append(words, args[i+1:]...)
			break
		}
		switch a {
		case "--ocr":
			opt.ocr = true
		case "--no-ocr":
			opt.noOCR = true
		case "--images":
			opt.images = true
		case "--no-images":
			opt.noImages = true
		default:
			if a != "-" && strings.HasPrefix(a, "-") {
				return opt, nil, fmt.Errorf("unknown option %q", a)
			}
			words = append(words, a)
		}
	}
	if opt.ocr && opt.noOCR {
		return opt, nil, errors.New("--ocr and --no-ocr cannot be combined")
	}
	if opt.images && opt.noImages {
		return opt, nil, errors.New("--images and --no-images cannot be combined")
	}
	return opt, words, nil
}

func oneRefFromWords(verb string, stdin io.Reader, stderr io.Writer, words []string) (string, error) {
	if len(words) == 1 && words[0] == "-" {
		refs, err := output.ReadRefs(stdin)
		if err != nil {
			return "", err
		}
		if len(refs) == 0 {
			return "", errors.New("stdin had no path")
		}
		if len(refs) > 1 {
			fmt.Fprintf(stderr, "nn: %s: %d paths piped in, using the first: %s\n", verb, len(refs), refs[0].Path)
		}
		return refs[0].Path, nil
	}
	if len(words) == 0 {
		return "", fmt.Errorf("%s needs a NOTE, or - to read a path from stdin", verb)
	}
	return strings.Join(words, " "), nil
}

var (
	showIsTerminal = cli.IsTerminal
	showProtocol   = termimg.DetectEnv
)

func cmdShow(inv *invocation) int {
	opt, words, err := parseShowOptions(inv.args)
	if err != nil {
		return inv.misuse("%v", err)
	}
	ref, err := oneRefFromWords("show", inv.stdin, inv.stderr, words)
	if err != nil {
		return inv.misuse("%v", err)
	}

	env, err := app.Open()
	if err != nil {
		return inv.fail(err)
	}
	rel, err := env.Resolve(inv.ctx, ref)
	if err != nil {
		return inv.fail(err)
	}
	n, err := env.Vault.Load(rel)
	if err != nil {
		return inv.fail(err)
	}

	switch {
	case opt.ocr, opt.noOCR:
	default:
		opt.ocr = env.Cfg.Show.OCR
	}
	switch {
	case opt.images:
		opt.noImages = false
	case opt.noImages:
	default:
		opt.noImages = env.Cfg.Show.Images == "never"
	}

	p := resolveColor(cli.ColorAuto, false, cli.ColorMode(env.Cfg.Output.Color), inv.stdout)
	renderNote(inv.stdout, inv.stderr, env, n, opt, p)

	env.RecordOpen(rel)
	return output.ExitOK
}

func renderNote(w, stderr io.Writer, env *app.Env, n *vault.Note, opt showOptions, p cli.Palette) {
	fmt.Fprintln(w, p.Bold(n.Title))
	if len(n.Tags) > 0 {
		parts := make([]string, len(n.Tags))
		for i, t := range n.Tags {
			parts[i] = "#" + t
		}
		fmt.Fprintln(w, strings.Join(parts, " "))
	}
	if meta := showMetaLine(n); meta != "" {
		fmt.Fprintln(w, p.Dim(meta))
	}
	fmt.Fprintln(w)
	renderBody(w, n.Body, p)

	if len(n.Embeds) > 0 {
		fmt.Fprintln(w)
	}
	proto := showProtocol()
	for _, embed := range n.Embeds {
		renderImage(w, stderr, env, embed, opt, proto)
	}
}

func showMetaLine(n *vault.Note) string {
	var parts []string
	if !n.Date.IsZero() {
		parts = append(parts, "date: "+n.Date.Format("2006-01-02"))
	}
	if n.Where != "" {
		parts = append(parts, "where: "+n.Where)
	}
	if n.Repo != "" {
		parts = append(parts, "repo: "+n.Repo)
	}
	if n.Via != "" {
		parts = append(parts, "via: "+n.Via)
	}
	return strings.Join(parts, "  ")
}

func renderBody(w io.Writer, body string, p cli.Palette) {
	masked := strings.Split(vault.MaskCode(body), "\n")
	lines := strings.Split(body, "\n")
	for i, line := range lines {
		if i < len(masked) && isATXHeading(masked[i]) {
			fmt.Fprintln(w, p.Bold(line))
			continue
		}
		fmt.Fprintln(w, line)
	}
}

func isATXHeading(line string) bool {
	t := strings.TrimLeft(line, " \t")
	if len(line)-len(t) > 3 {
		return false
	}
	n := 0
	for n < len(t) && t[n] == '#' {
		n++
	}
	if n == 0 || n > 6 {
		return false
	}
	return n == len(t) || t[n] == ' ' || t[n] == '\t'
}

// renderImage's ![[...]] target is untrusted: ocr.Locate is the single vault-boundary/symlink
// check, used for both the inline read and the OCR sidecar lookup. Abs follows the link for
// reading; Rel does not, since sidecars are filed under the note's own spelling of the path.
func renderImage(w, stderr io.Writer, env *app.Env, embed string, opt showOptions, proto termimg.Protocol) {
	img, pathErr := ocr.Locate(env.Vault.Root, embed)
	warned := false

	inline := !opt.noImages && showIsTerminal(w) && proto != termimg.ProtocolNone
	if inline {
		if pathErr != nil {
			fmt.Fprintf(stderr, "nn: show: warning: %v\n", pathErr)
			warned = true
			inline = false
		} else if data, err := os.ReadFile(img.Abs); err != nil {
			fmt.Fprintf(stderr, "nn: show: warning: %v\n", err)
			inline = false
		} else if err := termimg.Encode(w, proto, path.Base(embed), data); err != nil {
			inline = false
		} else {
			fmt.Fprintln(w)
		}
	}
	if !inline {
		fmt.Fprintf(w, "[image: %s]\n", embed)
	}
	if !opt.ocr {
		return
	}
	if pathErr != nil {
		if !warned {
			fmt.Fprintf(stderr, "nn: show: warning: %v\n", pathErr)
		}
		return
	}
	result, err := ocr.LoadSidecar(env.Vault.Root, img.Rel)
	if err == nil {
		for _, line := range result.Lines {
			fmt.Fprintln(w, "  "+line.Text)
		}
	}
}
