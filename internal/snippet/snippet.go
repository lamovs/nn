// Package snippet fills in {{name}} and {{name:default}} placeholders in a
// plain string of code.
package snippet

import (
	"regexp"
	"strings"
)

// Placeholder is one named hole in a snippet: {{name}} has no default,
// {{name:default}} does (which may itself be empty).
type Placeholder struct {
	Name       string
	Default    string
	HasDefault bool
}

var pattern = regexp.MustCompile(`\{\{\s*([A-Za-z_][A-Za-z0-9_]*)\s*(?::([^{}]*))?\s*\}\}`)

// Find returns every placeholder in code, first-appearance order, deduped.
func Find(code string) []Placeholder {
	seen := map[string]bool{}
	var out []Placeholder
	for _, m := range pattern.FindAllStringSubmatch(code, -1) {
		name := m[1]
		if seen[name] {
			continue
		}
		seen[name] = true
		out = append(out, Placeholder{Name: name, Default: strings.TrimSpace(m[2]), HasDefault: m[0] != "" && strings.Contains(m[0], ":")})
	}
	return out
}

// Missing returns the placeholder names with neither a default nor a value
// in values.
func Missing(code string, values map[string]string) []string {
	var out []string
	for _, ph := range Find(code) {
		if ph.HasDefault {
			continue
		}
		if _, ok := values[ph.Name]; ok {
			continue
		}
		out = append(out, ph.Name)
	}
	return out
}

// Render substitutes each placeholder: values[name], else its own default,
// else it is left as written.
func Render(code string, values map[string]string) string {
	return pattern.ReplaceAllStringFunc(code, func(m string) string {
		sub := pattern.FindStringSubmatch(m)
		name, hasDefault := sub[1], strings.Contains(m, ":")
		if v, ok := values[name]; ok {
			return v
		}
		if hasDefault {
			return strings.TrimSpace(sub[2])
		}
		return m
	})
}
