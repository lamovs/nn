package webpage

import (
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

func TestExtractTitleAndDescription(t *testing.T) {
	cases := []struct {
		name, doc, title, desc string
	}{
		{"title and description",
			`<html><head><title> Page
			Title </title><meta name="Description" content="About &amp; more">` +
				`<meta property="og:title" content="OG"><meta property="og:description" content="OG desc"></head></html>`,
			"Page Title", "About & more"},
		{"og fallbacks", `<meta property="og:title" content="OG &quot;T&quot;"><meta property="og:description" content="D">`, `OG "T"`, "D"},
		{"first title wins", `<title>One</title><title>Two</title>`, "One", ""},
		{"empty title falls back", `<title> </title><meta name="og:title" content="Named">`, "Named", ""},
		{"uppercase", `<TITLE>Up</TITLE><META NAME="DESCRIPTION" CONTENT="Desc">`, "Up", "Desc"},
		{"svg title ignored", `<svg><title>Icon</title></svg><meta property="og:title" content="Real">`, "Real", ""},
		{"title markup is text", `<title>A <b>&lt;B&gt;</title>`, "A <b><B>", ""},
		{"unclosed title", `<title>Loose<body><p>text</p>`, "Loose", ""},
		{"controls dropped", "<title>a\x01b\x7fc</title>", "abc", ""},
	}
	for _, c := range cases {
		title, desc, _, _ := extractHTML(c.doc)
		if title != c.title || desc != c.desc {
			t.Errorf("%s: title %q desc %q, want %q %q", c.name, title, desc, c.title, c.desc)
		}
	}

	long := "<title>" + strings.Repeat("x", 10000) + "</title>"
	if title, _, _, _ := extractHTML(long); len(title) != maxFieldBytes {
		t.Errorf("long title kept %d bytes", len(title))
	}
}

func TestExtractText(t *testing.T) {
	eAcute, nbsp, copyright, nel := "\U000000E9", "\U000000A0", "\U000000A9", "\U00000085"
	cases := []struct {
		name, doc, want string
	}{
		{"paragraphs", `<p>One</p><p>Two  words</p>`, "One\n\nTwo words"},
		{"list", `<ul><li>a</li><li><a href="x">b</a> c</li></ul><p>after</p>`, "- a\n- b c\n\nafter"},
		{"br and div", `<div>line1<br>line2</div><div>line3</div>`, "line1\nline2\nline3"},
		{"double br", `a<br><br><br>b`, "a\n\nb"},
		{"headings", `<h1>Head</h1>body text`, "Head\n\nbody text"},
		{"whitespace", "  <p>\n  lots   of\t\tspace \n here  </p>  ", "lots of space here"},
		{"inline elements", `<p>a<b>b</b><i> c</i>d</p>`, "ab cd"},
		{"entities", `<p>&amp; &lt;b&gt; &#x41;&#66; &eacute;&nbsp;x &copy</p>`, "& <b> AB " + eAcute + " x " + copyright},
		{"nbsp collapses", "a" + nbsp + nbsp + "b", "a b"},
		{"comments", `a<!-- hidden <p>x</p> -->b<!---->c<!-->d`, "abcd"},
		{"doctype and pi", `<!DOCTYPE html><?xml version="1.0"?>text`, "text"},
		{"cdata", `a<![CDATA[ hidden ]]>b`, "ab"},
		{"attribute with >", `<a title="x > y" data-z='1>2'>Link</a>`, "Link"},
		{"unquoted attr", `<a href=/x?a=1>go</a>`, "go"},
		{"stray <", `1 < 2 and 3<4 </ 5`, "1 < 2 and 3<4 </ 5"},
		{"uppercase skip", `<SCRIPT>var a = "<p>no</p>";</SCRIPT><P>yes</P>`, "yes"},
		{"script with markup", `<script>if (a<b) { x = "</div>" }</script>ok`, "ok"},
		{"unclosed", `<p>one<p>two<div>three`, "one\n\ntwo\nthree"},
		{"cut tag", `text<a href="x`, "text"},
		{"pre", "<pre>a  b\nc\n\n\nd</pre>e", "a b\nc\n\nd\n\ne"},
		{"controls", "a\x00b\x1bc\x7fd" + nel + "e", "abcd e"},
		{"table", `<table><tr><td>a</td><td>b</td></tr><tr><td>c</td></tr></table>`, "a b\nc"},
		{"self-closing svg", `<svg/>visible`, "visible"},
		{"nested skip", `<nav><nav>x</nav>y</nav>z`, "z"},
		{"form content kept", `<form><p>article</p></form>`, "article"},
		{"form controls skipped", `<form><p>t</p><select><option>x</option></select><button>b</button></form>`, "t"},
	}
	for _, c := range cases {
		_, _, text, truncated := extractHTML(c.doc)
		if text != c.want || truncated {
			t.Errorf("%s: %q (truncated %v), want %q", c.name, text, truncated, c.want)
		}
	}
}

func TestExtractSkippedElements(t *testing.T) {
	var b strings.Builder
	b.WriteString("<body>keep1 ")
	for name := range skippedElements {
		b.WriteString("<" + name + ` class="x">SKIPPED <p>SKIPPED</p></` + strings.ToUpper(name) + ">")
	}
	b.WriteString(" keep2</body>")
	_, _, text, _ := extractHTML(b.String())
	if strings.Contains(text, "SKIPPED") || text != "keep1 keep2" {
		t.Errorf("text = %q", text)
	}

	_, _, text, _ = extractHTML(`<p>visible</p><footer>unclosed footer`)
	if text != "visible" {
		t.Errorf("unclosed skipped element: %q", text)
	}
}

func TestExtractMainArticle(t *testing.T) {
	long := strings.Repeat("word ", 120) // 600 runes
	doc := `<header>Site header</header><main><p>` + long + `</p></main><div>sidebar text</div><article>Second</article>`
	_, _, text, _ := extractHTML(doc)
	if strings.Contains(text, "Site header") || strings.Contains(text, "sidebar") {
		t.Errorf("main rule kept outer text: %q", text[:40])
	}
	if !strings.HasPrefix(text, "word word") || !strings.HasSuffix(text, "word\n\nSecond") {
		t.Errorf("main text = %q...%q", text[:20], text[len(text)-20:])
	}

	short := `<header>Site header</header><main><p>short main</p></main><div>sidebar text</div>`
	_, _, text, _ = extractHTML(short)
	if text != "Site header\n\nshort main\n\nsidebar text" {
		t.Errorf("short main = %q", text)
	}

	nested := `<div>outer</div><main>` + long[:250] + `<article>` + long[:260] + `</article></main>`
	_, _, text, _ = extractHTML(nested)
	if strings.Contains(text, "outer") {
		t.Errorf("nested main/article over 500 runes kept outer text")
	}

	unclosed := `<div>outer</div><article>` + long
	_, _, text, _ = extractHTML(unclosed)
	if strings.Contains(text, "outer") {
		t.Errorf("unclosed article kept outer text")
	}
}

func TestExtractCap(t *testing.T) {
	word := string(rune(0x00E9)) // two bytes
	doc := "<p>a" + strings.Repeat(word, maxTextBytes) + "</p>"
	_, _, text, truncated := extractHTML(doc)
	if !truncated || len(text) > maxTextBytes || !utf8.ValidString(text) {
		t.Errorf("cap: truncated %v, %d bytes, valid %v", truncated, len(text), utf8.ValidString(text))
	}
	if len(text) != maxTextBytes-1 {
		t.Errorf("cap cut at %d bytes, want %d", len(text), maxTextBytes-1)
	}

	plain, truncated := extractPlain("a" + strings.Repeat(word, maxTextBytes))
	if !truncated || len(plain) != maxTextBytes-1 || !utf8.ValidString(plain) {
		t.Errorf("plain cap: truncated %v, %d bytes", truncated, len(plain))
	}
}

func TestExtractPlain(t *testing.T) {
	got, truncated := extractPlain("\r\n line1\r\nline2\rline3\x00\x1b\t tab\x7f\n\n")
	if got != "line1\nline2\nline3\t tab" || truncated {
		t.Errorf("plain = %q", got)
	}
	if got, _ := extractPlain(" \r\n\t "); got != "" {
		t.Errorf("blank plain = %q", got)
	}
}

func TestMetaCharsetScanner(t *testing.T) {
	cases := map[string]string{
		`<meta charset=koi8-r>`:                                                "koi8-r",
		`<!-- <meta charset="cp1251"> --><meta charset="utf-8">`:               "utf-8",
		`<script>"<meta charset=cp1251>"</script><meta charset=l1>`:            "l1",
		`<meta http-equiv=content-type content="text/html; charset=cp1251">`:   "cp1251",
		`<meta content="text/html; charset=cp1251" http-equiv="Content-Type">`: "cp1251",
		`<meta http-equiv="refresh" content="0; charset=cp1251">`:              "",
		`<meta name="x" content="charset=cp1251">`:                             "",
		`<meta charset="koi8-r"`:                                               "",
	}
	for in, want := range cases {
		if got := metaCharset(in); got != want {
			t.Errorf("metaCharset(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestExtractHostileInputIsLinear(t *testing.T) {
	docs := map[string]string{
		"unclosed titles":    strings.Repeat("<title>x", 400000),
		"unclosed textareas": strings.Repeat("<textarea>x", 300000),
		"many attributes":    "<meta" + strings.Repeat(" a", 1<<20) + ` charset="koi8-r"><p>after</p>`,
		"many end tags":      "<script>" + strings.Repeat("</scrip", 500000),
		"stray brackets":     strings.Repeat("< <!", 1<<20),
	}
	for name, doc := range docs {
		start := time.Now()
		extractHTML(doc)
		metaCharset(doc)
		if d := time.Since(start); d > 10*time.Second {
			t.Errorf("%s: %v", name, d)
		}
	}
	if _, _, text, _ := extractHTML("<meta" + strings.Repeat(" a", 100) + "><p>after</p>"); text != "after" {
		t.Errorf("text after a long tag = %q", text)
	}
}
