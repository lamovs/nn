// Package app is the shared plumbing every nn command builds on.
package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/lamovs/nn/internal/config"
	"github.com/lamovs/nn/internal/corpus"
	"github.com/lamovs/nn/internal/search"
	"github.com/lamovs/nn/internal/state"
	"github.com/lamovs/nn/internal/vault"
)

type Env struct {
	Cfg   config.Config
	Vault *vault.Vault
	State *state.State
	Now   func() time.Time

	mu          sync.Mutex
	notes       []*vault.Note
	notesLoaded bool
	docs        []*search.Doc
	docsLoaded  bool
}

// Open: a bad config value or unreadable state file is only a warning.
func Open() (*Env, error) {
	cfg, problems, err := config.Load()
	if err != nil {
		return nil, err
	}
	WarnConfig(os.Stderr, problems)
	v, err := vault.Open(cfg)
	if err != nil {
		return nil, err
	}
	st, err := state.Load(cfg.State.HalfLife)
	if err != nil {
		fmt.Fprintf(os.Stderr, "nn: warning: load open history: %v\n", err)
	}
	return &Env{Cfg: cfg, Vault: v, State: st, Now: time.Now}, nil
}

func WarnConfig(w io.Writer, problems []config.Problem) {
	for _, p := range problems {
		switch {
		case p.Fatal, p.Kind == config.KindUnknown, p.Kind == config.KindReserved:
		case p.Kind == config.KindMoved:
			fmt.Fprintf(w, "nn: warning: config: %s\n", p.Error())
		default:
			fmt.Fprintf(w, "nn: warning: config: %s\n", p.Message)
		}
	}
}

func (e *Env) now() time.Time {
	if e.Now != nil {
		return e.Now()
	}
	return time.Now()
}

func (e *Env) Notes(ctx context.Context) ([]*vault.Note, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.notesLoaded {
		return e.notes, nil
	}
	notes, err := e.Vault.LoadAll(ctx)
	if err != nil {
		return nil, err
	}
	e.notes, e.notesLoaded = notes, true
	return e.notes, nil
}

func (e *Env) Docs(ctx context.Context) ([]*search.Doc, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.docsLoaded {
		return e.docs, nil
	}
	docs, err := corpus.Load(ctx, e.Vault)
	if err != nil {
		return nil, err
	}
	e.docs, e.docsLoaded = docs, true
	return e.docs, nil
}

func (e *Env) Resolve(ctx context.Context, ref string) (string, error) {
	rel, err := e.Vault.Resolve(ref)
	if err == nil {
		return rel, nil
	}
	if !errors.Is(err, vault.ErrNotFound) {
		return "", err
	}

	docs, err := e.Docs(ctx)
	if err != nil {
		return "", err
	}
	// A typed nil *state.State in the Ranker interface is not itself nil,
	// so Frecency would still be called and panic.
	var ranker search.Ranker
	if e.State != nil {
		ranker = e.State
	}
	q := search.Query{Text: ref, NoLayoutFallback: !e.Cfg.Search.LayoutFallback}
	results, err := search.Search(ctx, docs, q, ranker, e.now())
	if err != nil {
		return "", err
	}
	if len(results) == 0 {
		return "", vault.ErrNotFound
	}
	return results[0].Doc.Path, nil
}

func (e *Env) RecordOpen(path string) {
	if !e.Cfg.State.TrackOpens {
		return
	}
	e.State.RecordOpen(path, e.now())
	if err := e.State.Save(); err != nil {
		fmt.Fprintf(os.Stderr, "nn: warning: save open history: %v\n", err)
	}
}

func (e *Env) PostSave(ctx context.Context, noteRel, action string) error {
	hook := strings.TrimSpace(e.Cfg.Hooks.PostSave)
	if hook == "" {
		return nil
	}

	hookCtx, cancel := context.WithTimeout(ctx, e.Cfg.Hooks.PostSaveTimeout)
	defer cancel()

	cmd := exec.CommandContext(hookCtx, "sh", "-c", hook)
	cmd.Env = append(os.Environ(),
		"NN_NOTE="+e.Vault.Abs(noteRel),
		"NN_ROOT="+e.Vault.Root,
		"NN_ACTION="+action,
	)
	if err := cmd.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "nn: warning: post_save hook: %v\n", err)
	}
	return nil
}
