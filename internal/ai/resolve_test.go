package ai

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/lamovs/nn/internal/config"
)

// testConfig has the built-in profiles plus a claude profile "fast", a
// codex profile "deep" and a command profile "local", with shot on fast.
func testConfig() config.Config {
	cfg := config.Default()
	cfg.AI.Profiles["fast"] = config.Profile{Engine: "claude", Model: "haiku", Effort: "low"}
	cfg.AI.Profiles["deep"] = config.Profile{Engine: "codex", Effort: "high", Timeout: 5 * time.Minute}
	cfg.AI.Profiles["local"] = config.Profile{Engine: config.EngineCommand, Command: []string{"llm", "{prompt}"}}
	shot := cfg.AI.Tasks["shot"]
	shot.Profile = "fast"
	cfg.AI.Tasks["shot"] = shot
	return cfg
}

func TestResolveOrder(t *testing.T) {
	cfg := testConfig()
	for _, tc := range []struct {
		name, env, task string
		o               Overrides
		profile, from   string
		explicit        bool
	}{
		{"ai.profile", "", "title", Overrides{}, "claude", "ai.profile", false},
		{"task profile", "", "shot", Overrides{}, "fast", "ai.tasks.shot.profile", false},
		{"NN_AI over the task", "deep", "shot", Overrides{}, "deep", "NN_AI", false},
		{"NN_AI over ai.profile", "local", "title", Overrides{}, "local", "NN_AI", false},
		{"--ai over NN_AI", "deep", "shot", Overrides{Profile: "codex"}, "codex", "--ai", true},
		{"--ai over the task", "", "shot", Overrides{AI: true, Profile: "claude"}, "claude", "--ai", true},
		{"bare --ai keeps the order", "", "shot", Overrides{AI: true}, "fast", "ai.tasks.shot.profile", true},
		{"blank NN_AI is unset", "  ", "shot", Overrides{}, "fast", "ai.tasks.shot.profile", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(profileEnv, tc.env)
			call, err := Resolve(cfg, tc.task, tc.o)
			if err != nil {
				t.Fatal(err)
			}
			if call.Name != tc.profile || call.From != tc.from || call.Task != tc.task {
				t.Errorf("call = %+v, want profile %s from %s", call, tc.profile, tc.from)
			}
			if want := cfg.AI.Profiles[tc.profile].Engine; call.Profile.Engine != want {
				t.Errorf("engine = %s, want %s", call.Profile.Engine, want)
			}
			if call.Explicit != tc.explicit || tc.o.Explicit() != tc.explicit {
				t.Errorf("explicit = %v, Overrides.Explicit() = %v; want %v", call.Explicit, tc.o.Explicit(), tc.explicit)
			}
		})
	}
}

func TestResolveOverrides(t *testing.T) {
	cfg := testConfig()
	cfg.AI.Timeout = 42 * time.Second

	call, err := Resolve(cfg, "shot", Overrides{Model: "opus", Effort: "max"})
	if err != nil {
		t.Fatal(err)
	}
	if p := call.Profile; p.Model != "opus" || p.Effort != "max" || p.Timeout != 42*time.Second {
		t.Errorf("profile = %+v, want opus, max and ai.timeout", p)
	}
	if call.Explicit {
		t.Error("--model alone is not --ai")
	}

	call, err = Resolve(cfg, "title", Overrides{Profile: "deep"})
	if err != nil || call.Profile.Effort != "high" || call.Profile.Timeout != 5*time.Minute || !call.Explicit {
		t.Errorf("deep = %+v, %v, want its own effort and timeout", call, err)
	}
	if call, _ := Resolve(cfg, "title", Overrides{}); call.Profile.Model != "sonnet" {
		t.Errorf("built-in claude model = %q, want sonnet", call.Profile.Model)
	}

	call, err = Resolve(cfg, "ask", Overrides{Profile: "local"})
	if err != nil {
		t.Fatal(err)
	}
	call.Profile.Command[0] = "changed"
	if cfg.AI.Profiles["local"].Command[0] != "llm" {
		t.Error("changing the call's command changed the config")
	}
}

func TestResolveRefuses(t *testing.T) {
	cfg := testConfig()
	cfg.AI.Profiles["empty"] = config.Profile{Engine: config.EngineCommand}
	for _, tc := range []struct {
		name, env, task string
		o               Overrides
		want            string
	}{
		{"unknown --ai", "", "shot", Overrides{Profile: "gpt"}, "--ai=gpt: no such profile (profiles: claude, codex, deep, empty, fast, local)"},
		{"unknown NN_AI", "gtp", "shot", Overrides{}, "NN_AI=gtp: no such profile (profiles: claude, codex, deep, empty, fast, local)"},
		{"bad effort", "", "shot", Overrides{Effort: "xhigh"}, "--effort=xhigh: want low, medium, high, max"},
		{"unknown task", "", "summarize", Overrides{}, `unknown task "summarize"`},
		{"command without a command", "", "shot", Overrides{Profile: "empty"}, "profile empty: engine command needs a command"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(profileEnv, tc.env)
			if _, err := Resolve(cfg, tc.task, tc.o); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestResolveNoAI(t *testing.T) {
	t.Setenv(profileEnv, "nonexistent")
	_, err := Resolve(testConfig(), "shot", Overrides{NoAI: true, AI: true, Profile: "gpt", Effort: "bogus"})
	if !errors.Is(err, ErrOff) {
		t.Errorf("err = %v, want ErrOff", err)
	}
}

func TestProfileHelpers(t *testing.T) {
	cfg := testConfig()
	for _, tc := range []struct {
		name, binary, consent string
		image                 bool
	}{
		{"claude", "claude", "claude", true},
		{"fast", "claude", "claude", true},
		{"deep", "codex", "codex", true},
		{"local", "llm", "local", false},
	} {
		p := cfg.AI.Profiles[tc.name]
		if got := Binary(p); got != tc.binary {
			t.Errorf("Binary(%s) = %q, want %q", tc.name, got, tc.binary)
		}
		if got := ConsentKey(tc.name, p); got != tc.consent {
			t.Errorf("ConsentKey(%s) = %q, want %q", tc.name, got, tc.consent)
		}
		if got := AcceptsImage(p); got != tc.image {
			t.Errorf("AcceptsImage(%s) = %v", tc.name, got)
		}
	}
	withImage := config.Profile{Engine: config.EngineCommand, Command: []string{"llm", "-a", "--file={image}"}}
	if !AcceptsImage(withImage) {
		t.Error("AcceptsImage = false with {image} inside an argument")
	}
	if AcceptsImage(config.Profile{Engine: config.EngineCommand, Command: []string{"{image}"}}) {
		t.Error("{image} as the program is not an argument")
	}
	if Binary(config.Profile{Engine: config.EngineCommand}) != "" {
		t.Error("Binary of a command profile with no command")
	}
	var image []string
	for _, task := range config.Tasks() {
		if TakesImage(task) {
			image = append(image, task)
		}
	}
	if !slices.Equal(image, []string{"shot"}) {
		t.Errorf("tasks taking an image = %q", image)
	}
}

// TestEveryTaskSaysWhatItSends guards against a task added without a
// taskSends entry: consent messages need one for every task.
func TestEveryTaskSaysWhatItSends(t *testing.T) {
	for _, task := range config.Tasks() {
		if Sends(task) == "" {
			t.Errorf("task %s does not say what it sends", task)
		}
	}
	for task := range taskSends {
		if !slices.Contains(config.Tasks(), task) {
			t.Errorf("taskSends describes %s, which is no task", task)
		}
	}
	if Sends("summarize") != "" {
		t.Errorf("Sends of an unknown task = %q", Sends("summarize"))
	}
}

// TestREADMESaysWhatEachTaskSends keeps README.md in agreement with Sends.
func TestREADMESaysWhatEachTaskSends(t *testing.T) {
	src, err := os.ReadFile(filepath.Join("..", "..", "README.md"))
	if err != nil {
		t.Fatal(err)
	}
	for _, task := range config.Tasks() {
		if line := "- `" + task + "`: " + Sends(task) + "\n"; !strings.Contains(string(src), line) {
			t.Errorf("README.md lacks %q", line)
		}
	}
}
