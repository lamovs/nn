package main

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/lamovs/nn/internal/config"
)

func doctorAI(t *testing.T) (aiRows, other []doctorRow, code int) {
	t.Helper()
	stdout, _, code := runCmd(t, "", "doctor", "--json")
	for _, r := range decodeJSONRows[doctorRow](t, stdout) {
		if r.Name == "ai" {
			aiRows = append(aiRows, r)
		} else {
			other = append(other, r)
		}
	}
	return aiRows, other, code
}

func aiRow(ok bool, detail string, fix ...string) doctorRow {
	if fix == nil {
		fix = []string{}
	}
	return doctorRow{Name: "ai", OK: ok, Detail: detail, Fix: fix}
}

// assertNoOtherProblems fails when a non-ai row failed, so the exit code is the ai rows' doing.
func assertNoOtherProblems(t *testing.T, other []doctorRow) {
	t.Helper()
	for _, r := range other {
		if !r.OK {
			t.Errorf("row %s failed: %s", r.Name, r.Detail)
		}
	}
}

func assertAIRows(t *testing.T, got []doctorRow, want ...doctorRow) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Errorf("ai rows:\ngot  %#v\nwant %#v", got, want)
	}
}

func TestDoctorAIDefaults(t *testing.T) {
	newTestVault(t)
	withFakePlatformChecks(t, healthyPlatformChecks)

	rows, other, code := doctorAI(t)
	assertNoOtherProblems(t, other)
	assertAIRows(t, rows, aiRow(false, "claude is not installed", "install claude, or add the directory it is in to PATH"))
	if code != 1 {
		t.Errorf("code = %d, want 1", code)
	}
}

func TestDoctorAIConsentAndProgram(t *testing.T) {
	for _, tc := range []struct {
		name    string
		consent string
		found   bool
		want    func(dir string) doctorRow
		code    int
	}{
		{"ask, not found", "ask", false, func(string) doctorRow {
			return aiRow(false, "claude is not installed", "install claude, or add the directory it is in to PATH")
		}, 1},
		{"ask, found", "ask", true, func(string) doctorRow {
			return aiRow(false, "claude not approved", "nn setup ai")
		}, 1},
		{"always, not found", "always", false, func(string) doctorRow {
			return aiRow(false, "claude is not installed", "install claude, or add the directory it is in to PATH")
		}, 1},
		{"always, found", "always", true, func(dir string) doctorRow {
			return aiRow(true, "claude: "+filepath.Join(dir, "claude"))
		}, 0},
		{"never, not found", "never", false, func(string) doctorRow {
			return aiRow(true, "claude is off: ai.consent.claude is never")
		}, 0},
		{"never, found", "never", true, func(string) doctorRow {
			return aiRow(true, "claude is off: ai.consent.claude is never")
		}, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			newTestVault(t)
			withFakePlatformChecks(t, healthyPlatformChecks)
			writeConfig(t, "[ai.consent]\nclaude = \""+tc.consent+"\"\n")
			dir := ""
			if tc.found {
				dir = fakeEngines(t, "claude")
			}

			rows, other, code := doctorAI(t)
			assertNoOtherProblems(t, other)
			assertAIRows(t, rows, tc.want(dir))
			if code != tc.code {
				t.Errorf("code = %d, want %d", code, tc.code)
			}
			stdout, _, textCode := runCmd(t, "", "doctor")
			want := tc.want(dir)
			mark := "[!!]"
			if want.OK {
				mark = "[ok]"
			}
			line := mark + " ai: " + want.Detail + "\n"
			for _, fix := range want.Fix {
				line += "      " + fix + "\n"
			}
			if !strings.Contains(stdout, line) || textCode != tc.code {
				t.Errorf("text = %q, code = %d; want %q and %d", stdout, textCode, line, tc.code)
			}
		})
	}
}

func TestDoctorAICommandProfile(t *testing.T) {
	config := "[ai]\nprofile = \"local\"\n" +
		"[ai.profiles.local]\nengine = \"command\"\ncommand = [\"fake-model\", \"{prompt}\", \"{image}\"]\n"

	for _, consent := range []string{"ask", "always"} {
		t.Run(consent+", not found", func(t *testing.T) {
			newTestVault(t)
			withFakePlatformChecks(t, healthyPlatformChecks)
			writeConfig(t, config+"[ai.consent]\nlocal = \""+consent+"\"\n")

			rows, other, code := doctorAI(t)
			assertNoOtherProblems(t, other)
			assertAIRows(t, rows, aiRow(false, "local: fake-model is not installed", "install fake-model, or fix the command of profile local"))
			if code != 1 {
				t.Errorf("code = %d, want 1", code)
			}
		})
	}
	t.Run("ask, found", func(t *testing.T) {
		newTestVault(t)
		withFakePlatformChecks(t, healthyPlatformChecks)
		writeConfig(t, config)
		fakeEngines(t, "fake-model")

		rows, other, code := doctorAI(t)
		assertNoOtherProblems(t, other)
		assertAIRows(t, rows, aiRow(false, "local not approved", "nn setup ai"))
		if code != 1 {
			t.Errorf("code = %d, want 1", code)
		}
	})
}

func TestDoctorAITextMarks(t *testing.T) {
	newTestVault(t)
	withFakePlatformChecks(t, healthyPlatformChecks)
	writeConfig(t, "[ai.tasks.title]\nprofile = \"codex\"\n[ai.consent]\ncodex = \"always\"\n")
	fakeEngines(t, "claude")

	stdout, _, code := runCmd(t, "", "doctor")
	if code != 1 {
		t.Errorf("code = %d, want 1", code)
	}
	for _, want := range []string{
		"[!!] ai: claude not approved\n      nn setup ai\n",
		"[!!] ai: codex is not installed\n      install codex, or add the directory it is in to PATH\n",
		"[ok] ai: " + codexOverhead + "\n",
	} {
		if !strings.Contains(stdout, want) {
			t.Errorf("stdout lacks %q:\n%s", want, stdout)
		}
	}
}

func TestDoctorAICodexCost(t *testing.T) {
	t.Run("in use", func(t *testing.T) {
		newTestVault(t)
		withFakePlatformChecks(t, healthyPlatformChecks)
		writeConfig(t, "[ai]\nprofile = \"codex\"\n[ai.consent]\ncodex = \"always\"\n")
		dir := fakeEngines(t, "codex")

		rows, other, code := doctorAI(t)
		assertNoOtherProblems(t, other)
		assertAIRows(t, rows, aiRow(true, "codex: "+filepath.Join(dir, "codex")), aiRow(true, codexOverhead))
		if code != 0 {
			t.Errorf("code = %d, want 0", code)
		}
	})
	t.Run("never", func(t *testing.T) {
		newTestVault(t)
		withFakePlatformChecks(t, healthyPlatformChecks)
		writeConfig(t, "[ai]\nprofile = \"codex\"\n[ai.consent]\ncodex = \"never\"\n")

		rows, _, code := doctorAI(t)
		assertAIRows(t, rows, aiRow(true, "codex is off: ai.consent.codex is never"))
		if code != 0 {
			t.Errorf("code = %d, want 0", code)
		}
	})
}

func TestDoctorAIPromptFile(t *testing.T) {
	good := filepath.Join(t.TempDir(), "good.md")
	empty := filepath.Join(t.TempDir(), "empty.md")
	for path, body := range map[string]string{good: "Title the note.\n", empty: "  \n"} {
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	missing := filepath.Join(t.TempDir(), "missing.md")
	dir := t.TempDir()
	const fix = "fix the file, or stop using it: nn config ai.tasks.title.prompt_file \"\""

	for _, tc := range []struct {
		name, path string
		want       string // the start of the row's detail, "" for no row
	}{
		{"missing", missing, "ai.tasks.title.prompt_file: stat " + missing + ": "},
		{"empty", empty, "ai.tasks.title.prompt_file: " + empty + " is empty"},
		{"directory", dir, "ai.tasks.title.prompt_file: " + dir + " is not a regular file"},
		{"good", good, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			newTestVault(t)
			withFakePlatformChecks(t, healthyPlatformChecks)
			writeConfig(t, "[ai.tasks.title]\nprompt_file = "+config.TOMLString(tc.path)+"\n[ai.consent]\nclaude = \"never\"\n")

			rows, other, code := doctorAI(t)
			assertNoOtherProblems(t, other)
			var prompt []doctorRow
			for _, r := range rows {
				if strings.Contains(r.Detail, "prompt_file") {
					prompt = append(prompt, r)
				}
			}
			if tc.want == "" {
				if len(prompt) != 0 || code != 0 {
					t.Errorf("rows %v, code %d; want no row and 0", prompt, code)
				}
				return
			}
			if len(prompt) != 1 || prompt[0].OK || !strings.HasPrefix(prompt[0].Detail, tc.want) || !reflect.DeepEqual(prompt[0].Fix, []string{fix}) {
				t.Fatalf("rows = %#v, want one failed row starting %q", prompt, tc.want)
			}
			if code != 1 {
				t.Errorf("code = %d, want 1", code)
			}
		})
	}
}

// A command profile with no {image} is a problem for shot unless its run or consent is never.
func TestDoctorAIImageTask(t *testing.T) {
	const detail = "shot sends an image, but profile local takes none: its command has no {image}"
	const noImage = `["fake-model", "{prompt}"]`
	profile := func(command, consent string) string {
		return "[ai.profiles.local]\nengine = \"command\"\ncommand = " + command + "\n[ai.consent]\nlocal = \"" + consent + "\"\nclaude = \"always\"\n"
	}
	onShot := "[ai.tasks.shot]\nprofile = \"local\"\n"
	fix := []string{
		"put {image} in the command of profile local, where the path of the image goes",
		"or run shot on another profile: nn config ai.tasks.shot.profile claude",
		"or turn AI off for shot: nn config ai.tasks.shot.run never",
	}

	for _, tc := range []struct {
		name, config, nnAI string
		want               []string // the fix, nil for no row
	}{
		{"no image", profile(noImage, "always") + onShot, "", fix},
		{"no image, run never", profile(noImage, "always") + onShot + "run = \"never\"\n", "", nil},
		{"no image, run flag", profile(noImage, "always") + onShot + "run = \"flag\"\n", "", fix},
		{"no image, consent never", profile(noImage, "never") + onShot, "", nil},
		{"no image, consent ask", profile(noImage, "ask") + onShot, "", fix},
		{"image", profile(`["fake-model", "--image={image}", "{prompt}"]`, "always") + onShot, "", nil},
		{"no image, through NN_AI", profile(noImage, "always"), "local", []string{
			"put {image} in the command of profile local, where the path of the image goes",
			"or set NN_AI to a profile that takes images",
			"or turn AI off for shot: nn config ai.tasks.shot.run never",
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			newTestVault(t)
			withFakePlatformChecks(t, healthyPlatformChecks)
			writeConfig(t, tc.config)
			if tc.nnAI != "" {
				t.Setenv("NN_AI", tc.nnAI)
			}
			fakeEngines(t, "claude", "fake-model")

			rows, other, code := doctorAI(t)
			assertNoOtherProblems(t, other)
			var image []doctorRow
			for _, r := range rows {
				if strings.Contains(r.Detail, "{image}") {
					image = append(image, r)
				} else if !r.OK && !(tc.name == "no image, consent ask" && r.Detail == "local not approved") {
					t.Errorf("unexpected failed row %#v", r)
				}
			}
			if tc.want == nil {
				if len(image) != 0 || code != 0 {
					t.Errorf("image rows %#v, code %d; want none and 0", image, code)
				}
				return
			}
			assertAIRows(t, image, aiRow(false, detail, tc.want...))
			if code != 1 {
				t.Errorf("code = %d, want 1", code)
			}
		})
	}
}

// However many tasks share an ai.consent entry, it gets one row; a profile nothing uses gets none.
func TestDoctorAIOneRowPerConsentEntry(t *testing.T) {
	newTestVault(t)
	withFakePlatformChecks(t, healthyPlatformChecks)
	writeConfig(t, `[ai]
profile = "claude"

[ai.profiles.fast]
engine = "claude"
model = "haiku"

[ai.profiles.local]
engine = "command"
command = ["fake-model", "{prompt}", "{image}"]

[ai.profiles.other]
engine = "command"
command = ["fake-model", "{prompt}"]

[ai.profiles.spare]
engine = "command"
command = ["spare-model"]

[ai.profiles.idle]
engine = "codex"

[ai.tasks.title]
profile = "fast"

[ai.tasks.ask]
profile = "fast"

[ai.tasks.digest]
profile = "local"

[ai.tasks.triage]
profile = "local"

[ai.tasks.url]
profile = "other"
`)
	fakeEngines(t, "claude", "fake-model")

	rows, other, code := doctorAI(t)
	assertNoOtherProblems(t, other)
	assertAIRows(t, rows,
		aiRow(false, "claude not approved", "nn setup ai"),
		aiRow(false, "local not approved", "nn setup ai"),
		aiRow(false, "other not approved", "nn setup ai"),
	)
	if code != 1 {
		t.Errorf("code = %d, want 1", code)
	}
}

// A command profile with no command is config.Load's to report; the ai rows say nothing more about it.
func TestDoctorAICommandWithoutCommandIsTheConfigRowOnly(t *testing.T) {
	newTestVault(t)
	withFakePlatformChecks(t, healthyPlatformChecks)
	writeConfig(t, "[ai]\nprofile = \"broken\"\n[ai.profiles.broken]\nengine = \"command\"\n[ai.tasks.title]\nprofile = \"claude\"\n")

	stdout, _, code := runCmd(t, "", "doctor", "--json")
	if code != 1 {
		t.Errorf("code = %d, want 1 for the config row", code)
	}
	var configRow bool
	var rows []doctorRow
	for _, r := range decodeJSONRows[doctorRow](t, stdout) {
		switch {
		case r.Name == "ai":
			rows = append(rows, r)
		case r.Name == "config" && !r.OK && strings.Contains(r.Detail, "ai.profiles.broken") && strings.Contains(r.Detail, "needs a command"):
			configRow = true
		case !r.OK:
			t.Errorf("unexpected failed row %#v", r)
		}
	}
	if !configRow {
		t.Errorf("no config row for the profile without a command:\n%s", stdout)
	}
	assertAIRows(t, rows, aiRow(false, "claude is not installed", "install claude, or add the directory it is in to PATH"))
}

// NN_AI naming a nonexistent profile is a problem config.Load cannot see; it gets one row, not one per task.
func TestDoctorAINNAIWithoutSuchProfile(t *testing.T) {
	newTestVault(t)
	withFakePlatformChecks(t, healthyPlatformChecks)
	t.Setenv("NN_AI", "nope")

	rows, other, code := doctorAI(t)
	assertNoOtherProblems(t, other)
	assertAIRows(t, rows,
		aiRow(false, "NN_AI=nope: no such profile (profiles: claude, codex)", "name a profile that exists, or declare it under [ai.profiles]"),
		aiRow(false, "claude is not installed", "install claude, or add the directory it is in to PATH"),
	)
	if code != 1 {
		t.Errorf("code = %d, want 1", code)
	}
}

func TestDoctorAINNAIPicksTheProfileOfEveryTask(t *testing.T) {
	newTestVault(t)
	withFakePlatformChecks(t, healthyPlatformChecks)
	t.Setenv("NN_AI", "codex")

	rows, other, code := doctorAI(t)
	assertNoOtherProblems(t, other)
	assertAIRows(t, rows,
		aiRow(false, "claude is not installed", "install claude, or add the directory it is in to PATH"),
		aiRow(false, "codex is not installed", "install codex, or add the directory it is in to PATH"),
		aiRow(true, codexOverhead),
	)
	if code != 1 {
		t.Errorf("code = %d, want 1", code)
	}
}

// doctor writes nothing: no lock taken, an existing config keeps its bytes and mtime.
func TestDoctorAIWritesNothing(t *testing.T) {
	newTestVault(t)
	withFakePlatformChecks(t, healthyPlatformChecks)
	fakeEngines(t, "claude", "codex", "fake-model")
	locks := filepath.Join(os.Getenv("XDG_DATA_HOME"), "nn", "locks")

	if _, _, code := runCmd(t, "", "doctor"); code != 1 {
		t.Errorf("code = %d, want 1", code)
	}
	if _, err := os.Stat(os.Getenv("NN_CONFIG")); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("config after doctor: %v, want none", err)
	}
	if _, err := os.Stat(locks); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("locks after doctor: %v, want none", err)
	}

	path := writeConfig(t, "[ai]\nprofile = \"codex\"\n[ai.profiles.local]\nengine = \"command\"\ncommand = [\"fake-model\"]\n[ai.tasks.title]\nprofile = \"local\"\n")
	old := time.Now().Add(-time.Hour).Truncate(time.Second)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, code := runCmd(t, "", "doctor"); code != 1 {
		t.Errorf("code = %d, want 1", code)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) {
		t.Errorf("doctor changed the config:\n%s", after)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if !info.ModTime().Equal(old) {
		t.Errorf("config modification time after doctor: %v, want %v", info.ModTime(), old)
	}
	if _, err := os.Stat(locks); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("locks after doctor: %v, want none", err)
	}
}

// A name with a dot gets the line to add by hand; nn setup ai cannot write that key.
func TestDoctorAIConsentFixByKey(t *testing.T) {
	newTestVault(t)
	withFakePlatformChecks(t, healthyPlatformChecks)
	writeConfig(t, `[ai]
profile = "local"

[ai.profiles.local]
engine = "command"
command = ["fake-model", "{prompt}", "{image}"]

[ai.profiles."my box"]
engine = "command"
command = ["fake-model", "{prompt}"]

[ai.profiles."a.b"]
engine = "command"
command = ["fake-model", "{prompt}"]

[ai.tasks.title]
profile = "my box"

[ai.tasks.ask]
profile = "a.b"
`)
	fakeEngines(t, "fake-model")

	rows, other, code := doctorAI(t)
	assertNoOtherProblems(t, other)
	assertAIRows(t, rows,
		aiRow(false, "local not approved", "nn setup ai"),
		aiRow(false, "my box not approved", "nn setup ai"),
		aiRow(false, "a.b not approved", `set it by hand under [ai.consent]: "a.b" = "always"`),
	)
	for _, r := range rows {
		if strings.Contains(r.Detail+strings.Join(r.Fix, "\n"), "nn config ai.consent") {
			t.Errorf("row %#v offers an nn config command for consent", r)
		}
	}
	if code != 1 {
		t.Errorf("code = %d, want 1", code)
	}
}
