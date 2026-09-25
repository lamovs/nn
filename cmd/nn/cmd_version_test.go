package main

import (
	"strings"
	"testing"
)

func TestVersionPrintsVersion(t *testing.T) {
	stdout, _, code := runCmd(t, "", "version")
	if code != 0 {
		t.Fatalf("code = %d", code)
	}
	if !strings.HasPrefix(stdout, "nn version ") {
		t.Errorf("stdout = %q", stdout)
	}
}

func TestVersionMisuseMatchesOtherCommands(t *testing.T) {
	stdout, stderr, code := runCmd(t, "", "version", "--json")
	if code != 2 {
		t.Errorf("code = %d, want 2", code)
	}
	if stdout != "" {
		t.Errorf("stdout = %q, want nothing", stdout)
	}
	if !strings.HasPrefix(stderr, "nn: version: ") {
		t.Errorf("stderr = %q, want it to start with the usual nn: <verb>: prefix", stderr)
	}
	if !strings.Contains(stderr, `run "nn help version" for examples`) {
		t.Errorf("stderr = %q, want the help hint every other command prints", stderr)
	}
	if !strings.Contains(stderr, `"--json"`) {
		t.Errorf("stderr = %q, want it to name the offending argument", stderr)
	}
}
