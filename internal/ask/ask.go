// Package ask answers a question from bounded, locally retrieved note
// excerpts, without changing the vault.
package ask

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/lamovs/nn/internal/ai"
	"github.com/lamovs/nn/internal/config"
	"github.com/lamovs/nn/internal/corpus"
	"github.com/lamovs/nn/internal/search"
	"github.com/lamovs/nn/internal/vault"
)

// ErrInsufficient accompanies a validated partial answer or missing-data notice.
var ErrInsufficient = errors.New("ask: insufficient evidence")

// Model is the injectable model boundary. A nil Model uses ai.Run.
type Model func(context.Context, ai.Approval, ai.Request) (ai.Result, error)

type Options struct {
	Limits           config.AIContext
	NoLayoutFallback bool
}

type source struct {
	ID      string   `json:"id"`
	Path    string   `json:"path"`
	Title   string   `json:"title"`
	Tags    []string `json:"tags"`
	Excerpt string   `json:"excerpt"`
	spans   []fragment
}

type request struct {
	Question          string   `json:"question"`
	Sources           []source `json:"sources"`
	SearchesRemaining int      `json:"searches_remaining"`
	MustFinalize      bool     `json:"must_finalize"`
}

// Session is used synchronously; Run may be called only once.
type Session struct {
	vault    *vault.Vault
	opts     Options
	question string
	docs     []*search.Doc
	sources  []source
	paths    map[string]bool
	chars    int
	now      time.Time
	started  bool
	bound    *bound
}

// Prepare searches locally before asking permission to send any data.
func Prepare(ctx context.Context, v *vault.Vault, opts Options, question string) (*Session, error) {
	question = strings.TrimSpace(question)
	if question == "" || !utf8.ValidString(question) {
		return nil, errors.New("ask: a nonempty UTF-8 question is required")
	}
	if len(question) > ai.MaxTextBytes/2 {
		return nil, errors.New("ask: question is too long")
	}
	if opts.Limits.Notes < 1 || opts.Limits.Chars < 1 || opts.Limits.SearchRounds < 0 || opts.Limits.SearchRounds > 3 {
		return nil, errors.New("ask: invalid context limits")
	}
	docs, err := corpus.Load(ctx, v)
	if err != nil {
		return nil, err
	}
	s := &Session{vault: v, opts: opts, question: question, paths: map[string]bool{}, sources: []source{}, now: time.Now()}
	for _, doc := range docs {
		if strings.HasSuffix(strings.ToLower(doc.Path), ".md") && !SummaryNote(doc.Via) {
			s.docs = append(s.docs, doc)
		}
	}
	if _, err := s.retrieve(ctx, question, opts.Limits.SearchRounds+1); err != nil {
		return nil, err
	}
	if _, err := s.request(opts.Limits.SearchRounds, false); err != nil {
		return nil, err
	}
	return s, nil
}

// LocalAnswer: an insufficient-data notice when no model call would help.
func (s *Session) LocalAnswer() (string, bool) {
	if len(s.sources) == 0 && s.opts.Limits.SearchRounds == 0 {
		return "Not enough data: no matching note excerpts were found.\n", true
	}
	return "", false
}

// ContextDescription covers the entire invocation, follow-ups included.
func (s *Session) ContextDescription() string {
	return fmt.Sprintf("the question and the paths, titles, tags and excerpts of up to %d notes (%d characters total), including up to %d additional local searches", s.opts.Limits.Notes, s.opts.Limits.Chars, s.opts.Limits.SearchRounds)
}

// Run never writes output itself; one timeout covers every model call.
func (s *Session) Run(ctx context.Context, approval ai.Approval, call ai.Call, prompt string, model Model) (string, error) {
	if s.started {
		return "", errors.New("ask: session already used")
	}
	s.started = true
	if text, ok := s.LocalAnswer(); ok {
		return text, ErrInsufficient
	}
	if call.Task != "ask" || call.Profile.Timeout <= 0 {
		return "", errors.New("ask: an ask profile with a timeout is required")
	}
	if model == nil {
		model = ai.Run
	}
	ctx, cancel := context.WithTimeout(ctx, call.Profile.Timeout)
	defer cancel()
	remaining := s.opts.Limits.SearchRounds
	queries := map[string]bool{queryKey(s.question): true}
	finalize := false
	for {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		finalize = finalize || !s.hasRoom()
		text, err := s.request(remaining, finalize)
		if err != nil {
			return "", err
		}
		req := ai.Request{System: prompt, Text: text}
		if err := ai.ValidateRequest(req); err != nil {
			return "", err
		}
		result, err := model(ctx, approval, req)
		if err != nil {
			return "", err
		}
		if err := ctx.Err(); err != nil {
			return "", err
		}
		if err := validateReply(result.Ask); err != nil {
			return "", err
		}
		reply := result.Ask
		if reply.Action != "search" {
			return s.render(reply)
		}
		if remaining == 0 || finalize {
			return "", errors.New("ask: model requested search after the search allowance ended")
		}
		query := strings.TrimSpace(reply.Query)
		key := queryKey(query)
		if key == "" || len(terms(query)) == 0 || queries[key] || strings.IndexFunc(query, unicode.IsControl) >= 0 {
			return "", errors.New("ask: model requested a malformed or repeated search")
		}
		queries[key] = true
		progress, err := s.retrieve(ctx, query, remaining)
		if err != nil {
			return "", err
		}
		remaining--
		finalize = !progress
	}
}

func (s *Session) request(remaining int, finalize bool) (string, error) {
	if finalize {
		remaining = 0
	}
	data, err := json.Marshal(request{s.question, s.sources, remaining, finalize || remaining == 0})
	if err != nil {
		return "", err
	}
	if len(data) > ai.MaxTextBytes {
		return "", errors.New("ask: question and context exceed the AI request byte limit")
	}
	return string(data), nil
}

func queryKey(s string) string {
	words := terms(s)
	sort.Strings(words)
	return strings.Join(words, " ")
}
