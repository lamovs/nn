package ai

import (
	"bytes"
	"errors"
	"fmt"
	"image"
	"image/png"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/lamovs/nn/internal/config"
)

const fakeMark = "# nn-ai-test-fake"

// fakeEngine answers from files the test left under $NN_FAKE (echo,
// stdout, stderr, out, sleep, exit).
const fakeEngine = `#!/bin/sh
` + fakeMark + `
d=$NN_FAKE
: > "$d/argv"
for a in "$@"; do printf '%s\0' "$a" >> "$d/argv"; done
cat > "$d/stdin"
if [ "${CLAUDE_CODE_EFFORT_LEVEL+set}" = set ]; then
	printf 'set:%s' "$CLAUDE_CODE_EFFORT_LEVEL" > "$d/effort_env"
else
	printf unset > "$d/effort_env"
fi
pwd -P > "$d/cwd"
ls -A > "$d/cwd_list"
prev=
for a in "$@"; do
	case $prev in
	-o)
		ls -ld "${a%/*}" > "$d/private_dir"
		if [ -f "$d/out" ]; then cat "$d/out" > "$a"; fi
		;;
	--output-schema)
		cat "$a" > "$d/schema"
		ls -l "$a" > "$d/schema_mode"
		;;
	-C)
		(cd "$a" && pwd -P) > "$d/codex_dir"
		;;
	esac
	case $a in
	--image=*)
		cat "${a#--image=}" > "$d/image"
		ls -l "${a#--image=}" > "$d/image_mode"
		printf '%s' "${a#--image=}" > "$d/image_path"
		;;
	esac
	prev=$a
done
if [ -f "$d/echo" ]; then printf 'user\n' >&2; cat "$d/stdin" >&2; printf '\n' >&2; fi
if [ -f "$d/stderr" ]; then cat "$d/stderr" >&2; fi
if [ -f "$d/stdout" ]; then cat "$d/stdout"; fi
if [ -f "$d/sleep" ]; then sleep "$(cat "$d/sleep")"; fi
if [ -f "$d/exit" ]; then exit "$(cat "$d/exit")"; fi
exit 0
`

// fakeSpawner hangs after starting a process of its own, recording both pids.
const fakeSpawner = `#!/bin/sh
` + fakeMark + `
sleep 30 &
printf '%s' "$!" > "$NN_FAKE/grandchild.tmp" && mv "$NN_FAKE/grandchild.tmp" "$NN_FAKE/grandchild"
printf '%s' "$$" > "$NN_FAKE/child.tmp" && mv "$NN_FAKE/child.tmp" "$NN_FAKE/child"
sleep 30
`

// fakeLeaver answers and exits, leaving a process holding its output open.
const fakeLeaver = `#!/bin/sh
` + fakeMark + `
sleep 5 &
printf '%s' '{"title":"Left","tags":[],"body":"behind"}'
`

var fakePrograms = map[string]string{
	"claude":     fakeEngine,
	"codex":      fakeEngine,
	"fake-model": fakeEngine,
	"spawner":    fakeSpawner,
	"leaver":     fakeLeaver,
}

var guardedTools = []string{"claude", "codex", "osascript", "notify-send"}
var realTools = []string{"cat", "ls", "mv", "sleep"}
var testBin string

// TestMain checks afterwards that the real ~/.config/nn and
// ~/.local/share/nn/locks gained nothing.
func TestMain(m *testing.M) {
	code, err := runIsolated(m)
	if err != nil {
		fmt.Fprintln(os.Stderr, "nn: internal/ai TestMain:", err)
		os.Exit(1)
	}
	os.Exit(code)
}

func runIsolated(m *testing.M) (int, error) {
	realHome, err := os.UserHomeDir()
	if err != nil {
		return 0, err
	}
	watched := []string{
		filepath.Join(realHome, ".config", "nn"),
		filepath.Join(realHome, ".local", "share", "nn", "locks"),
	}
	before := make([]string, len(watched))
	for i, dir := range watched {
		before[i] = listing(dir)
	}

	links := map[string]string{}
	for _, name := range realTools {
		path, err := exec.LookPath(name)
		if err != nil {
			return 0, err
		}
		links[name] = path
	}

	dir, err := os.MkdirTemp("", "nn-ai-test-*")
	if err != nil {
		return 0, err
	}
	defer os.RemoveAll(dir)
	testBin = filepath.Join(dir, "bin")
	for _, d := range []string{testBin, filepath.Join(dir, "home"), filepath.Join(dir, "tmp")} {
		if err := os.Mkdir(d, 0o755); err != nil {
			return 0, err
		}
	}
	for name, target := range links {
		if err := os.Symlink(target, filepath.Join(testBin, name)); err != nil {
			return 0, err
		}
	}
	for name, script := range fakePrograms {
		if err := os.WriteFile(filepath.Join(testBin, name), []byte(script), 0o755); err != nil {
			return 0, err
		}
	}
	for key, value := range map[string]string{
		"PATH":            testBin,
		"HOME":            filepath.Join(dir, "home"),
		"TMPDIR":          filepath.Join(dir, "tmp"),
		"NN_CONFIG":       filepath.Join(dir, "config", "config.toml"),
		"NN_FAKE":         filepath.Join(dir, "no-fake"),
		"XDG_CONFIG_HOME": filepath.Join(dir, "xdg-config"),
		"XDG_DATA_HOME":   filepath.Join(dir, "xdg-data"),
		"XDG_CACHE_HOME":  filepath.Join(dir, "xdg-cache"),
		"XDG_STATE_HOME":  filepath.Join(dir, "xdg-state"),
	} {
		if err := os.Setenv(key, value); err != nil {
			return 0, err
		}
	}
	for _, key := range []string{profileEnv, "NN_ROOT", claudeEffortEnv} {
		if err := os.Unsetenv(key); err != nil {
			return 0, err
		}
	}

	code := m.Run()
	for i, dir := range watched {
		if after := listing(dir); after != before[i] {
			return 0, fmt.Errorf("the tests changed %s:\nbefore: %s\nafter: %s", dir, before[i], after)
		}
	}
	return code, nil
}

func listing(dir string) string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "absent"
	}
	var b strings.Builder
	for _, e := range entries {
		info, err := e.Info()
		if err != nil {
			continue
		}
		fmt.Fprintf(&b, "%s %d %d; ", e.Name(), info.Size(), info.ModTime().UnixNano())
	}
	return b.String()
}

func TestNoRealEnginesInPath(t *testing.T) {
	if got := os.Getenv("PATH"); got != testBin {
		t.Fatalf("PATH = %q, want only %q", got, testBin)
	}
	for _, name := range guardedTools {
		path, err := exec.LookPath(name)
		if _, fake := fakePrograms[name]; !fake {
			if err == nil {
				t.Errorf("%s is reachable through PATH at %s", name, path)
			}
			continue
		}
		if err != nil {
			t.Errorf("fake %s is not in PATH: %v", name, err)
			continue
		}
		info, statErr := os.Lstat(path)
		script, readErr := os.ReadFile(path)
		if filepath.Dir(path) != testBin || statErr != nil || !info.Mode().IsRegular() || readErr != nil || !bytes.Contains(script, []byte(fakeMark)) {
			t.Errorf("%s resolves to %s, which is not the fake", name, path)
		}
	}
}

type fake struct {
	t   *testing.T
	dir string
}

func newFake(t *testing.T) *fake {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("NN_FAKE", dir)
	t.Cleanup(func() { assertNoRunLeft(t) })
	return &fake{t: t, dir: dir}
}

func (f *fake) put(name, content string) {
	f.t.Helper()
	if err := os.WriteFile(filepath.Join(f.dir, name), []byte(content), 0o644); err != nil {
		f.t.Fatal(err)
	}
}

func (f *fake) get(name string) string {
	f.t.Helper()
	data, err := os.ReadFile(filepath.Join(f.dir, name))
	if err != nil {
		f.t.Fatalf("the fake left no %s: %v", name, err)
	}
	return string(data)
}

func (f *fake) has(name string) bool {
	_, err := os.Stat(filepath.Join(f.dir, name))
	return err == nil
}

func (f *fake) argv() []string {
	f.t.Helper()
	raw := f.get("argv")
	if raw == "" {
		return []string{}
	}
	return strings.Split(strings.TrimSuffix(raw, "\x00"), "\x00")
}

func (f *fake) assertRanPrivately() {
	f.t.Helper()
	if list := f.get("cwd_list"); list != "" {
		f.t.Errorf("the engine's directory held %q, want nothing", list)
	}
	cwd := strings.TrimSpace(f.get("cwd"))
	here, _ := os.Getwd()
	if here, err := filepath.EvalSymlinks(here); err == nil && cwd == here {
		f.t.Errorf("the engine ran in the test's own directory %s", cwd)
	}
	if filepath.Base(cwd) != "work" || !strings.HasPrefix(filepath.Base(filepath.Dir(cwd)), "nn-ai-") {
		f.t.Errorf("the engine ran in %s, want the work directory of a run", cwd)
	}
	if _, err := os.Stat(cwd); !errors.Is(err, os.ErrNotExist) {
		f.t.Errorf("the engine's directory %s is still there after the run: %v", cwd, err)
	}
}

func assertNoRunLeft(t *testing.T) {
	t.Helper()
	entries, err := os.ReadDir(os.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "nn-ai-") {
			t.Errorf("a run left %s in %s", e.Name(), os.TempDir())
		}
	}
}

func claudeCall(model, effort string) Call {
	return Call{Task: "shot", Name: "claude", From: "ai.profile",
		Profile: config.Profile{Engine: "claude", Model: model, Effort: effort, Timeout: 20 * time.Second}}
}

func codexCall(model, effort string) Call {
	return Call{Task: "shot", Name: "codex", From: "ai.profile",
		Profile: config.Profile{Engine: "codex", Model: model, Effort: effort, Timeout: 20 * time.Second}}
}

func commandCall(command ...string) Call {
	return Call{Task: "shot", Name: "local", From: "ai.profile",
		Profile: config.Profile{Engine: config.EngineCommand, Command: command, Timeout: 20 * time.Second}}
}

var pngImage = func() []byte {
	var data bytes.Buffer
	if err := png.Encode(&data, image.NewRGBA(image.Rect(0, 0, 1, 1))); err != nil {
		panic(err)
	}
	return data.Bytes()
}()

// waitForFile waits for the fake to write name, for up to ten seconds.
func (f *fake) waitForFile(name string) string {
	f.t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if data, err := os.ReadFile(filepath.Join(f.dir, name)); err == nil && len(data) > 0 {
			return string(data)
		}
		time.Sleep(10 * time.Millisecond)
	}
	f.t.Fatalf("the fake never wrote %s", name)
	return ""
}

// gone: a zombie counts as gone, since it runs nothing.
func gone(pid int) bool {
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if err := syscall.Kill(pid, 0); errors.Is(err, syscall.ESRCH) || zombie(pid) {
			return true
		}
		time.Sleep(20 * time.Millisecond)
	}
	return false
}

func zombie(pid int) bool {
	if runtime.GOOS != "linux" {
		return false
	}
	stat, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return false
	}
	// The state follows the command name, which is in parentheses.
	i := bytes.LastIndexByte(stat, ')')
	return i >= 0 && i+2 < len(stat) && stat[i+2] == 'Z'
}
