package vault

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestParseFrontmatter(t *testing.T) {
	tests := []struct {
		name   string
		src    string
		fields map[string]any
		body   string
		start  int
		broken bool
	}{
		{
			name:   "flow lists and scalars",
			src:    "---\ndate: 2026-05-12\ntags: [docker, devtools, macos]\naliases: [Colima, Docker альтернатива]\n---\n\n# Colima setup\n",
			fields: map[string]any{"date": "2026-05-12", "tags": []any{"docker", "devtools", "macos"}, "aliases": []any{"Colima", "Docker альтернатива"}},
			body:   "\n# Colima setup\n",
			start:  6,
		},
		{
			name:   "indented block list",
			src:    "---\ntags:\n  - go\n  - concurrency\naliases:\n  - Go channels\n---\nbody",
			fields: map[string]any{"tags": []any{"go", "concurrency"}, "aliases": []any{"Go channels"}},
			body:   "body",
			start:  8,
		},
		{
			name:   "block list at key indentation",
			src:    "---\ntags:\n- a\n- b\nnext: x\n---\n",
			fields: map[string]any{"tags": []any{"a", "b"}, "next": "x"},
			body:   "",
			start:  7,
		},
		{
			name: "quoting, comments and typed scalars",
			src: "---\n# leading comment\ntitle: \"He said \\\"hi\\\" \\u0041\"\nsingle: 'it''s: fine'\ntime: \"14:02\"\n" +
				"plain: value # trailing comment\nurl: https://example.com/a#b\ncount: 42\nratio: 0.5\non: true\nnothing: ~\nempty:\n" +
				"\"quoted key\": 1\ndate: 2026-09-16\n---\n",
			fields: map[string]any{
				"title": "He said \"hi\" A", "single": "it's: fine", "time": "14:02", "plain": "value",
				"url": "https://example.com/a#b", "count": 42, "ratio": 0.5, "on": true, "nothing": nil,
				"empty": nil, "quoted key": 1, "date": "2026-09-16",
			},
			start: 16,
		},
		{
			name:   "flow list with quotes, nesting and continuation",
			src:    "---\ntags: [\"a, b\", 'c]', [d, e], {k: v},\n  f]\n---\n",
			fields: map[string]any{"tags": []any{"a, b", "c]", []any{"d", "e"}, map[string]any{"k": "v"}, "f"}},
			start:  5,
		},
		{
			name: "nested maps and lists of maps",
			src:  "---\nmeta:\n  author: me\n  links:\n    - name: a\n      url: x\n    - name: b\n---\n",
			fields: map[string]any{"meta": map[string]any{
				"author": "me",
				"links":  []any{map[string]any{"name": "a", "url": "x"}, map[string]any{"name": "b"}},
			}},
			start: 9,
		},
		{
			name:   "block scalars",
			src:    "---\nliteral: |\n  one\n  two\nfolded: >-\n  one\n  two\n\n  three\nafter: x\n---\n",
			fields: map[string]any{"literal": "one\ntwo\n", "folded": "one two\nthree", "after": "x"},
			start:  12,
		},
		{
			name:   "empty frontmatter",
			src:    "---\n---\nbody\n",
			fields: map[string]any{},
			body:   "body\n",
			start:  3,
		},
		{
			name:   "crlf and bom",
			src:    "\xef\xbb\xbf---\r\ntags: [a]\r\n---\r\nbody\r\n",
			fields: map[string]any{"tags": []any{"a"}},
			body:   "body\r\n",
			start:  4,
		},
		{
			name:   "closing delimiter at end of file",
			src:    "---\ntags: [a]\n---",
			fields: map[string]any{"tags": []any{"a"}},
			start:  4,
		},
		{name: "no frontmatter", src: "# Title\n", body: "# Title\n", start: 1},
		{name: "horizontal rule later", src: "text\n---\nmore\n", body: "text\n---\nmore\n", start: 1},
		{name: "never closed", src: "---\ntags: [a]\nbody\n", body: "---\ntags: [a]\nbody\n", start: 1},
		{name: "unterminated flow list", src: "---\ntags: [a, b\ndate: x\n---\nbody\n", broken: true},
		{name: "unterminated quote", src: "---\ntitle: \"abc\n---\n", broken: true},
		{name: "line without colon", src: "---\njust some text\n---\nbody\n", broken: true},
		{name: "bad indentation", src: "---\na: 1\n  b: 2\n---\n", broken: true},
		{name: "junk after flow list", src: "---\ntags: [a] b\n---\n", broken: true},
		{name: "bad escape", src: "---\na: \"\\q\"\n---\n", broken: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fields, body, start, err := ParseFrontmatter(tt.src)
			if tt.broken {
				if !errors.Is(err, ErrFrontmatter) || fields != nil || body != tt.src || start != 1 {
					t.Fatalf("broken: fields %v, body %q, start %d, err %v", fields, body, start, err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(fields, tt.fields) {
				t.Errorf("fields = %#v\nwant   %#v", fields, tt.fields)
			}
			if body != tt.body {
				t.Errorf("body = %q, want %q", body, tt.body)
			}
			if start != tt.start {
				t.Errorf("bodyStartLine = %d, want %d", start, tt.start)
			}
		})
	}
}

func TestYAMLStringRoundTrip(t *testing.T) {
	values := []string{
		"docker", "Docker cleanup", "Чашка ТВ", "14:02", "~/src/demo", "a: b", "key:",
		"#tag", "- dash", "[x]", "a, b", "true", "null", "42", "3.5", "it's", `say "hi"`,
		" padded ", "", "tab\there", "line\nbreak", "c#", "note #1", "back\\slash", "@at", "%pct",
		`grep -o "user:"`, "a:'b", "a:\"b", "a:\t\"b", "url:'", "{k:'v'}", "a:'b'c:'d",
	}
	for _, v := range values {
		for _, inFlow := range []bool{false, true} {
			var src string
			if inFlow {
				src = "---\nk: " + yamlFlowList([]string{v}) + "\n---\n"
			} else {
				src = "---\nk: " + yamlString(v, false) + "\n---\n"
			}
			fields, _, _, err := ParseFrontmatter(src)
			if err != nil {
				t.Errorf("%q (flow %v): %v in %q", v, inFlow, err, src)
				continue
			}
			got := fields["k"]
			if inFlow {
				list, _ := got.([]any)
				if len(list) != 1 {
					t.Errorf("%q: flow list read back as %#v", v, got)
					continue
				}
				got = list[0]
			}
			if got != v {
				t.Errorf("%q (flow %v): read back %#v from %q", v, inFlow, got, src)
			}
		}
	}
	if got := yamlFlowList([]string{"docker", "Docker cleanup"}); got != "[docker, Docker cleanup]" {
		t.Errorf("plain flow list = %s", got)
	}
	if strings.Contains(yamlString("~/src/demo", false), `"`) {
		t.Errorf("where should stay plain")
	}
}

func TestYAMLRoundTripExhaustive(t *testing.T) {
	alphabet := []string{"a", ":", `"`, "'", "[", "]", "{", "}", ",", "#", " ", "\\", "-"}
	var values []string
	var grow func(prefix string, depth int)
	grow = func(prefix string, depth int) {
		if depth == 0 {
			return
		}
		for _, c := range alphabet {
			values = append(values, prefix+c)
			grow(prefix+c, depth-1)
		}
	}
	grow("", 4)

	bad := 0
	for _, v := range values {
		for _, inFlow := range []bool{false, true} {
			var src string
			if inFlow {
				src = "---\nk: " + yamlFlowList([]string{v}) + "\n---\n"
			} else {
				src = "---\nk: " + yamlString(v, false) + "\n---\n"
			}
			fields, _, _, err := ParseFrontmatter(src)
			var got any
			if err == nil {
				got = fields["k"]
				if inFlow {
					if list, ok := got.([]any); ok && len(list) == 1 {
						got = list[0]
					}
				}
			}
			if err != nil || got != any(v) {
				bad++
				if bad <= 10 {
					t.Errorf("%q (flow %v): wrote %q, read back %#v (%v)", v, inFlow, src, got, err)
				}
			}
		}
	}
	if bad > 10 {
		t.Errorf("%d broken round trips in total", bad)
	}
}
