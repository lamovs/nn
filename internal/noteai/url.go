package noteai

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/lamovs/nn/internal/ai"
	"github.com/lamovs/nn/internal/app"
	"github.com/lamovs/nn/internal/vault"
)

const (
	urlPayloadBytes     = 160 << 10
	urlTagsBytes        = 32 << 10
	urlTitleRunes       = 300
	urlDescriptionRunes = 500
	urlTruncatedLine    = "The page text was truncated before summarizing."
)

type URLInput struct {
	Link, Host             string   // webpage.Link.Raw and Host()
	LinkPath               string   // link.URL.EscapedPath(); its last non-empty segment may name the file
	PageTitle, Description string   // raw page strings; SaveURL sanitizes
	Text                   string   // extracted page text; never written to the vault
	Truncated              bool     // the fetcher cut the body or text
	NotFetched             string   // fixed REASON of a fetch-class failure; "" when fetched
	Title                  string   // manual --title
	Tags                   []string // manual -t
	Where, Repo            string   // capture context, as add and shot write it
}

// urlPayload is the whole model input of the url task: data only, never
// instructions.
type urlPayload struct {
	URL          string   `json:"url"`
	PageTitle    string   `json:"page_title"`
	Description  string   `json:"description"`
	Text         string   `json:"text"`
	Truncated    bool     `json:"truncated"`
	ExistingTags []string `json:"existing_tags"`
}

// SaveURL: page text reaches only the private cache (request.json), never
// the vault.
func SaveURL(ctx context.Context, env *app.Env, plan *Plan, in URLInput, stderr io.Writer) (*vault.Note, error) {
	if in.Link == "" || strings.ContainsFunc(in.Link, func(r rune) bool {
		return r == '<' || r == '>' || unicode.IsSpace(r) || unicode.IsControl(r)
	}) {
		return nil, errors.New("url: invalid link")
	}
	if plan != nil && plan.Task != "url" {
		return nil, errors.New("link save requires the url task")
	}
	manual := strings.TrimSpace(in.Title)
	pageAlias := aliasText(in.PageTitle)
	hint := filenameHint(in.Host, in.LinkPath)
	now := time.Time{}
	if env.Now != nil {
		now = env.Now()
	}
	linkNote := func(title, filename, extra string) vault.NewNote {
		return vault.NewNote{Title: title, FilenameHint: filename, Tags: in.Tags, Body: urlLinkBody(in) + extra, Via: "url", Where: in.Where, Repo: in.Repo, Now: now}
	}
	create := func(n vault.NewNote) (*vault.Note, error) {
		note, err := env.Vault.Create(n)
		if err != nil {
			return nil, err
		}
		env.PostSave(ctx, note.Path, "create")
		return note, nil
	}
	plainTitle := manual
	if plainTitle == "" {
		plainTitle = pageAlias
	}
	plain := func() (*vault.Note, error) { return create(linkNote(plainTitle, hint, "")) }
	if in.NotFetched != "" {
		hintText := ""
		if strings.HasPrefix(in.NotFetched, "unsupported content type") {
			hintText = "; use nn add LINK to keep it as plain text"
		}
		fmt.Fprintf(stderr, "nn: url: fetch %s: %s; saved the link without a summary%s\n", in.Host, in.NotFetched, hintText)
		return create(linkNote(manual, hint, ""))
	}
	if plan == nil {
		return plain()
	}
	if strings.TrimSpace(in.Text) == "" {
		fmt.Fprintln(stderr, "nn: warning: url: the page has no readable text; saved the link without a summary")
		return plain()
	}
	call, err := plan.Approval.Call()
	if err != nil {
		return nil, err
	}
	if call.Task != plan.Task {
		return nil, errors.New("metadata task does not match approval")
	}
	vocabulary, vocabErr := Vocabulary(ctx, env)
	payload, payloadFits := urlRequest(in, vocabulary)
	if !payloadFits && plan.Mode == "wait" {
		fmt.Fprintln(stderr, "nn: warning: url: the link does not fit the model input limit; saved the link without a summary")
		return plain()
	}
	dir, err := NewCache("url")
	if err != nil {
		fmt.Fprintf(stderr, "nn: warning: url: AI cache: %v; saving the link without a summary\n", err)
		return plain()
	}
	// Retained before any note, model or worker exists.
	if err := os.WriteFile(filepath.Join(dir, "request.json"), []byte(marshalPayload(payload)), 0600); err != nil {
		_ = os.RemoveAll(dir)
		fmt.Fprintf(stderr, "nn: warning: url: AI cache: %v; saving the link without a summary\n", err)
		return plain()
	}
	req, reqErr := func() (ai.Request, error) {
		if vocabErr != nil {
			return ai.Request{}, vocabErr
		}
		if !payloadFits {
			return ai.Request{}, errors.New("the link does not fit the model input limit")
		}
		prompt, err := ai.Prompt(env.Cfg, "url")
		if err != nil {
			return ai.Request{}, err
		}
		req := ai.Request{System: prompt, Text: marshalPayload(payload)}
		return req, ai.ValidateRequest(req)
	}()
	if plan.Mode == "wait" {
		fallback := func(cause error) (*vault.Note, error) {
			WriteStatus(dir, "Link summary failed; page text retained")
			fmt.Fprintf(stderr, "nn: warning: link summary: %v; recovery cache: %s\n", cause, dir)
			note, err := create(linkNote(plainTitle, hint, Recovery("url", dir)))
			if err != nil {
				return nil, fmt.Errorf("save note: %w; page text retained at %s", err, dir)
			}
			return note, nil
		}
		if reqErr != nil {
			return fallback(reqErr)
		}
		if err := ctx.Err(); err != nil {
			return fallback(err)
		}
		result, err := ai.Run(ctx, plan.Approval, req)
		if err == nil && result.URL == nil {
			err = errors.New("model returned no link summary")
		}
		if err != nil {
			return fallback(err)
		}
		reply := *result.URL
		answer, _ := json.Marshal(reply)
		if err := os.WriteFile(filepath.Join(dir, "answer.json"), answer, 0600); err != nil {
			WriteStatus(dir, "Could not retain link summary before save")
		}
		title := manual
		if title == "" {
			title = urlTitle(reply)
		}
		if title == "" {
			title = pageAlias
		}
		filename := title
		if filename == "" {
			filename = hint
		}
		n := linkNote(title, filename, "\n\n"+urlSummary(reply, payload.Truncated))
		n.TrailingTags = Keywords(reply.Tags, vocabulary)
		note, err := create(n)
		if err != nil {
			return nil, fmt.Errorf("save note: %w; page text and answer retained at %s", err, dir)
		}
		_ = os.RemoveAll(dir)
		return note, nil
	}
	backgroundName := pageAlias
	if backgroundName == "" {
		backgroundName = hint
	}
	note, err := create(linkNote(manual, backgroundName, ""))
	if err != nil {
		return nil, fmt.Errorf("save note: %w; page text retained at %s", err, dir)
	}
	startErr := reqErr
	if startErr == nil {
		j := Job{Version: 1, ID: filepath.Base(dir), Task: "url", AllowTitle: manual == "", Config: env.Cfg.Path, Root: env.Vault.Root, Inbox: env.Vault.Inbox, Note: note.Path, System: req.System}
		if fitJob(&j, payload) {
			startErr = Start(ctx, dir, j, plan.Approval, ai.Request{System: j.System, Text: j.Text})
		} else {
			startErr = errors.New("link summary does not fit the background job limit")
		}
	}
	if startErr != nil {
		WriteStatus(dir, "Could not start link summary worker; page text retained")
		fmt.Fprintf(stderr, "nn: warning: link summary: %v; recovery cache: %s\n", startErr, dir)
		if updated, e := appendEnrichment(env.Vault, note.Path, vault.Enrichment{Body: Recovery("url", dir)}); e == nil {
			note = updated
		}
		return note, nil
	}
	fmt.Fprintf(stderr, "nn: url: summary running in background; recovery cache: %s\n", dir)
	return note, nil
}

// urlRequest reports false, keeping the whole Text, when even an empty
// Text would not fit urlPayloadBytes.
func urlRequest(in URLInput, vocabulary []string) (urlPayload, bool) {
	tags := []string{}
	if len(vocabulary) > 0 {
		if data, err := json.Marshal(vocabulary); err == nil && len(data) <= urlTagsBytes {
			tags = vocabulary
		}
	}
	p := urlPayload{URL: in.Link, PageTitle: cleanLine(in.PageTitle, urlTitleRunes), Description: cleanLine(in.Description, urlDescriptionRunes), Text: in.Text, Truncated: in.Truncated, ExistingTags: tags}
	ok := shrinkText(&p, func() int { return len(marshalPayload(p)) }, urlPayloadBytes)
	return p, ok
}

func fitJob(j *Job, p urlPayload) bool {
	size := func() int {
		j.Text = marshalPayload(p)
		data, err := json.Marshal(*j)
		if err != nil {
			return maxJobBytes + 1
		}
		return len(data)
	}
	ok := shrinkText(&p, size, maxJobBytes)
	j.Text = marshalPayload(p)
	return ok
}

// shrinkText fails, restoring p, only when even an empty Text does not fit.
func shrinkText(p *urlPayload, size func() int, limit int) bool {
	if size() <= limit {
		return true
	}
	original := *p
	text := original.Text
	p.Truncated = true
	cut := func(n int) int {
		for n > 0 && n < len(text) && !utf8.RuneStart(text[n]) {
			n--
		}
		return n
	}
	fits := func(n int) bool {
		p.Text = text[:cut(n)]
		return size() <= limit
	}
	if !fits(0) {
		*p = original
		return false
	}
	// Invariant: fits(lo) holds and fits(hi) does not.
	lo, hi := 0, len(text)
	for hi-lo > 1 {
		if mid := lo + (hi-lo)/2; fits(mid) {
			lo = mid
		} else {
			hi = mid
		}
	}
	keep := cut(lo)
	if nl := strings.LastIndexByte(text[:keep], '\n'); nl >= 0 && nl >= keep-keep/4 {
		keep = nl
	}
	p.Text = text[:keep]
	return true
}

// marshalPayload keeps <, > and & literal: this is model input, not HTML.
func marshalPayload(p urlPayload) string {
	var b strings.Builder
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(p); err != nil {
		return ""
	}
	return strings.TrimSuffix(b.String(), "\n")
}

func parseURLPayload(text string) (urlPayload, error) {
	invalid := errors.New("invalid link summary payload")
	var raw map[string]json.RawMessage
	dec := json.NewDecoder(strings.NewReader(text))
	if err := dec.Decode(&raw); err != nil {
		return urlPayload{}, invalid
	}
	if _, err := dec.Token(); err != io.EOF {
		return urlPayload{}, invalid
	}
	keys := []string{"url", "page_title", "description", "text", "truncated", "existing_tags"}
	if len(raw) != len(keys) {
		return urlPayload{}, invalid
	}
	for _, key := range keys {
		if value, ok := raw[key]; !ok || string(value) == "null" {
			return urlPayload{}, invalid
		}
	}
	var p urlPayload
	strict := json.NewDecoder(strings.NewReader(text))
	strict.DisallowUnknownFields()
	if err := strict.Decode(&p); err != nil || p.URL == "" || p.ExistingTags == nil {
		return urlPayload{}, invalid
	}
	return p, nil
}

// urlLinkBody never comes from the model. in.Link is written unescaped as an
// autolink, safe only because ParseLink refuses whitespace, < and >.
func urlLinkBody(in URLInput) string {
	lines := []string{"Source: <" + in.Link + ">"}
	if in.NotFetched != "" {
		return strings.Join(append(lines, "Page not fetched: "+escapeText(in.NotFetched, urlTitleRunes)), "\n")
	}
	if title := escapeText(in.PageTitle, urlTitleRunes); title != "" {
		lines = append(lines, "Page title: "+title)
	}
	if description := escapeText(in.Description, urlDescriptionRunes); description != "" {
		lines = append(lines, "Description: "+description)
	}
	if strings.TrimSpace(in.Text) == "" {
		lines = append(lines, "Page not summarized: no readable text")
	}
	return strings.Join(lines, "\n")
}

func urlSummary(reply ai.URLReply, truncated bool) string {
	summary := "## Summary\n\n" + neutralize(strings.TrimSpace(reply.Body))
	if truncated {
		summary += "\n\n" + urlTruncatedLine
	}
	return summary
}

func urlTitle(reply ai.URLReply) string {
	return neutralize(stripAliasMarks(Title(reply.Title)))
}

// neutralize renders model text inert against Markdown, Obsidian comment,
// Templater and Dataview syntax; every backslash is doubled so it can't
// cancel an escape.
func neutralize(s string) string {
	var b strings.Builder
	var prev rune
	lineStart := true
	for i, r := range s {
		// A blockquote prefix ("> > ") does not stop a setext underline.
		if lineStart && r != ' ' && r != '\t' && r != '>' {
			lineStart = false
			if r == '=' || r == '-' {
				end := strings.IndexByte(s[i:], '\n')
				if end < 0 {
					end = len(s) - i
				}
				if strings.Trim(s[i:i+end], string(r)+" \t") == "" {
					b.WriteByte('\\')
				}
			}
		}
		if r == '\n' {
			lineStart = true
		}
		switch r {
		case ':':
			if prev == ':' {
				b.WriteByte('\\')
			}
			b.WriteRune(r)
		case '%':
			if prev == '%' {
				b.WriteByte('\\')
			}
			b.WriteRune(r)
		case '\\':
			b.WriteString(`\\`)
		case '<':
			b.WriteString("&lt;")
		case '`', '~', '[', ']', '#':
			b.WriteByte('\\')
			b.WriteRune(r)
		default:
			b.WriteRune(r)
		}
		prev = r
	}
	return b.String()
}

func escapeText(s string, maxRunes int) string {
	var b strings.Builder
	for _, r := range cleanLine(s, maxRunes) {
		if strings.ContainsRune("\\`*_[]<>#!|~$%", r) {
			b.WriteByte('\\')
		}
		b.WriteRune(r)
	}
	return b.String()
}

// aliasText: no backslash escapes, but strips marks that break wikilink aliases.
func aliasText(s string) string {
	return stripAliasMarks(cleanLine(s, urlTitleRunes))
}

func stripAliasMarks(s string) string {
	s = strings.NewReplacer("|", "", "#", "", "^", "").Replace(s)
	// Removing one pair can join two brackets into a new pair.
	for strings.Contains(s, "[[") || strings.Contains(s, "]]") {
		s = strings.ReplaceAll(strings.ReplaceAll(s, "[[", ""), "]]", "")
	}
	s = strings.Join(strings.Fields(s), " ")
	// "%%" opens an Obsidian comment.
	for strings.Contains(s, "%%") {
		s = strings.ReplaceAll(s, "%%", "%")
	}
	// Templater runs "<%" anywhere; the removals above can join a new opener.
	for strings.Contains(s, "<%") {
		s = strings.ReplaceAll(s, "<%", "<")
	}
	return s
}

func cleanLine(s string, maxRunes int) string {
	s = strings.Map(func(r rune) rune {
		switch {
		case unicode.IsSpace(r):
			return ' '
		case unicode.IsControl(r), unicode.Is(unicode.Cf, r):
			return -1
		}
		return r
	}, s)
	s = strings.Join(strings.Fields(s), " ")
	if runes := []rune(s); len(runes) > maxRunes {
		s = strings.TrimSpace(string(runes[:maxRunes]))
	}
	return s
}

// filenameHint: capability ids and tokens must never reach a file name.
func filenameHint(host, linkPath string) string {
	segment := ""
	for _, part := range strings.Split(linkPath, "/") {
		if part != "" {
			segment = part
		}
	}
	decoded, err := url.PathUnescape(segment)
	if err != nil || decoded == "" || len(decoded) > 60 {
		return host
	}
	start := 0
	for i := 0; i <= len(decoded); i++ {
		if i < len(decoded) && !strings.ContainsRune("-_.", rune(decoded[i])) {
			continue
		}
		if !hintWord(decoded[start:i]) {
			return host
		}
		start = i + 1
	}
	return strings.TrimSpace(host + " " + decoded)
}

func hintWord(word string) bool {
	if word == "" {
		return false
	}
	digits := true
	for _, r := range word {
		if r < '0' || r > '9' {
			digits = false
		}
	}
	if digits {
		return len(word) <= 4
	}
	for _, r := range word {
		if !unicode.IsLetter(r) {
			return false
		}
	}
	return true
}
