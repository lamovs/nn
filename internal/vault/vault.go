// Package vault is nn's model of the Obsidian vault.
package vault

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/lamovs/nn/internal/config"
)

// ErrNotImplemented marks functionality this package only stubs out.
var ErrNotImplemented = errors.New("not implemented")

// ErrNotFound is returned by Resolve when no note matches ref.
var ErrNotFound = errors.New("not found")

// AmbiguousError is returned by Resolve when more than one note matches ref.
type AmbiguousError struct {
	Candidates []string
}

func (e *AmbiguousError) Error() string {
	if len(e.Candidates) == 0 {
		return "ambiguous reference"
	}
	return "ambiguous reference: " + strings.Join(e.Candidates, ", ")
}

const (
	// TemplatesDir and AssetsDir are the inbox's template and image directories.
	TemplatesDir = "_templates"
	AssetsDir    = "assets"
)

type Vault struct {
	Root, Inbox string
}

// Open checks that cfg.Vault.Root is an existing directory.
func Open(cfg config.Config) (*Vault, error) {
	root := strings.TrimSpace(cfg.Vault.Root)
	if root == "" {
		return nil, errors.New(`vault.root is not set; run "nn config vault.root PATH"`)
	}
	root, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	info, err := os.Stat(root)
	if err != nil {
		return nil, fmt.Errorf("vault root: %w", err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("vault root %s is not a directory", root)
	}

	inbox := strings.TrimSpace(cfg.Vault.Inbox)
	if inbox == "" {
		inbox = config.Default().Vault.Inbox
	}
	if filepath.IsAbs(inbox) {
		rel, err := filepath.Rel(root, inbox)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return nil, fmt.Errorf("inbox %s is outside the vault root %s", inbox, root)
		}
		inbox = rel
	}
	inbox = cleanRel(inbox)
	if inbox == ".." || strings.HasPrefix(inbox, "../") {
		return nil, fmt.Errorf("inbox %s is outside the vault root %s", cfg.Vault.Inbox, root)
	}
	return &Vault{Root: root, Inbox: inbox}, nil
}

// Abs turns a root-relative, slash-separated path into an absolute one.
func (v *Vault) Abs(rel string) string {
	return filepath.Join(v.Root, filepath.FromSlash(rel))
}

type Note struct {
	Path    string // relative to root
	Title   string // first alias, else first H1, else stem
	Aliases []string
	Tags    []string // frontmatter + inline, deduplicated

	Date, Modified time.Time

	Where, Repo, Via string

	Fields map[string]any // the whole frontmatter, as parsed

	Body          string
	BodyStartLine int // 1-based file line where the body starts

	Embeds []string // relative image paths from ![[...]] and ![](...)

	InInbox bool
}

func IsImage(name string) bool {
	switch strings.ToLower(path.Ext(name)) {
	case ".png", ".jpg", ".jpeg", ".gif", ".webp":
		return true
	}
	return false
}

func isNote(name string) bool {
	return strings.EqualFold(path.Ext(name), ".md")
}

// Walk calls fn with the root-relative path of every note, in path order.
func (v *Vault) Walk(ctx context.Context, fn func(rel string) error) error {
	return v.walk(ctx, isNote, fn)
}

func (v *Vault) walk(ctx context.Context, match func(name string) bool, fn func(rel string) error) error {
	root := v.Root
	if resolved, err := filepath.EvalSymlinks(root); err == nil {
		root = resolved
	}
	templates := path.Join(v.Inbox, TemplatesDir)
	return filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		if p == root {
			return err
		}
		if err != nil {
			if d != nil && d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		rel, relErr := filepath.Rel(root, p)
		if relErr != nil {
			return relErr
		}
		rel = filepath.ToSlash(rel)
		name := d.Name()
		if d.IsDir() {
			if strings.HasPrefix(name, ".") || rel == templates {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasPrefix(name, ".") || !match(name) {
			return nil
		}
		return fn(rel)
	})
}

// Load reads and parses one note.
func (v *Vault) Load(rel string) (*Note, error) {
	return v.load(cleanRel(rel), &lazyImages{vault: v})
}

func (v *Vault) load(rel string, images *lazyImages) (*Note, error) {
	abs := v.Abs(rel)
	src, err := os.ReadFile(abs)
	if err != nil {
		return nil, err
	}
	info, err := os.Stat(abs)
	if err != nil {
		return nil, err
	}
	return v.parseNote(rel, string(src), info.ModTime(), images), nil
}

func (v *Vault) parseNote(rel, src string, modified time.Time, images *lazyImages) *Note {
	fields, body, start, err := ParseFrontmatter(src)
	if err != nil || fields == nil {
		fields = map[string]any{}
	}
	n := &Note{
		Path:          rel,
		Fields:        fields,
		Body:          body,
		BodyStartLine: start,
		Modified:      modified,
		Where:         stringField(fields, "where"),
		Repo:          stringField(fields, "repo"),
		Via:           stringField(fields, "via"),
		InInbox:       v.InInbox(rel),
	}

	n.Aliases = dedupeFold(fieldList(fields, false, "aliases", "alias"))
	n.Tags = dedupeFold(append(fieldList(fields, true, "tags", "tag"), InlineTags(body)...))
	n.Date = parseDate(fields)

	switch {
	case len(n.Aliases) > 0:
		n.Title = n.Aliases[0]
	case strings.TrimSpace(stringField(fields, "title")) != "":
		n.Title = strings.TrimSpace(stringField(fields, "title"))
	default:
		n.Title = firstH1(body)
	}
	if n.Title == "" {
		n.Title = strings.TrimSuffix(path.Base(rel), path.Ext(rel))
	}

	n.Embeds = v.resolveEmbeds(rel, body, images)
	return n
}

// InInbox reports whether a root-relative path lies in the inbox.
func (v *Vault) InInbox(rel string) bool {
	if v.Inbox == "" || v.Inbox == "." {
		return true
	}
	return strings.HasPrefix(cleanRel(rel), v.Inbox+"/")
}

// LoadAll loads every note concurrently and returns them in path order.
func (v *Vault) LoadAll(ctx context.Context) ([]*Note, error) {
	var notes []*Note
	err := v.LoadEach(ctx, func(n *Note) error {
		notes = append(notes, n)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return notes, nil
}

// LoadEach loads notes concurrently but calls fn with each one, in order, never concurrently.
func (v *Vault) LoadEach(ctx context.Context, fn func(*Note) error) error {
	var rels []string
	if err := v.Walk(ctx, func(rel string) error {
		rels = append(rels, rel)
		return nil
	}); err != nil {
		return err
	}
	if len(rels) == 0 {
		return nil
	}

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	type loaded struct {
		i    int
		note *Note
		err  error
	}
	images := &lazyImages{vault: v}
	results := make(chan loaded, len(rels))
	var next atomic.Int64
	var wg sync.WaitGroup
	workers := min(runtime.GOMAXPROCS(0), len(rels))
	for range workers {
		wg.Go(func() {
			for ctx.Err() == nil {
				i := int(next.Add(1) - 1)
				if i >= len(rels) {
					return
				}
				n, err := v.load(rels[i], images)
				results <- loaded{i: i, note: n, err: err}
			}
		})
	}
	go func() {
		wg.Wait()
		close(results)
	}()

	pending := make(map[int]loaded)
	emit := 0
	var firstErr error
	for r := range results {
		if firstErr != nil {
			continue
		}
		pending[r.i] = r
		for {
			cur, ok := pending[emit]
			if !ok {
				break
			}
			delete(pending, emit)
			emit++
			if cur.err != nil {
				if errors.Is(cur.err, fs.ErrNotExist) {
					continue
				}
				firstErr = cur.err
				cancel()
				break
			}
			if err := fn(cur.note); err != nil {
				firstErr = err
				cancel()
				break
			}
		}
	}
	if firstErr != nil {
		return firstErr
	}
	if emit < len(rels) {
		return context.Cause(ctx)
	}
	return nil
}

// Images returns every image in the vault, root-relative, in path order.
func (v *Vault) Images(ctx context.Context) ([]string, error) {
	var out []string
	err := v.walk(ctx, IsImage, func(rel string) error {
		out = append(out, rel)
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Strings(out)
	return out, nil
}

// Resolve looks up ref as a path, suffix, stem, alias or title; the first match wins.
func (v *Vault) Resolve(ref string) (string, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return "", ErrNotFound
	}
	if filepath.IsAbs(ref) {
		rel, err := filepath.Rel(v.Root, ref)
		if err != nil || strings.HasPrefix(filepath.ToSlash(rel), "../") || rel == ".." {
			return "", ErrNotFound
		}
		ref = rel
	}
	r := cleanRel(ref)
	withMD := r
	if !isNote(r) {
		withMD = r + ".md"
	}

	var rels []string
	if err := v.Walk(context.Background(), func(rel string) error {
		rels = append(rels, rel)
		return nil
	}); err != nil {
		return "", err
	}

	lowerR, lowerMD := strings.ToLower(r), strings.ToLower(withMD)
	tiers := []func(rel string) bool{
		func(rel string) bool { return rel == r || rel == withMD },
		func(rel string) bool {
			l := strings.ToLower(rel)
			return l == lowerR || l == lowerMD
		},
	}
	for _, match := range tiers {
		if found, err := pick(rels, match); found != "" || err != nil {
			return found, err
		}
	}

	if info, err := os.Stat(v.Abs(withMD)); err == nil && !info.IsDir() && !strings.HasPrefix(withMD, "../") {
		return withMD, nil
	}

	if strings.Contains(r, "/") {
		found, err := pick(rels, func(rel string) bool {
			return strings.HasSuffix(strings.ToLower(rel), "/"+lowerMD)
		})
		if found != "" || err != nil {
			return found, err
		}
	}

	stemRef := strings.ToLower(strings.TrimSuffix(r, path.Ext(withMD)))
	found, err := pick(rels, func(rel string) bool {
		return strings.ToLower(stem(rel)) == stemRef
	})
	if found != "" || err != nil {
		return found, err
	}

	notes, err := v.LoadAll(context.Background())
	if err != nil {
		return "", err
	}
	byAlias := func(n *Note) bool {
		for _, a := range n.Aliases {
			if strings.EqualFold(a, ref) {
				return true
			}
		}
		return false
	}
	byTitle := func(n *Note) bool { return strings.EqualFold(n.Title, ref) }
	for _, match := range []func(*Note) bool{byAlias, byTitle} {
		var hits []string
		for _, n := range notes {
			if match(n) {
				hits = append(hits, n.Path)
			}
		}
		if found, err := pick(hits, func(string) bool { return true }); found != "" || err != nil {
			return found, err
		}
	}
	return "", ErrNotFound
}

func pick(rels []string, match func(string) bool) (string, error) {
	var hits []string
	for _, rel := range rels {
		if match(rel) {
			hits = append(hits, rel)
		}
	}
	switch len(hits) {
	case 0:
		return "", nil
	case 1:
		return hits[0], nil
	}
	sort.Strings(hits)
	return "", &AmbiguousError{Candidates: hits}
}

func stem(rel string) string {
	base := path.Base(rel)
	return strings.TrimSuffix(base, path.Ext(base))
}

func cleanRel(rel string) string {
	rel = path.Clean(filepath.ToSlash(strings.TrimSpace(rel)))
	rel = strings.TrimLeft(rel, "/")
	if rel == "" {
		return "."
	}
	return rel
}

func stringField(fields map[string]any, key string) string {
	switch v := fields[key].(type) {
	case nil:
		return ""
	case string:
		return strings.TrimSpace(v)
	case []any, map[string]any:
		return ""
	default:
		return fmt.Sprint(v)
	}
}

func fieldList(fields map[string]any, splitString bool, keys ...string) []string {
	for _, key := range keys {
		raw, ok := fields[key]
		if !ok || raw == nil {
			continue
		}
		var items []string
		switch v := raw.(type) {
		case string:
			if splitString {
				items = strings.FieldsFunc(v, func(r rune) bool { return r == ',' || r == ' ' || r == '\t' })
			} else {
				items = []string{v}
			}
		case []any:
			for _, item := range v {
				switch item.(type) {
				case nil, []any, map[string]any:
					continue
				}
				items = append(items, fmt.Sprint(item))
			}
		case map[string]any:
			continue
		default:
			items = []string{fmt.Sprint(v)}
		}
		var out []string
		for _, item := range items {
			item = strings.TrimSpace(item)
			if splitString {
				item = strings.TrimPrefix(item, "#")
			}
			if item != "" {
				out = append(out, item)
			}
		}
		return out
	}
	return nil
}

func dedupeFold(items []string) []string {
	if len(items) == 0 {
		return nil
	}
	seen := make(map[string]bool, len(items))
	out := items[:0:0]
	for _, item := range items {
		key := strings.ToLower(item)
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, item)
	}
	return out
}

func parseDate(fields map[string]any) time.Time {
	raw := stringField(fields, "date")
	if raw == "" {
		return time.Time{}
	}
	for _, layout := range []string{time.RFC3339, "2006-01-02T15:04:05", "2006-01-02T15:04", "2006-01-02 15:04:05", "2006-01-02 15:04"} {
		if t, err := time.ParseInLocation(layout, raw, time.Local); err == nil {
			return t
		}
	}
	day, err := time.ParseInLocation("2006-01-02", raw, time.Local)
	if err != nil {
		return time.Time{}
	}
	if clock, err := time.Parse("15:04", stringField(fields, "time")); err == nil {
		day = day.Add(time.Duration(clock.Hour())*time.Hour + time.Duration(clock.Minute())*time.Minute)
	}
	return day
}

// lazyImages builds the vault's image index on first use.
type lazyImages struct {
	vault *Vault
	once  sync.Once
	base  map[string][]string // lowercased basename -> rels
}

func (l *lazyImages) index() *lazyImages {
	l.once.Do(func() {
		l.base = map[string][]string{}
		_ = l.vault.walk(context.Background(), IsImage, func(rel string) error {
			key := strings.ToLower(path.Base(rel))
			l.base[key] = append(l.base[key], rel)
			return nil
		})
	})
	return l
}

func (v *Vault) resolveEmbed(rel, target string, markdown bool, images *lazyImages) string {
	target = strings.TrimLeft(filepath.ToSlash(strings.TrimSpace(target)), "/")
	dir := path.Dir(rel)
	var tries []string
	if markdown || strings.HasPrefix(target, "./") || strings.HasPrefix(target, "../") {
		tries = append(tries, path.Join(dir, target))
	}
	tries = append(tries, path.Clean(target))

	for _, try := range tries {
		if strings.HasPrefix(try, "../") {
			continue
		}
		if info, err := os.Stat(v.Abs(try)); err == nil && !info.IsDir() {
			return try
		}
	}

	idx := images.index()
	cands := idx.base[strings.ToLower(path.Base(target))]
	if strings.Contains(path.Clean(target), "/") {
		suffix := "/" + strings.ToLower(path.Clean(target))
		var filtered []string
		for _, c := range cands {
			if strings.HasSuffix("/"+strings.ToLower(c), suffix) {
				filtered = append(filtered, c)
			}
		}
		cands = filtered
	}
	if best := Closest(rel, cands); best != "" {
		return best
	}
	return path.Clean(target)
}

// Closest picks the candidate nearest to from by directory distance.
func Closest(from string, cands []string) string {
	if len(cands) == 0 {
		return ""
	}
	fromDir := splitDir(from)
	best := ""
	bestDist, bestDepth := 0, 0
	for _, c := range cands {
		cDir := splitDir(c)
		common := 0
		for common < len(fromDir) && common < len(cDir) && fromDir[common] == cDir[common] {
			common++
		}
		dist := len(fromDir) - common + len(cDir) - common
		depth := len(cDir)
		if best == "" || dist < bestDist || dist == bestDist && (depth < bestDepth || depth == bestDepth && c < best) {
			best, bestDist, bestDepth = c, dist, depth
		}
	}
	return best
}

func splitDir(rel string) []string {
	dir := path.Dir(rel)
	if dir == "." || dir == "/" {
		return nil
	}
	return strings.Split(dir, "/")
}
