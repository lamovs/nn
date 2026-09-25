package triage

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/lamovs/nn/internal/ai"
	"github.com/lamovs/nn/internal/config"
	"github.com/lamovs/nn/internal/links"
	"github.com/lamovs/nn/internal/vault"
)

func fixture(t *testing.T, files map[string]string) *vault.Vault {
	t.Helper()
	root := t.TempDir()
	t.Setenv("XDG_DATA_HOME", filepath.Join(t.TempDir(), "data"))
	for p, b := range files {
		name := filepath.Join(root, filepath.FromSlash(p))
		if err := os.MkdirAll(filepath.Dir(name), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(name, []byte(b), 0600); err != nil {
			t.Fatal(err)
		}
	}
	return &vault.Vault{Root: root, Inbox: "inbox"}
}
func prepare(t *testing.T, v *vault.Vault) *Session {
	t.Helper()
	s, e := Prepare(context.Background(), v, Options{Limits: config.AIContext{Notes: 8, Chars: 12000, SearchRounds: 1}})
	if e != nil {
		t.Fatal(e)
	}
	return s
}
func call() ai.Call { return ai.Call{Task: "triage", Profile: config.Profile{Timeout: time.Second}} }
func proposal(id string) ai.TriageProposal {
	return ai.TriageProposal{NoteID: id, Tags: []string{}, Links: []string{}, Topic: "Topic", Reason: "Useful addition"}
}
func propose(t *testing.T, s *Session, ps ...ai.TriageProposal) *Plan {
	t.Helper()
	p, e := s.Run(context.Background(), ai.Approval{}, call(), "prompt", func(context.Context, ai.Approval, ai.Request) (ai.Result, error) {
		return ai.Result{Triage: &ai.TriageReply{Action: "propose", Proposals: ps}}, nil
	})
	if e != nil {
		t.Fatal(e)
	}
	return p
}
func all(t *testing.T, p *Plan) *Selection {
	t.Helper()
	var ids []string
	for _, a := range p.Actions() {
		ids = append(ids, a.ID)
	}
	s, e := p.Select(ids)
	if e != nil {
		t.Fatal(e)
	}
	return s
}

func TestContextSnapshotsAndSearchBudget(t *testing.T) {
	v := fixture(t, map[string]string{"inbox/a.md": strings.Repeat("source text ", 400), "reference.md": "needle details"})
	s := prepare(t, v)
	calls := 0
	_, err := s.Run(context.Background(), ai.Approval{}, call(), "prompt", func(_ context.Context, _ ai.Approval, req ai.Request) (ai.Result, error) {
		calls++
		var r struct {
			Sources      []source `json:"sources"`
			MustFinalize bool     `json:"must_finalize"`
		}
		if err := json.Unmarshal([]byte(req.Text), &r); err != nil {
			t.Fatal(err)
		}
		chars := 0
		for _, src := range r.Sources {
			chars += utf8.RuneCountInString(src.ID + src.Path + src.Title + strings.Join(src.Tags, "") + src.Excerpt)
		}
		if chars > 12000 || len(r.Sources) > 8 {
			t.Fatal("budget exceeded")
		}
		if calls == 1 {
			if len(r.Sources) != 1 || !r.Sources[0].Subject {
				t.Fatal("bad initial context")
			}
			if err := os.WriteFile(v.Abs("inbox/a.md"), []byte("later edit"), 0600); err != nil {
				t.Fatal(err)
			}
			return ai.Result{Triage: &ai.TriageReply{Action: "search", Query: "needle", Proposals: []ai.TriageProposal{}}}, nil
		}
		if !r.MustFinalize || len(r.Sources) != 2 || r.Sources[1].Subject {
			t.Fatal("reference admission/finalization")
		}
		if strings.Contains(r.Sources[0].Excerpt, "later edit") {
			t.Fatal("snapshot changed")
		}
		return ai.Result{Triage: &ai.TriageReply{Action: "propose", Proposals: []ai.TriageProposal{}}}, nil
	})
	if err != nil || calls != 2 {
		t.Fatalf("calls %d: %v", calls, err)
	}
}
func TestRejectUnseenReferenceAndSearchAfterFinal(t *testing.T) {
	for _, kind := range []string{"unknown", "self", "reference", "search"} {
		t.Run(kind, func(t *testing.T) {
			v := fixture(t, map[string]string{"inbox/a.md": "body", "other.md": "outside"})
			s := prepare(t, v)
			p := proposal("N1")
			p.Links = []string{"N999"}
			if kind == "self" {
				p.Links = []string{"N1"}
			}
			if kind == "reference" {
				p.NoteID = "N2"
				p.Links = []string{}
			}
			_, err := s.Run(context.Background(), ai.Approval{}, call(), "prompt", func(context.Context, ai.Approval, ai.Request) (ai.Result, error) {
				if kind == "search" {
					return ai.Result{Triage: &ai.TriageReply{Action: "search", Query: "doesnotexist", Proposals: []ai.TriageProposal{}}}, nil
				}
				return ai.Result{Triage: &ai.TriageReply{Action: "propose", Proposals: []ai.TriageProposal{p}}}, nil
			})
			if err == nil {
				t.Fatal("accepted invalid model reply")
			}
		})
	}
}
func TestSelectionPreviewApplyPreservesBytesAndBackup(t *testing.T) {
	original := "---\ncustom: exact\n---\nOriginal body.\n"
	v := fixture(t, map[string]string{"inbox/a.md": original})
	s := prepare(t, v)
	p1 := proposal("N1")
	p1.Title = "Useful title"
	p1.Tags = []string{"Go", "go", "two words"}
	p := propose(t, s, p1)
	if len(p.Actions()) != 3 {
		t.Fatalf("actions: %+v", p.Actions())
	}
	sel, err := p.Select([]string{"2"})
	if err != nil {
		t.Fatal(err)
	}
	preview, err := sel.Preview()
	if err != nil || !strings.Contains(preview, "+#go") || strings.Contains(preview, "+Useful title") {
		t.Fatalf("preview: %s %v", preview, err)
	}
	report, err := sel.Apply(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(v.Abs("inbox/a.md"))
	if !strings.HasPrefix(string(got), original) || !strings.Contains(string(got), "#go") || strings.Contains(string(got), "Useful title") {
		t.Fatalf("unexpected note %q", got)
	}
	if report.Notes[0].Status != StatusApplied {
		t.Fatal(report)
	}
	backup, _ := os.ReadFile(filepath.Join(report.RecoveryDir, "0001.before"))
	if string(backup) != original {
		t.Fatal("backup differs")
	}
	for _, p := range []string{report.RecoveryDir, filepath.Join(report.RecoveryDir, "0001.before"), filepath.Join(report.RecoveryDir, "manifest.json")} {
		info, e := os.Stat(p)
		if e != nil || info.Mode().Perm()&0077 != 0 {
			t.Fatalf("insecure recovery %v %v", info, e)
		}
	}
	if _, err = sel.Apply(context.Background()); err == nil {
		t.Fatal("selection reused")
	}
	fresh := prepare(t, v)
	p2 := proposal("N1")
	p2.Tags = []string{"go"}
	if len(propose(t, fresh, p2).Actions()) != 0 {
		t.Fatal("existing tag offered again")
	}
}
func TestManualTitlesAndUnsafeTitle(t *testing.T) {
	for _, body := range []string{"# Manual\nbody", "---\naliases: [Manual]\n---\nbody", "---\ntitle: Manual\n---\nbody"} {
		v := fixture(t, map[string]string{"inbox/a.md": body})
		s := prepare(t, v)
		p := proposal("N1")
		p.Title = "Replacement"
		p.Tags = []string{"safe"}
		result := propose(t, s, p)
		if len(result.Actions()) != 1 || result.Actions()[0].Kind != "tag" {
			t.Fatal("manual title replaced")
		}
	}
	v := fixture(t, map[string]string{"inbox/a.md": "body"})
	p := proposal("N1")
	p.Title = "[smuggled](link)"
	p.Tags = []string{"safe"}
	result := propose(t, prepare(t, v), p)
	if len(result.Actions()) != 1 || !strings.Contains(result.Description(), "Omitted title") {
		t.Fatal("unsafe title")
	}
}

func TestContextDisclosesManualTitlePresence(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		hasTitle   bool
	}{
		{"filename fallback", "Unheaded body text.\n", false},
		{"heading", "# Manual heading\nbody\n", true},
		{"alias", "---\naliases: [Manual alias]\n---\nbody\n", true},
		{"frontmatter", "---\ntitle: Manual title\n---\nbody\n", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			v := fixture(t, map[string]string{"inbox/display-fallback.md": tc.body})
			s := prepare(t, v)
			p, err := s.Run(context.Background(), ai.Approval{}, call(), "prompt", func(_ context.Context, _ ai.Approval, req ai.Request) (ai.Result, error) {
				var envelope struct {
					Sources []map[string]json.RawMessage `json:"sources"`
				}
				if err := json.Unmarshal([]byte(req.Text), &envelope); err != nil || len(envelope.Sources) != 1 {
					t.Fatalf("source envelope=%s err=%v", req.Text, err)
				}
				fields := envelope.Sources[0]
				want := "false"
				if tc.hasTitle {
					want = "true"
				}
				if string(fields["has_title"]) != want || string(fields["subject"]) != "true" {
					t.Fatalf("manual title or subject marker missing: %s", req.Text)
				}
				var title string
				if err := json.Unmarshal(fields["title"], &title); err != nil || title == "" {
					t.Fatal("display title must be preserved, including filename fallback")
				}
				proposal := proposal("N1")
				proposal.Title = "Suggested title"
				return ai.Result{Triage: &ai.TriageReply{Action: "propose", Proposals: []ai.TriageProposal{proposal}}}, nil
			})
			if err != nil {
				t.Fatal(err)
			}
			if tc.hasTitle && len(p.Actions()) != 0 {
				t.Fatal("manual title replacement was offered")
			}
			if !tc.hasTitle && (len(p.Actions()) != 1 || p.Actions()[0].Kind != "title") {
				t.Fatal("missing title addition was suppressed by its display label")
			}
		})
	}
}
func mutual(t *testing.T, v *vault.Vault) *Plan {
	t.Helper()
	s := prepare(t, v)
	a, b := proposal("N1"), proposal("N2")
	a.Links = []string{"N2"}
	b.Links = []string{"N1"}
	return propose(t, s, a, b)
}
func TestMutualLinksAndStaleDependencies(t *testing.T) {
	t.Run("same-run", func(t *testing.T) {
		v := fixture(t, map[string]string{"inbox/a.md": "A", "inbox/b.md": "B"})
		r, e := all(t, mutual(t, v)).Apply(context.Background())
		if e != nil {
			t.Fatal(e)
		}
		for _, n := range r.Notes {
			if n.Status != StatusApplied {
				t.Fatal(r)
			}
		}
	})
	t.Run("stale-source", func(t *testing.T) {
		v := fixture(t, map[string]string{"inbox/a.md": "A", "inbox/b.md": "B"})
		sel := all(t, mutual(t, v))
		os.WriteFile(v.Abs("inbox/a.md"), []byte("external"), 0600)
		if _, e := sel.Preview(); !errors.Is(e, vault.ErrSnapshotChanged) {
			t.Fatal(e)
		}
		if _, e := sel.Apply(context.Background()); !errors.Is(e, vault.ErrSnapshotChanged) {
			t.Fatal(e)
		}
		b, _ := os.ReadFile(v.Abs("inbox/b.md"))
		if string(b) != "B" {
			t.Fatal("partial preflight mutation")
		}
	})
	t.Run("stale-target", func(t *testing.T) {
		v := fixture(t, map[string]string{"inbox/a.md": "A", "inbox/b.md": "B"})
		p := mutual(t, v)
		sel, e := p.Select([]string{"1"})
		if e != nil {
			t.Fatal(e)
		}
		os.WriteFile(v.Abs("inbox/b.md"), []byte("external"), 0600)
		if _, e := sel.Preview(); !errors.Is(e, vault.ErrSnapshotChanged) {
			t.Fatal(e)
		}
		if _, e := sel.Apply(context.Background()); !errors.Is(e, vault.ErrSnapshotChanged) {
			t.Fatal(e)
		}
	})
}
func TestUnsafeWikiTargetOmitted(t *testing.T) {
	v := fixture(t, map[string]string{"inbox/a.md": "A", "inbox/b#bad.md": "B"})
	s := prepare(t, v)
	p := proposal("N1")
	p.Links = []string{"N2"}
	p.Tags = []string{"valid"}
	plan := propose(t, s, p)
	if len(plan.Actions()) != 1 || !strings.Contains(plan.Description(), "Omitted link") {
		t.Fatal("unsafe link accepted")
	}
}

func TestWikiMarkupKeepsTargetIdentity(t *testing.T) {
	t.Run("inline code collision", func(t *testing.T) {
		original := "Manual [[./b`x`.md]] remains unchanged.\n"
		v := fixture(t, map[string]string{
			"inbox/a.md":    original,
			"inbox/b`x`.md": "Intended target.\n",
			"inbox/b   .md": "Different target after code masking.\n",
		})
		s := prepare(t, v)
		var from, target *source
		for _, src := range s.sources {
			switch src.Path {
			case "inbox/a.md":
				from = src
			case "inbox/b`x`.md":
				target = src
			}
		}
		if from == nil || target == nil {
			t.Fatal("missing test sources")
		}
		parsed := links.Parse("[[./b`x`.md]]", 1)
		if len(parsed) != 1 {
			t.Fatal("fixture no longer exercises inline code masking")
		}
		if got, ok := s.graph.Resolve(from.Path, parsed[0].Target); !ok || got != "inbox/b   .md" {
			t.Fatalf("fixture collision=%q ok=%v", got, ok)
		}
		p := proposal(from.ID)
		p.Links, p.Tags = []string{target.ID}, []string{"safe"}
		plan := propose(t, s, p)
		if actions := plan.Actions(); len(actions) != 1 || actions[0].Kind != "tag" || !strings.Contains(plan.Description(), "Omitted link") {
			t.Fatalf("unsafe link offered: %+v", actions)
		}
		if _, err := all(t, plan).Apply(context.Background()); err != nil {
			t.Fatal(err)
		}
		body, err := os.ReadFile(v.Abs("inbox/a.md"))
		if err != nil || !strings.HasPrefix(string(body), original) {
			t.Fatalf("existing manual link changed: %q err=%v", body, err)
		}
	})
	t.Run("punctuation and qualified duplicate basename", func(t *testing.T) {
		name := "(alpha) & beta +_-.md"
		targetPath := "reference/" + name
		v := fixture(t, map[string]string{
			"inbox/deep/a.md": "Source.\n",
			targetPath:        "Intended target.\n",
			"other/" + name:   "Different note with the same basename.\n",
		})
		s := prepare(t, v)
		if !s.admit(s.snapshots[targetPath], false, 1000, "") {
			t.Fatal("reference not admitted")
		}
		from, target := s.sources[0], s.sources[1]
		rendered, err := s.link(from, target)
		if err != nil {
			t.Fatal(err)
		}
		parsed := links.Parse(rendered, 1)
		if len(parsed) != 1 || parsed[0].Embed {
			t.Fatalf("rendered link=%q parsed=%+v", rendered, parsed)
		}
		if got, ok := s.graph.Resolve(from.Path, parsed[0].Target); !ok || got != targetPath {
			t.Fatalf("wrong roundtrip target=%q ok=%v", got, ok)
		}
		p := proposal(from.ID)
		p.Links = []string{target.ID}
		if actions := propose(t, s, p).Actions(); len(actions) != 1 || actions[0].Kind != "link" {
			t.Fatalf("safe qualified link was lost: %+v", actions)
		}
	})
}
func TestBackupFailureAndReceiptFailure(t *testing.T) {
	t.Run("backup", func(t *testing.T) {
		v := fixture(t, map[string]string{"inbox/a.md": "A"})
		p := proposal("N1")
		p.Tags = []string{"new"}
		sel := all(t, propose(t, prepare(t, v), p))
		block := filepath.Join(t.TempDir(), "file")
		os.WriteFile(block, []byte("x"), 0600)
		t.Setenv("XDG_DATA_HOME", block)
		if _, e := sel.Apply(context.Background()); e == nil {
			t.Fatal("expected backup error")
		}
		b, _ := os.ReadFile(v.Abs("inbox/a.md"))
		if string(b) != "A" {
			t.Fatal("wrote before backup")
		}
	})
	t.Run("receipt", func(t *testing.T) {
		v := fixture(t, map[string]string{"inbox/a.md": "A", "inbox/b.md": "B"})
		sel := all(t, mutual(t, v))
		n := 0
		sel.persist = func(dir string, j *journal) error {
			n++
			if n == 2 {
				return errors.New("receipt full")
			}
			return saveJournal(dir, j)
		}
		r, e := sel.Apply(context.Background())
		if e == nil || r.Notes[0].Status != StatusUncertain || !r.Notes[0].Changed || r.Notes[1].Status != StatusUnattempted {
			t.Fatalf("%+v %v", r, e)
		}
		a, _ := os.ReadFile(v.Abs("inbox/a.md"))
		b, _ := os.ReadFile(v.Abs("inbox/b.md"))
		if !strings.Contains(string(a), "[[") || string(b) != "B" {
			t.Fatal("partial write wrong")
		}
	})
	t.Run("later-conflict", func(t *testing.T) {
		v := fixture(t, map[string]string{"inbox/a.md": "A", "inbox/b.md": "B"})
		sel := all(t, mutual(t, v))
		n := 0
		sel.persist = func(dir string, j *journal) error {
			n++
			err := saveJournal(dir, j)
			if n == 2 {
				os.WriteFile(v.Abs("inbox/b.md"), []byte("external"), 0600)
			}
			return err
		}
		r, e := sel.Apply(context.Background())
		if !errors.Is(e, vault.ErrSnapshotChanged) || r.Notes[0].Status != StatusApplied || r.Notes[1].Status != StatusConflict {
			t.Fatalf("%+v %v", r, e)
		}
		b, _ := os.ReadFile(v.Abs("inbox/b.md"))
		if string(b) != "external" {
			t.Fatal("overwrote edit")
		}
	})
}
func TestEmptyAndExplicitSelection(t *testing.T) {
	v := fixture(t, map[string]string{"outside.md": "reference"})
	s, e := Prepare(context.Background(), v, Options{Limits: config.AIContext{Notes: 8, Chars: 12000, SearchRounds: 1}})
	if e != nil || !s.Empty() {
		t.Fatalf("%v %v", s, e)
	}
	_, e = Prepare(context.Background(), v, Options{Limits: config.AIContext{Notes: 8, Chars: 12000}, Notes: []string{"outside.md"}})
	if e == nil {
		t.Fatal("allowed outside subject")
	}
}
func TestFinalSymlinkRejected(t *testing.T) {
	v := fixture(t, map[string]string{"inbox/a.md": "A"})
	s := prepare(t, v)
	p := proposal("N1")
	p.Tags = []string{"safe"}
	sel := all(t, propose(t, s, p))
	target := filepath.Join(t.TempDir(), "outside")
	os.WriteFile(target, []byte("external"), 0600)
	os.Remove(v.Abs("inbox/a.md"))
	if err := os.Symlink(target, v.Abs("inbox/a.md")); err != nil {
		t.Fatal(err)
	}
	if _, e := sel.Apply(context.Background()); e == nil {
		t.Fatal("symlink mutation")
	}
	b, _ := os.ReadFile(target)
	if string(b) != "external" {
		t.Fatal("outside changed")
	}
}

func TestUnsupportedDiscoveryDoesNotBlockRegularSubjects(t *testing.T) {
	v := fixture(t, map[string]string{"inbox/a.md": "A", "target.md": "Reference"})
	if err := os.Symlink(v.Abs("target.md"), v.Abs("unrelated.md")); err != nil {
		t.Fatal(err)
	}
	opts := Options{Limits: config.AIContext{Notes: 8, Chars: 12000, SearchRounds: 1}, Notes: []string{"inbox/a.md"}}
	s, err := Prepare(context.Background(), v, opts)
	if err != nil || len(s.sources) != 1 || s.sources[0].Path != "inbox/a.md" {
		t.Fatalf("unrelated symlink blocked subject: session=%v err=%v", s, err)
	}
	if _, exists := s.snapshots["unrelated.md"]; exists || !slices.Contains(s.notePaths, "unrelated.md") {
		t.Fatal("unsupported reference was read or omitted from ambiguity inventory")
	}
	if err := os.Symlink(v.Abs("target.md"), v.Abs("inbox/linked.md")); err != nil {
		t.Fatal(err)
	}
	opts.Notes = []string{"inbox/linked.md"}
	if _, err := Prepare(context.Background(), v, opts); err == nil || !strings.Contains(err.Error(), "not a regular inbox note") {
		t.Fatalf("explicit symlink subject accepted: %v", err)
	}
	// Automatic selection skips the unsupported inbox entry too.
	opts.Notes = nil
	s, err = Prepare(context.Background(), v, opts)
	if err != nil || len(s.sources) != 1 || s.sources[0].Path != "inbox/a.md" {
		t.Fatalf("automatic discovery=%v err=%v", s, err)
	}
}

func TestSkippedSymlinkStillMakesLinkCaseAmbiguous(t *testing.T) {
	v := fixture(t, map[string]string{"inbox/a.md": "Source", "inbox/b.md": "Target"})
	if err := os.Symlink(v.Abs("inbox/b.md"), v.Abs("inbox/B.md")); err != nil {
		if os.IsExist(err) {
			t.Skip("filesystem does not support case-distinct entries")
		}
		t.Fatal(err)
	}
	s := prepare(t, v)
	if _, exists := s.snapshots["inbox/B.md"]; exists || !slices.Contains(s.notePaths, "inbox/B.md") {
		t.Fatal("symlink was admitted or removed from the path inventory")
	}
	p := proposal("N1")
	p.Links = []string{"N2"}
	p.Tags = []string{"safe"}
	plan := propose(t, s, p)
	if actions := plan.Actions(); len(actions) != 1 || actions[0].Kind != "tag" || !strings.Contains(plan.Description(), "case-ambiguous") {
		t.Fatalf("case-ambiguous link offered: %+v", actions)
	}
}

func TestSharedContextNoteLimitIsCappedLocally(t *testing.T) {
	t.Run("valid larger setting with one note", func(t *testing.T) {
		v := fixture(t, map[string]string{"inbox/a.md": "A"})
		opts := Options{Limits: config.AIContext{Notes: 33, Chars: 12000, SearchRounds: 1}}
		s, err := Prepare(context.Background(), v, opts)
		if err != nil || len(s.sources) != 1 || s.opts.Limits.Notes != 32 || opts.Limits.Notes != 33 || !strings.Contains(s.ContextDescription(), "up to 32 notes") {
			t.Fatalf("larger shared limit rejected or changed: session=%v err=%v", s, err)
		}
	})
	for _, limit := range []int{2, 32, 33, 1000} {
		t.Run(fmt.Sprint(limit), func(t *testing.T) {
			files := make(map[string]string)
			for i := 0; i < 40; i++ {
				files[fmt.Sprintf("inbox/%02d.md", i)] = "Body"
			}
			v := fixture(t, files)
			s, err := Prepare(context.Background(), v, Options{Limits: config.AIContext{Notes: limit, Chars: 12000}})
			if err != nil {
				t.Fatal(err)
			}
			want := min(limit, 32)
			if len(s.sources) != want || s.chars > 12000 || s.opts.Limits.Notes != want {
				t.Fatalf("effective bound exceeded: sources=%d chars=%d limit=%d", len(s.sources), s.chars, s.opts.Limits.Notes)
			}
		})
	}
}

func TestLimitsAndNoProgressFinalization(t *testing.T) {
	v := fixture(t, map[string]string{"inbox/a.md": strings.Repeat("a", 1000), "inbox/b.md": strings.Repeat("b", 1000), "r.md": strings.Repeat("needle", 1000)})
	s, e := Prepare(context.Background(), v, Options{Limits: config.AIContext{Notes: 3, Chars: 300, SearchRounds: 1}})
	if e != nil {
		t.Fatal(e)
	}
	initial := s.sources[0].Excerpt
	seen := 0
	_, e = s.Run(context.Background(), ai.Approval{}, call(), "p", func(_ context.Context, _ ai.Approval, req ai.Request) (ai.Result, error) {
		seen++
		if s.chars > 300 || len(s.sources) > 3 {
			t.Fatal("global allowance exceeded")
		}
		if s.sources[0].Excerpt != initial {
			t.Fatal("excerpt expanded between rounds")
		}
		if seen == 1 {
			return ai.Result{Triage: &ai.TriageReply{Action: "search", Query: "absent", Proposals: []ai.TriageProposal{}}}, nil
		}
		var obj map[string]any
		json.Unmarshal([]byte(req.Text), &obj)
		if obj["must_finalize"] != true || obj["searches_remaining"] != float64(0) {
			t.Fatalf("not finalized: %s", req.Text)
		}
		return ai.Result{Triage: &ai.TriageReply{Action: "propose", Proposals: []ai.TriageProposal{}}}, nil
	})
	if e != nil || seen != 2 {
		t.Fatalf("%d %v", seen, e)
	}
	_, e = Prepare(context.Background(), v, Options{Limits: config.AIContext{Notes: 3, Chars: 2, SearchRounds: 1}, Notes: []string{"inbox/a.md"}})
	if e == nil {
		t.Fatal("oversize metadata silently admitted")
	}
}
func TestRepeatedSearchAndDeadline(t *testing.T) {
	t.Run("repeat", func(t *testing.T) {
		v := fixture(t, map[string]string{"inbox/a.md": "A", "r.md": "needle"})
		s, e := Prepare(context.Background(), v, Options{Limits: config.AIContext{Notes: 8, Chars: 12000, SearchRounds: 3}})
		if e != nil {
			t.Fatal(e)
		}
		_, e = s.Run(context.Background(), ai.Approval{}, call(), "p", func(context.Context, ai.Approval, ai.Request) (ai.Result, error) {
			return ai.Result{Triage: &ai.TriageReply{Action: "search", Query: "needle", Proposals: []ai.TriageProposal{}}}, nil
		})
		if e == nil || !strings.Contains(e.Error(), "repeated") {
			t.Fatal(e)
		}
	})
	t.Run("deadline", func(t *testing.T) {
		v := fixture(t, map[string]string{"inbox/a.md": "A"})
		s := prepare(t, v)
		c := call()
		c.Profile.Timeout = 5 * time.Millisecond
		_, e := s.Run(context.Background(), ai.Approval{}, c, "p", func(ctx context.Context, _ ai.Approval, _ ai.Request) (ai.Result, error) {
			<-ctx.Done()
			return ai.Result{}, ctx.Err()
		})
		if !errors.Is(e, context.DeadlineExceeded) {
			t.Fatal(e)
		}
	})
}
func TestCaseAmbiguousLinkAndDuplicateSelection(t *testing.T) {
	v := fixture(t, map[string]string{"inbox/a.md": "A", "inbox/b.md": "B"})
	s := prepare(t, v)
	// Model a case-sensitive walk namespace regardless of the host's case
	// behavior, including a skipped entry with no readable snapshot.
	s.notePaths = append(s.notePaths, "inbox/B.md")
	p := proposal("N1")
	p.Links = []string{"N2"}
	p.Tags = []string{"good"}
	plan := propose(t, s, p)
	if len(plan.Actions()) != 1 || !strings.Contains(plan.Description(), "case-ambiguous") {
		t.Fatal("ambiguous link offered")
	}
	if _, e := plan.Select([]string{"1", "1"}); e == nil {
		t.Fatal("duplicate action")
	}
	if _, e := plan.Select([]string{"999"}); e == nil {
		t.Fatal("unknown action")
	}
	exported := plan.Actions()
	exported[0].Summary = "mutated"
	if plan.Actions()[0].Summary == "mutated" {
		t.Fatal("plan mutable through getter")
	}
}
func TestReferenceCannotReceiveProposal(t *testing.T) {
	v := fixture(t, map[string]string{"inbox/a.md": "A", "r.md": "needle"})
	s := prepare(t, v)
	calls := 0
	_, e := s.Run(context.Background(), ai.Approval{}, call(), "p", func(context.Context, ai.Approval, ai.Request) (ai.Result, error) {
		calls++
		if calls == 1 {
			return ai.Result{Triage: &ai.TriageReply{Action: "search", Query: "needle", Proposals: []ai.TriageProposal{}}}, nil
		}
		return ai.Result{Triage: &ai.TriageReply{Action: "propose", Proposals: []ai.TriageProposal{proposal("N2")}}}, nil
	})
	if e == nil {
		t.Fatal("reference note mutated")
	}
}
func TestRecoverySymlinkAndCancelledApply(t *testing.T) {
	for _, kind := range []string{"symlink", "cancelled"} {
		t.Run(kind, func(t *testing.T) {
			v := fixture(t, map[string]string{"inbox/a.md": "A"})
			p := proposal("N1")
			p.Tags = []string{"new"}
			sel := all(t, propose(t, prepare(t, v), p))
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if kind == "symlink" {
				data := os.Getenv("XDG_DATA_HOME")
				os.MkdirAll(filepath.Join(data, "nn"), 0700)
				os.Symlink(t.TempDir(), filepath.Join(data, "nn", "triage"))
			} else {
				cancel()
			}
			r, e := sel.Apply(ctx)
			if e == nil {
				t.Fatal("expected apply failure")
			}
			if r.Notes[0].Changed {
				t.Fatal("reported nonexistent mutation")
			}
			b, _ := os.ReadFile(v.Abs("inbox/a.md"))
			if string(b) != "A" {
				t.Fatal("unexpected mutation")
			}
		})
	}
}
func TestCombinedSelectionUsesExactRenderer(t *testing.T) {
	v := fixture(t, map[string]string{"inbox/a.md": "```sh\necho hi", "inbox/b.md": "B"})
	s := prepare(t, v)
	p := proposal("N1")
	p.Title = "Shell command"
	p.Tags = []string{"shell"}
	p.Links = []string{"N2"}
	plan := propose(t, s, p)
	sel := all(t, plan)
	expected, e := vault.PreviewEnrichment(sel.notes[0].source.snapshot, sel.notes[0].enrichment)
	if e != nil {
		t.Fatal(e)
	}
	preview, e := sel.Preview()
	if e != nil || !strings.Contains(preview, "+```") {
		t.Fatalf("%s %v", preview, e)
	}
	r, e := sel.Apply(context.Background())
	if e != nil || !r.Notes[0].Changed {
		t.Fatalf("%+v %v", r, e)
	}
	actual, _ := os.ReadFile(v.Abs("inbox/a.md"))
	if string(actual) != string(expected) {
		t.Fatal("preview/write bytes differ")
	}
}

func TestRecoveryManifestIdentifiesVault(t *testing.T) {
	roots := map[string]bool{}
	for i := 0; i < 2; i++ {
		v := fixture(t, map[string]string{"inbox/a.md": "A"})
		p := proposal("N1")
		p.Tags = []string{"new"}
		report, err := all(t, propose(t, prepare(t, v), p)).Apply(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		data, err := os.ReadFile(filepath.Join(report.RecoveryDir, "manifest.json"))
		if err != nil {
			t.Fatal(err)
		}
		var j journal
		if err := json.Unmarshal(data, &j); err != nil {
			t.Fatal(err)
		}
		want, err := filepath.EvalSymlinks(v.Root)
		if err != nil {
			t.Fatal(err)
		}
		if j.VaultRoot != want || roots[j.VaultRoot] {
			t.Fatalf("ambiguous vault root %q", j.VaultRoot)
		}
		roots[j.VaultRoot] = true
	}
}
