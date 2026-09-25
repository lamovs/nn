package output

import (
	"bytes"
	"fmt"
	"io"
	"reflect"
	"strings"
	"testing"

	"github.com/lamovs/nn/internal/cli"
)

func TestParseFlagsExtractsKnownFlags(t *testing.T) {
	opt, rest, err := ParseFlags([]string{"docker", "--json", "-t", "docker", "--color=never"})
	if err != nil {
		t.Fatal(err)
	}
	if !opt.JSON {
		t.Errorf("opt = %+v, want JSON set", opt)
	}
	if opt.Color != cli.ColorNever {
		t.Errorf("Color = %q, want never", opt.Color)
	}
	if !opt.ColorSet {
		t.Errorf("opt = %+v, want ColorSet", opt)
	}
	wantRest := []string{"docker", "-t", "docker"}
	if !reflect.DeepEqual(rest, wantRest) {
		t.Errorf("rest = %v, want %v", rest, wantRest)
	}
}

// --color=auto and no flag both leave Color at cli.ColorAuto.
func TestParseFlagsColorSetOnlyWhenGiven(t *testing.T) {
	if opt, _, err := ParseFlags([]string{"docker"}); err != nil || opt.ColorSet {
		t.Errorf("opt = %+v, err = %v, want ColorSet false with no --color", opt, err)
	}
	if opt, _, err := ParseFlags([]string{"--color=auto"}); err != nil || !opt.ColorSet || opt.Color != cli.ColorAuto {
		t.Errorf("opt = %+v, err = %v, want ColorSet true and Color auto", opt, err)
	}
}

func TestParseFlagsFormatWithSpaceValue(t *testing.T) {
	opt, rest, err := ParseFlags([]string{"--format", "{{.Path}}", "query"})
	if err != nil {
		t.Fatal(err)
	}
	if opt.Format != "{{.Path}}" {
		t.Errorf("Format = %q", opt.Format)
	}
	if !reflect.DeepEqual(rest, []string{"query"}) {
		t.Errorf("rest = %v", rest)
	}
}

func TestParseFlagsMissingValue(t *testing.T) {
	if _, _, err := ParseFlags([]string{"--format"}); err == nil {
		t.Fatal("expected an error for --format with no value")
	}
	if _, _, err := ParseFlags([]string{"--color"}); err == nil {
		t.Fatal("expected an error for --color with no value")
	}
	if _, _, err := ParseFlags([]string{"--color=purple"}); err == nil {
		t.Fatal("expected an error for an invalid --color value")
	}
}

func TestParseFlagsMutuallyExclusiveForms(t *testing.T) {
	cases := [][]string{
		{"--json", "--tsv"},
		{"--json", "--paths"},
		{"--paths", "--format", "{{.Path}}"},
		{"--tsv", "--format", "{{.Path}}"},
		{"--json", "-0"},
	}
	for _, args := range cases {
		_, _, err := ParseFlags(args)
		if err == nil {
			t.Errorf("ParseFlags(%v): expected a mutual-exclusivity error", args)
			continue
		}
		if !strings.Contains(err.Error(), "mutually exclusive") {
			t.Errorf("ParseFlags(%v): err = %q, want it to say mutually exclusive", args, err)
		}
	}
}

func TestParseFlagsPathsAndNULCombineFine(t *testing.T) {
	opt, _, err := ParseFlags([]string{"--paths", "-0"})
	if err != nil {
		t.Fatalf("--paths and -0 together: %v", err)
	}
	if !opt.Paths || !opt.NUL {
		t.Errorf("opt = %+v, want both Paths and NUL set", opt)
	}
}

func TestParseFlagsRepeatingOneFormIsFine(t *testing.T) {
	if _, _, err := ParseFlags([]string{"--json", "--json"}); err != nil {
		t.Errorf("repeating --json: %v", err)
	}
	if _, _, err := ParseFlags([]string{"--color=always", "--color=always"}); err != nil {
		t.Errorf("repeating --color: %v", err)
	}
}

func TestParseFlagsColorCombinesWithAnyForm(t *testing.T) {
	for _, args := range [][]string{
		{"--json", "--color=always"},
		{"--tsv", "--color=never"},
		{"--paths", "--color=always"},
		{"--format", "{{.Path}}", "--color=always"},
	} {
		if _, _, err := ParseFlags(args); err != nil {
			t.Errorf("ParseFlags(%v): %v", args, err)
		}
	}
}

func TestParseFlagsStopsAtDoubleDash(t *testing.T) {
	opt, rest, err := ParseFlags([]string{"a", "--", "--json", "b"})
	if err != nil {
		t.Fatal(err)
	}
	if opt.JSON {
		t.Error("--json after -- must not be parsed as a flag")
	}
	if !reflect.DeepEqual(rest, []string{"a", "--", "--json", "b"}) {
		t.Errorf("rest = %v", rest)
	}
}

func TestParseFlagsDefaultColorIsAuto(t *testing.T) {
	opt, _, err := ParseFlags(nil)
	if err != nil {
		t.Fatal(err)
	}
	if opt.Color != cli.ColorAuto {
		t.Errorf("default Color = %q, want auto", opt.Color)
	}
}

type row struct {
	Path string
	Text string
}

func testSpec() Spec[row] {
	return Spec[row]{
		Text: func(w io.Writer, rows []row) error {
			for _, r := range rows {
				fmt.Fprintf(w, "%s: %s\n", r.Path, r.Text)
			}
			return nil
		},
		Path:      func(r row) string { return r.Path },
		TSVHeader: []string{"path", "text"},
		TSV:       func(r row) []string { return []string{r.Path, r.Text} },
	}
}

func TestEmitText(t *testing.T) {
	var buf bytes.Buffer
	rows := []row{{Path: "a.md", Text: "hello"}}
	if err := Emit(&buf, Options{}, rows, testSpec()); err != nil {
		t.Fatal(err)
	}
	if got := buf.String(); got != "a.md: hello\n" {
		t.Errorf("got %q", got)
	}
}

func TestEmitJSONAlwaysArray(t *testing.T) {
	var buf bytes.Buffer
	if err := Emit[row](&buf, Options{JSON: true}, nil, testSpec()); err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(buf.String()); got != "[]" {
		t.Errorf("empty rows -> %q, want []", got)
	}

	buf.Reset()
	rows := []row{{Path: "a.md", Text: "hi"}}
	if err := Emit(&buf, Options{JSON: true}, rows, testSpec()); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), `"Path":"a.md"`) {
		t.Errorf("got %q", buf.String())
	}
}

func TestEmitPathsUniqueInOrder(t *testing.T) {
	var buf bytes.Buffer
	rows := []row{{Path: "b.md"}, {Path: "a.md"}, {Path: "b.md"}}
	if err := Emit(&buf, Options{Paths: true}, rows, testSpec()); err != nil {
		t.Fatal(err)
	}
	if got := buf.String(); got != "b.md\na.md\n" {
		t.Errorf("got %q", got)
	}
}

func TestEmitNUL(t *testing.T) {
	var buf bytes.Buffer
	rows := []row{{Path: "a.md"}, {Path: "b.md"}}
	if err := Emit(&buf, Options{NUL: true}, rows, testSpec()); err != nil {
		t.Fatal(err)
	}
	if got := buf.String(); got != "a.md\x00b.md\x00" {
		t.Errorf("got %q", got)
	}
}

func TestEmitTSV(t *testing.T) {
	var buf bytes.Buffer
	rows := []row{{Path: "a.md", Text: "hi"}}
	if err := Emit(&buf, Options{TSV: true}, rows, testSpec()); err != nil {
		t.Fatal(err)
	}
	want := "path\ttext\na.md\thi\n"
	if got := buf.String(); got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestEmitTSVEscapesSeparators(t *testing.T) {
	var buf bytes.Buffer
	rows := []row{{Path: "a.md", Text: "echo one\necho two\tagain\r\nc:\\tmp"}}
	if err := Emit(&buf, Options{TSV: true}, rows, testSpec()); err != nil {
		t.Fatal(err)
	}

	got := buf.String()
	lines := strings.Split(strings.TrimSuffix(got, "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("output = %q, want exactly a header line and one record", got)
	}
	if n := len(strings.Split(lines[1], "\t")); n != 2 {
		t.Errorf("record = %q has %d columns, want 2", lines[1], n)
	}
	want := `a.md` + "\t" + `echo one\necho two\tagain\r\nc:\\tmp`
	if lines[1] != want {
		t.Errorf("record = %q, want %q", lines[1], want)
	}
}

func TestEmitFormat(t *testing.T) {
	var buf bytes.Buffer
	rows := []row{{Path: "a.md", Text: "hi"}}
	err := Emit(&buf, Options{Format: "{{.Path}}={{.Text}}"}, rows, testSpec())
	if err != nil {
		t.Fatal(err)
	}
	if got := buf.String(); got != "a.md=hi\n" {
		t.Errorf("got %q", got)
	}
}

func TestEmitUnsupportedPaths(t *testing.T) {
	var buf bytes.Buffer
	spec := Spec[row]{Text: testSpec().Text}
	if err := Emit(&buf, Options{Paths: true}, []row{{Path: "a"}}, spec); err == nil {
		t.Fatal("expected an error when Spec.Path is nil")
	}
}

func TestReadRefsNewlineSeparated(t *testing.T) {
	in := "a/b.md\n\nc/d.md:12\n  \nplain\n"
	refs, err := ReadRefs(strings.NewReader(in))
	if err != nil {
		t.Fatal(err)
	}
	want := []Ref{{Path: "a/b.md"}, {Path: "c/d.md", Line: 12}, {Path: "plain"}}
	if !reflect.DeepEqual(refs, want) {
		t.Errorf("refs = %+v, want %+v", refs, want)
	}
}

func TestReadRefsNULSeparated(t *testing.T) {
	in := "a/b.md\x00c/d.md:3\x00"
	refs, err := ReadRefs(strings.NewReader(in))
	if err != nil {
		t.Fatal(err)
	}
	want := []Ref{{Path: "a/b.md"}, {Path: "c/d.md", Line: 3}}
	if !reflect.DeepEqual(refs, want) {
		t.Errorf("refs = %+v, want %+v", refs, want)
	}
}

func TestReadRefsNonNumericSuffixStaysInPath(t *testing.T) {
	refs, err := ReadRefs(strings.NewReader("note:not-a-number\n"))
	if err != nil {
		t.Fatal(err)
	}
	want := []Ref{{Path: "note:not-a-number"}}
	if !reflect.DeepEqual(refs, want) {
		t.Errorf("refs = %+v, want %+v", refs, want)
	}
}
