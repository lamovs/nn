package app

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lamovs/nn/internal/config"
)

func TestWarnConfigNamesBadValuesAndMovedKeys(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "xdg-config"))
	t.Setenv("NN_ROOT", t.TempDir())
	cfgPath := filepath.Join(home, "config.toml")
	t.Setenv("NN_CONFIG", cfgPath)
	body := "inbox = \"notes\"\n[search]\nlimit = \"x\"\nlimt = 5\n[tui]\ntheme = \"dark\"\n"
	if err := os.WriteFile(cfgPath, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	_, problems, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	WarnConfig(&buf, problems)

	lines := strings.Split(strings.TrimSuffix(buf.String(), "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("warnings = %q, want two lines", buf.String())
	}
	want := []string{
		`nn: warning: config: inbox was renamed to vault.inbox and is ignored; move it under [vault]: inbox = "notes"`,
		`nn: warning: config: search.limit = "x": want an integer; using 20`,
	}
	for i := range want {
		if lines[i] != want[i] {
			t.Errorf("line %d = %q, want %q", i, lines[i], want[i])
		}
	}
}
