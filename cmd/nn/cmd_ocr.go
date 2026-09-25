package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/lamovs/nn/internal/app"
	"github.com/lamovs/nn/internal/cli"
	"github.com/lamovs/nn/internal/ocr"
	"github.com/lamovs/nn/internal/output"
)

func init() {
	register(command{
		help: cli.Help{
			Verb:    "ocr",
			Summary: "print or refresh OCR text for images",
			Examples: []cli.Example{
				{Cmd: "nn ocr nn/assets/hotkeys.png", What: "prints the recognized text"},
				{Cmd: "nn ls --img --paths | nn ocr -", What: "OCRs every image path piped in"},
				{Cmd: "nn ocr --reindex", What: "fills in missing or stale OCR sidecars, and drops the ones whose image is gone"},
				{Cmd: "nn ocr --reindex --force", What: "re-recognizes every image, even ones already cached"},
			},
			Sections: []cli.HelpSection{
				{Title: "Caching", Items: []string{
					"A sidecar is cached by the image's sha256 and langs, not by ocr.tesseract_psm: changing that key needs nn ocr --reindex --force to actually re-recognize already-cached images.",
				}},
			},
			SeeAlso: []string{"shot", "s"},
		},
		run: cmdOCR,
	})
}

type ocrOptions struct {
	langs    string
	hasLangs bool
	reindex  bool
	force    bool
}

func parseOCROptions(args []string) (ocrOptions, error) {
	var opt ocrOptions
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--langs":
			v, ok := flagValue(args, &i)
			if !ok {
				return opt, fmt.Errorf("--langs needs a value")
			}
			opt.langs, opt.hasLangs = v, true
		case "--reindex":
			opt.reindex = true
		case "--force":
			opt.force = true
		default:
			return opt, unknownOption{args[i]}
		}
	}
	return opt, nil
}

type ocrRow struct {
	Path   string      `json:"path"`
	Result *ocr.Result `json:"result,omitempty"`
	Error  string      `json:"error,omitempty"`
}

func cmdOCR(inv *invocation) int {
	outOpt, rest, err := output.ParseFlags(inv.refinements)
	if err != nil {
		return inv.misuse("%v", err)
	}
	opt, oerr := parseOCROptions(rest)
	if oerr != nil {
		var unknown unknownOption
		if errors.As(oerr, &unknown) {
			return inv.misuseWord("unknown option ", unknown.opt)
		}
		return inv.misuse("%v", oerr)
	}
	if opt.reindex && len(inv.data) > 0 {
		return inv.misuse("--reindex does not take image arguments")
	}

	env, err := app.Open()
	if err != nil {
		return inv.fail(err)
	}
	engine := ocrEngineFor(env.Cfg)
	langs := defaultOCRLangs(env.Cfg)
	if opt.hasLangs {
		langs = splitLangsArg(opt.langs)
	}

	if opt.reindex {
		images, err := env.Vault.Images(inv.ctx)
		if err != nil {
			return inv.fail(err)
		}
		stats, err := ocr.Reindex(inv.ctx, env.Vault.Root, images, engine, langs, opt.force, func(done, total int) {
			fmt.Fprintf(inv.stderr, "nn: ocr: %d/%d\n", done, total)
		})
		if err != nil {
			return inv.fail(err)
		}
		// Reported separately: an image reached only through a symlink out of the vault is not indexed.
		boundary := ""
		if stats.Outside > 0 {
			boundary += fmt.Sprintf(", %d outside the vault skipped", stats.Outside)
		}
		if stats.OutsideOrphans > 0 {
			boundary += fmt.Sprintf(", %d sidecar(s) of images outside the vault removed", stats.OutsideOrphans)
		}
		fmt.Fprintf(inv.stderr, "nn: ocr: %d image(s) indexed, %d no longer on disk skipped, %d orphaned sidecar(s) removed%s\n",
			stats.Processed, stats.Skipped, stats.Orphans, boundary)
		return output.ExitOK
	}

	refs, err := ocrTargets(inv)
	if err != nil {
		return inv.misuse("%v", err)
	}
	if len(refs) == 0 {
		return inv.misuse("give an image path, \"-\" for a list on stdin, or --reindex")
	}

	rows := make([]ocrRow, 0, len(refs))
	failed := false
	for _, ref := range refs {
		// ocr.Locate owns the vault boundary check for both relative and absolute image paths.
		img, rerr := ocr.Locate(env.Vault.Root, ref)
		if rerr != nil {
			rows = append(rows, ocrRow{Path: ref, Error: rerr.Error()})
			failed = true
			continue
		}
		if _, statErr := os.Stat(img.Abs); statErr != nil {
			rows = append(rows, ocrRow{Path: ref, Error: statErr.Error()})
			failed = true
			continue
		}
		var result *ocr.Result
		if opt.force {
			result, err = ocr.Refresh(inv.ctx, env.Vault.Root, img.Rel, engine, langs)
		} else {
			result, err = ocr.Ensure(inv.ctx, env.Vault.Root, img.Rel, engine, langs)
		}
		if err != nil {
			rows = append(rows, ocrRow{Path: ref, Error: err.Error()})
			failed = true
			continue
		}
		rows = append(rows, ocrRow{Path: ref, Result: result})
	}

	err = output.Emit(inv.stdout, outOpt, rows, output.Spec[ocrRow]{
		Text: func(w io.Writer, rows []ocrRow) error {
			first := true
			for _, r := range rows {
				if r.Error != "" {
					fmt.Fprintf(inv.stderr, "nn: ocr: %s: %s\n", r.Path, r.Error)
					continue
				}
				if !first {
					fmt.Fprintln(w)
				}
				first = false
				for _, l := range r.Result.Lines {
					fmt.Fprintln(w, l.Text)
				}
			}
			return nil
		},
		Path: func(r ocrRow) string { return r.Path },
	})
	if err != nil {
		return inv.fail(err)
	}
	if failed {
		return output.ExitError
	}
	return output.ExitOK
}

func ocrTargets(inv *invocation) ([]string, error) {
	if len(inv.data) == 1 && inv.data[0] == "-" {
		refs, err := output.ReadRefs(inv.stdin)
		if err != nil {
			return nil, err
		}
		out := make([]string, len(refs))
		for i, r := range refs {
			out[i] = r.Path
		}
		return out, nil
	}
	return inv.data, nil
}

func splitLangsArg(s string) []string {
	var out []string
	for _, part := range strings.FieldsFunc(s, func(r rune) bool {
		return r == '+' || r == ',' || r == ' ' || r == '\t'
	}) {
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}
