package webpage

import (
	"errors"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestDecodeTables(t *testing.T) {
	cases := []struct {
		label string
		in    string
		want  []rune
	}{
		{"windows-1251", "\xCF\xF0\xE8\xE2\xE5\xF2", []rune{0x041F, 0x0440, 0x0438, 0x0432, 0x0435, 0x0442}},
		{"windows-1251", "\xA8\xB8\x88\xB9\xC0\xFF", []rune{0x0401, 0x0451, 0x20AC, 0x2116, 0x0410, 0x044F}},
		{"cp1251", "\xC0", []rune{0x0410}},
		{"x-cp1251", "\xC0", []rune{0x0410}},
		{"koi8-r", "\xF0\xD2\xC9\xD7\xC5\xD4", []rune{0x041F, 0x0440, 0x0438, 0x0432, 0x0435, 0x0442}},
		{"koi8-r", "\xC1\xE1\xB3\xA3\xFF\x9A", []rune{0x0430, 0x0410, 0x0401, 0x0451, 0x042A, 0x00A0}},
		{"KOI8", "\xC1", []rune{0x0430}},
		{"cskoi8r", "\xC1", []rune{0x0430}},
		{"windows-1252", "\x80\x9F\xE9\xA0", []rune{0x20AC, 0x0178, 0x00E9, 0x00A0}},
		{"iso-8859-1", "\x80\xE9", []rune{0x20AC, 0x00E9}},
		{"latin1", "\xE9", []rune{0x00E9}},
		{"us-ascii", "\x80\xE9", []rune{0x20AC, 0x00E9}},
		{"ascii", "a\xE9", []rune{'a', 0x00E9}},
	}
	for _, c := range cases {
		got, err := decodeBody([]byte(c.in), c.label, false, false)
		if err != nil {
			t.Errorf("%s %q: %v", c.label, c.in, err)
			continue
		}
		if got != string(c.want) {
			t.Errorf("%s %q = %U, want %U", c.label, c.in, []rune(got), c.want)
		}
	}

	for name, table := range map[string]*[128]rune{"windows-1252": &windows1252, "windows-1251": &windows1251, "koi8-r": &koi8r} {
		if len(table) != 128 {
			t.Errorf("%s has %d entries", name, len(table))
		}
		seen := map[rune]int{}
		for i, r := range table {
			if r < 0x80 {
				t.Errorf("%s[0x%02X] = %U maps into ASCII", name, i+0x80, r)
			}
			if r == 0xFFFD {
				continue
			}
			if prev, ok := seen[r]; ok {
				t.Errorf("%s: %U at 0x%02X and 0x%02X", name, r, prev+0x80, i+0x80)
			}
			seen[r] = i
		}
	}
}

func TestDecodePriority(t *testing.T) {
	word1251 := "\xCF\xF0\xE8\xE2\xE5\xF2"
	wordKOI := "\xF0\xD2\xC9\xD7\xC5\xD4"
	want := string([]rune{0x041F, 0x0440, 0x0438, 0x0432, 0x0435, 0x0442})
	metaKOI := `<meta charset="koi8-r">`
	metaEquiv1251 := `<META HTTP-EQUIV="Content-Type" CONTENT="text/html; charset='windows-1251'">`
	utf8Word := want
	rep := string(utf8.RuneError)

	cases := []struct {
		name, header, body string
		html               bool
		want               string
	}{
		{"BOM over header and meta", "windows-1251", "\xEF\xBB\xBF" + metaKOI + utf8Word, true, metaKOI + utf8Word},
		{"header over meta", "koi8-r", metaEquiv1251 + wordKOI, true, metaEquiv1251 + want},
		{"meta charset", "", metaKOI + wordKOI, true, metaKOI + want},
		{"meta http-equiv", "", metaEquiv1251 + word1251, true, metaEquiv1251 + want},
		{"meta ignored for text", "", metaKOI + utf8Word, false, metaKOI + utf8Word},
		{"quoted uppercase header label", ` "Windows-1251" `, word1251, false, want},
		{"undeclared valid UTF-8", "", utf8Word, true, utf8Word},
		{"utf8 label with stray bytes", "utf8", "a\xFFb\xC3", false, "a" + rep + "b" + rep},
		{"unknown label, ASCII body", "shift_jis", "plain ascii\r\n\ttext", false, "plain ascii\r\n\ttext"},
		{"unknown meta label, ASCII body", "", `<meta charset="x-unknown">hi`, true, `<meta charset="x-unknown">hi`},
	}
	for _, c := range cases {
		got, err := decodeBody([]byte(c.body), c.header, c.html, false)
		if err != nil {
			t.Errorf("%s: %v", c.name, err)
			continue
		}
		if got != c.want {
			t.Errorf("%s = %q, want %q", c.name, got, c.want)
		}
	}

	pad := "<!-- " + strings.Repeat("x", 1100) + " -->"
	refused := []struct {
		name, header, body string
		html               bool
		label              string
	}{
		{"undeclared non-UTF-8", "", word1251, true, ""},
		{"meta after 1024 bytes", "", pad + metaKOI + wordKOI, true, ""},
		{"utf-7 with ASCII body", "utf-7", "+ADw-script+AD4-", false, "utf-7"},
		{"utf-16 label with ASCII body", "utf-16le", "abc", false, "utf-16le"},
		{"iso-2022 label", "iso-2022-jp", "abc", false, "iso-2022-jp"},
		{"hz label", "hz-gb-2312", "~{abc~}", false, "hz-gb-2312"},
		{"unknown label, non-ASCII body", "shift_jis", "a\x82\xA0", false, "shift_jis"},
		{"unknown label, control byte", "gbk", "a\x01b", false, "gbk"},
		{"UTF-16 BE BOM", "utf-8", "\xFE\xFF\x00a", false, "utf-16be"},
		{"UTF-16 LE BOM", "", "\xFF\xFEa\x00", true, "utf-16le"},
	}
	for _, c := range refused {
		_, err := decodeBody([]byte(c.body), c.header, c.html, false)
		var ue *unsupportedEncoding
		if !errors.As(err, &ue) {
			t.Errorf("%s: err = %v, want unsupported encoding", c.name, err)
			continue
		}
		if ue.label != c.label {
			t.Errorf("%s: label %q, want %q", c.name, ue.label, c.label)
		}
	}
}

func TestDecodeCutBody(t *testing.T) {
	body := []byte("ab" + string(rune(0x044F)))
	body = body[:len(body)-1] // cut inside the last rune
	got, err := decodeBody(body, "", false, true)
	if err != nil || got != "ab" {
		t.Errorf("cut undeclared = %q, %v", got, err)
	}
	got, err = decodeBody(body, "utf-8", false, true)
	if err != nil || got != "ab" {
		t.Errorf("cut utf-8 = %q, %v", got, err)
	}
	if _, err := decodeBody(body, "", false, false); err == nil {
		t.Error("uncut invalid body accepted")
	}
	four := []byte(string(rune(0x1F600)))
	for n := 1; n < 4; n++ {
		if got, err := decodeBody(append([]byte("x"), four[:n]...), "", false, true); err != nil || got != "x" {
			t.Errorf("cut after %d bytes = %q, %v", n, got, err)
		}
	}
}

func TestCharsetFromContent(t *testing.T) {
	cases := map[string]string{
		"text/html; charset=koi8-r":       "koi8-r",
		"text/html;charset = \"cp1251\"":  "cp1251",
		"text/html; CHARSET='latin1'; x":  "latin1",
		"text/html; charset=utf-8 foo":    "utf-8",
		"text/html; charsetx; charset=l1": "l1",
		"text/html":                       "",
		"charset=\"unterminated":          "",
	}
	for in, want := range cases {
		if got := charsetFromContent(in); got != want {
			t.Errorf("charsetFromContent(%q) = %q, want %q", in, got, want)
		}
	}
}
