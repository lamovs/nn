package vault

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

type Image struct {
	Data []byte
	Ext  string
}

type NewNote struct {
	Title, Body string
	// FilenameHint supplies a provisional name when Title is empty.
	FilenameHint string
	// TrailingTags are rendered after the body and image embeds.
	TrailingTags     []string
	Tags             []string
	Via, Where, Repo string
	Images           []Image
	Now              time.Time
}

// Create writes a new note into the inbox and returns it as loaded back.
func (v *Vault) Create(n NewNote) (*Note, error) {
	result, err := v.CreateWithAssets(n)
	return result.Note, err
}

// WriteResult also carries each input image's destination, in order.
type WriteResult struct {
	Note   *Note
	Images []string
}

// CreateWithAssets creates a note and returns exact input-image destinations.
func (v *Vault) CreateWithAssets(n NewNote) (WriteResult, error) {
	now := n.Now
	if now.IsZero() {
		now = time.Now()
	}
	title := strings.Join(strings.Fields(n.Title), " ")
	body := strings.TrimRight(strings.TrimLeft(n.Body, "\r\n"), " \t\r\n")
	tags := normalizeTags(n.Tags)
	if title == "" && strings.TrimSpace(body) == "" && len(n.Images) == 0 {
		return WriteResult{}, errors.New("nothing to save: the note has no title, body or image")
	}

	filenameTitle := title
	if filenameTitle == "" {
		filenameTitle = n.FilenameHint
	}
	base := noteSlug(filenameTitle, body, tags, len(n.Images) > 0, now)
	taken, err := v.takenStems()
	if err != nil {
		return WriteResult{}, err
	}
	name := freeName(base, func(name string) bool { return taken[name] })

	inboxDir, err := v.EnsureDir(v.Inbox)
	if err != nil {
		return WriteResult{}, err
	}

	embeds, imagePaths, created, err := v.saveImages(name, n.Images)
	if err != nil {
		return WriteResult{}, err
	}
	rollback := func() {
		for _, rel := range created {
			os.Remove(v.Abs(rel))
		}
	}

	var fm strings.Builder
	fm.WriteString("---\n")
	fmt.Fprintf(&fm, "date: %s\n", now.Format("2006-01-02"))
	fmt.Fprintf(&fm, "tags: %s\n", yamlFlowList(tags))
	if title != "" {
		fmt.Fprintf(&fm, "aliases: %s\n", yamlFlowList([]string{title}))
	}
	fmt.Fprintf(&fm, "time: %q\n", now.Format("15:04"))
	for _, kv := range [][2]string{{"where", n.Where}, {"repo", n.Repo}, {"via", n.Via}} {
		if value := strings.TrimSpace(kv[1]); value != "" {
			fmt.Fprintf(&fm, "%s: %s\n", kv[0], yamlString(value, false))
		}
	}
	fm.WriteString("---\n")
	content := fm.String() + joinBlocks(body, embeds, "\n")
	if footer := keywordFooter(n.TrailingTags, append(append([]string(nil), tags...), InlineTags(body)...)); footer != "" {
		if len(embeds) > 0 {
			content = fm.String() + appendMetadata(body, strings.Join(embeds, "\n"))
		}
		content = appendMetadata(content, footer)
	}

	// Refuse frontmatter nn cannot parse back: the note would lose its
	// date, tags and aliases.
	if _, _, _, err := ParseFrontmatter(content); err != nil {
		rollback()
		return WriteResult{}, err
	}

	for {
		rel := path.Join(v.Inbox, name+".md")
		err := writeAtomic(filepath.Join(inboxDir, name+".md"), []byte(content), createMode(), true)
		if errors.Is(err, fs.ErrExist) {
			taken[name] = true
			name = freeName(base, func(name string) bool { return taken[name] })
			continue
		}
		if err != nil {
			rollback()
			return WriteResult{}, err
		}
		note, err := v.Load(rel)
		if err != nil {
			return WriteResult{}, err
		}
		return WriteResult{Note: note, Images: imagePaths}, nil
	}
}

// Append adds text and image embeds to rel, replacing it atomically under lock.
func (v *Vault) Append(rel string, text string, images []Image) (*Note, error) {
	result, err := v.AppendWithAssets(rel, text, images)
	return result.Note, err
}

// AppendWithAssets appends content and returns exact input-image destinations.
func (v *Vault) AppendWithAssets(rel, text string, images []Image) (WriteResult, error) {
	return v.appendNote(rel, text, images, nil)
}

func (v *Vault) appendNote(rel string, text string, images []Image, enrich func(string) string) (WriteResult, error) {
	rel = cleanRel(rel)
	if !isNote(rel) {
		return WriteResult{}, fmt.Errorf("%s is not a note", rel)
	}
	target, err := v.noteForWrite(rel)
	if err != nil {
		return WriteResult{}, err
	}
	info, err := os.Stat(target)
	if err != nil {
		return WriteResult{}, err
	}
	if !info.Mode().IsRegular() {
		return WriteResult{}, fmt.Errorf("%s is not a regular file", rel)
	}
	if !isNote(target) {
		return WriteResult{}, fmt.Errorf("%s is not a note", rel)
	}

	text = strings.TrimRight(strings.TrimLeft(text, "\r\n"), " \t\r\n")
	if enrich == nil && strings.TrimSpace(text) == "" && len(images) == 0 {
		return WriteResult{}, errors.New("nothing to append")
	}

	locked, unlock, err := lockNote(target)
	if err != nil {
		return WriteResult{}, err
	}
	defer unlock()

	// Re-stat and re-read under the lock, not the earlier snapshot.
	info, err = locked.Stat()
	if err != nil {
		return WriteResult{}, err
	}
	if !info.Mode().IsRegular() {
		return WriteResult{}, fmt.Errorf("%s is not a regular file", rel)
	}
	src, err := io.ReadAll(locked)
	if err != nil {
		return WriteResult{}, err
	}
	if enrich != nil {
		text = enrich(string(src))
		if text == "" {
			note, err := v.Load(rel)
			return WriteResult{Note: note}, err
		}
	}

	imageName := Slug(stem(rel))
	if imageName == "" {
		imageName = "image"
	}
	embeds, imagePaths, created, err := v.saveImages(imageName, images)
	if err != nil {
		return WriteResult{}, err
	}

	newline := "\n"
	if bytes.Contains(src, []byte("\r\n")) {
		newline = "\r\n"
		text = strings.ReplaceAll(strings.ReplaceAll(text, "\r\n", "\n"), "\n", "\r\n")
	}
	addition := joinBlocks(text, embeds, newline)
	existing := strings.TrimRight(string(src), " \t\r\n")
	content := addition
	if existing != "" {
		content = existing + newline + newline + addition
	}
	if enrich != nil {
		content = appendMetadata(string(src), text)
	}

	if err := writeAtomic(target, []byte(content), info.Mode().Perm(), false); err != nil {
		for _, c := range created {
			os.Remove(v.Abs(c))
		}
		return WriteResult{}, err
	}
	note, err := v.Load(rel)
	return WriteResult{Note: note, Images: imagePaths}, err
}

func joinBlocks(body string, embeds []string, newline string) string {
	var parts []string
	if body != "" {
		parts = append(parts, body)
	}
	if len(embeds) > 0 {
		parts = append(parts, strings.Join(embeds, newline))
	}
	if len(parts) == 0 {
		return ""
	}
	return strings.Join(parts, newline+newline) + newline
}

func normalizeTags(tags []string) []string {
	var out []string
	for _, t := range tags {
		t = strings.TrimPrefix(strings.TrimSpace(t), "#")
		t = strings.Join(strings.Fields(t), "-")
		if t != "" {
			out = append(out, t)
		}
	}
	return dedupeFold(out)
}

func noteSlug(title, body string, tags []string, hasImages bool, now time.Time) string {
	if s := Slug(title); s != "" {
		return s
	}
	for _, line := range splitLines(body) {
		if _, _, isFence := openFence(line); isFence {
			continue
		}
		words := strings.Fields(line)
		if len(words) > 6 {
			words = words[:6]
		}
		if s := Slug(strings.Join(words, " ")); s != "" {
			return s
		}
	}
	prefix := "note"
	if hasImages {
		prefix = "shot"
	}
	if len(tags) > 0 {
		if s := Slug(tags[0]); s != "" {
			prefix = s
		}
	}
	return prefix + "-" + now.Format("2006-01-02-1504")
}

func freeName(base string, taken func(string) bool) string {
	name := base
	for i := 2; taken(name); i++ {
		name = fmt.Sprintf("%s-%d", base, i)
	}
	return name
}

// takenStems returns lowercased stems already in use, so a new name doesn't collide.
func (v *Vault) takenStems() (map[string]bool, error) {
	taken := map[string]bool{}
	err := v.Walk(context.Background(), func(rel string) error {
		taken[strings.ToLower(stem(rel))] = true
		return nil
	})
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(v.Abs(v.Inbox))
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	for _, e := range entries {
		taken[strings.ToLower(strings.TrimSuffix(e.Name(), path.Ext(e.Name())))] = true
	}
	return taken, nil
}

// saveImages stores images under <inbox>/assets, reusing any file with the same sha256.
func (v *Vault) saveImages(name string, images []Image) (embeds, imagePaths, created []string, err error) {
	if len(images) == 0 {
		return nil, nil, nil, nil
	}
	dirRel := path.Join(v.Inbox, AssetsDir)
	dir, err := v.EnsureDir(dirRel)
	if err != nil {
		return nil, nil, nil, err
	}
	var made []string
	defer func() {
		if err != nil {
			for _, rel := range made {
				os.Remove(v.Abs(rel))
			}
		}
	}()

	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, nil, nil, err
	}
	existing := map[string]bool{}
	sums := map[[sha256.Size]byte]string{}
	sizes := map[int64][]string{}
	for _, e := range entries {
		existing[strings.ToLower(e.Name())] = true
		if !e.Type().IsRegular() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		if info, err := e.Info(); err == nil {
			sizes[info.Size()] = append(sizes[info.Size()], e.Name())
		}
	}
	for size := range sizes {
		sort.Strings(sizes[size])
	}

	for _, img := range images {
		ext := strings.ToLower(strings.TrimPrefix(strings.TrimSpace(img.Ext), "."))
		if ext == "jpeg" {
			ext = "jpg"
		}
		if !IsImage("x." + ext) {
			return nil, nil, nil, fmt.Errorf("unsupported image type %q", img.Ext)
		}
		if len(img.Data) == 0 {
			return nil, nil, nil, errors.New("image is empty")
		}
		sum := sha256.Sum256(img.Data)

		file, found := sums[sum]
		if !found {
			for _, candidate := range sizes[int64(len(img.Data))] {
				data, err := os.ReadFile(filepath.Join(dir, candidate))
				if err != nil {
					continue
				}
				if sha256.Sum256(data) == sum {
					file, found = candidate, true
					break
				}
			}
		}

		if !found {
			for i := 1; ; i++ {
				file = name + "." + ext
				if i > 1 {
					file = fmt.Sprintf("%s-%d.%s", name, i, ext)
				}
				if existing[strings.ToLower(file)] {
					continue
				}
				err := writeAtomic(filepath.Join(dir, file), img.Data, createMode(), true)
				if errors.Is(err, fs.ErrExist) {
					existing[strings.ToLower(file)] = true
					continue
				}
				if err != nil {
					return nil, nil, nil, err
				}
				existing[strings.ToLower(file)] = true
				made = append(made, path.Join(dirRel, file))
				break
			}
		}
		sums[sum] = file
		imagePaths = append(imagePaths, path.Join(dirRel, file))
		embeds = append(embeds, "![["+path.Join(dirRel, file)+"]]")
	}
	return embeds, imagePaths, made, nil
}

// createMode leaves the file mode to the user's umask, like any other tool.
func createMode() fs.FileMode { return 0o666 &^ processUmask }

const dirMode fs.FileMode = 0o777

const (
	lockWait = 5 * time.Second
	lockPoll = 10 * time.Millisecond
)

// lockNote locks the note at path, retrying if a concurrent append just replaced it.
func lockNote(abs string) (*os.File, func(), error) {
	deadline := time.Now().Add(lockWait)
	for {
		f, err := os.Open(abs)
		if err != nil {
			return nil, nil, err
		}
		release := func() {
			_ = unlockFile(f)
			_ = f.Close()
		}
		switch lockErr := lockFile(f); {
		case lockErr == nil:
			replaced, err := replacedSince(f, abs)
			if err == nil && !replaced {
				return f, release, nil
			}
			release()
			if err != nil {
				return nil, nil, err
			}
		case errors.Is(lockErr, errNoLocking):
			return f, func() { _ = f.Close() }, nil
		case errors.Is(lockErr, errLocked):
			f.Close()
			time.Sleep(lockPoll)
		default:
			f.Close()
			return nil, nil, lockErr
		}
		if time.Now().After(deadline) {
			return nil, nil, fmt.Errorf("%s is %w; try again", filepath.Base(abs), errLocked)
		}
	}
}

// replacedSince reports whether abs has stopped naming the open file f.
func replacedSince(f *os.File, abs string) (bool, error) {
	open, err := f.Stat()
	if err != nil {
		return false, err
	}
	now, err := os.Stat(abs)
	if err != nil {
		return false, err
	}
	return !os.SameFile(open, now), nil
}

// writeAtomic writes via a temp file next to dst; noClobber leaves an existing dst alone.
func writeAtomic(dst string, data []byte, perm fs.FileMode, noClobber bool) error {
	dir := filepath.Dir(dst)
	tmp, err := os.CreateTemp(dir, ".nn-*.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)

	_, writeErr := tmp.Write(data)
	chmodErr := tmp.Chmod(perm)
	syncErr := tmp.Sync()
	closeErr := tmp.Close()
	if err := errors.Join(writeErr, chmodErr, syncErr, closeErr); err != nil {
		return err
	}

	if noClobber {
		// A hard link fails with EEXIST instead of replacing dst.
		err := os.Link(tmpPath, dst)
		switch {
		case err == nil:
		case errors.Is(err, fs.ErrExist):
			return fmt.Errorf("%s: %w", dst, fs.ErrExist)
		default:
			if _, statErr := os.Lstat(dst); statErr == nil {
				return fmt.Errorf("%s: %w", dst, fs.ErrExist)
			}
			if err := os.Rename(tmpPath, dst); err != nil {
				return err
			}
		}
	} else if err := os.Rename(tmpPath, dst); err != nil {
		return err
	}

	if d, err := os.Open(dir); err == nil {
		d.Sync()
		d.Close()
	}
	return nil
}

// Templates lists <inbox>/_templates by name, without ".md".
func (v *Vault) Templates() ([]string, error) {
	entries, err := os.ReadDir(v.Abs(path.Join(v.Inbox, TemplatesDir)))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var names []string
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || strings.HasPrefix(name, ".") || !isNote(name) {
			continue
		}
		names = append(names, strings.TrimSuffix(name, path.Ext(name)))
	}
	sort.Strings(names)
	return names, nil
}

var templateKeys = map[string]bool{"date": true, "time": true, "title": true, "tags": true, "where": true, "repo": true}

var placeholder = regexp.MustCompile(`\{\{\s*([A-Za-z_][A-Za-z0-9_]*)\s*\}\}`)

// RenderTemplate substitutes {{key}} placeholders; the templates directory is checked against the vault boundary first.
func (v *Vault) RenderTemplate(name string, vars map[string]string) (string, error) {
	name = strings.TrimSuffix(strings.TrimSpace(name), ".md")
	if name == "" || strings.ContainsAny(name, `/\`) || strings.HasPrefix(name, ".") {
		return "", fmt.Errorf("invalid template name %q", name)
	}
	rel := path.Join(v.Inbox, TemplatesDir)
	dir, inside, err := v.RealDir(rel)
	if err != nil {
		return "", fmt.Errorf("template %q: %w", name, err)
	}
	if !inside {
		return "", fmt.Errorf("template %q: %s resolves to %s, which is %w", name, rel, dir, ErrOutsideVault)
	}
	src, err := os.ReadFile(filepath.Join(dir, name+".md"))
	if errors.Is(err, fs.ErrNotExist) {
		return "", fmt.Errorf("template %q: %w", name, ErrNotFound)
	}
	if err != nil {
		return "", err
	}

	now := time.Now()
	values := map[string]string{"date": now.Format("2006-01-02"), "time": now.Format("15:04")}
	for k, val := range vars {
		values[k] = val
	}
	return placeholder.ReplaceAllStringFunc(string(src), func(m string) string {
		key := placeholder.FindStringSubmatch(m)[1]
		if val, ok := values[key]; ok {
			return val
		}
		if templateKeys[key] {
			return ""
		}
		return m
	}), nil
}
