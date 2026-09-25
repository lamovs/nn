package capture

import (
	"reflect"
	"testing"

	"github.com/lamovs/nn/internal/vault"
)

func notes() []*vault.Note {
	return []*vault.Note{
		{Path: "docker-cleanup.md", Title: "Docker cleanup", Aliases: []string{"Docker cleanup", "docker prune"},
			Body: "docker system prune -a removes every unused image and container"},
		{Path: "chashka-tv-deshevle-ceny-rynka.md", Title: "Чашка ТВ дешевле цены рынка",
			Body: "Продавец сравнивает цену чашки и телевизора на рынке"},
		{Path: "go-concurrency.md", Title: "Go Concurrency", Aliases: []string{"Go каналы"},
			Body: "goroutines channels mutexes and the usual patterns"},
		{Path: "colima.md", Title: "Colima", Body: "lightweight docker desktop replacement for macos"},
	}
}

func TestSimilarNotes(t *testing.T) {
	tests := []struct {
		name        string
		title, body string
		want        []string
	}{
		{"same title", "Docker cleanup", "", []string{"docker-cleanup.md"}},
		{"title differs in case and form", "docker cleanups", "", []string{"docker-cleanup.md"}},
		{"matches an alias", "Go каналы", "", []string{"go-concurrency.md"}},
		{"cyrillic title against transliterated stem", "Чашка ТВ дешевле цены рынка", "", []string{"chashka-tv-deshevle-ceny-rynka.md"}},
		{"near duplicate body", "", "docker system prune -a removes every unused image", []string{"docker-cleanup.md"}},
		{"short body reads as a title", "", "docker prune", []string{"docker-cleanup.md"}},
		{"unrelated", "Kubernetes ingress troubleshooting", "kubectl describe ingress", nil},
		{"empty", "", "", nil},
	}
	for _, tt := range tests {
		got := SimilarNotes(notes(), tt.title, tt.body, 0.6, 3)
		var paths []string
		for _, s := range got {
			paths = append(paths, s.Path)
			if s.Score < 0.6 || s.Score > 1 {
				t.Errorf("%s: score %v out of range", tt.name, s.Score)
			}
			if s.Title == "" {
				t.Errorf("%s: missing title for %s", tt.name, s.Path)
			}
		}
		if !reflect.DeepEqual(paths, tt.want) {
			t.Errorf("%s: SimilarNotes = %v, want %v", tt.name, paths, tt.want)
		}
	}

	// threshold 0 must still exclude a genuine zero score.
	if got := SimilarNotes(notes(), "Kubernetes ingress troubleshooting", "kubectl describe ingress", 0, 3); got != nil {
		t.Errorf("zero threshold = %v, want nil (no note shares any words)", got)
	}

	// threshold 0 must not be treated as "off".
	if got := SimilarNotes(notes(), "Docker cleanup", "", 0, 3); len(got) != 1 || got[0].Path != "docker-cleanup.md" {
		t.Errorf("zero threshold = %v, want only docker-cleanup.md", got)
	}

	many := []*vault.Note{}
	for _, p := range []string{"a.md", "b.md", "c.md", "d.md", "e.md"} {
		many = append(many, &vault.Note{Path: p, Title: "Docker cleanup", Body: "docker system prune"})
	}
	if got := SimilarNotes(many, "Docker cleanup", "", 0.6, 3); len(got) != 3 {
		t.Errorf("top 3 = %d results", len(got))
	}
	if got := SimilarNotes(append(many, nil), "Docker cleanup", "", 0.6, 3); len(got) != 3 {
		t.Errorf("nil note in the list: %d results", len(got))
	}
}

func TestSimilarTags(t *testing.T) {
	existing := map[string]int{
		"golang": 12, "docker": 30, "note": 4, "git": 9, "json": 6, "tsod": 5,
		"macos": 7, "library": 3, "python3": 2,
	}
	tests := []struct {
		name string
		tags []string
		want []TagSuggestion
	}{
		{"prefix", []string{"go"}, []TagSuggestion{{Tag: "go", Existing: "golang", Count: 12}}},
		{"typo", []string{"dockr"}, []TagSuggestion{{Tag: "dockr", Existing: "docker", Count: 30}}},
		{"plural", []string{"notes"}, []TagSuggestion{{Tag: "notes", Existing: "note", Count: 4}}},
		{"plural ies", []string{"libraries"}, []TagSuggestion{{Tag: "libraries", Existing: "library", Count: 3}}},
		{"transliteration", []string{"цод"}, []TagSuggestion{{Tag: "цод", Existing: "tsod", Count: 5}}},
		{"leading hash and case", []string{"#Dockr"}, []TagSuggestion{{Tag: "Dockr", Existing: "docker", Count: 30}}},
		{"version digits", []string{"python"}, []TagSuggestion{{Tag: "python", Existing: "python3", Count: 2}}},
		{"already known", []string{"docker"}, nil},
		{"js is not json", []string{"js"}, nil},
		{"git is not github", []string{"github"}, nil},
		{"short typo", []string{"gil"}, nil},
		{"nothing alike", []string{"kubernetes"}, nil},
		{"empty", []string{"", "  #  "}, nil},
	}
	for _, tt := range tests {
		got := SimilarTags(existing, tt.tags)
		if !reflect.DeepEqual(got, tt.want) {
			t.Errorf("%s: SimilarTags(%v) = %+v, want %+v", tt.name, tt.tags, got, tt.want)
		}
	}

	// feeding existing tags back in lists each similar pair once.
	pairs := SimilarTags(map[string]int{"go": 3, "golang": 12, "note": 4, "notes": 1}, []string{"go", "golang", "note", "notes"})
	want := []TagSuggestion{
		{Tag: "go", Existing: "golang", Count: 12},
		{Tag: "notes", Existing: "note", Count: 4},
	}
	if !reflect.DeepEqual(pairs, want) {
		t.Errorf("pairs = %+v, want %+v", pairs, want)
	}

	if got := SimilarTags(nil, []string{"go"}); got != nil {
		t.Errorf("no existing tags = %+v", got)
	}
}

func TestLatinKeyAndStem(t *testing.T) {
	if latinKey("цод") != latinKey("tsod") {
		t.Errorf("latinKey: %q vs %q", latinKey("цод"), latinKey("tsod"))
	}
	if latinKey("хабр") != latinKey("khabr") {
		t.Errorf("latinKey: %q vs %q", latinKey("хабр"), latinKey("khabr"))
	}
	if latinKey("docker") != "docker" {
		t.Errorf("latinKey(docker) = %q", latinKey("docker"))
	}
	if stemWord("контейнеры") == stemWord("контекст") {
		t.Error("stemWord collapsed two different words")
	}
	if a, b := stemWord(vault.Transliterate("заметка")), stemWord(vault.Transliterate("заметки")); a != b {
		t.Errorf("stemWord: %q vs %q", a, b)
	}
	if levenshtein("docker", "dockr") != 1 || levenshtein("цод", "код") != 1 {
		t.Error("levenshtein")
	}
}
