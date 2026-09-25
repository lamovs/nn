package ask

import "testing"

// zeroWidthSpace is U+200B, a format character Literal drops.
const zeroWidthSpace = "\xe2\x80\x8b"

func TestLiteralMatchesRenderer(t *testing.T) {
	cases := []string{
		"",
		"plain text",
		"line one\nline two\r\n\tline three",
		"soft\xc2\xadhyphen and zero" + zeroWidthSpace + "width and \xe2\x80\xaeoverride",
		"\x1b]8;;http://e\x07link\x1b]8;;\x07",
		"[t](obsidian://x) <%* x %> `code` [[x]] #tag",
		"https://e.test www.example.org",
		"key:: v - [ ] x a:::b %% hidden 100%%",
		"\\[x\\] &lt; ==x== *b* _i_ !img",
		"separator\xe2\x80\xa8inside\xe2\x80\xa9text",
		"\xd0\xbf\xd1\x80\xd0\xb8\xd0\xb2\xd0\xb5\xd1\x82",
	}
	for _, text := range cases {
		if got, want := Literal(text), literal(text); got != want {
			t.Errorf("Literal(%q) = %q, want %q", text, got, want)
		}
	}
	if got := Literal("a\nb" + zeroWidthSpace); got != "a b" {
		t.Fatalf("Literal = %q", got)
	}
}
