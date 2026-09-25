package triage

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"unicode"

	"github.com/lamovs/nn/internal/ai"
	"github.com/lamovs/nn/internal/links"
	"github.com/lamovs/nn/internal/vault"
)

type Action struct{ ID, NoteID, Path, Kind, Summary string }
type operation struct {
	action Action
	value  string
	target *source
}
type Plan struct {
	session     *Session
	ops         []operation
	description string
}
type selectedNote struct {
	source     *source
	enrichment vault.Enrichment
	actions    []Action
	targets    []*source
}
type Selection struct {
	plan    *Plan
	notes   []selectedNote
	used    bool
	persist func(string, *journal) error
}

func (s *Session) plan(proposals []ai.TriageProposal) (*Plan, error) {
	p := &Plan{session: s}
	var description strings.Builder
	description.WriteString("Included inbox notes (Markdown only):\n")
	for _, src := range s.sources {
		if src.Subject {
			fmt.Fprintf(&description, "  %s %q\n", src.ID, src.Path)
		}
	}
	seen := map[string]bool{}
	for _, proposal := range proposals {
		src := s.byID[proposal.NoteID]
		if src == nil || !src.Subject || seen[proposal.NoteID] {
			return nil, errors.New("triage: proposal names an unknown, repeated, or non-subject source")
		}
		seen[proposal.NoteID] = true
		fmt.Fprintf(&description, "\n%s %q\nTopic: %s\nReason: %s\n", src.ID, src.Path, display(proposal.Topic), display(proposal.Reason))
		add := func(kind, value, summary string, target *source) {
			p.ops = append(p.ops, operation{Action{ID: fmt.Sprint(len(p.ops) + 1), NoteID: src.ID, Path: src.Path, Kind: kind, Summary: summary}, value, target})
		}
		if proposal.Title != "" && !vault.HasTitle(src.snapshot.Note) {
			if safeTitle(proposal.Title) {
				add("title", proposal.Title, "Add title: "+proposal.Title, nil)
			} else {
				description.WriteString("Omitted title: Markdown structure is not allowed in a title.\n")
			}
		}
		existing := map[string]bool{}
		for _, tag := range vault.NormalizeKeywords(src.snapshot.Note.Tags) {
			existing[tag] = true
		}
		for _, tag := range vault.NormalizeKeywords(proposal.Tags) {
			if !existing[tag] {
				existing[tag] = true
				add("tag", tag, "Add tag: #"+tag, nil)
			}
		}
		linkIDs := map[string]bool{}
		for _, id := range proposal.Links {
			target := s.byID[id]
			if target == nil || target == src || linkIDs[id] {
				return nil, errors.New("triage: link names an unknown, self, or repeated source")
			}
			linkIDs[id] = true
			rendered, err := s.link(src, target)
			if err != nil {
				fmt.Fprintf(&description, "Omitted link to %q: %s\n", target.Path, display(err.Error()))
				continue
			}
			found := false
			for _, l := range links.Parse(src.snapshot.Note.Body, src.snapshot.Note.BodyStartLine) {
				if resolved, ok := s.graph.Resolve(src.Path, l.Target); ok && resolved == target.Path {
					found = true
					break
				}
			}
			if !found {
				add("link", rendered, "Add link: "+rendered, target)
			}
		}
	}
	if len(p.ops) == 0 {
		description.WriteString("\nNo applicable additions.\n")
	}
	p.description = description.String()
	return p, nil
}

func safeTitle(title string) bool {
	return strings.TrimSpace(title) != "" && !strings.Contains(strings.ToLower(title), "://") && !strings.ContainsAny(title, "\r\n[]<>`*_#!|\\") && strings.IndexFunc(title, func(r rune) bool { return unicode.IsControl(r) || unicode.Is(unicode.Cf, r) }) < 0
}
func display(s string) string {
	return strings.Map(func(r rune) rune {
		if (unicode.IsControl(r) && r != '\n' && r != '\t') || unicode.Is(unicode.Cf, r) {
			return '\ufffd'
		}
		return r
	}, s)
}
func safeWikiPath(p string) bool {
	return strings.TrimSpace(p) == p && !strings.ContainsAny(p, "[]#^|`\\\r\n") && strings.IndexFunc(p, func(r rune) bool { return unicode.IsControl(r) || unicode.Is(unicode.Cf, r) }) < 0
}
func (s *Session) link(from, target *source) (string, error) {
	if !safeWikiPath(target.Path) {
		return "", errors.New("path cannot be represented safely in wiki syntax")
	}
	count := 0
	for _, p := range s.notePaths {
		if strings.EqualFold(p, target.Path) {
			count++
		}
	}
	if count != 1 {
		return "", errors.New("case-ambiguous target")
	}
	// Relative qualification prevents basename/alias guessing.
	rel, err := filepath.Rel(filepath.Dir(filepath.FromSlash(from.Path)), filepath.FromSlash(target.Path))
	if err != nil {
		return "", err
	}
	rel = filepath.ToSlash(rel)
	if !strings.HasPrefix(rel, "../") {
		rel = "./" + rel
	}
	if !safeWikiPath(rel) {
		return "", errors.New("unsafe qualified path")
	}
	rendered := "[[" + rel + "]]"
	parsed := links.Parse(rendered, 1)
	if len(parsed) != 1 || parsed[0].Embed || parsed[0].Target != rel || parsed[0].Heading != "" || parsed[0].Block != "" || parsed[0].Display != "" {
		return "", errors.New("qualified link markup does not preserve its target")
	}
	resolved, ok := s.graph.Resolve(from.Path, parsed[0].Target)
	if !ok || resolved != target.Path {
		return "", errors.New("qualified link does not resolve to the intended target")
	}
	return rendered, nil
}
func (p *Plan) Actions() []Action {
	result := make([]Action, 0, len(p.ops))
	for _, op := range p.ops {
		result = append(result, op.action)
	}
	return result
}
func (p *Plan) Description() string { return p.description }

func (p *Plan) Select(ids []string) (*Selection, error) {
	wanted := map[string]bool{}
	for _, id := range ids {
		if wanted[id] {
			return nil, fmt.Errorf("triage: repeated action %q", id)
		}
		wanted[id] = true
	}
	sel := &Selection{plan: p, persist: saveJournal}
	indexes := map[string]int{}
	for _, op := range p.ops {
		if !wanted[op.action.ID] {
			continue
		}
		delete(wanted, op.action.ID)
		src := p.session.byID[op.action.NoteID]
		i, ok := indexes[src.ID]
		if !ok {
			i = len(sel.notes)
			indexes[src.ID] = i
			sel.notes = append(sel.notes, selectedNote{source: src})
		}
		n := &sel.notes[i]
		n.actions = append(n.actions, op.action)
		switch op.action.Kind {
		case "title":
			n.enrichment.Title = op.value
			n.enrichment.AllowTitle = true
		case "tag":
			n.enrichment.Tags = append(n.enrichment.Tags, op.value)
		case "link":
			if n.enrichment.Body != "" {
				n.enrichment.Body += "\n"
			}
			n.enrichment.Body += op.value
			n.targets = append(n.targets, op.target)
		}
	}
	if len(wanted) > 0 {
		return nil, errors.New("triage: unknown action selection")
	}
	sort.SliceStable(sel.notes, func(i, j int) bool { return sel.notes[i].source.Path < sel.notes[j].source.Path })
	return sel, nil
}

func (s *Selection) check(ctx context.Context, expected map[string]vault.Snapshot) error {
	v := s.plan.session.v
	for _, n := range s.notes {
		snap := n.source.snapshot
		if latest, ok := expected[snap.Path]; ok {
			snap = latest
		}
		if err := v.CheckSnapshot(snap); err != nil {
			return fmt.Errorf("triage: source %q: %w", snap.Path, err)
		}
		if err := s.checkTargets(ctx, n, expected); err != nil {
			return err
		}
	}
	return nil
}
func (s *Selection) checkTargets(ctx context.Context, n selectedNote, expected map[string]vault.Snapshot) error {
	v := s.plan.session.v
	for _, target := range n.targets {
		snap := target.snapshot
		if latest, ok := expected[snap.Path]; ok {
			snap = latest
		}
		if err := v.CheckSnapshot(snap); err != nil {
			return fmt.Errorf("triage: target %q: %w", snap.Path, err)
		}
		// A newly created case-colliding note must not change link identity after preview.
		count := 0
		if err := v.Walk(ctx, func(p string) error {
			if strings.EqualFold(p, snap.Path) {
				count++
			}
			return nil
		}); err != nil {
			return err
		}
		if count != 1 {
			return fmt.Errorf("triage: target %q: %w", snap.Path, vault.ErrSnapshotChanged)
		}
	}
	return nil
}

// Preview: escaped controls keep the diff output safe.
func (s *Selection) Preview() (string, error) {
	if err := s.check(context.Background(), nil); err != nil {
		return "", err
	}
	var out strings.Builder
	out.WriteString("Diff display: controls and backslashes are escaped as \\u{hex}.\n")
	for _, n := range s.notes {
		before := n.source.snapshot.Bytes
		after, err := vault.PreviewEnrichment(n.source.snapshot, n.enrichment)
		if err != nil {
			return "", err
		}
		if string(before) == string(after) {
			continue
		}
		fmt.Fprintf(&out, "--- %q\n+++ %q\n", n.source.Path, n.source.Path)
		oldLines := diffLines(string(before))
		newLines := diffLines(string(after))
		fmt.Fprintf(&out, "@@ -1,%d +1,%d @@\n", len(oldLines), len(newLines))
		writeDiff(&out, "-", string(before))
		writeDiff(&out, "+", string(after))
	}
	return out.String(), nil
}
func diffLines(text string) []string {
	if text == "" {
		return nil
	}
	return strings.Split(strings.TrimSuffix(text, "\n"), "\n")
}
func writeDiff(out *strings.Builder, prefix, text string) {
	for _, line := range diffLines(text) {
		out.WriteString(prefix)
		out.WriteString(escapeControls(line))
		out.WriteByte('\n')
	}
	if text != "" && !strings.HasSuffix(text, "\n") {
		out.WriteString("\\ No newline at end of file\n")
	}
}
func escapeControls(text string) string {
	var b strings.Builder
	for _, r := range text {
		if r == '\\' || unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			fmt.Fprintf(&b, "\\u{%04x}", r)
		} else {
			b.WriteRune(r)
		}
	}
	return b.String()
}
