package vault

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestSlug(t *testing.T) {
	tests := []struct{ in, want string }{
		{"Чашка ТВ дешевле цены рынка", "chashka-tv-deshevle-ceny-rynka"},
		{"BigInt из драйвера приходит строкой", "bigint-iz-drayvera-prihodit-strokoy"},
		{"Docker cleanup", "docker-cleanup"},
		{"  --Hello,   World!!  ", "hello-world"},
		{"Съешь ещё этих мягких французских булок", "sesh-esche-etih-myagkih-francuzskih-bulok"},
		{"йогурт, хурма, цапля, щука, жук, юла, яма, эхо, ёж", "yogurt-hurma-caplya-schuka-zhuk-yula-yama-eho-ezh"},
		{"Don't panic", "dont-panic"},
		{"Go 1.27 release", "go-1-27-release"},
		{"Café crème", "cafe-creme"},
		{"日本語", ""},
		{"", ""},
		{strings.Repeat("word ", 20), "word-word-word-word-word-word-word-word-word-word-word-word"},
		{strings.Repeat("x", 70), strings.Repeat("x", 60)},
		{"a " + strings.Repeat("y", 70), "a-" + strings.Repeat("y", 58)},
	}
	for _, tt := range tests {
		got := Slug(tt.in)
		if got != tt.want {
			t.Errorf("Slug(%q) = %q, want %q", tt.in, got, tt.want)
		}
		if len(got) > 60 {
			t.Errorf("Slug(%q) is %d chars", tt.in, len(got))
		}
	}
}

func TestSlugNormalizesUnicode(t *testing.T) {
	tests := []struct{ name, nfc, nfd, want string }{
		// "Elka i may": io at the front, short i at the end.
		{
			"short i",
			"\u0401\u043b\u043a\u0430 \u0438 \u043c\u0430\u0439",
			"\u0415\u0308\u043b\u043a\u0430 \u0438 \u043c\u0430\u0438\u0306",
			"elka-i-may",
		},
		// "Kyiv" in Ukrainian, with yi.
		{"yi", "\u041a\u0438\u0457\u0432", "\u041a\u0438\u0456\u0308\u0432", "kiyiv"},
		// "Belarus" plus a lone short u.
		{
			"short u",
			"\u0411\u0435\u043b\u0430\u0440\u0443\u0441\u044c \u045e",
			"\u0411\u0435\u043b\u0430\u0440\u0443\u0441\u044c \u0443\u0306",
			"belarus-u",
		},
		{"latin accents", "Caf\u00e9 cr\u00e8me", "Cafe\u0301 cre\u0300me", "cafe-creme"},
		{"caron", "\u0160koda servis", "S\u030ckoda servis", "skoda-servis"},
		// Ogonek and caron together, the way a Lithuanian title arrives.
		{"ogonek", "\u0104\u017euolas", "A\u0328Z\u030cuolas", "azuolas"},
		// Double acute and macron, Hungarian and Latvian.
		{"double acute", "\u0150r\u00fclt id\u0151", "O\u030bru\u0308lt ido\u030b", "orult-ido"},
		{"macron", "J\u016bras \u016bdens", "Ju\u0304ras u\u0304dens", "juras-udens"},
		// A stress mark is not a letter and must not split the word.
		{
			"stress mark",
			"\u0437\u0430\u043c\u0435\u0442\u043a\u0430",
			"\u0437\u0430\u043c\u0435\u0301\u0442\u043a\u0430",
			"zametka",
		},
	}
	for _, tt := range tests {
		if got := Slug(tt.nfc); got != tt.want {
			t.Errorf("%s: Slug(NFC) = %q, want %q", tt.name, got, tt.want)
		}
		if got := Slug(tt.nfd); got != tt.want {
			t.Errorf("%s: Slug(NFD) = %q, want %q", tt.name, got, tt.want)
		}
	}

	atomic := map[string]string{
		"\u0141\u00f3d\u017a":      "lodz",      // l with stroke, z with acute
		"\u0110uro \u0160e\u0107a": "duro-seca", // d with stroke, s and c with caron and acute
		"Ko\u0161\u0131k":          "kosik",     // dotless i, Turkish
	}
	for title, want := range atomic {
		if got := Slug(title); got != want {
			t.Errorf("Slug(%q) = %q, want %q", title, got, want)
		}
	}
}

func TestInlineTags(t *testing.T) {
	tests := []struct {
		name string
		body string
		want []string
	}{
		{"simple", "text #go and #golang\n#start", []string{"go", "golang", "start"}},
		{"cyrillic and nested", "про #цод и #work/review, #snake_case #kebab-case", []string{"цод", "work/review", "snake_case", "kebab-case"}},
		{"case-insensitive dedupe", "#Docker #docker #DOCKER", []string{"Docker"}},
		{"headings are not tags", "# Heading\n## Sub #real\n###nope", []string{"real"}},
		{"numbers", "issue #123 and #2024 but #v2 and #2024-01", []string{"v2", "2024-01"}},
		{"urls and anchors", "see https://example.com/page#section and [x](#anchor) [[note#heading]] C#", nil},
		{"inline code", "use `#notatag` and ``a #also` b`` then #yes", []string{"yes"}},
		{"fenced code", "```sh\necho #nope\n```\n~~~\n#nope2\n~~~\nafter #ok", []string{"ok"}},
		{"unclosed fence", "#before\n```\n#inside", []string{"before"}},
		{"trailing slash and punctuation", "#tag/ #done. (#paren)", []string{"tag", "done"}},
		{"empty", "", nil},
	}
	for _, tt := range tests {
		if got := InlineTags(tt.body); !reflect.DeepEqual(got, tt.want) {
			t.Errorf("%s: InlineTags = %#v, want %#v", tt.name, got, tt.want)
		}
	}
}

func TestCodeBlocks(t *testing.T) {
	body := "intro\n```go title=\"x\"\nfunc main() {}\n\nx := 1\n```\ntext\n  ~~~~\n  indented\n  ```\n  still code\n  ~~~~\n```{.python}\nprint(1)\n```\n````\nunclosed\n"
	got := CodeBlocks(body, 10)
	want := []CodeBlock{
		{Lang: "go", Code: "func main() {}\n\nx := 1", StartLine: 11, EndLine: 15},
		{Lang: "", Code: "indented\n```\nstill code", StartLine: 17, EndLine: 21},
		{Lang: "python", Code: "print(1)", StartLine: 22, EndLine: 24},
		{Lang: "", Code: "unclosed", StartLine: 25, EndLine: 26},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("CodeBlocks =\n%#v\nwant\n%#v", got, want)
	}
	if got := CodeBlocks("no code", 1); got != nil {
		t.Errorf("no code: %#v", got)
	}
	crlf := CodeBlocks("```sh\r\nls\r\n```\r\n", 1)
	if len(crlf) != 1 || crlf[0].Code != "ls" || crlf[0].Lang != "sh" {
		t.Errorf("crlf: %#v", crlf)
	}
}

func TestMaskCode(t *testing.T) {
	body := "a `b` c\n```\nx\n```\nd ``e`` `f\n"
	got := MaskCode(body)
	want := "a     c\n   \n \n   \nd       `f\n"
	if got != want {
		t.Fatalf("MaskCode = %q, want %q", got, want)
	}
	if len(got) != len(body) {
		t.Fatalf("length changed")
	}
	cyr := "`код` [[ссылка]]"
	if m := MaskCode(cyr); len(m) != len(cyr) || !strings.HasSuffix(m, "[[ссылка]]") {
		t.Fatalf("MaskCode(%q) = %q", cyr, m)
	}
}

func TestFirstH1(t *testing.T) {
	tests := []struct{ body, want string }{
		{"text\n# Title\n# Second", "Title"},
		{"```\n# not\n```\n#  Real `code` title ##\n", "Real `code` title"},
		{"## Sub\n#nospace", ""},
		{"    # too indented\n", ""},
		{"# C#", "C#"},
	}
	for _, tt := range tests {
		if got := firstH1(tt.body); got != tt.want {
			t.Errorf("firstH1(%q) = %q, want %q", tt.body, got, tt.want)
		}
	}
}

func TestContext(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	repo := filepath.Join(home, "src", "demo")
	sub := filepath.Join(repo, "internal", "cli")
	if err := os.MkdirAll(filepath.Join(repo, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	worktree := filepath.Join(home, "wt", "feature")
	if err := os.MkdirAll(worktree, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(worktree, ".git"), []byte("gitdir: x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	plain := filepath.Join(home, "plain")
	if err := os.MkdirAll(plain, 0o755); err != nil {
		t.Fatal(err)
	}

	tests := []struct{ cwd, where, repo string }{
		{sub, "~/src/demo/internal/cli", "demo"},
		{repo, "~/src/demo", "demo"},
		{worktree, "~/wt/feature", "feature"},
		{plain, "~/plain", ""},
		{home, "~", ""},
	}
	for _, tt := range tests {
		where, repo := Context(tt.cwd)
		if where != tt.where || repo != tt.repo {
			t.Errorf("Context(%q) = %q, %q; want %q, %q", tt.cwd, where, repo, tt.where, tt.repo)
		}
	}
}
