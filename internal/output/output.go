// Package output is the shared rendering layer for commands that list records.
package output

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"text/template"

	"github.com/lamovs/nn/internal/cli"
)

const (
	ExitOK          = 0
	ExitNotFound    = 1
	ExitError       = 2
	ExitInterrupted = 130
)

type Options struct {
	JSON   bool
	Paths  bool
	NUL    bool
	TSV    bool
	Format string
	Color  cli.ColorMode

	ColorSet bool
}

const (
	flagJSON   = "--json"
	flagPaths  = "--paths"
	flagNUL    = "-0"
	flagTSV    = "--tsv"
	flagFormat = "--format"
	flagColor  = "--color"
)

// ParseFlags: the output-form flags are mutually exclusive, --color is not.
func ParseFlags(args []string) (Options, []string, error) {
	opt := Options{Color: cli.ColorAuto}
	rest := make([]string, 0, len(args))
	var forms []string // the form-selecting flags seen, in order, for the exclusivity check

	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			rest = append(rest, args[i:]...)
			break
		}

		name, value, hasValue := strings.Cut(a, "=")
		switch name {
		case flagJSON:
			opt.JSON = true
			forms = append(forms, flagJSON)
			continue
		case flagPaths:
			opt.Paths = true
			forms = append(forms, flagPaths)
			continue
		case flagNUL:
			opt.NUL = true
			forms = append(forms, flagNUL)
			continue
		case flagTSV:
			opt.TSV = true
			forms = append(forms, flagTSV)
			continue
		case flagFormat:
			if !hasValue {
				if i+1 >= len(args) {
					return Options{}, nil, fmt.Errorf("%s needs a value", flagFormat)
				}
				i++
				value = args[i]
			}
			opt.Format = value
			forms = append(forms, flagFormat)
			continue
		case flagColor:
			if !hasValue {
				if i+1 >= len(args) {
					return Options{}, nil, fmt.Errorf("%s needs a value (auto, always or never)", flagColor)
				}
				i++
				value = args[i]
			}
			mode, err := cli.ParseColorMode(value)
			if err != nil {
				return Options{}, nil, fmt.Errorf("%s: %w", flagColor, err)
			}
			opt.Color, opt.ColorSet = mode, true
			continue
		}
		rest = append(rest, a)
	}
	if err := checkExclusive(forms); err != nil {
		return Options{}, nil, err
	}
	return opt, rest, nil
}

func formFamily(flag string) string {
	if flag == flagNUL {
		return flagPaths
	}
	return flag
}

func checkExclusive(forms []string) error {
	seen := map[string]bool{}
	var families []string
	for _, f := range forms {
		fam := formFamily(f)
		if !seen[fam] {
			seen[fam] = true
			families = append(families, fam)
		}
	}
	if len(families) <= 1 {
		return nil
	}
	return fmt.Errorf("%s are mutually exclusive", strings.Join(families, ", "))
}

type Spec[T any] struct {
	Text func(io.Writer, []T) error

	Path func(T) string // nil if --paths/-0 is unsupported

	TSVHeader []string
	TSV       func(T) []string
}

// Emit's precedence is JSON, paths/-0, TSV, --format, then text.
func Emit[T any](w io.Writer, opt Options, rows []T, spec Spec[T]) error {
	switch {
	case opt.JSON:
		return emitJSON(w, rows)
	case opt.Paths || opt.NUL:
		if spec.Path == nil {
			return errors.New("this command does not support --paths or -0")
		}
		return emitPaths(w, rows, spec.Path, opt.NUL)
	case opt.TSV:
		if spec.TSV == nil {
			return errors.New("this command does not support --tsv")
		}
		return emitTSV(w, rows, spec)
	case opt.Format != "":
		return emitFormat(w, rows, opt.Format)
	default:
		if spec.Text == nil {
			return errors.New("this command has no text output")
		}
		return spec.Text(w, rows)
	}
}

func emitJSON[T any](w io.Writer, rows []T) error {
	if rows == nil {
		rows = []T{}
	}
	return json.NewEncoder(w).Encode(rows)
}

func emitPaths[T any](w io.Writer, rows []T, path func(T) string, nul bool) error {
	seen := make(map[string]bool, len(rows))
	sep := "\n"
	if nul {
		sep = "\x00"
	}
	bw := bufio.NewWriter(w)
	for _, r := range rows {
		p := path(r)
		if p == "" || seen[p] {
			continue
		}
		seen[p] = true
		if _, err := bw.WriteString(p + sep); err != nil {
			return err
		}
	}
	return bw.Flush()
}

// tsvEscape keeps a field from breaking a TSV record into extra columns.
var tsvEscape = strings.NewReplacer(
	`\`, `\\`,
	"\t", `\t`,
	"\n", `\n`,
	"\r", `\r`,
)

func emitTSV[T any](w io.Writer, rows []T, spec Spec[T]) error {
	bw := bufio.NewWriter(w)
	if len(spec.TSVHeader) > 0 {
		if _, err := bw.WriteString(joinTSV(spec.TSVHeader)); err != nil {
			return err
		}
	}
	for _, r := range rows {
		if _, err := bw.WriteString(joinTSV(spec.TSV(r))); err != nil {
			return err
		}
	}
	return bw.Flush()
}

func joinTSV(fields []string) string {
	var b strings.Builder
	for i, f := range fields {
		if i > 0 {
			b.WriteByte('\t')
		}
		b.WriteString(tsvEscape.Replace(f))
	}
	b.WriteByte('\n')
	return b.String()
}

func emitFormat[T any](w io.Writer, rows []T, format string) error {
	tmpl, err := template.New("format").Parse(format)
	if err != nil {
		return fmt.Errorf("--format: %w", err)
	}
	bw := bufio.NewWriter(w)
	for _, r := range rows {
		if err := tmpl.Execute(bw, r); err != nil {
			return fmt.Errorf("--format: %w", err)
		}
		if err := bw.WriteByte('\n'); err != nil {
			return err
		}
	}
	return bw.Flush()
}

type Ref struct {
	Path string
	Line int
}

func ReadRefs(r io.Reader) ([]Ref, error) {
	data, err := io.ReadAll(r)
	if err != nil {
		return nil, err
	}

	sep := byte('\n')
	if bytes.IndexByte(data, 0) >= 0 {
		sep = 0
	}

	var refs []Ref
	for _, field := range bytes.Split(data, []byte{sep}) {
		field = bytes.TrimRight(field, "\r")
		s := strings.TrimSpace(string(field))
		if s == "" {
			continue
		}
		refs = append(refs, parseRef(s))
	}
	return refs, nil
}

func parseRef(s string) Ref {
	if i := strings.LastIndex(s, ":"); i >= 0 {
		if line, err := strconv.Atoi(s[i+1:]); err == nil && line > 0 {
			return Ref{Path: s[:i], Line: line}
		}
	}
	return Ref{Path: s}
}
