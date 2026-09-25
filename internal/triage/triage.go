// Package triage builds reviewable, additive inbox plans from bounded excerpts.
package triage

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/lamovs/nn/internal/ai"
	"github.com/lamovs/nn/internal/config"
	"github.com/lamovs/nn/internal/links"
	"github.com/lamovs/nn/internal/search"
	"github.com/lamovs/nn/internal/vault"
)

type Options struct {
	Limits           config.AIContext
	NoLayoutFallback bool
	Notes            []string
}
type Model func(context.Context, ai.Approval, ai.Request) (ai.Result, error)
type source struct {
	ID       string   `json:"id"`
	Path     string   `json:"path"`
	Title    string   `json:"title"`
	HasTitle bool     `json:"has_title"`
	Tags     []string `json:"tags"`
	Subject  bool     `json:"subject"`
	Excerpt  string   `json:"excerpt"`
	snapshot vault.Snapshot
}
type Session struct {
	v         *vault.Vault
	opts      Options
	snapshots map[string]vault.Snapshot
	notePaths []string // Includes unsupported entries for link ambiguity checks.
	docs      []*search.Doc
	sources   []*source
	byID      map[string]*source
	admitted  map[string]bool
	graph     *links.Graph
	chars     int
	started   bool
}

// Prepare takes same-byte snapshots before any data is sent to a model.
func Prepare(ctx context.Context, v *vault.Vault, opts Options) (*Session, error) {
	if v == nil || opts.Limits.Notes < 1 || opts.Limits.Chars < 1 || opts.Limits.SearchRounds < 0 || opts.Limits.SearchRounds > 3 {
		return nil, errors.New("triage: invalid vault or context limits")
	}
	if opts.Limits.Notes > 32 {
		opts.Limits.Notes = 32
	}
	s := &Session{v: v, opts: opts, snapshots: map[string]vault.Snapshot{}, byID: map[string]*source{}, admitted: map[string]bool{}}
	requested := make(map[string]bool, len(opts.Notes))
	for _, p := range opts.Notes {
		requested[p] = true
	}
	var notes []*vault.Note
	if err := v.Walk(ctx, func(rel string) error {
		s.notePaths = append(s.notePaths, rel)
		info, err := os.Lstat(v.Abs(rel))
		if err != nil {
			return fmt.Errorf("triage: inspect %q: %w", rel, err)
		}
		if !info.Mode().IsRegular() {
			if requested[rel] {
				return fmt.Errorf("triage: %q is not a regular inbox note; symlink subjects are unsupported", rel)
			}
			return nil
		}
		snap, err := v.ReadSnapshot(rel)
		if err != nil {
			return fmt.Errorf("triage: read %q: %w", rel, err)
		}
		s.snapshots[rel] = snap
		notes = append(notes, snap.Note)
		n := snap.Note
		d := &search.Doc{Path: n.Path, Title: n.Title, Aliases: n.Aliases, Tags: n.Tags, Date: n.Date, Modified: n.Modified, Where: n.Where, Repo: n.Repo, InInbox: n.InInbox}
		for i, line := range strings.Split(n.Body, "\n") {
			d.Lines = append(d.Lines, search.Line{Num: n.BodyStartLine + i, Text: line})
		}
		s.docs = append(s.docs, d)
		return nil
	}); err != nil {
		return nil, err
	}
	s.graph = links.Build(notes)
	paths := append([]string(nil), opts.Notes...)
	explicit := len(paths) > 0
	if !explicit {
		for p := range s.snapshots {
			if v.InInbox(p) {
				paths = append(paths, p)
			}
		}
		sort.Strings(paths)
	}
	slots := opts.Limits.Notes / (opts.Limits.SearchRounds + 1)
	if slots < 1 {
		slots = 1
	}
	budget := opts.Limits.Chars / (opts.Limits.SearchRounds + 1)
	if explicit && len(paths) > slots {
		return nil, fmt.Errorf("triage: select at most %d inbox notes with these context limits", slots)
	}
	seen := map[string]bool{}
	for _, p := range paths {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if seen[p] {
			return nil, fmt.Errorf("triage: duplicate note %q", p)
		}
		seen[p] = true
		snap, ok := s.snapshots[p]
		if !ok || !v.InInbox(p) {
			return nil, fmt.Errorf("triage: %q is not an exact inbox Markdown path", p)
		}
		if len(s.sources) >= slots {
			break
		}
		remainingSlots := slots - len(s.sources)
		allowance := (budget - s.chars) / remainingSlots
		if !s.admit(snap, true, allowance, "") && explicit {
			return nil, fmt.Errorf("triage: context too small for %q", p)
		}
	}
	if len(s.sources) == 0 && len(paths) > 0 {
		return nil, errors.New("triage: no inbox notes fit the selected context")
	}
	return s, nil
}

func (s *Session) Empty() bool { return len(s.sources) == 0 }

func (s *Session) ContextDescription() string {
	return fmt.Sprintf("inbox Markdown excerpts and related references from up to %d notes (%d characters total), with up to %d local searches", s.opts.Limits.Notes, s.opts.Limits.Chars, s.opts.Limits.SearchRounds)
}

func (s *Session) admit(snap vault.Snapshot, subject bool, allowance int, query string) bool {
	if s.admitted[snap.Path] || len(s.sources) >= s.opts.Limits.Notes {
		return false
	}
	if room := s.opts.Limits.Chars - s.chars; allowance > room {
		allowance = room
	}
	n := snap.Note
	src := &source{ID: fmt.Sprintf("N%d", len(s.sources)+1), Path: n.Path, Title: n.Title, HasTitle: vault.HasTitle(n), Tags: append([]string{}, n.Tags...), Subject: subject, snapshot: snap}
	metadata := utf8.RuneCountInString(src.ID + src.Path + src.Title + strings.Join(src.Tags, ""))
	if !utf8.Valid(snap.Bytes) || !utf8.ValidString(src.Path+src.Title+strings.Join(src.Tags, "")) || metadata+1 > allowance {
		return false
	}
	body := []rune(n.Body)
	size := allowance - metadata
	start := 0
	if query != "" {
		lower := strings.ToLower(n.Body)
		for _, term := range strings.Fields(query) {
			if i := strings.Index(lower, strings.ToLower(term)); i >= 0 {
				start = utf8.RuneCountInString(lower[:i]) - size/3
				break
			}
		}
		if start < 0 {
			start = 0
		}
		if start > len(body) {
			start = len(body)
		}
	}
	end := start + size
	if end > len(body) {
		end = len(body)
	}
	src.Excerpt = string(body[start:end])
	s.sources = append(s.sources, src)
	s.byID[src.ID] = src
	s.admitted[src.Path] = true
	s.chars += metadata + utf8.RuneCountInString(src.Excerpt)
	return true
}

// Run consumes the session once, with a single deadline for all refinement.
func (s *Session) Run(ctx context.Context, approval ai.Approval, call ai.Call, prompt string, model Model) (*Plan, error) {
	if s.Empty() {
		return &Plan{session: s, description: "No inbox notes to review.\n"}, nil
	}
	if s.started {
		return nil, errors.New("triage: session already used")
	}
	s.started = true
	if call.Task != "triage" || call.Profile.Timeout <= 0 {
		return nil, errors.New("triage: a triage profile with timeout is required")
	}
	if model == nil {
		model = ai.Run
	}
	ctx, cancel := context.WithTimeout(ctx, call.Profile.Timeout)
	defer cancel()
	remaining := s.opts.Limits.SearchRounds
	final := false
	queries := map[string]bool{}
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		final = final || len(s.sources) >= s.opts.Limits.Notes || s.chars >= s.opts.Limits.Chars || remaining == 0
		rounds := remaining
		if final {
			rounds = 0
		}
		payload := struct {
			Sources           []*source `json:"sources"`
			SearchesRemaining int       `json:"searches_remaining"`
			MustFinalize      bool      `json:"must_finalize"`
		}{s.sources, rounds, final}
		data, err := json.Marshal(payload)
		if err != nil {
			return nil, err
		}
		req := ai.Request{System: prompt, Text: string(data)}
		if err := ai.ValidateRequest(req); err != nil {
			return nil, err
		}
		result, err := model(ctx, approval, req)
		if err != nil {
			return nil, err
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if err := ai.ValidateTriageReply(result.Triage); err != nil {
			return nil, err
		}
		r := result.Triage
		if r.Action == "propose" {
			return s.plan(r.Proposals)
		}
		if final || remaining == 0 {
			return nil, errors.New("triage: search allowance exhausted")
		}
		key := strings.ToLower(strings.Join(strings.Fields(r.Query), " "))
		if key == "" || queries[key] || strings.IndexFunc(r.Query, unicode.IsControl) >= 0 || strings.IndexFunc(r.Query, func(r rune) bool { return unicode.IsLetter(r) || unicode.IsNumber(r) }) < 0 {
			return nil, errors.New("triage: malformed or repeated search")
		}
		queries[key] = true
		results, err := search.Search(ctx, s.docs, search.Query{Text: r.Query, NoLayoutFallback: s.opts.NoLayoutFallback}, nil, time.Now())
		if err != nil {
			return nil, err
		}
		count := len(s.sources)
		slots := (s.opts.Limits.Notes - count) / remaining
		if slots < 1 {
			slots = 1
		}
		budget := (s.opts.Limits.Chars - s.chars) / remaining
		each := budget / slots
		for _, res := range results {
			if len(s.sources)-count >= slots {
				break
			}
			s.admit(s.snapshots[res.Doc.Path], false, each, r.Query)
		}
		remaining--
		final = len(s.sources) == count
	}
}
