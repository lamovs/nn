package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/lamovs/nn/internal/ai"
	"github.com/lamovs/nn/internal/app"
	"github.com/lamovs/nn/internal/capture"
	"github.com/lamovs/nn/internal/cli"
	"github.com/lamovs/nn/internal/config"
	"github.com/lamovs/nn/internal/noteai"
	"github.com/lamovs/nn/internal/ocr"
	"github.com/lamovs/nn/internal/output"
	"github.com/lamovs/nn/internal/platform"
	"github.com/lamovs/nn/internal/vault"
)

func init() {
	def := config.Default()
	register(command{
		help: cli.Help{
			Verb:    "add",
			Alias:   "a",
			Summary: "capture a new note from words, stdin, the clipboard or the editor",
			Examples: []cli.Example{
				{Cmd: "nn add Docker cleanup notes", What: "creates nn/docker-cleanup-notes.md with that title"},
				{Cmd: "docker system prune -af | nn add Docker cleanup", What: "words become the title, stdin becomes the body"},
				{Cmd: `nn add "docker system prune -af" --code=sh`, What: "the words become a fenced sh code block"},
				{Cmd: "nn add -- -5 min plank", What: "a title starting with a dash, protected by --"},
				{Cmd: "nn add one more thing --to docker-cleanup-notes", What: "appends a line to an existing note"},
				{Cmd: "nn add meeting notes --preview", What: "shows what would be saved, without writing it"},
				{Cmd: "nn add", What: "with nothing piped in a terminal, opens your editor on an empty draft"},
			},
			Sections: []cli.HelpSection{
				{Title: "Sources", Items: []string{
					"Words only: the words become the title, the body is empty.",
					"Words plus piped stdin: the words become the title, stdin becomes the body (text or an image, sniffed from its bytes).",
					"--code or --title turns bare words into the body instead of the title, wrapped in a fenced code block for --code.",
					"--clip reads the clipboard instead of stdin, preferring an image and otherwise using plain text.",
					"Nothing at all, in a terminal: opens VISUAL or EDITOR on an empty draft (or a template with -T).",
					"-T TEMPLATE applies to any of these: the template gives the note its skeleton, the words or --title still name it, and whatever was captured follows the template's body. It makes a new note, so it cannot be combined with --to.",
				}},
				{Title: "Checks", Items: []string{
					"In a terminal: a similar existing note offers append/new/cancel, a similar tag asks to reuse it, and a likely secret asks to save anyway.",
					"Without a terminal (scripts, pipes): similar notes and tags only warn on stderr; a likely secret refuses with exit code 2 unless --allow-secret is given.",
					fmt.Sprintf("How similar counts as similar is capture.similar_threshold and capture.similar_limit (default: %v, %d).", def.Capture.SimilarThreshold, def.Capture.SimilarLimit),
				}},
				{Title: "OCR", Items: []string{
					fmt.Sprintf("Captured images are OCR'd by default (default: capture.ocr, %v); --no-ocr skips it, --ocr runs it even when capture.ocr is false. The two cannot be combined.", def.Capture.OCR),
				}},
				{Title: "AI metadata", Items: []string{
					"--ai[=PROFILE] suggests a title and trailing #keywords; --no-ai disables it. --model MODEL and --effort low|medium|high|max override the profile.",
					"--ai-mode background (default) enriches the saved note later; wait waits before saving; auto waits in a terminal. Manual titles and original content are preserved.",
					"ai.tasks.title controls policy and profile. Consent is required. Appends send only new content; --preview performs no AI work. Saving a possible secret locally does not authorize sending it: --allow-secret is also required.",
				}},
			},
			SeeAlso: []string{"shot", "edit", "snip"},
		},
		run: cmdAdd,
	})
}

type addOptions struct {
	overrides   ai.Overrides
	aiMode      string
	tags        []string
	template    string
	hasTemplate bool
	title       string
	hasTitle    bool
	code        bool
	codeLang    string
	editor      bool
	to          string
	hasTo       bool
	clip        bool
	ocr         bool
	noOCR       bool
	new         bool
	allowSecret bool
	preview     bool
}

func parseAddOptions(args []string) (addOptions, error) {
	var opt addOptions
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "-t":
			v, ok := flagValue(args, &i)
			if !ok {
				return opt, fmt.Errorf("-t needs a tag")
			}
			opt.tags = append(opt.tags, v)
		case a == "-T":
			v, ok := flagValue(args, &i)
			if !ok {
				return opt, fmt.Errorf("-T needs a template name")
			}
			opt.template, opt.hasTemplate = v, true
		case a == "--title":
			v, ok := flagValue(args, &i)
			if !ok {
				return opt, fmt.Errorf("--title needs a value")
			}
			opt.title, opt.hasTitle = v, true
		case a == "--code":
			opt.code = true
		case strings.HasPrefix(a, "--code="):
			opt.code = true
			opt.codeLang = strings.TrimPrefix(a, "--code=")
		case a == "-e":
			opt.editor = true
		case a == "--to":
			v, ok := flagValue(args, &i)
			if !ok {
				return opt, fmt.Errorf("--to needs a note")
			}
			opt.to, opt.hasTo = v, true
		case a == "--clip":
			opt.clip = true
		case a == "--ocr":
			opt.ocr = true
		case a == "--no-ocr":
			opt.noOCR = true
		case a == "--new":
			opt.new = true
		case a == "--allow-secret":
			opt.allowSecret = true
		case a == "--preview":
			opt.preview = true
		case a == "--ai":
			opt.overrides.AI = true
		case a == "--no-ai":
			opt.overrides.NoAI = true
		case a == "--model" || a == "--effort" || a == "--ai-mode":
			value, ok := flagValue(args, &i)
			if !ok || value == "" {
				return opt, fmt.Errorf("%s needs a value", a)
			}
			switch a {
			case "--model":
				opt.overrides.Model = value
			case "--effort":
				opt.overrides.Effort = value
			case "--ai-mode":
				opt.aiMode = value
			}
		default:
			key, value, hasValue := strings.Cut(a, "=")
			if !hasValue || !slices.Contains([]string{"--ai", "--model", "--effort", "--ai-mode"}, key) {
				return opt, unknownOption{a}
			}
			if value == "" {
				return opt, fmt.Errorf("%s needs a value", key)
			}
			switch key {
			case "--ai":
				opt.overrides.AI, opt.overrides.Profile = true, value
			case "--model":
				opt.overrides.Model = value
			case "--effort":
				opt.overrides.Effort = value
			case "--ai-mode":
				opt.aiMode = value
			}
		}
	}
	if opt.hasTemplate && opt.hasTo {
		return opt, errors.New("-T and --to cannot be combined: a template makes a new note")
	}
	if opt.ocr && opt.noOCR {
		return opt, errors.New("--ocr and --no-ocr cannot be combined")
	}
	if opt.aiMode != "" && !slices.Contains([]string{"auto", "wait", "background"}, opt.aiMode) {
		return opt, errors.New("--ai-mode: want auto, wait or background")
	}
	return opt, nil
}

// flagValue consumes args[*i+1] as a flag's value; ok is false when there is none.
func flagValue(args []string, i *int) (string, bool) {
	if *i+1 >= len(args) {
		return "", false
	}
	*i++
	return args[*i], true
}

type unknownOption struct{ opt string }

func (e unknownOption) Error() string { return fmt.Sprintf("unknown option %q", e.opt) }

func cmdAdd(inv *invocation) int {
	outOpt, rest, err := output.ParseFlags(inv.refinements)
	if err != nil {
		return inv.misuse("%v", err)
	}
	opt, err := parseAddOptions(rest)
	if err != nil {
		var unknown unknownOption
		if errors.As(err, &unknown) {
			return inv.misuseWord("unknown option ", unknown.opt)
		}
		return inv.misuse("%v", err)
	}

	env, err := app.Open()
	if err != nil {
		return inv.fail(err)
	}

	words := strings.TrimSpace(strings.Join(inv.data, " "))
	hasWords := words != ""

	isTTYStdin := cli.IsTerminal(inv.stdin)
	var source []byte
	sourceExt, sourceIsImage := "", false
	if opt.clip {
		source, sourceExt, err = captureClipboardImage(inv.ctx)
		if errors.Is(err, platform.ErrNoImage) || errors.Is(err, platform.ErrImageUnavailable) {
			source, err = captureClipboardText(inv.ctx)
			if err == nil && strings.TrimSpace(string(source)) == "" {
				err = errors.New("clipboard contains no text or image")
			}
		} else if err == nil {
			sourceIsImage = true
		}
		if err != nil {
			return inv.fail(err)
		}
	} else if !isTTYStdin {
		source, err = io.ReadAll(inv.stdin)
		if err != nil {
			return inv.fail(fmt.Errorf("read stdin: %w", err))
		}
		if len(source) > 0 {
			sourceExt, sourceIsImage = capture.DetectImage(source)
		}
	}
	haveSource := len(source) > 0

	where, repo := vault.Context("")
	content, err := assembleAdd(inv.ctx, env, opt, words, hasWords, source, haveSource, sourceIsImage, sourceExt, isTTYStdin, where, repo)
	if err != nil {
		if errors.Is(err, app.ErrEmptyDraft) {
			fmt.Fprintln(inv.stderr, "nn: add: empty draft, nothing saved")
			return output.ExitOK
		}
		return inv.fail(err)
	}
	title, body, images := content.title, content.body, content.images
	for _, key := range content.tmpl.dropped {
		fmt.Fprintf(inv.stderr, "nn: add: template %q: ignoring frontmatter key %q\n", opt.template, key)
	}
	if content.tmpl.where != "" {
		where = content.tmpl.where
	}
	if content.tmpl.repo != "" {
		repo = content.tmpl.repo
	}

	bodyEmpty := strings.TrimSpace(body) == ""
	noImages := len(images) == 0
	if bodyEmpty && noImages && (opt.hasTo || title == "") {
		fmt.Fprintln(inv.stderr, "nn: add: nothing to save")
		return output.ExitOK
	}

	via := "text"
	switch {
	case content.usedEditor:
		via = "editor"
	case opt.clip:
		via = "clip"
	case haveSource:
		via = "stdin"
	}

	var notes []*vault.Note
	if !opt.hasTo {
		notes, err = env.Notes(inv.ctx)
		if err != nil {
			return inv.fail(err)
		}
	}

	possibleSecret := len(capture.FindSecrets(title+"\n"+body)) > 0
	if !opt.allowSecret {
		if code := checkSecrets(inv, title+"\n"+body); code >= 0 {
			return code
		}
	}

	tags := opt.tags
	if !opt.hasTo && len(tags) > 0 {
		tags = adjustSimilarTags(inv, tags, notes)
	}
	// Template tags join after the similar-tag check: fixed vocabulary, not user input.
	if len(content.tmpl.tags) > 0 {
		tags = append(append([]string(nil), tags...), content.tmpl.tags...)
	}

	if !opt.hasTo && !opt.new {
		if similars := capture.SimilarNotes(notes, title, body, env.Cfg.Capture.SimilarThreshold, env.Cfg.Capture.SimilarLimit); len(similars) > 0 {
			// Without a terminal nn never asks: a piped capture has no one to answer.
			decision := "new"
			if addInteractive() {
				decision = askSimilarNote(similars)
			}
			switch decision {
			case "cancel":
				fmt.Fprintln(inv.stderr, "nn: add: cancelled")
				return output.ExitOK
			case "append":
				opt.to, opt.hasTo = similars[0].Path, true
			default:
				fmt.Fprintf(inv.stderr, "nn: add: similar note exists: %s (%s)\n", similars[0].Path, similars[0].Title)
			}
		}
	}

	// --to resolves by the vault's own name rules, not env.Resolve's search fallback.
	var target string
	if opt.hasTo {
		rel, rerr := env.Vault.Resolve(opt.to)
		if rerr != nil {
			if errors.Is(rerr, vault.ErrNotFound) {
				return inv.fail(fmt.Errorf("no note matches %q: --to takes an existing note by path, file name, alias or title", opt.to))
			}
			return resolveFailure(inv, opt.to, rerr)
		}
		target = rel
	}

	if opt.preview {
		return printAddPreview(inv, outOpt, target, title, body, tags, images)
	}

	if opt.hasTo && strings.TrimSpace(body) == "" && len(images) == 0 {
		return inv.fail(errors.New("selected append has no body; use --to to append the supplied words"))
	}
	wantsAI := !opt.overrides.NoAI && (opt.overrides.Explicit() || env.Cfg.AI.Tasks["title"].Run == "always")
	metadataText := body
	if title != "" && !opt.hasTo {
		metadataText = title + "\n\n" + body
	}
	var recognized []*ocr.Result
	preOCR := wantsAI && !(possibleSecret && !opt.allowSecret) && wantOCR(env.Cfg, opt.ocr, opt.noOCR) && len(images) > 0
	if preOCR {
		var text string
		recognized, text = recognizeAddImages(inv.ctx, env.Cfg, images, inv.stderr)
		metadataText = joinBody(metadataText, text)
		if !opt.allowSecret && !possibleSecret && len(capture.FindSecrets(text)) > 0 {
			possibleSecret = true
			// The image is already stored locally regardless; OCR only adds a transfer guard.
		}
	}
	var plan *noteai.Plan
	if wantsAI && possibleSecret && !opt.allowSecret {
		fmt.Fprintln(inv.stderr, "nn: add: AI skipped: possible secret; --allow-secret is required to send this content")
	} else {
		plan, err = noteai.Prepare(&env.Cfg, "title", opt.overrides, opt.aiMode, shotAsker{inv.ctx}, inv.stderr)
		if err != nil {
			return inv.fail(err)
		}
	}
	action := "create"
	if opt.hasTo {
		action = "append"
		if len(tags) > 0 {
			body = joinBody(body, tagLine(tags))
		}
	}
	base := vault.NewNote{Title: title, Body: body, Tags: tags, Via: via, Where: where, Repo: repo, Images: images, Now: envNow(env)}
	input := noteai.Input{Note: base, To: target, Text: metadataText, AllowTitle: !opt.hasTo && title == ""}
	if len(images) > 0 {
		input.Image, input.Ext = images[0].Data, images[0].Ext
	}
	input.AfterSave = func(note *vault.Note, imagePaths []string) {
		if wantOCR(env.Cfg, opt.ocr, opt.noOCR) && len(images) > 0 {
			embeds := imagePaths
			if preOCR {
				for i, result := range recognized {
					if result != nil && i < len(embeds) {
						if err := ocr.SaveSidecar(env.Vault.Root, embeds[i], result); err != nil {
							fmt.Fprintf(inv.stderr, "nn: warning: OCR cache: %v\n", err)
						}
					}
				}
			} else {
				runOCR(inv.ctx, env.Cfg, env.Vault.Root, embeds, inv.stderr)
			}
		}
		env.PostSave(inv.ctx, note.Path, action)
	}
	note, err := noteai.Save(inv.ctx, env, plan, input, inv.stderr)
	if err != nil {
		return inv.fail(err)
	}

	return emitAddResult(inv, outOpt, note.Path, action)
}

// addContent is what assembleAdd worked out: title, body, images, and a -T template's own frontmatter.
type addContent struct {
	title      string
	body       string
	images     []vault.Image
	usedEditor bool
	tmpl       templateNote
}

// assembleAdd works out title, body and images: words plus stdin combine (words title, stdin body).
func assembleAdd(ctx context.Context, env *app.Env, opt addOptions, words string, hasWords bool, source []byte, haveSource, sourceIsImage bool, sourceExt string, isTTYStdin bool, where, repo string) (addContent, error) {
	var (
		title, body string
		images      []vault.Image
		usedEditor  bool
		tmpl        templateNote
	)
	wrap := func(text string) string {
		if !opt.code || text == "" {
			return text
		}
		return "```" + opt.codeLang + "\n" + strings.TrimRight(text, "\n") + "\n```"
	}

	// --code, --title and --to each mean "words are body, not title".
	wordsAsBody := opt.code || opt.hasTitle || opt.hasTo

	switch {
	case hasWords && haveSource && sourceIsImage:
		images = []vault.Image{{Data: source, Ext: sourceExt}}
		if wordsAsBody {
			body, title = wrap(words), opt.title
		} else {
			title = pick(opt.hasTitle, opt.title, words)
		}
	case hasWords && haveSource:
		if wordsAsBody {
			combined := words
			if s := string(source); s != "" {
				combined += "\n\n" + s
			}
			body, title = wrap(combined), opt.title
		} else {
			title = pick(opt.hasTitle, opt.title, words)
			body = wrap(string(source))
		}
	case hasWords:
		if wordsAsBody {
			body, title = wrap(words), opt.title
		} else {
			title = words
		}
	case haveSource && sourceIsImage:
		title = opt.title
		images = []vault.Image{{Data: source, Ext: sourceExt}}
	case haveSource:
		title = opt.title
		body = wrap(string(source))
	default:
		title = opt.title
		switch {
		case opt.hasTemplate:
			// -T stands in for the empty editor buffer when there is no other source.
		case !isTTYStdin || !addInteractive():
			if !opt.hasTo {
				return addContent{}, errors.New("nothing to add: give words, pipe text or an image, or use --clip")
			}
		default:
			initial := ""
			if opt.code {
				initial = "```" + opt.codeLang + "\n\n```\n"
			}
			draft, derr := app.EditDraft(ctx, env, initial)
			if derr != nil && !errors.Is(derr, app.ErrEmptyDraft) {
				return addContent{}, derr
			}
			if derr == nil {
				body, usedEditor = draft, true
			}
		}
	}

	if opt.hasTemplate {
		rendered, terr := renderTemplateNote(env, opt, title, where, repo, envNow(env))
		if terr != nil {
			return addContent{}, terr
		}
		tmpl = rendered
		body = joinBody(tmpl.body, body)
		if title == "" {
			title = tmpl.title
		}
	}

	if opt.editor && !usedEditor {
		draft, derr := app.EditDraft(ctx, env, body)
		if derr != nil && !errors.Is(derr, app.ErrEmptyDraft) {
			return addContent{}, derr
		}
		if derr == nil {
			body, usedEditor = draft, true
		} else {
			body = ""
		}
	}
	return addContent{title: title, body: body, images: images, usedEditor: usedEditor, tmpl: tmpl}, nil
}

// templateNote is what a rendered -T template contributes: its body, plus the frontmatter parts a note can carry.
type templateNote struct {
	body    string
	title   string
	tags    []string
	where   string
	repo    string
	dropped []string // keys carrying a value nn cannot keep, for a warning
}

// renderTemplateNote folds the template's frontmatter in, so the note gets exactly one frontmatter block.
func renderTemplateNote(env *app.Env, opt addOptions, title, where, repo string, now time.Time) (templateNote, error) {
	rendered, err := env.Vault.RenderTemplate(opt.template, map[string]string{
		"date":  now.Format("2006-01-02"),
		"time":  now.Format("15:04"),
		"title": title,
		"tags":  strings.Join(opt.tags, ", "),
		"where": where,
		"repo":  repo,
	})
	if err != nil {
		return templateNote{}, err
	}
	fields, body, _, err := vault.ParseFrontmatter(rendered)
	if err != nil {
		return templateNote{}, fmt.Errorf("template %q: frontmatter: %w", opt.template, err)
	}

	t := templateNote{body: body}
	keys := make([]string, 0, len(fields))
	for key := range fields {
		keys = append(keys, key)
	}
	slices.Sort(keys) // a map's order is random; warnings and tags are not
	for _, key := range keys {
		value := fields[key]
		switch key {
		case "tags", "tag":
			t.tags = append(t.tags, fieldStrings(value, true)...)
		case "aliases", "alias":
			if aliases := fieldStrings(value, false); len(aliases) > 0 && t.title == "" {
				t.title = aliases[0]
			}
		case "where":
			t.where = scalarString(value)
		case "repo":
			t.repo = scalarString(value)
		case "date", "time", "via":
			// nn stamps these itself on every note it writes.
		default:
			if hasValue(value) {
				t.dropped = append(t.dropped, key)
			}
		}
	}
	return t, nil
}

// fieldStrings reads a frontmatter value as a list of strings, splitting a scalar on commas/spaces when split is set.
func fieldStrings(value any, split bool) []string {
	var items []string
	switch v := value.(type) {
	case nil, map[string]any:
		return nil
	case []any:
		for _, item := range v {
			if s := scalarString(item); s != "" {
				items = append(items, s)
			}
		}
		return items
	case string:
		if split {
			return strings.FieldsFunc(v, func(r rune) bool { return r == ',' || r == ' ' || r == '\t' })
		}
	}
	if s := scalarString(value); s != "" {
		items = append(items, s)
	}
	return items
}

// scalarString renders a frontmatter scalar as text; a list or map gives "".
func scalarString(value any) string {
	switch v := value.(type) {
	case nil, []any, map[string]any:
		return ""
	case string:
		return strings.TrimSpace(v)
	default:
		return strings.TrimSpace(fmt.Sprint(v))
	}
}

// hasValue reports whether a frontmatter key carries anything, so an empty placeholder like "repo:" is not reported as dropped.
func hasValue(value any) bool {
	switch v := value.(type) {
	case nil:
		return false
	case string:
		return strings.TrimSpace(v) != ""
	case []any:
		return len(v) > 0
	case map[string]any:
		return len(v) > 0
	default:
		return true
	}
}

// joinBody joins above and below with one blank line between; either half may be empty.
func joinBody(above, below string) string {
	if strings.TrimSpace(above) == "" {
		return below
	}
	if strings.TrimSpace(below) == "" {
		return above
	}
	return strings.TrimRight(above, "\n") + "\n\n" + strings.TrimLeft(below, "\n")
}

func pick(has bool, explicit, fallback string) string {
	if has {
		return explicit
	}
	return fallback
}

func tagLine(tags []string) string {
	parts := make([]string, len(tags))
	for i, t := range tags {
		parts[i] = "#" + t
	}
	return strings.Join(parts, " ")
}

func envNow(env *app.Env) time.Time {
	if env.Now != nil {
		return env.Now()
	}
	return time.Now()
}

// checkSecrets returns -1 to continue, or the exit code for a refusal or cancellation.
func checkSecrets(inv *invocation, text string) int {
	secrets := capture.FindSecrets(text)
	if len(secrets) == 0 {
		return -1
	}
	kinds := make([]string, 0, len(secrets))
	seen := map[string]bool{}
	for _, s := range secrets {
		if !seen[s.Kind] {
			seen[s.Kind] = true
			kinds = append(kinds, s.Kind)
		}
	}
	summary := strings.Join(kinds, ", ")
	if !addInteractive() {
		fmt.Fprintf(inv.stderr, "nn: add: refusing to save: possible secret (%s); use --allow-secret to save anyway\n", summary)
		return output.ExitError
	}
	ok, err := addConfirm(fmt.Sprintf("this looks like it contains a secret (%s); save anyway?", summary), false)
	if err != nil || !ok {
		fmt.Fprintln(inv.stderr, "nn: add: cancelled")
		return output.ExitOK
	}
	return -1
}

// tagCounts tallies how many notes carry each tag, first spelling wins.
func tagCounts(notes []*vault.Note) map[string]int {
	counts := map[string]int{}
	seen := map[string]string{}
	for _, n := range notes {
		for _, t := range n.Tags {
			key := strings.ToLower(t)
			if spelling, ok := seen[key]; ok {
				counts[spelling]++
				continue
			}
			seen[key] = t
			counts[t]++
		}
	}
	return counts
}

// adjustSimilarTags offers to reuse a similar existing tag; without a terminal it only warns.
func adjustSimilarTags(inv *invocation, tags []string, notes []*vault.Note) []string {
	suggestions := capture.SimilarTags(tagCounts(notes), tags)
	if len(suggestions) == 0 {
		return tags
	}
	out := append([]string(nil), tags...)
	for _, s := range suggestions {
		if !addInteractive() {
			fmt.Fprintf(inv.stderr, "nn: %s: tag %q is similar to existing %q (%d uses)\n", inv.verb, s.Tag, s.Existing, s.Count)
			continue
		}
		use, err := addConfirm(fmt.Sprintf("use %s instead of %s?", s.Existing, s.Tag), true)
		if err == nil && use {
			for i, t := range out {
				if t == s.Tag {
					out[i] = s.Existing
				}
			}
		}
	}
	return out
}

// decideSimilarNote asks, on a terminal, to append/new/cancel; an unrecognized answer defaults to "new".
func decideSimilarNote(similars []capture.Similar) string {
	var b strings.Builder
	fmt.Fprintln(&b, "similar note found:")
	for _, s := range similars {
		fmt.Fprintf(&b, "  %s (%s)\n", s.Path, s.Title)
	}
	b.WriteString("[a]ppend / [n]ew / [c]ancel ")
	ans, err := app.Ask(b.String())
	if err != nil {
		return "new"
	}
	switch strings.ToLower(strings.TrimSpace(ans)) {
	case "a", "append":
		return "append"
	case "c", "cancel":
		return "cancel"
	default:
		return "new"
	}
}

// resolveFailure turns a Resolve error into a command failure, listing candidates for an ambiguous reference.
func resolveFailure(inv *invocation, ref string, err error) int {
	var amb *vault.AmbiguousError
	if errors.As(err, &amb) {
		fmt.Fprintf(inv.stderr, "nn: %s: %q is ambiguous:\n", inv.verb, ref)
		for _, c := range amb.Candidates {
			fmt.Fprintf(inv.stderr, "  %s\n", c)
		}
		return output.ExitError
	}
	if errors.Is(err, vault.ErrNotFound) {
		return inv.fail(fmt.Errorf("no note matches %q", ref))
	}
	return inv.fail(err)
}

var addInteractive = app.Interactive
var addConfirm = app.Confirm

var captureClipboardImage = platform.ClipboardImage
var captureClipboardText = platform.ClipboardText

// askSimilarNote indirects decideSimilarNote so tests can tell whether the prompt was reached at all.
var askSimilarNote = decideSimilarNote

// ocrEngineFor and defaultOCRLangs indirect ocr.Default/DefaultLangs so tests can substitute a fake engine.
var (
	ocrEngineFor    = ocr.Default
	defaultOCRLangs = ocr.DefaultLangs
)

// wantOCR resolves capture.ocr against --ocr/--no-ocr: either flag overrides the config default.
func wantOCR(cfg config.Config, ocr, noOCR bool) bool {
	switch {
	case ocr:
		return true
	case noOCR:
		return false
	default:
		return cfg.Capture.OCR
	}
}

// recognizeAddImages reuses OCR results in the permanent sidecars after saving.
func recognizeAddImages(ctx context.Context, cfg config.Config, images []vault.Image, stderr io.Writer) ([]*ocr.Result, string) {
	results := make([]*ocr.Result, len(images))
	dir, err := os.MkdirTemp("", "nn-add-ocr-*")
	if err != nil {
		fmt.Fprintf(stderr, "nn: warning: OCR: %v\n", err)
		return results, ""
	}
	defer os.RemoveAll(dir)
	var lines []string
	for i, image := range images {
		name := fmt.Sprintf("capture-%d.%s", i, image.Ext)
		if err := os.WriteFile(filepath.Join(dir, name), image.Data, 0600); err != nil {
			fmt.Fprintf(stderr, "nn: warning: OCR: %v\n", err)
			continue
		}
		result, err := ocr.Ensure(ctx, dir, name, ocrEngineFor(cfg), defaultOCRLangs(cfg))
		if err != nil {
			fmt.Fprintf(stderr, "nn: warning: OCR: %v\n", err)
			continue
		}
		results[i] = result
		for _, line := range result.Lines {
			lines = append(lines, line.Text)
		}
	}
	return results, strings.Join(lines, "\n")
}

// runOCR is best-effort: a failure is only a warning, since the note is already saved.
func runOCR(ctx context.Context, cfg config.Config, root string, embeds []string, stderr io.Writer) {
	if len(embeds) == 0 {
		return
	}
	engine := ocrEngineFor(cfg)
	langs := defaultOCRLangs(cfg)
	for _, rel := range embeds {
		if _, err := ocr.Ensure(ctx, root, rel, engine, langs); err != nil {
			fmt.Fprintf(stderr, "nn: warning: ocr %s: %v\n", rel, err)
		}
	}
}

type addRow struct {
	Path   string `json:"path"`
	Action string `json:"action"`
}

func emitAddResult(inv *invocation, outOpt output.Options, path, action string) int {
	rows := []addRow{{Path: path, Action: action}}
	err := output.Emit(inv.stdout, outOpt, rows, output.Spec[addRow]{
		Text: func(w io.Writer, rows []addRow) error {
			for _, r := range rows {
				prefix := "+"
				if r.Action == "append" {
					prefix = "~"
				}
				if _, err := fmt.Fprintf(w, "%s %s\n", prefix, r.Path); err != nil {
					return err
				}
			}
			return nil
		},
		Path:      func(r addRow) string { return r.Path },
		TSVHeader: []string{"path", "action"},
		TSV:       func(r addRow) []string { return []string{r.Path, r.Action} },
	})
	if err != nil {
		return inv.fail(err)
	}
	return output.ExitOK
}

type previewRow struct {
	To     string   `json:"to,omitempty"`
	Title  string   `json:"title,omitempty"`
	Tags   []string `json:"tags,omitempty"`
	Body   string   `json:"body,omitempty"`
	Images int      `json:"images,omitempty"`
}

// printAddPreview shows what --preview would have saved, through output.Emit like every other result.
func printAddPreview(inv *invocation, outOpt output.Options, to, title, body string, tags []string, images []vault.Image) int {
	rows := []previewRow{{To: to, Title: title, Tags: tags, Body: body, Images: len(images)}}
	err := output.Emit(inv.stdout, outOpt, rows, output.Spec[previewRow]{
		Text: func(w io.Writer, rows []previewRow) error {
			for _, r := range rows {
				var b strings.Builder
				if r.To != "" {
					fmt.Fprintf(&b, "to: %s\n", r.To)
				}
				if r.Title != "" {
					fmt.Fprintf(&b, "title: %s\n", r.Title)
				}
				if len(r.Tags) > 0 {
					fmt.Fprintf(&b, "tags: %s\n", strings.Join(r.Tags, ", "))
				}
				if r.Images > 0 {
					fmt.Fprintf(&b, "images: %d\n", r.Images)
				}
				if r.Body != "" {
					b.WriteString("\n")
					b.WriteString(r.Body)
					b.WriteString("\n")
				}
				if _, err := io.WriteString(w, b.String()); err != nil {
					return err
				}
			}
			return nil
		},
		Path:      func(r previewRow) string { return r.To },
		TSVHeader: []string{"to", "title", "tags", "body", "images"},
		// output.Emit escapes every field, so the body goes out raw here rather than double-escaped.
		TSV: func(r previewRow) []string {
			return []string{r.To, r.Title, strings.Join(r.Tags, ","), r.Body, strconv.Itoa(r.Images)}
		},
	})
	if err != nil {
		return inv.fail(err)
	}
	return output.ExitOK
}
