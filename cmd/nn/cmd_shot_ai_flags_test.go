package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestShotAIExplicitNeverBeforeCapture(t *testing.T) {
	for _, tc := range []struct {
		name   string
		config string
		args   []string
		key    string
	}{
		{"run", "[ai.tasks.shot]\nrun = \"never\"\n", []string{"--ai"}, "ai.tasks.shot.run"},
		{"run with profile", "[ai.tasks.shot]\nrun = \"never\"\n", []string{"--ai=codex"}, "ai.tasks.shot.run"},
		{"claude consent", "[ai.consent]\nclaude = \"never\"\n", []string{"--ai"}, "ai.consent.claude"},
		{"codex consent", "[ai.consent]\ncodex = \"never\"\n", []string{"--ai=codex"}, "ai.consent.codex"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			newTestVault(t)
			writeConfig(t, tc.config)
			withNoScreenshotExpected(t)
			fakeEngines(t, "claude", "codex")
			stdout, stderr, code := runCmd(t, "", append([]string{"shot"}, tc.args...)...)
			if code != 2 || stdout != "" || !strings.Contains(stderr, tc.key+" is never") {
				t.Fatalf("code=%d stdout=%q stderr=%q; want refusal naming %s", code, stdout, stderr, tc.key)
			}
		})
	}
}

func TestShotAINoAIOverridesConfiguredAndExplicitAI(t *testing.T) {
	for _, policy := range []string{"always", "never"} {
		t.Run(policy, func(t *testing.T) {
			root := newTestVault(t)
			writeConfig(t, "[ai.tasks.shot]\nrun = \""+policy+"\"\n[ai.consent]\nclaude = \"never\"\n")
			t.Setenv("NN_AI", "missing-environment-profile")
			withFakeScreenshot(t, fakePNG, "png", nil)
			fakeEngines(t, "claude", "codex")
			_, stderr, code := runCmd(t, "", "shot", "--ai=missing-explicit-profile", "--no-ai", "--no-ocr")
			if code != 0 {
				t.Fatalf("--no-ai did not preserve ordinary capture: code=%d stderr=%q", code, stderr)
			}
			assets, err := os.ReadDir(filepath.Join(root, "nn", "assets"))
			if err != nil || len(assets) != 1 {
				t.Fatalf("ordinary capture not saved: assets=%v err=%v", assets, err)
			}
			if strings.Contains(stderr, "AI needs consent") || strings.Contains(stderr, "analysis running") {
				t.Fatalf("AI handling happened despite --no-ai: %q", stderr)
			}
		})
	}
}

func TestShotAIInvalidFlagsBeforeCapture(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		want string
	}{
		{"mode", []string{"--ai", "--ai-mode", "immediate"}, "--ai-mode"},
		{"mode equals", []string{"--ai-mode=immediate"}, "--ai-mode"},
		{"mode missing", []string{"--ai-mode"}, "--ai-mode"},
		{"mode empty", []string{"--ai-mode="}, "--ai-mode"},
		{"effort", []string{"--ai", "--effort", "ultra"}, "--effort"},
		{"effort equals", []string{"--ai", "--effort=ultra"}, "--effort"},
		{"effort missing", []string{"--effort"}, "--effort"},
		{"effort empty", []string{"--effort="}, "--effort"},
		{"model missing", []string{"--model"}, "--model"},
		{"model empty", []string{"--model="}, "--model"},
		{"profile empty", []string{"--ai="}, "--ai"},
		{"profile absent", []string{"--ai=missing-profile"}, "missing-profile"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			newTestVault(t)
			withNoScreenshotExpected(t)
			fakeEngines(t, "claude", "codex")
			stdout, stderr, code := runCmd(t, "", append([]string{"shot"}, tc.args...)...)
			if code != 2 || stdout != "" || !strings.Contains(stderr, tc.want) {
				t.Fatalf("code=%d stdout=%q stderr=%q; want error naming %s", code, stdout, stderr, tc.want)
			}
		})
	}
}

func TestShotAIFlagPolicyRequiresExplicitAI(t *testing.T) {
	for _, args := range [][]string{
		{"shot", "--no-ocr"},
		{"shot", "--no-ocr", "--model", "custom-model", "--effort", "high", "--ai-mode", "wait"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			root := newTestVault(t)
			writeConfig(t, "[ai.tasks.shot]\nrun = \"flag\"\n[ai.consent]\nclaude = \"always\"\n")
			// A bad selector must remain irrelevant when the task does not run.
			t.Setenv("NN_AI", "missing-environment-profile")
			withFakeScreenshot(t, fakePNG, "png", nil)
			fakeEngines(t, "claude", "codex")
			_, stderr, code := runCmd(t, "", args...)
			if code != 0 {
				t.Fatalf("flag policy should save without AI: code=%d stderr=%q", code, stderr)
			}
			assets, err := os.ReadDir(filepath.Join(root, "nn", "assets"))
			if err != nil || len(assets) != 1 {
				t.Fatalf("ordinary capture not saved: assets=%v err=%v", assets, err)
			}
		})
	}
}

func TestShotAIProfileSelectionPrecedence(t *testing.T) {
	const profiles = `
[ai.profiles.task-choice]
engine = "command"
command = ["must-not-run-task", "{image}"]
[ai.profiles.env-choice]
engine = "command"
command = ["must-not-run-env", "{image}"]
[ai.profiles.flag-choice]
engine = "command"
command = ["must-not-run-flag", "{image}"]
[ai.consent]
claude = "never"
task-choice = "never"
env-choice = "never"
flag-choice = "never"
`
	for _, tc := range []struct {
		name string
		task string
		env  string
		flag string
		key  string
	}{
		{"flag beats env and task", "task-choice", "env-choice", "--ai=flag-choice", "flag-choice"},
		{"flag beats invalid env", "task-choice", "absent-profile", "--ai=flag-choice", "flag-choice"},
		{"env beats task", "task-choice", "env-choice", "--ai", "env-choice"},
		{"task beats global", "task-choice", "", "--ai", "task-choice"},
		{"global default", "", "", "--ai", "claude"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			newTestVault(t)
			writeConfig(t, "[ai]\nprofile = \"claude\"\n[ai.tasks.shot]\nprofile = \""+tc.task+"\"\n"+profiles)
			t.Setenv("NN_AI", tc.env)
			withNoScreenshotExpected(t)
			fakeEngines(t, "claude", "codex", "must-not-run-task", "must-not-run-env", "must-not-run-flag")
			_, stderr, code := runCmd(t, "", "shot", tc.flag)
			if code != 2 || !strings.Contains(stderr, "ai.consent."+tc.key+" is never") {
				t.Fatalf("wrong profile chosen: code=%d stderr=%q; want consent key %s", code, stderr, tc.key)
			}
		})
	}
}
