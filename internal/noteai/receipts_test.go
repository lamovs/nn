package noteai

import (
	"context"
	"errors"
	"io"
	"reflect"
	"testing"

	"github.com/lamovs/nn/internal/vault"
)

func TestSaveAssetReceiptsAcrossModes(t *testing.T) {
	for _, mode := range []string{"wait", "fallback", "off", "background handoff failure"} {
		t.Run(mode, func(t *testing.T) {
			answer := `{"title":"Suggested","tags":["new"],"body":"ignored"}`
			if mode == "fallback" {
				answer = ""
			}
			env, plan, _ := metadataFixture(t, false, answer)
			if mode == "off" {
				plan = nil
			}
			if mode == "background handoff failure" {
				plan.Mode = "background"
				previous := workerExecutable
				workerExecutable = func() (string, error) { return "", errors.New("fake unavailable worker") }
				t.Cleanup(func() { workerExecutable = previous })
			}
			a := vault.Image{Data: []byte("receipt A"), Ext: "png"}
			b := vault.Image{Data: []byte("receipt B"), Ext: "png"}
			initial, err := env.Vault.CreateWithAssets(vault.NewNote{Title: "Receipt target", Images: []vault.Image{a, b}})
			if err != nil {
				t.Fatal(err)
			}
			calls := 0
			_, err = Save(context.Background(), env, plan, Input{To: initial.Note.Path, Note: vault.NewNote{Body: "New source", Images: []vault.Image{a, a}}, Text: "New source", AfterSave: func(note *vault.Note, paths []string) {
				calls++
				if !reflect.DeepEqual(paths, []string{initial.Images[0], initial.Images[0]}) {
					t.Errorf("mode=%s receipt=%v", mode, paths)
				}
				if len(note.Embeds) != 2 {
					t.Errorf("note embeds=%v", note.Embeds)
				}
			}}, io.Discard)
			if err != nil || calls != 1 {
				t.Fatalf("mode=%s callbacks=%d err=%v", mode, calls, err)
			}
		})
	}
}

func TestSaveReceiptSurvivesEnrichmentReread(t *testing.T) {
	for _, failEnrichment := range []bool{false, true} {
		t.Run(map[bool]string{false: "reread includes concurrent image", true: "enrichment fails after initial append"}[failEnrichment], func(t *testing.T) {
			env, plan, _ := metadataFixture(t, false, `{"title":"Unused","tags":["new"],"body":"ignored"}`)
			a := vault.Image{Data: []byte("A"), Ext: "png"}
			initial, err := env.Vault.CreateWithAssets(vault.NewNote{Title: "Target", Images: []vault.Image{a, {Data: []byte("B"), Ext: "png"}}})
			if err != nil {
				t.Fatal(err)
			}
			previous := appendEnrichment
			appendEnrichment = func(v *vault.Vault, rel string, e vault.Enrichment) (*vault.Note, error) {
				if _, err := v.AppendWithAssets(rel, "Concurrent capture", []vault.Image{{Data: []byte("C"), Ext: "png"}}); err != nil {
					return nil, err
				}
				if failEnrichment {
					return nil, errors.New("controlled metadata write failure")
				}
				return previous(v, rel, e)
			}
			t.Cleanup(func() { appendEnrichment = previous })
			calls := 0
			note, err := Save(context.Background(), env, plan, Input{To: initial.Note.Path, Note: vault.NewNote{Body: "New A capture", Images: []vault.Image{a}}, Text: "New A capture", AfterSave: func(note *vault.Note, paths []string) {
				calls++
				if !reflect.DeepEqual(paths, []string{initial.Images[0]}) {
					t.Errorf("receipt changed after metadata reread: %v", paths)
				}
				if !failEnrichment && len(note.Embeds) != 3 {
					t.Errorf("test did not include interleaved C: %v", note.Embeds)
				}
			}}, io.Discard)
			if calls != 1 || note == nil || (err != nil) != failEnrichment {
				t.Fatalf("callbacks=%d note=%+v err=%v", calls, note, err)
			}
		})
	}
}

func TestFailedInitialSaveDoesNotCallAfterSave(t *testing.T) {
	env, _, _ := metadataFixture(t, false, `{"title":"unused","tags":[],"body":""}`)
	calls := 0
	_, err := Save(context.Background(), env, nil, Input{To: "missing.md", Note: vault.NewNote{Body: "New source", Images: []vault.Image{{Data: []byte("A"), Ext: "png"}}}, AfterSave: func(*vault.Note, []string) { calls++ }}, io.Discard)
	if err == nil || calls != 0 {
		t.Fatalf("failed append callback=%d err=%v", calls, err)
	}
}
