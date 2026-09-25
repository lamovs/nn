// Package digest summarizes a selected set of notes with one model call.
package digest

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/lamovs/nn/internal/ai"
	"github.com/lamovs/nn/internal/search"
	"github.com/lamovs/nn/internal/vault"
)

const (
	MaxNotes = 64
	MinChars = 1000
	MaxChars = 32000
)

const (
	maxTopicRunes = 200
	maxTags       = 16
	maxTagRunes   = 64
)

type Selection struct {
	Topic        string // "" = none; <= 200 runes
	Tags         []string
	Since, Until time.Time // Until is exclusive; zero = none
	Inbox        bool
	Here         *search.Here
	// Paths: nil means no list, a non-nil empty slice selects nothing.
	Paths []string
}

type Options struct {
	Notes, Chars     int // effective, already clamped by the CLI
	NoLayoutFallback bool
	AllowSecret      bool
	Now              time.Time
}

// Report: BeyondLimit = Matched - Included - SkippedSecret.
type Report struct {
	Matched, Included, BeyondLimit, SkippedSecret, SkippedNonNote, Truncated int
	Layout                                                                   bool
}

// Model is the injectable model boundary; nil uses ai.Run.
type Model func(context.Context, ai.Approval, ai.Request) (ai.Result, error)

type source struct {
	ID        string   `json:"id"`
	Path      string   `json:"path"`
	Title     string   `json:"title"`
	Date      string   `json:"date"`
	Tags      []string `json:"tags"`
	Excerpt   string   `json:"excerpt"`
	Truncated bool     `json:"truncated"`
	date      time.Time
}

type request struct {
	Selection    string   `json:"selection"`
	Sources      []source `json:"sources"`
	OmittedNotes int      `json:"omitted_notes"`
}

// Session.Run may be called only once.
type Session struct {
	vault   *vault.Vault
	sel     Selection
	opts    Options
	report  Report
	sources []source // chronological; sources[i].ID == "S<i+1>"
	byID    map[string]int
	chars   int
	text    string
	started bool
}

// Prepare matching nothing gives an Empty session, not an error.
func Prepare(ctx context.Context, v *vault.Vault, docs []*search.Doc, sel Selection, opts Options) (*Session, error) {
	if v == nil {
		return nil, errors.New("no vault")
	}
	sel, err := checkSelection(sel)
	if err != nil {
		return nil, err
	}
	if opts.Notes < 1 || opts.Notes > MaxNotes || opts.Chars < MinChars || opts.Chars > MaxChars {
		return nil, fmt.Errorf("invalid digest limits: notes must be 1..%d and chars %d..%d", MaxNotes, MinChars, MaxChars)
	}
	s := &Session{vault: v, sel: sel, opts: opts, byID: map[string]int{}}
	candidates, err := s.selectNotes(ctx, docs)
	if err != nil {
		return nil, err
	}
	if s.report.Matched == 0 {
		return s, nil
	}
	if err := s.admit(ctx, candidates); err != nil {
		return nil, err
	}
	data, err := json.Marshal(request{Selection: sel.describe(), Sources: s.sources, OmittedNotes: s.report.BeyondLimit + s.report.SkippedSecret})
	if err != nil {
		return nil, err
	}
	s.text = string(data)
	return s, nil
}

func checkSelection(sel Selection) (Selection, error) {
	sel.Topic = strings.TrimSpace(sel.Topic)
	if !utf8.ValidString(sel.Topic) || utf8.RuneCountInString(sel.Topic) > maxTopicRunes {
		return sel, fmt.Errorf("the topic must be valid UTF-8 of at most %d characters", maxTopicRunes)
	}
	if len(sel.Tags) > maxTags {
		return sel, fmt.Errorf("at most %d tags", maxTags)
	}
	for _, tag := range sel.Tags {
		if strings.TrimSpace(tag) == "" || !utf8.ValidString(tag) || utf8.RuneCountInString(tag) > maxTagRunes {
			return sel, fmt.Errorf("a tag must be nonempty valid UTF-8 of at most %d characters", maxTagRunes)
		}
	}
	if !sel.Since.IsZero() && !sel.Until.IsZero() && !sel.Until.After(sel.Since) {
		return sel, errors.New("the end of the period is not after its start")
	}
	if sel.Topic != "" && sel.Paths != nil {
		return sel, errors.New("a topic and a list of note paths cannot be combined")
	}
	if sel.Topic == "" && len(sel.Tags) == 0 && sel.Since.IsZero() && sel.Until.IsZero() && !sel.Inbox && sel.Here == nil && sel.Paths == nil {
		return sel, errors.New("a topic, filter or list of note paths is required")
	}
	return sel, nil
}

func (s *Session) Report() Report { return s.report }

func (s *Session) Empty() bool { return s.report.Matched == 0 }

func (s *Session) ContextDescription() string {
	return fmt.Sprintf("the selection and the paths, titles, dates, tags and excerpts of %s (%d characters)", count(len(s.sources), "selected note"), s.chars)
}

func (s *Session) Request(prompt string) ai.Request {
	return ai.Request{System: prompt, Text: s.text}
}

func (s *Session) Run(ctx context.Context, approval ai.Approval, call ai.Call, prompt string, model Model) (*Digest, error) {
	if s.started {
		return nil, errors.New("digest session already used")
	}
	s.started = true
	if len(s.sources) == 0 {
		return nil, errors.New("no notes to digest")
	}
	if call.Task != "digest" || call.Profile.Timeout <= 0 {
		return nil, errors.New("a digest profile with a timeout is required")
	}
	if model == nil {
		model = ai.Run
	}
	ctx, cancel := context.WithTimeout(ctx, call.Profile.Timeout)
	defer cancel()
	req := s.Request(prompt)
	if err := ai.ValidateRequest(req); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	result, err := model(ctx, approval, req)
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	points, err := s.validate(result.Digest)
	if err != nil {
		return nil, err
	}
	return &Digest{session: s, points: points}, nil
}

func count(n int, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	return fmt.Sprintf("%d %ss", n, noun)
}
