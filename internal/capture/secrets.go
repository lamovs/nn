package capture

import (
	"math"
	"regexp"
	"strings"
)

// Secret is a possible credential found in captured text, with its excerpt
// masked.
type Secret struct {
	Kind    string
	Line    int
	Excerpt string
}

type secretRule struct {
	kind  string
	re    *regexp.Regexp
	keep  int // leading characters the excerpt shows; -1 shows it whole
	check func(match string) bool
}

var secretRules = []secretRule{
	{kind: "private-key", re: regexp.MustCompile(`-----BEGIN [A-Z0-9 ]*PRIVATE KEY[A-Z ]*-----`), keep: -1},
	{kind: "github-token", re: regexp.MustCompile(`\bgh[pousr]_[A-Za-z0-9]{36,}`), keep: 6},
	{kind: "github-token", re: regexp.MustCompile(`\bgithub_pat_[A-Za-z0-9_]{22,}`), keep: 13},
	{kind: "anthropic-key", re: regexp.MustCompile(`\bsk-ant-[A-Za-z0-9_-]{20,}`), keep: 9},
	{
		kind: "openai-key",
		re:   regexp.MustCompile(`\bsk-(?:proj-)?[A-Za-z0-9_-]{20,}`),
		keep: 5,
		check: func(m string) bool {
			body := strings.TrimPrefix(strings.TrimPrefix(m, "sk-"), "proj-")
			return !strings.HasPrefix(m, "sk-ant-") && mixedCharacters(body) && entropy(body) >= 3
		},
	},
	{kind: "aws-key", re: regexp.MustCompile(`\b(?:AKIA|ASIA)[A-Z0-9]{16}\b`), keep: 6},
	{kind: "slack-token", re: regexp.MustCompile(`\bxox[abprs]-[A-Za-z0-9-]{10,}`), keep: 7},
	{kind: "jwt", re: regexp.MustCompile(`\beyJ[A-Za-z0-9_-]{10,}\.eyJ[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}`), keep: 6},
}

// assignmentPattern matches "api_key = value" and its JSON/YAML/query spellings.
var assignmentPattern = regexp.MustCompile(`(?i)([A-Za-z0-9_.-]*(?:key|token|secret|passwo?r?d|pwd|pass|credential|auth)[A-Za-z0-9_.-]*)["']?[ \t]*(?::=|=>|=|:)[ \t]*["']?([^\s"'` + "`" + `,;]+)`)

var (
	hexPattern  = regexp.MustCompile(`^[0-9a-fA-F]+$`)
	uuidPattern = regexp.MustCompile(`^\{?[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}\}?$`)
	identifier  = regexp.MustCompile(`^[A-Za-z_$][A-Za-z0-9_$]*(\.[A-Za-z_$][A-Za-z0-9_$]*)+$`)
)

// FindSecrets scans text for likely credentials, line by line.
func FindSecrets(text string) []Secret {
	var out []Secret
	for i, line := range strings.Split(text, "\n") {
		num := i + 1
		var spans [][2]int
		for _, rule := range secretRules {
			for _, m := range rule.re.FindAllStringIndex(line, -1) {
				match := line[m[0]:m[1]]
				if isPlaceholder(match) || rule.check != nil && !rule.check(match) {
					continue
				}
				spans = append(spans, [2]int{m[0], m[1]})
				out = append(out, Secret{Kind: rule.kind, Line: num, Excerpt: mask(match, rule.keep)})
			}
		}
		for _, m := range assignmentPattern.FindAllStringSubmatchIndex(line, -1) {
			key, value := line[m[2]:m[3]], line[m[4]:m[5]]
			if !highEntropySecret(value) || overlaps(spans, m[4], m[5]) {
				continue
			}
			spans = append(spans, [2]int{m[4], m[5]})
			out = append(out, Secret{Kind: "secret-assignment", Line: num, Excerpt: key + "=" + mask(value, 2)})
		}
	}
	return out
}

func overlaps(spans [][2]int, start, end int) bool {
	for _, s := range spans {
		if start < s[1] && s[0] < end {
			return true
		}
	}
	return false
}

func highEntropySecret(v string) bool {
	if len(v) < 12 || isPlaceholder(v) {
		return false
	}
	if hexPattern.MatchString(v) || uuidPattern.MatchString(v) {
		return false // git hashes, checksums, ids
	}
	if strings.ContainsAny(v, "()[]{}<>$%\\") || strings.Contains(v, "://") || identifier.MatchString(v) {
		return false
	}
	if strings.HasPrefix(v, "/") || strings.HasPrefix(v, "./") || strings.HasPrefix(v, "~") {
		return false
	}
	return mixedCharacters(v) && entropy(v) >= 3
}

func mixedCharacters(s string) bool {
	var letter, digit bool
	for _, r := range s {
		switch {
		case r >= '0' && r <= '9':
			digit = true
		case r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z':
			letter = true
		}
	}
	return letter && digit
}

func entropy(s string) float64 {
	if s == "" {
		return 0
	}
	counts := map[rune]int{}
	n := 0
	for _, r := range s {
		counts[r]++
		n++
	}
	var bits float64
	for _, c := range counts {
		p := float64(c) / float64(n)
		bits -= p * math.Log2(p)
	}
	return bits
}

var placeholders = []string{
	"example", "xxxx", "your", "changeme", "placeholder", "redacted", "secret_here",
	"<", "...", "****", "00000000", "1234567890",
}

func isPlaceholder(s string) bool {
	lower := strings.ToLower(s)
	for _, p := range placeholders {
		if strings.Contains(lower, p) {
			return true
		}
	}
	return false
}

func mask(s string, keep int) string {
	if keep < 0 {
		return s
	}
	if keep > len(s)/3 {
		keep = len(s) / 3
	}
	return s[:keep] + "********"
}
