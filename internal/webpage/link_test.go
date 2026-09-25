package webpage

import (
	"slices"
	"strings"
	"testing"
	"unicode"
)

func TestParseLink(t *testing.T) {
	idn := "\U0000043F\U00000440\U00000438\U0000043C\U00000435\U00000440.test"
	ok := []struct{ in, raw, host string }{
		{"https://example.test/a?b=c#d", "https://example.test/a?b=c#d", "example.test"},
		{"  http://Example.TEST:8080/x \n", "http://Example.TEST:8080/x", "example.test"},
		{"HTTPS://example.test", "HTTPS://example.test", "example.test"},
		{"http://[::1]:80/", "http://[::1]:80/", "::1"},
		{"https://example.test/a[1]", "https://example.test/a[1]", "example.test"},
		{"https://" + idn + "/", "https://" + idn + "/", idn},
	}
	for _, c := range ok {
		l, err := ParseLink(c.in)
		if err != nil {
			t.Errorf("ParseLink(%q) = %v", c.in, err)
			continue
		}
		if l.Raw != c.raw || l.Host() != c.host || l.URL == nil {
			t.Errorf("ParseLink(%q) = raw %q host %q", c.in, l.Raw, l.Host())
		}
	}

	bad := []string{
		"",
		"   ",
		"example.test/page",
		"/relative/path",
		"//example.test/x",
		"ftp://example.test/x",
		"javascript:alert(1)",
		"mailto:a@example.test",
		"http:///path",
		"http://:80/x",
		"http:example.test",
		"https://user@example.test/",
		"https://user:pass@example.test/",
		"https://@example.test/",
		"https://example.test/a b",
		"https://example.test/a\tb",
		"https://example.test/a\x00",
		"https://example.test/a\x7f",
		"https://example.test/a\U00000085b",
		"https://example.test/a\U000000A0b",
		"https://example.test/a\u202Etxt.exe",
		"https://exa\u200Bmple.test/",
		"https://example.test/<x>",
		"https://example.test/\"x\"",
		"https://example.test/`x`",
		"https://example.test/a\\b",
		"https://example.test/[[x]]",
		"https://example.test/x]]",
		"https://example.test/%zz",
		"https://example.test/\xff",
		"https://example.test/" + strings.Repeat("a", 8<<10),
		"https://example.test https://other.test",
		"http://%C2%9B31m.test/",
		"https://a%E2%80%AEb.test/",
		"https://a%E2%80%8Bb.test/",
		"http://%FF.test/",
		"http://a%20b.test/",
	}
	for _, in := range bad {
		_, err := ParseLink(in)
		if err == nil {
			t.Errorf("ParseLink(%q) accepted", in)
			continue
		}
		if len(in) > 12 && strings.Contains(err.Error(), in[8:]) {
			t.Errorf("ParseLink(%q) error quotes the link: %v", in, err)
		}
	}

	limit := "https://example.test/" + strings.Repeat("a", 8<<10-len("https://example.test/"))
	if _, err := ParseLink(limit); err != nil {
		t.Errorf("8 KiB link refused: %v", err)
	}
}

func TestSensitive(t *testing.T) {
	flagged := []string{
		"https://e.test/cb?token=abc",
		"https://e.test/cb?access_token=abc",
		"https://e.test/cb?api_key=abc",
		"https://e.test/cb?apiKey=abc",
		"https://e.test/f?X-Amz-Signature=abc&X-Amz-Date=1",
		"https://e.test/f?sig=abc",
		"https://e.test/oauth?code=abc&state=1",
		"https://e.test/app#access_token=abc&type=bearer",
		"https://e.test/app#!/cb?id_token=abc",
		"https://e.test/x?PASSWORD=abc",
		"https://e.test/x?user.pass=abc",
		"https://e.test/x?session-id=abc",
		"https://e.test/x?my%5Ftoken=abc",
		"https://e.test/x?q=1;sid=abc",
		"https://e.test/x?client_secret=abc",
		"https://e.test/x?auth=abc",
		"https://e.test/x?otp=1",
		"https://e.test/x?credentials=1",
		"https://e.test/p;jsessionid=x",
		"https://e.test/a;sid=1/b",
		"https://e.test/x?PHPSESSID=abc",
	}
	for _, s := range flagged {
		l := mustParse(t, s)
		if len(l.Sensitive()) == 0 {
			t.Errorf("Sensitive(%q) = none", s)
		}
	}

	clean := []string{
		"https://e.test/x?author=abc",
		"https://e.test/x?authuser=1",
		"https://e.test/x?authority=abc",
		"https://e.test/watch?v=abc&q=go&page=2",
		"https://e.test/x?utm_source=news&utm_medium=mail",
		"https://e.test/x#section-2",
		"https://e.test/x#token",
		"https://e.test/token/secret/page",
		"https://e.test/x?keyword=go",
		"https://e.test/p;v=2",
	}
	for _, s := range clean {
		l := mustParse(t, s)
		if got := l.Sensitive(); len(got) != 0 {
			t.Errorf("Sensitive(%q) = %v", s, got)
		}
	}

	l := mustParse(t, "https://e.test/x?a=1#token=VALUE123")
	if got := l.Sensitive(); !slices.Equal(got, []string{"fragment-parameter"}) {
		t.Errorf("fragment kinds = %v", got)
	}
	l = mustParse(t, "https://e.test/a;sid=1/b?x=1")
	if got := l.Sensitive(); !slices.Equal(got, []string{"path-parameter"}) {
		t.Errorf("path kinds = %v", got)
	}
	l = mustParse(t, "https://e.test/x?token=VALUE123")
	if got := l.Sensitive(); !slices.Equal(got, []string{"query-parameter"}) {
		t.Errorf("query kinds = %v", got)
	}
	// Fixture is split so secret scanners do not flag it.
	l = mustParse(t, "https://e.test/repo/gh"+"p_abcdefghijklmnopqrstuvwxyz0123456789AB")
	got := l.Sensitive()
	if !slices.Equal(got, []string{"github-token"}) {
		t.Errorf("FindSecrets kinds = %v", got)
	}
	for _, kind := range got {
		if strings.Contains(kind, "ghp_") {
			t.Errorf("kind carries a value: %q", kind)
		}
	}
}

func mustParse(t *testing.T, s string) Link {
	t.Helper()
	l, err := ParseLink(s)
	if err != nil {
		t.Fatalf("ParseLink(%q): %v", s, err)
	}
	return l
}

func TestParseLinkHostCharacters(t *testing.T) {
	for _, s := range []string{"http://%C2%9B31m.test/", "https://a%E2%80%AEb.test/", "https://a%E2%80%8Bb.test/", "http://%FF.test/"} {
		l, err := ParseLink(s)
		if err == nil {
			t.Errorf("%s accepted with host %q", s, l.Host())
			continue
		}
		if err.Error() != "link host contains control or format characters" {
			t.Errorf("%s: %v", s, err)
		}
	}
	for _, s := range []string{"https://%D0%BF%D1%80%D0%B8%D0%BC%D0%B5%D1%80.test/", "https://xn--e1afmkfd.test/"} {
		l, err := ParseLink(s)
		if err != nil {
			t.Errorf("%s refused: %v", s, err)
			continue
		}
		if strings.ContainsFunc(l.Host(), func(r rune) bool { return unicode.IsControl(r) || unicode.Is(unicode.Cf, r) }) {
			t.Errorf("%s: host %q", s, l.Host())
		}
	}
}
