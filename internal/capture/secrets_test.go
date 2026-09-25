package capture

import (
	"strings"
	"testing"
)

func TestFindSecretsPositive(t *testing.T) {
	tests := []struct {
		name, line, kind, secret string
	}{
		// Fixtures are split so secret scanners do not flag them.
		{"github classic", "export GH_TOKEN=gh" + "p_A1b2C3d4E5f6G7h8I9j0K1l2M3n4O5p6Q7r8", "github-token", "gh" + "p_A1b2C3d4E5f6G7h8I9j0K1l2M3n4O5p6Q7r8"},
		{"github oauth", "gh" + "o_A1b2C3d4E5f6G7h8I9j0K1l2M3n4O5p6Q7r8 in the log", "github-token", "gh" + "o_A1b2C3d4E5f6G7h8I9j0K1l2M3n4O5p6Q7r8"},
		{"github fine grained", "github_pa" + "t_11ABCDE0A0aBcDeFgHiJkL_9zYxWvUtSrQpOnMlKjIhGfEdCbA0987654321", "github-token", "github_pa" + "t_11ABCDE0A0aBcDeFgHiJkL_9zYxWvUtSrQpOnMlKjIhGfEdCbA0987654321"},
		{"anthropic", "ANTHROPIC_API_KEY=sk-" + "ant-api03-7f2Kd9Lm4Qp1Rs8Tv3Wx6Yz0Ab5Cd2Ef", "anthropic-key", "sk-" + "ant-api03-7f2Kd9Lm4Qp1Rs8Tv3Wx6Yz0Ab5Cd2Ef"},
		{"openai", "key: sk-" + "proj-9Hs2Kd7Lm4Qp1Rs8Tv3Wx6Yz0Ab5Cd2Ef4Gh", "openai-key", "sk-" + "proj-9Hs2Kd7Lm4Qp1Rs8Tv3Wx6Yz0Ab5Cd2Ef4Gh"},
		{"aws", "aws_access_key_id = AK" + "IA2E0RTY7BNMQWZXCV", "aws-key", "AK" + "IA2E0RTY7BNMQWZXCV"},
		{"slack", "xox" + "b-2894751037-4827159630-9Fd2Kq7Lm4Zp1Rs8Tv", "slack-token", "xox" + "b-2894751037-4827159630-9Fd2Kq7Lm4Zp1Rs8Tv"},
		{"pem", "-----BEGIN OPENSSH PRIVATE KEY-----", "private-key", ""},
		{"jwt", "Authorization: Bearer eyJ" + "hbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9." + "eyJ" + "zdWIiOiIxMjM0NTY3ODkwIn0.dBjftJeZ4CVPmB92K27uhbUJU1p1r_wW1gFWFOEjXk", "jwt", ""},
		{"assignment", `db_password = "8Jk2Lm9Qp4Rs7Tv3Wx6Yz"`, "secret-assignment", "8Jk2Lm9Qp4Rs7Tv3Wx6Yz"},
		{"json token", `{"api_token": "aB3dE6fG9hJ2kL5mN8pQ"}`, "secret-assignment", "aB3dE6fG9hJ2kL5mN8pQ"},
		{"query string", "https://api.example.org/v1/items?access_token=Zx9Cv2Bn5Mq8Wr4Ty7Ui0Op", "secret-assignment", "Zx9Cv2Bn5Mq8Wr4Ty7Ui0Op"},
	}
	for _, tt := range tests {
		got := FindSecrets("first line\n" + tt.line + "\nlast line")
		if len(got) != 1 {
			t.Errorf("%s: FindSecrets = %+v, want one secret", tt.name, got)
			continue
		}
		s := got[0]
		if s.Kind != tt.kind || s.Line != 2 {
			t.Errorf("%s: kind %q line %d", tt.name, s.Kind, s.Line)
		}
		if tt.secret != "" && strings.Contains(s.Excerpt, tt.secret) {
			t.Errorf("%s: excerpt %q leaks the secret", tt.name, s.Excerpt)
		}
		if len(s.Excerpt) == 0 {
			t.Errorf("%s: empty excerpt", tt.name)
		}
	}
}

func TestFindSecretsNegative(t *testing.T) {
	clean := []string{
		"fix(links): resolve aliases, commit 3f2a9c1e8b7d6a5f4e3d2c1b0a9f8e7d6c5b4a39",
		"image digest sha256:9b2c1d4e5f60718293a4b5c6d7e8f90123456789abcdef0123456789abcdef01",
		"request id 550e8400-e29b-41d4-a716-446655440000 failed",
		"session_token: 550e8400-e29b-41d4-a716-446655440000",
		"password: hunter2",
		"token: see the internal docs for how to get one",
		`api_key = os.Getenv("OPENAI_API_KEY")`,
		"secret_key_base: ${RAILS_MASTER_KEY}",
		"export GITHUB_TOKEN=$GH_TOKEN",
		"aws_access_key_id = AK" + "IAIOSFODNN7EXAMPLE",
		"github token format: gh" + "p_xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx",
		"token_path = /etc/nn/credentials.json",
		"the risk-assessment-framework-2024 review",
		"keyboard shortcut: Cmd+Shift+4",
		"password = changeme123456",
		"key: value",
		"| password | see vault |",
		"sk-learn and scikit-learn are the same thing here",
		"private key handling is described below",
	}
	for _, line := range clean {
		if got := FindSecrets(line); len(got) != 0 {
			t.Errorf("false positive on %q: %+v", line, got)
		}
	}
	if got := FindSecrets(""); got != nil {
		t.Errorf("empty text = %+v", got)
	}
}

func TestFindSecretsMultiple(t *testing.T) {
	text := "ok\ngh" + "p_A1b2C3d4E5f6G7h8I9j0K1l2M3n4O5p6Q7r8 and password=7Hj3Kd9Lm2Qp5Rs8Tv\nsecond gh" + "p_Z9y8X7w6V5u4T3s2R1q0P9o8N7m6L5k4J3h2\n"
	got := FindSecrets(text)
	if len(got) != 3 {
		t.Fatalf("FindSecrets = %+v", got)
	}
	if got[0].Line != 2 || got[1].Line != 2 || got[2].Line != 3 {
		t.Errorf("lines = %d %d %d", got[0].Line, got[1].Line, got[2].Line)
	}
	kinds := map[string]int{}
	for _, s := range got {
		kinds[s.Kind]++
	}
	if kinds["github-token"] != 2 || kinds["secret-assignment"] != 1 {
		t.Errorf("kinds = %v", kinds)
	}

	// A token that also matches the assignment rule is reported once.
	single := FindSecrets("GITHUB_TOKEN=gh" + "p_A1b2C3d4E5f6G7h8I9j0K1l2M3n4O5p6Q7r8")
	if len(single) != 1 || single[0].Kind != "github-token" {
		t.Fatalf("overlapping rules = %+v", single)
	}
}

func TestMask(t *testing.T) {
	if got := mask("gh"+"p_abcdefghijklmnopqrstuvwxyz", 6); got != "ghp_ab********" {
		t.Errorf("mask = %q", got)
	}
	if got := mask("short", 6); got != "s********" {
		t.Errorf("short mask = %q", got)
	}
	if got := mask("-----BEGIN PRIVATE KEY-----", -1); got != "-----BEGIN PRIVATE KEY-----" {
		t.Errorf("header mask = %q", got)
	}
}
