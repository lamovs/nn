package config

import (
	"slices"
	"strings"
)

// DefaultFile: dynamic tables and [tui] are commented-out examples.
func DefaultFile() []byte {
	var b strings.Builder
	b.WriteString("# nn configuration, every key at its default.\n")
	b.WriteString("# Only vault.root is required. \"nn config KEY VALUE\" sets a key in place.\n")

	current := ""
	for i := range schema {
		k := &schema[i]
		sec := strings.Join(section(k.segs), ".")
		commented := k.Status == StatusReserved || slices.ContainsFunc(k.segs, isPlaceholder)
		prefix := ""
		if commented {
			prefix = "# "
		}
		if k.Type == TypeTable {
			b.WriteString("\n# " + k.Doc + "\n")
			b.WriteString(prefix + "[" + k.Path + "]\n")
			continue
		}
		if sec != current {
			current = sec
			b.WriteString("\n")
			if doc := sectionDoc(k.segs); doc != "" {
				b.WriteString("# " + doc + "\n")
			}
			b.WriteString(prefix + "[" + sec + "]\n")
		}
		b.WriteString("# " + k.Doc + "\n")
		line := prefix + k.segs[len(k.segs)-1] + " = " + literal(k.Default)
		if c := constraint(k); c != "" {
			line += "  # " + c
		}
		b.WriteString(line + "\n")
	}
	return []byte(b.String())
}

func sectionDoc(segs []string) string {
	for n := len(segs) - 1; n > 0; n-- {
		if doc, ok := sectionDocs[strings.Join(segs[:n], ".")]; ok {
			return doc
		}
	}
	return ""
}

func constraint(k *Key) string {
	if len(k.Enum) > 0 {
		return strings.Join(k.Enum, ", ")
	}
	return k.Range.String()
}

// KeyTable is the Markdown table README.md carries between its config-table markers.
func KeyTable() string {
	var b strings.Builder
	b.WriteString("| Key | Type | Default | Values | Env | Description |\n")
	b.WriteString("| --- | --- | --- | --- | --- | --- |\n")
	for i := range schema {
		k := &schema[i]
		key, def := "`"+k.Path+"`", ""
		if k.Type == TypeTable {
			key = "`[" + k.Path + "]`"
		} else {
			def = "`" + literal(k.Default) + "`"
		}
		typ := string(k.Type)
		if k.Status == StatusReserved {
			typ += ", reserved"
		}
		values := constraint(k)
		if k.Tasks != nil {
			values = strings.TrimPrefix(values+"; TASK: "+strings.Join(k.Tasks, ", "), "; ")
		}
		env := ""
		if k.Env != "" {
			env = "`" + k.Env + "`"
		}
		cells := []string{key, typ, def, values, env, k.Doc}
		for j, c := range cells {
			cells[j] = strings.ReplaceAll(c, "|", `\|`)
		}
		b.WriteString("| " + strings.Join(cells, " | ") + " |\n")
	}
	return b.String()
}
