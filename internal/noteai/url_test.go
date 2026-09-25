package noteai

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/lamovs/nn/internal/ai"
	"github.com/lamovs/nn/internal/app"
	"github.com/lamovs/nn/internal/config"
	"github.com/lamovs/nn/internal/vault"
)

const urlTestLink = "https://e.test/blog/go-pgo?utm_source=x#part"

func urlFixture(t *testing.T, answer string) (*app.Env, *Plan, string) {
	t.Helper()
	env, _, base := metadataFixture(t, false, answer)
	env.Now = func() time.Time { return time.Date(2026, 9, 24, 10, 30, 0, 0, time.UTC) }
	env.Cfg.Hooks.PostSave = "printf '%s\\n' \"$NN_ACTION\" >> \"$NN_METADATA_BASE/hooks\""
	plan, err := Prepare(&env.Cfg, "url", ai.Overrides{}, "wait", onceAsker{}, io.Discard)
	if err != nil || plan == nil || plan.Task != "url" {
		t.Fatalf("prepare url: %+v %v", plan, err)
	}
	return env, plan, base
}

func urlInput() URLInput {
	return URLInput{Link: urlTestLink, Host: "e.test", LinkPath: "/blog/go-pgo", PageTitle: "Profile-guided optimization", Description: "How PGO works", Text: "Readable page text."}
}

func urlCaches(t *testing.T, base string) []string {
	t.Helper()
	dirs, _ := filepath.Glob(filepath.Join(base, "cache", "nn", "ai", "url-*"))
	return dirs
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
	return string(data)
}

// liveMarks returns characters of set not preceded by an odd number of
// backslashes, which Markdown would still read as syntax.
func liveMarks(s, set string) string {
	var live []rune
	backslashes := 0
	for _, r := range s {
		if strings.ContainsRune(set, r) && backslashes%2 == 0 {
			live = append(live, r)
		}
		if r == '\\' {
			backslashes++
		} else {
			backslashes = 0
		}
	}
	return string(live)
}

func TestPrepareURLHasNoRunGating(t *testing.T) {
	env, _, _ := urlFixture(t, `{"title":"t","tags":[],"body":"b"}`)
	if env.Cfg.AI.Tasks["url"].Run != "" {
		t.Fatalf("url run=%q", env.Cfg.AI.Tasks["url"].Run)
	}
	var stderr bytes.Buffer
	if plan, err := Prepare(&env.Cfg, "url", ai.Overrides{NoAI: true}, "", onceAsker{}, &stderr); err != nil || plan != nil {
		t.Fatalf("no-ai=%+v %v", plan, err)
	}
	if plan, err := Prepare(&env.Cfg, "url", ai.Overrides{}, "", nil, &stderr); err != nil || plan != nil || !strings.Contains(stderr.String(), "nn: url: AI needs consent") {
		t.Fatalf("unasked=%+v %v %q", plan, err, stderr.String())
	}
	if plan, err := Prepare(&env.Cfg, "url", ai.Overrides{}, "auto", onceAsker{}, io.Discard); err != nil || plan == nil || plan.Mode != "wait" {
		t.Fatalf("interactive auto=%+v %v", plan, err)
	}
	if plan, err := Prepare(&env.Cfg, "url", ai.Overrides{}, "", onceAsker{}, io.Discard); err != nil || plan == nil || plan.Mode != "background" {
		t.Fatalf("default mode=%+v %v", plan, err)
	}
	for key := range env.Cfg.AI.Consent {
		env.Cfg.AI.Consent[key] = "always"
	}
	if plan, err := Prepare(&env.Cfg, "url", ai.Overrides{}, "auto", nil, io.Discard); err != nil || plan == nil || plan.Mode != "background" {
		t.Fatalf("always auto=%+v %v", plan, err)
	}
	for key := range env.Cfg.AI.Consent {
		env.Cfg.AI.Consent[key] = "never"
	}
	stderr.Reset()
	if plan, err := Prepare(&env.Cfg, "url", ai.Overrides{}, "", onceAsker{}, &stderr); err != nil || plan != nil || stderr.Len() != 0 {
		t.Fatalf("never=%+v %v %q", plan, err, stderr.String())
	}
	var consent *ai.ConsentError
	if _, err := Prepare(&env.Cfg, "url", ai.Overrides{AI: true}, "", onceAsker{}, io.Discard); !errors.As(err, &consent) {
		t.Fatalf("explicit never=%v", err)
	}
	if _, err := Prepare(&env.Cfg, "last", ai.Overrides{}, "", nil, io.Discard); err == nil {
		t.Fatal("unsupported task accepted")
	}
	if dir, err := NewCache("url"); err != nil || !strings.HasPrefix(filepath.Base(dir), "url-") {
		t.Fatalf("cache=%q %v", dir, err)
	}
	if got := Recovery("url", "/c/d"); got != "\n\nLink summary did not finish. Original retained at:\n\n    /c/d\n" {
		t.Fatalf("recovery=%q", got)
	}
}

func TestURLPayloadBudgets(t *testing.T) {
	in := urlInput()
	in.Text = "a < b && c > d"
	p, _ := urlRequest(in, nil)
	text := marshalPayload(p)
	if !strings.Contains(text, `"existing_tags":[]`) || !strings.Contains(text, `"text":"a < b && c > d"`) || p.Truncated {
		t.Fatalf("payload=%s", text)
	}
	if got, err := parseURLPayload(text); err != nil || got.Text != in.Text || got.URL != urlTestLink {
		t.Fatalf("round trip=%+v %v", got, err)
	}
	var vocabulary []string
	for i := 0; len(vocabulary)*10 <= urlTagsBytes; i++ {
		vocabulary = append(vocabulary, strings.Repeat("t", 6)+string(rune('a'+i%26))+strings.Repeat("x", i%3))
	}
	if p, _ := urlRequest(in, vocabulary); len(p.ExistingTags) != 0 || p.ExistingTags == nil {
		t.Fatalf("oversized vocabulary sent: %d", len(p.ExistingTags))
	}
	if p, _ := urlRequest(in, []string{"Go", "perf"}); strings.Join(p.ExistingTags, ",") != "Go,perf" {
		t.Fatalf("tags=%v", p.ExistingTags)
	}
	in.Text = strings.Repeat("\u041f\u0440\u0438\u0432\u0435\u0442 ", 40000) + "\n" + strings.Repeat("\"", 200000)
	in.PageTitle = strings.Repeat("T", 5000)
	in.Description = strings.Repeat("line\n", 5000)
	p, _ = urlRequest(in, []string{"Go"})
	text = marshalPayload(p)
	if len(text) > urlPayloadBytes || len(text) < urlPayloadBytes/2 || !p.Truncated || !utf8.ValidString(p.Text) {
		t.Fatalf("payload bytes=%d truncated=%v", len(text), p.Truncated)
	}
	if utf8.RuneCountInString(p.PageTitle) != urlTitleRunes || strings.Contains(p.Description, "\n") || utf8.RuneCountInString(p.Description) > urlDescriptionRunes {
		t.Fatalf("page fields not bounded: %d %d", len(p.PageTitle), len(p.Description))
	}
	for _, bad := range []string{
		`{"url":"u","page_title":"","description":"","text":"t","truncated":false}`,
		`{"url":"u","page_title":"","description":"","text":"t","truncated":false,"existing_tags":null}`,
		`{"url":"u","page_title":"","description":"","text":"t","truncated":false,"existing_tags":[],"extra":1}`,
		`{"url":"u","page_title":"","description":"","text":"t","truncated":"no","existing_tags":[]}`,
		`{"url":"u","page_title":"","description":"","text":"t","truncated":false,"existing_tags":[]} {}`,
		`{"url":"","page_title":"","description":"","text":"t","truncated":false,"existing_tags":[]}`,
		`not json`,
	} {
		if _, err := parseURLPayload(bad); err == nil {
			t.Errorf("accepted malformed payload %s", bad)
		}
	}
}

func TestURLJobFitsWorstCaseText(t *testing.T) {
	for name, unit := range map[string]string{"lt": "<", "amp-gt": "&>", "backslash-quote": `\"`, "newline": "\n"} {
		t.Run(name, func(t *testing.T) {
			in := urlInput()
			in.Text = strings.Repeat(unit, (1<<20)/len(unit))
			p, _ := urlRequest(in, []string{"Go"})
			if len(marshalPayload(p)) > urlPayloadBytes {
				t.Fatal("payload over budget")
			}
			j := Job{Version: 1, ID: "url-x", Task: "url", AllowTitle: true, Config: "/c/nn.toml", Root: "/v", Inbox: "nn", Note: "nn/e-test.md", System: strings.Repeat("prompt ", 600)}
			if !fitJob(&j, p) {
				t.Fatal("job did not fit")
			}
			data, _ := json.Marshal(j)
			if len(data) > maxJobBytes || len(data) < maxJobBytes/2 {
				t.Fatalf("job bytes=%d", len(data))
			}
			got, err := parseURLPayload(j.Text)
			if err != nil || got.Text == "" || strings.Trim(got.Text, unit) != "" || !got.Truncated {
				t.Fatalf("fitted payload=%v truncated=%v", err, got.Truncated)
			}
		})
	}
	j := Job{System: strings.Repeat("<", 80<<10)}
	if fitJob(&j, urlPayload{URL: urlTestLink, Text: strings.Repeat("<", 1000), ExistingTags: []string{}}) {
		t.Fatal("job with an oversized fixed part reported as fitting")
	}
}

func TestURLBackgroundStartNeverExceedsJobLimit(t *testing.T) {
	for name, unit := range map[string]string{"lt": "<", "amp-gt": "&>", "backslash-quote": `\"`, "newline": "\n"} {
		t.Run(name, func(t *testing.T) {
			env, plan, base := urlFixture(t, `{"title":"t","tags":[],"body":"b"}`)
			plan.Mode = "background"
			previous := workerExecutable
			workerExecutable = func() (string, error) { return "", errors.New("fake unavailable worker") }
			t.Cleanup(func() { workerExecutable = previous })
			in := urlInput()
			in.Text = "Readable\n" + strings.Repeat(unit, (1<<20)/len(unit))
			var stderr bytes.Buffer
			note, err := SaveURL(context.Background(), env, plan, in, &stderr)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(stderr.String(), "exceeds limit") || !strings.Contains(stderr.String(), "fake unavailable worker") {
				t.Fatalf("stderr=%s", stderr.String())
			}
			dirs := urlCaches(t, base)
			if len(dirs) != 1 {
				t.Fatalf("caches=%v", dirs)
			}
			job, err := os.Stat(filepath.Join(dirs[0], "job.json"))
			if err != nil || job.Size() > maxJobBytes {
				t.Fatalf("job.json=%v %v", job, err)
			}
			request := readFile(t, filepath.Join(dirs[0], "request.json"))
			if p, err := parseURLPayload(request); err != nil || len(request) > urlPayloadBytes || !strings.HasPrefix(p.Text, "Readable\n"+unit) || !p.Truncated {
				t.Fatalf("request.json bytes=%d err=%v", len(request), err)
			}
			if !strings.Contains(note.Body, "Link summary did not finish. Original retained at:\n\n    "+dirs[0]) {
				t.Fatalf("body=%s", note.Body)
			}
			if unit != "\n" && strings.Contains(note.Body, unit+unit+unit) {
				t.Fatal("page text landed in the vault")
			}
		})
	}
}

func TestURLPageFieldsAndLinkStayInert(t *testing.T) {
	hostile := "C# #tag [[x]] ![](http://e.test/p.png) <img src=x> $x$ %%c%% a|b ~s~ `c` *e* _u_ \\"
	in := urlInput()
	in.Link = "https://e.test/a![x](https://e.test/p.png)"
	in.LinkPath = "/a![x](https:/e.test/p.png)"
	in.PageTitle = hostile + "\n^ref\u200b\x07"
	in.Description = hostile
	body := urlLinkBody(in)
	lines := strings.Split(body, "\n")
	if lines[0] != "Source: <"+in.Link+">" || strings.Count(body, "p.png)") != 3 {
		t.Fatalf("body=%s", body)
	}
	for _, line := range lines[1:] {
		if strings.Contains(line, in.Link) || strings.Contains(line, "![") {
			t.Fatalf("link or embed outside Source: %q", line)
		}
	}
	if tags := vault.InlineTags(body); len(tags) != 0 {
		t.Fatalf("tags=%v in %s", tags, body)
	}
	if live := liveMarks(strings.Join(lines[1:], "\n"), "#[]<>!|~$%`*_"); live != "" {
		t.Fatalf("live marks %q in %s", live, body)
	}
	if strings.Contains(body, "\u200b") || strings.Contains(body, "\x07") {
		t.Fatal("format or control character kept")
	}
	alias := aliasText(in.PageTitle)
	if got := aliasText("a [b] <c> & d_e"); got != "a [b] <c> & d_e" {
		t.Fatalf("alias escaped: %q", got)
	}
	if got := aliasText("<%* x %> <<%% y <[[%"); strings.Contains(got, "<%") {
		t.Fatalf("Templater opener in alias: %q", got)
	}
	if strings.ContainsAny(alias, "|#^") || strings.Contains(alias, "[[") || strings.Contains(alias, "]]") || strings.Contains(alias, "%%") || strings.Contains(alias, "\u200b") {
		t.Fatalf("alias=%q", alias)
	}
	if got := aliasText("a[[[b]]]c [|[d] ]^]"); strings.Contains(got, "[[") || strings.Contains(got, "]]") {
		t.Fatalf("alias brackets=%q", got)
	}
	for in, want := range map[string]string{
		"a%%b": "a%b",
		"%%%%": "%",
		"%|%":  "%",
		"%[[%": "%",
		"<%%x": "<x",
		"100%": "100%",
	} {
		if got := aliasText(in); got != want {
			t.Errorf("aliasText(%q)=%q want %q", in, got, want)
		}
	}
	if got := urlLinkBody(URLInput{Link: "https://e.test/", NotFetched: "HTTP 403"}); got != "Source: <https://e.test/>\nPage not fetched: HTTP 403" {
		t.Fatalf("not fetched=%q", got)
	}
	if got := urlLinkBody(URLInput{Link: "https://e.test/", PageTitle: "Empty"}); got != "Source: <https://e.test/>\nPage title: Empty\nPage not summarized: no readable text" {
		t.Fatalf("no text=%q", got)
	}
}

func TestURLSavedLinkNoteIsInert(t *testing.T) {
	env, _, _ := urlFixture(t, `{"title":"t","tags":[],"body":"b"}`)
	in := urlInput()
	in.Link = "https://e.test/a![x](https://e.test/p.png)"
	in.PageTitle = "C# [[Wiki]] | ^x #tag"
	in.Description = "#todo ![](http://e.test/p.png)"
	note, err := SaveURL(context.Background(), env, nil, in, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if len(note.Aliases) != 1 || note.Aliases[0] != "C Wiki x tag" || len(note.Tags) != 0 || len(vault.InlineTags(note.Body)) != 0 {
		t.Fatalf("note=%+v", note)
	}
}

func TestFilenameHint(t *testing.T) {
	for path, want := range map[string]string{
		"/blog/pgo":               "e.test pgo",
		"/2024/05/my-first-post":  "e.test my-first-post",
		"/posts/hello_world.html": "e.test hello_world.html",
		"/p/2024":                 "e.test 2024",
		"/a/":                     "e.test a",
		"/wiki/%D0%9F%D1%80%D0%B8%D0%B2%D0%B5%D1%82": "e.test \u041f\u0440\u0438\u0432\u0435\u0442",
		"":                    "e.test",
		"/":                   "e.test",
		"/c/9f86d081884c7d65": "e.test",
		"/s/aGVsbG8gd29ybGQ=": "e.test",
		"/d/550e8400-e29b-41d4-a716-446655440000": "e.test",
		"/p/12345": "e.test",
		"/p/a--b":  "e.test",
		"/p/v1.2":  "e.test",
		"/p/%zz":   "e.test",
		"/p/a%2Fb": "e.test",
		"/" + strings.Repeat("word-", 12) + "end": "e.test",
	} {
		if got := filenameHint("e.test", path); got != want {
			t.Errorf("filenameHint(%q)=%q want %q", path, got, want)
		}
	}
}

func TestURLNeutralizesModelOutput(t *testing.T) {
	adversarial := []string{
		"```dataviewjs\nrequire('child_process')\n```",
		"`$=dv.el('p', 1)`",
		"<%* tR += 1 %>",
		"#todo and x #inbox",
		"[t](obsidian://open?vault=x)",
		"- [ ] task",
		"![x](http://e.test/p.png)",
		"[[x]] and ![[y]]",
		"~~~\nfence\n~~~",
		"<img src=http://e.test/p.png>",
		"\\`live\\` \\[\\[x]] \\<b> \\#tag \\\\#tag \\![x](http://e.test/p.png)",
		"status:: done\n[due:: 2026]\n\\::x",
		"Title\n===",
		"Title\n---",
		"Title\n  = = \n-\n\\---",
		"%%dv.execute(\"require('child_process')\")%%",
		"100% done, %%hidden%%, %%%",
	}
	for _, text := range adversarial {
		got := neutralize(text)
		if strings.Contains(got, "<") || liveMarks(got, "`~[]#") != "" || strings.Contains(strings.ReplaceAll(got, `\:`, ""), "::") || strings.Contains(got, "%%") {
			t.Errorf("neutralize(%q)=%q", text, got)
		}
		if tags := vault.InlineTags(got); len(tags) != 0 {
			t.Errorf("tags %v from %q", tags, text)
		}
		summary := urlSummary(ai.URLReply{Body: text}, false)
		if strings.Count(summary, "\n## ") != 0 || !strings.HasPrefix(summary, "## Summary\n\n") || strings.Contains(summary, "- [ ]") {
			t.Errorf("summary=%q", summary)
		}
		for _, line := range strings.Split(summary, "\n") {
			if line = strings.TrimSpace(line); line != "" && (strings.Trim(line, "=") == "" || strings.Trim(line, "-") == "") {
				t.Errorf("setext or break line %q in %q", line, summary)
			}
		}
		title := urlTitle(ai.URLReply{Title: text})
		if strings.ContainsAny(title, "|#^<\n") || liveMarks(title, "`~[]") != "" {
			t.Errorf("urlTitle(%q)=%q", text, title)
		}
	}
	for text, want := range map[string]string{
		"std::vector":          `std:\:vector`,
		"status:: done":        `status:\: done`,
		"a:::b":                `a:\:\:b`,
		"\\::":                 `\\:\:`,
		"Title\n===":           "Title\n\\===",
		"Title\n---\n":         "Title\n\\---\n",
		"Title\n  -- \nnext":   "Title\n  \\-- \nnext",
		"=\n-":                 "\\=\n\\-",
		"a = b\n- item\n\\===": "a = b\n- item\n\\\\===",
		"> T\n> ===":           "> T\n> \\===",
		"> > T\n> > ---":       "> > T\n> > \\---",
		">>===":                ">>\\===",
		"%%c%%":                `%\%c%\%`,
		"%%%":                  `%\%\%`,
		`%\%`:                  `%\\%`,
		"100%":                 "100%",
	} {
		if got := neutralize(text); got != want {
			t.Errorf("neutralize(%q)=%q want %q", text, got, want)
		}
	}
	if got := neutralize("a\\`b"); got != "a\\\\\\`b" {
		t.Fatalf("backslash first: %q", got)
	}
	if title := urlTitle(ai.URLReply{Title: "%%x%%"}); strings.Contains(title, "%%") {
		t.Errorf("urlTitle(%q)=%q", "%%x%%", title)
	}
	if got := urlSummary(ai.URLReply{Body: " Body. "}, true); got != "## Summary\n\nBody.\n\n"+urlTruncatedLine {
		t.Fatalf("truncated summary=%q", got)
	}
}

func TestURLWaitSuccess(t *testing.T) {
	for _, manual := range []bool{false, true} {
		t.Run(map[bool]string{false: "generated", true: "manual"}[manual], func(t *testing.T) {
			env, plan, base := urlFixture(t, `{"title":"Go PGO [guide] #x","tags":["existing","New Word","bad!"],"body":"The page says PGO helps.\n\n- One #todo\n- Two <b>"}`)
			if _, err := env.Vault.Create(vault.NewNote{Title: "Private old", Body: "Private old body", Tags: []string{"Existing"}}); err != nil {
				t.Fatal(err)
			}
			if err := os.Remove(filepath.Join(base, "hooks")); err != nil && !errors.Is(err, os.ErrNotExist) {
				t.Fatal(err)
			}
			in := urlInput()
			in.Truncated = true
			in.Where, in.Repo = "src/proj", "nn"
			if manual {
				in.Title = "Manual title"
				in.Tags = []string{"manual"}
			}
			var stderr bytes.Buffer
			note, err := SaveURL(context.Background(), env, plan, in, &stderr)
			if err != nil {
				t.Fatal(err)
			}
			data := readFile(t, env.Vault.Abs(note.Path))
			alias, tags, file := `["Go PGO \\[guide\\] x"]`, "[]", "nn/go-pgo-guide-x.md"
			if manual {
				alias, tags, file = "[Manual title]", "[manual]", "nn/manual-title.md"
			}
			want := "---\ndate: 2026-09-24\ntags: " + tags + "\naliases: " + alias + "\ntime: \"10:30\"\nwhere: src/proj\nrepo: nn\nvia: url\n---\n" +
				"Source: <" + urlTestLink + ">\nPage title: Profile-guided optimization\nDescription: How PGO works\n\n" +
				"## Summary\n\nThe page says PGO helps.\n\n- One \\#todo\n- Two &lt;b>\n\n" + urlTruncatedLine + "\n\n#Existing #new-word\n"
			if data != want || note.Path != file {
				t.Fatalf("note %s:\n%s\nwant:\n%s\nstderr=%s", note.Path, data, want, stderr.String())
			}
			if calls := readFile(t, filepath.Join(base, "calls")); calls != "x" {
				t.Fatalf("calls=%q", calls)
			}
			sent := readFile(t, filepath.Join(base, "request"))
			if !strings.Contains(sent, `"url":"`+urlTestLink+`"`) || !strings.Contains(sent, `"existing_tags":["Existing"]`) || strings.Contains(sent, "Private old") {
				t.Fatalf("request=%s", sent)
			}
			if dirs := urlCaches(t, base); len(dirs) != 0 {
				t.Fatalf("cache kept: %v", dirs)
			}
			if hooks := readFile(t, filepath.Join(base, "hooks")); hooks != "create\n" {
				t.Fatalf("hooks=%q", hooks)
			}
		})
	}
}

func TestURLWaitFailureKeepsPageText(t *testing.T) {
	for _, mode := range []string{"empty", "invalid", "cancel"} {
		t.Run(mode, func(t *testing.T) {
			answer := map[string]string{"empty": "", "invalid": `{"title":"t","tags":[],"body":"b","extra":1}`, "cancel": `{"title":"t","tags":[],"body":"b"}`}[mode]
			env, plan, base := urlFixture(t, answer)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if mode == "cancel" {
				cancel()
			}
			var stderr bytes.Buffer
			note, err := SaveURL(ctx, env, plan, urlInput(), &stderr)
			if err != nil {
				t.Fatal(err)
			}
			dirs := urlCaches(t, base)
			if len(dirs) != 1 {
				t.Fatalf("caches=%v", dirs)
			}
			if !strings.Contains(note.Body, "Source: <"+urlTestLink+">") || !strings.Contains(note.Body, "Link summary did not finish. Original retained at:\n\n    "+dirs[0]) || strings.Contains(note.Body, "Summary\n") {
				t.Fatalf("body=%s", note.Body)
			}
			if len(note.Aliases) != 1 || note.Aliases[0] != "Profile-guided optimization" {
				t.Fatalf("aliases=%v", note.Aliases)
			}
			request, err := parseURLPayload(readFile(t, filepath.Join(dirs[0], "request.json")))
			if err != nil || request.Text != "Readable page text." {
				t.Fatalf("request.json=%+v %v", request, err)
			}
			if info, err := os.Stat(filepath.Join(dirs[0], "request.json")); err != nil || info.Mode().Perm() != 0600 {
				t.Fatalf("request.json mode=%v %v", info, err)
			}
			if !strings.Contains(stderr.String(), "recovery cache: "+dirs[0]) {
				t.Fatalf("stderr=%s", stderr.String())
			}
			if _, err := os.Stat(filepath.Join(dirs[0], "status.json")); err != nil {
				t.Fatal(err)
			}
			calls, hooks := readFile(t, filepath.Join(base, "calls")), readFile(t, filepath.Join(base, "hooks"))
			if mode == "cancel" && (calls != "" || hooks != "") || mode != "cancel" && (calls != "x" || hooks != "create\n") {
				t.Fatalf("calls=%q hooks=%q", calls, hooks)
			}
		})
	}
}

func TestURLWithoutModel(t *testing.T) {
	for _, mode := range []string{"not fetched", "unsupported", "no plan", "empty text"} {
		t.Run(mode, func(t *testing.T) {
			env, plan, base := urlFixture(t, `{"title":"t","tags":[],"body":"b"}`)
			in := urlInput()
			in.Title = ""
			switch mode {
			case "not fetched":
				in = URLInput{Link: urlTestLink, Host: "e.test", LinkPath: "/blog/go-pgo", NotFetched: "HTTP 403", Tags: []string{"manual"}, Where: "src/proj", Repo: "nn"}
			case "unsupported":
				in = URLInput{Link: urlTestLink, Host: "e.test", NotFetched: "unsupported content type application/pdf"}
			case "no plan":
				plan = nil
			case "empty text":
				in.Text = " \n "
			}
			var stderr bytes.Buffer
			note, err := SaveURL(context.Background(), env, plan, in, &stderr)
			if err != nil {
				t.Fatal(err)
			}
			if calls := readFile(t, filepath.Join(base, "calls")); calls != "" {
				t.Fatal("model called")
			}
			if dirs := urlCaches(t, base); len(dirs) != 0 {
				t.Fatalf("cache created: %v", dirs)
			}
			if hooks := readFile(t, filepath.Join(base, "hooks")); hooks != "create\n" {
				t.Fatalf("hooks=%q", hooks)
			}
			switch mode {
			case "not fetched":
				if note.Body != "Source: <"+urlTestLink+">\nPage not fetched: HTTP 403\n" || note.Path != "nn/e-test-go-pgo.md" || len(note.Aliases) != 0 || strings.Join(note.Tags, ",") != "manual" || note.Where != "src/proj" || note.Repo != "nn" || note.Via != "url" {
					t.Fatalf("note=%+v", note)
				}
				if stderr.String() != "nn: url: fetch e.test: HTTP 403; saved the link without a summary\n" {
					t.Fatalf("stderr=%q", stderr.String())
				}
			case "unsupported":
				if !strings.HasSuffix(stderr.String(), "; use nn add LINK to keep it as plain text\n") || strings.Contains(stderr.String(), "blog") || note.Path != "nn/e-test.md" {
					t.Fatalf("stderr=%q path=%s", stderr.String(), note.Path)
				}
			case "no plan":
				if note.Body != "Source: <"+urlTestLink+">\nPage title: Profile-guided optimization\nDescription: How PGO works\n" || stderr.Len() != 0 {
					t.Fatalf("note=%q stderr=%q", note.Body, stderr.String())
				}
			case "empty text":
				if !strings.HasSuffix(note.Body, "\nPage not summarized: no readable text\n") || !strings.Contains(stderr.String(), "no readable text") {
					t.Fatalf("note=%q stderr=%q", note.Body, stderr.String())
				}
			}
		})
	}
}

func TestURLBackgroundRetainsPageTextFirst(t *testing.T) {
	env, plan, base := urlFixture(t, `{"title":"t","tags":[],"body":"b"}`)
	plan.Mode = "background"
	if err := os.WriteFile(filepath.Join(base, "vault", "nn"), []byte("blocks the inbox"), 0600); err != nil {
		t.Fatal(err)
	}
	_, err := SaveURL(context.Background(), env, plan, urlInput(), io.Discard)
	dirs := urlCaches(t, base)
	if err == nil || len(dirs) != 1 || !strings.Contains(err.Error(), "page text retained at "+dirs[0]) {
		t.Fatalf("err=%v dirs=%v", err, dirs)
	}
	if request, err := parseURLPayload(readFile(t, filepath.Join(dirs[0], "request.json"))); err != nil || request.Text != "Readable page text." {
		t.Fatalf("request.json=%+v %v", request, err)
	}
	if _, err := os.Stat(filepath.Join(dirs[0], "job.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("job written without a note")
	}
}

func TestURLInvalidInputsRefused(t *testing.T) {
	env, plan, _ := urlFixture(t, `{"title":"t","tags":[],"body":"b"}`)
	for _, link := range []string{"", "https://e.test/a b", "https://e.test/<x>", "https://e.test/\x00"} {
		in := urlInput()
		in.Link = link
		if _, err := SaveURL(context.Background(), env, plan, in, io.Discard); err == nil {
			t.Errorf("link %q accepted", link)
		}
	}
	plan.Task = "title"
	if _, err := SaveURL(context.Background(), env, plan, urlInput(), io.Discard); err == nil {
		t.Fatal("title plan accepted")
	}
}

func TestBackgroundURLRealWorker(t *testing.T) {
	binary := filepath.Join(t.TempDir(), "nn")
	build := exec.Command("go", "build", "-o", binary, "../../cmd/nn")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %v %s", err, out)
	}
	setup := func(t *testing.T, answer string, gated bool) (*app.Env, *Plan, string) {
		env, plan, base := urlFixture(t, answer)
		plan.Mode = "background"
		script := "#!/bin/sh\nprintf x >> \"$NN_METADATA_BASE/calls\"\n/bin/cat > \"$NN_METADATA_BASE/request\"\n"
		if gated {
			script += "while [ ! -e \"$NN_METADATA_BASE/go\" ]; do sleep 0.05; done\n"
		}
		script += "printf '%s' '" + strings.ReplaceAll(answer, "'", "'\\''") + "'\n"
		if err := os.WriteFile(filepath.Join(base, "fake-model"), []byte(script), 0700); err != nil {
			t.Fatal(err)
		}
		configBytes, _ := os.ReadFile(env.Cfg.Path)
		if err := os.WriteFile(env.Cfg.Path, append(configBytes, []byte("\n[hooks]\npost_save="+config.TOMLString(env.Cfg.Hooks.PostSave)+"\n")...), 0600); err != nil {
			t.Fatal(err)
		}
		previous := workerExecutable
		workerExecutable = func() (string, error) { return binary, nil }
		t.Cleanup(func() { workerExecutable = previous })
		return env, plan, base
	}
	wait := func(t *testing.T, env *app.Env, base string, note *vault.Note) *vault.Note {
		t.Helper()
		deadline := time.Now().Add(10 * time.Second)
		for {
			n, err := env.Vault.Load(note.Path)
			if err == nil && readFile(t, filepath.Join(base, "hooks")) == "create\nappend\n" {
				return n
			}
			if time.Now().After(deadline) {
				t.Fatalf("background did not complete: %+v %v", n, err)
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	t.Run("success", func(t *testing.T) {
		env, plan, base := setup(t, `{"title":"Worker [title]","tags":["background"],"body":"Summary body #todo"}`, false)
		in := urlInput()
		in.Truncated = true
		var stderr bytes.Buffer
		note, err := SaveURL(context.Background(), env, plan, in, &stderr)
		if err != nil || note.Path != "nn/profile-guided-optimization.md" || len(note.Aliases) != 0 {
			t.Fatalf("note=%+v err=%v", note, err)
		}
		dirs := urlCaches(t, base)
		if len(dirs) != 1 || !strings.Contains(stderr.String(), "nn: url: summary running in background; recovery cache: "+dirs[0]) {
			t.Fatalf("stderr=%s dirs=%v", stderr.String(), dirs)
		}
		n := wait(t, env, base, note)
		want := "Source: <" + urlTestLink + ">\nPage title: Profile-guided optimization\nDescription: How PGO works\n\n# Worker \\[title\\]\n\n## Summary\n\nSummary body \\#todo\n\n" + urlTruncatedLine + "\n\n#background\n"
		if n.Body != want {
			t.Fatalf("body=%q\nwant=%q", n.Body, want)
		}
		if calls := readFile(t, filepath.Join(base, "calls")); calls != "x" {
			t.Fatalf("calls=%q", calls)
		}
		// The worker removes the cache after the append hook, so poll for it.
		for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(10 * time.Millisecond) {
			if _, err := os.Stat(dirs[0]); errors.Is(err, os.ErrNotExist) {
				break
			}
			if time.Now().After(deadline) {
				t.Fatal("cache kept after success")
			}
		}
	})
	t.Run("failure", func(t *testing.T) {
		env, plan, base := setup(t, "not json", false)
		note, err := SaveURL(context.Background(), env, plan, urlInput(), io.Discard)
		if err != nil {
			t.Fatal(err)
		}
		n := wait(t, env, base, note)
		dirs := urlCaches(t, base)
		if len(dirs) != 1 || !strings.HasSuffix(n.Body, "Link summary did not finish. Original retained at:\n\n    "+dirs[0]+"\n") || strings.Contains(n.Body, "Summary\n") {
			t.Fatalf("body=%q dirs=%v", n.Body, dirs)
		}
		for _, name := range []string{"request.json", "job.json", "status.json"} {
			if _, err := os.Stat(filepath.Join(dirs[0], name)); err != nil {
				t.Fatal(err)
			}
		}
	})
	t.Run("manual heading wins", func(t *testing.T) {
		env, plan, base := setup(t, `{"title":"Generated","tags":["late"],"body":"Late summary"}`, true)
		note, err := SaveURL(context.Background(), env, plan, urlInput(), io.Discard)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := env.Vault.Append(note.Path, "# Manual heading", nil); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(base, "go"), nil, 0600); err != nil {
			t.Fatal(err)
		}
		n := wait(t, env, base, note)
		if !strings.Contains(n.Body, "# Manual heading") || strings.Contains(n.Body, "# Generated") || !strings.Contains(n.Body, "Late summary") {
			t.Fatalf("body=%q", n.Body)
		}
	})
	for name, change := range map[string]func(dir string, j *Job){
		"image": func(dir string, j *Job) {
			j.Image = "original.png"
			_ = os.WriteFile(filepath.Join(dir, j.Image), metadataImage(t), 0600)
		},
		"malformed payload": func(dir string, j *Job) { j.Text = `{"url":"x","text":"t"}` },
	} {
		t.Run("rejects "+name, func(t *testing.T) {
			env, plan, base := setup(t, `{"title":"t","tags":[],"body":"b"}`, false)
			note, err := env.Vault.Create(vault.NewNote{Body: "target"})
			if err != nil {
				t.Fatal(err)
			}
			dir, err := NewCache("url")
			if err != nil {
				t.Fatal(err)
			}
			prompt, _ := ai.Prompt(env.Cfg, "url")
			j := Job{Version: 1, ID: filepath.Base(dir), Task: "url", AllowTitle: true, Config: env.Cfg.Path, Root: env.Vault.Root, Inbox: env.Vault.Inbox, Note: note.Path, System: prompt, Text: func() string { p, _ := urlRequest(urlInput(), nil); return marshalPayload(p) }()}
			change(dir, &j)
			err = Start(context.Background(), dir, j, plan.Approval, ai.Request{System: j.System, Text: j.Text})
			if err == nil || strings.Contains(err.Error(), "exceeds limit") {
				t.Fatalf("start=%v", err)
			}
			if calls := readFile(t, filepath.Join(base, "calls")); calls != "" {
				t.Fatal("rejected job reached the model")
			}
		})
	}
}

func TestURLHostilePageFieldsFit(t *testing.T) {
	hostile := func() URLInput {
		in := urlInput()
		in.PageTitle = strings.Repeat("<", 4<<10)
		in.Description = strings.Repeat("<", 4<<10)
		in.Text = strings.Repeat("<", 1<<20)
		return in
	}
	p, ok := urlRequest(hostile(), []string{"Go"})
	if !ok || len(marshalPayload(p)) > urlPayloadBytes || !p.Truncated {
		t.Fatalf("payload fits=%v bytes=%d", ok, len(marshalPayload(p)))
	}
	j := Job{Version: 1, ID: "url-x", Task: "url", Root: "/v", Note: "nn/e-test.md", System: strings.Repeat("<", ai.MaxSystemBytes/2)}
	if !fitJob(&j, p) {
		t.Fatal("job did not fit")
	}
	if data, _ := json.Marshal(j); len(data) > maxJobBytes {
		t.Fatalf("job bytes=%d", len(data))
	}
	t.Run("wait", func(t *testing.T) {
		env, plan, base := urlFixture(t, `{"title":"Angle brackets","tags":[],"body":"A page of brackets."}`)
		note, err := SaveURL(context.Background(), env, plan, hostile(), io.Discard)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(note.Body, "Page title: "+strings.Repeat(`\<`, urlTitleRunes)+"\n") || !strings.Contains(note.Body, "Description: "+strings.Repeat(`\<`, urlDescriptionRunes)+"\n") || !strings.Contains(note.Body, "A page of brackets.") {
			t.Fatalf("body=%.400s", note.Body)
		}
		if calls := readFile(t, filepath.Join(base, "calls")); calls != "x" {
			t.Fatalf("calls=%q", calls)
		}
	})
	t.Run("background", func(t *testing.T) {
		env, plan, base := urlFixture(t, `{"title":"t","tags":[],"body":"b"}`)
		plan.Mode = "background"
		previous := workerExecutable
		workerExecutable = func() (string, error) { return "", errors.New("fake unavailable worker") }
		t.Cleanup(func() { workerExecutable = previous })
		var stderr bytes.Buffer
		if _, err := SaveURL(context.Background(), env, plan, hostile(), &stderr); err != nil {
			t.Fatal(err)
		}
		dirs := urlCaches(t, base)
		if len(dirs) != 1 || strings.Contains(stderr.String(), "exceeds limit") || !strings.Contains(stderr.String(), "fake unavailable worker") {
			t.Fatalf("stderr=%s dirs=%v", stderr.String(), dirs)
		}
		if info, err := os.Stat(filepath.Join(dirs[0], "job.json")); err != nil || info.Size() > maxJobBytes {
			t.Fatalf("job.json=%v %v", info, err)
		}
	})
}

func TestURLPayloadThatCannotFit(t *testing.T) {
	huge := func() URLInput {
		in := urlInput()
		in.Link = "https://e.test/" + strings.Repeat("a", 200<<10)
		in.LinkPath = "/" + strings.Repeat("a", 200<<10)
		in.Text = "Readable page text."
		in.Truncated = false
		return in
	}
	p, ok := urlRequest(huge(), nil)
	if ok || p.Text != "Readable page text." || p.Truncated {
		t.Fatalf("fits=%v payload text=%q truncated=%v", ok, p.Text, p.Truncated)
	}
	// Only an empty text fits a monotone size: the search ends there. When
	// even that fails, the payload is restored.
	q := urlPayload{Text: strings.Repeat("x", 1<<20), ExistingTags: []string{}}
	sizes := 0
	stubborn := func() int {
		sizes++
		if q.Text == "" {
			return 0
		}
		return 11
	}
	if !shrinkText(&q, stubborn, 10) || q.Text != "" || !q.Truncated || sizes > 32 {
		t.Fatalf("stubborn shrink: %d %v sizes=%d", len(q.Text), q.Truncated, sizes)
	}
	q = urlPayload{Text: strings.Repeat("x", 1<<20), ExistingTags: []string{}}
	if shrinkText(&q, func() int { return 11 }, 10) || len(q.Text) != 1<<20 || q.Truncated {
		t.Fatalf("unfit shrink not restored: %d %v", len(q.Text), q.Truncated)
	}
	t.Run("wait", func(t *testing.T) {
		env, plan, base := urlFixture(t, `{"title":"t","tags":[],"body":"b"}`)
		var stderr bytes.Buffer
		note, err := SaveURL(context.Background(), env, plan, huge(), &stderr)
		if err != nil || !strings.HasPrefix(note.Body, "Source: <https://e.test/aaa") || strings.Contains(note.Body, "retained") {
			t.Fatalf("note=%.200v err=%v", note, err)
		}
		if !strings.Contains(stderr.String(), "does not fit the model input limit") || readFile(t, filepath.Join(base, "calls")) != "" || len(urlCaches(t, base)) != 0 {
			t.Fatalf("stderr=%s", stderr.String())
		}
	})
	t.Run("background", func(t *testing.T) {
		env, plan, base := urlFixture(t, `{"title":"t","tags":[],"body":"b"}`)
		plan.Mode = "background"
		var stderr bytes.Buffer
		note, err := SaveURL(context.Background(), env, plan, huge(), &stderr)
		if err != nil {
			t.Fatal(err)
		}
		dirs := urlCaches(t, base)
		if len(dirs) != 1 || !strings.Contains(note.Body, "Link summary did not finish. Original retained at:\n\n    "+dirs[0]) || !strings.Contains(stderr.String(), "does not fit the model input limit") {
			t.Fatalf("body=%.200s stderr=%s dirs=%v", note.Body, stderr.String(), dirs)
		}
		if request, err := parseURLPayload(readFile(t, filepath.Join(dirs[0], "request.json"))); err != nil || request.Text != "Readable page text." {
			t.Fatalf("request.json=%v", err)
		}
		if _, err := os.Stat(filepath.Join(dirs[0], "job.json")); !errors.Is(err, os.ErrNotExist) || readFile(t, filepath.Join(base, "calls")) != "" {
			t.Fatal("oversized link reached the worker")
		}
	})
}

func TestURLTemplaterNeverReachesTheNote(t *testing.T) {
	templater := "<%* require('child_process').exec('touch /tmp/x') %>"
	for name, answer := range map[string]string{"plain": "", "wait with empty model title": `{"title":"","tags":[],"body":"The page says <%* hi %>."}`} {
		t.Run(name, func(t *testing.T) {
			env, plan, _ := urlFixture(t, answer)
			if name == "plain" {
				plan = nil
			}
			in := urlInput()
			in.PageTitle = templater + " <[[%x"
			in.Description = templater
			note, err := SaveURL(context.Background(), env, plan, in, io.Discard)
			if err != nil {
				t.Fatal(err)
			}
			data := readFile(t, env.Vault.Abs(note.Path))
			if strings.Contains(data, "<%") || len(note.Aliases) != 1 {
				t.Fatalf("Templater opener in note:\n%s", data)
			}
			if plan != nil && !strings.Contains(data, "## Summary") {
				t.Fatalf("summary missing:\n%s", data)
			}
		})
	}
}

// Expensive runes first, then cheap ones: a fitting prefix exists and the
// search must find it rather than give up.
func TestURLFitJobFindsPrefixBehindExpensiveRunes(t *testing.T) {
	base := Job{Version: 1, ID: "url-x", Task: "url", AllowTitle: true, Config: "/c/nn.toml", Root: "/v", Inbox: "nn", Note: "nn/e-test.md", System: strings.Repeat("prompt ", 600)}
	empty := base
	empty.Text = marshalPayload(urlPayload{URL: urlTestLink, Truncated: true, ExistingTags: []string{}})
	fixed, _ := json.Marshal(empty)
	budget := maxJobBytes - len(fixed)
	expensive := budget/6 - 500
	in := urlInput()
	in.Text = strings.Repeat("<", expensive) + strings.Repeat("a", 158000-expensive)
	p, ok := urlRequest(in, nil)
	if !ok || p.Truncated {
		t.Fatalf("payload stage fits=%v truncated=%v", ok, p.Truncated)
	}
	candidate := expensive + (budget - 6*expensive) - 64
	probe := base
	probe.Text = marshalPayload(urlPayload{URL: p.URL, PageTitle: p.PageTitle, Description: p.Description, Text: in.Text[:candidate], Truncated: true, ExistingTags: p.ExistingTags})
	if data, _ := json.Marshal(probe); len(data) > maxJobBytes {
		t.Fatalf("candidate prefix does not fit: %d", len(data))
	}
	j := base
	if !fitJob(&j, p) {
		t.Fatalf("fitJob gave up although a %d-byte prefix fits", candidate)
	}
	got, err := parseURLPayload(j.Text)
	data, _ := json.Marshal(j)
	if err != nil || len(data) > maxJobBytes || len(got.Text) < candidate || !got.Truncated {
		t.Fatalf("fitted text=%d job=%d err=%v", len(got.Text), len(data), err)
	}
}
