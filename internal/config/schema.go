package config

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
)

type Type string

const (
	TypeString   Type = "string"
	TypePath     Type = "path" // a string with "~" expanded
	TypeInt      Type = "int"
	TypeFloat    Type = "float"
	TypeBool     Type = "bool"
	TypeEnum     Type = "enum"
	TypeDuration Type = "duration" // Go duration, plus Nd and Nw
	TypeList     Type = "list"     // list of strings
	TypeTable    Type = "table"    // a reserved section only
)

type Status string

const (
	StatusActive   Status = "active"
	StatusReserved Status = "reserved"
)

const (
	placeholderName = "NAME" // ai.profiles.NAME: any profile name
	placeholderTask = "TASK" // ai.tasks.TASK: one of tasks
	placeholderKey  = "KEY"  // ai.consent.KEY: an engine or a command profile
)

// Range bounds a numeric or duration key. The zero Range bounds nothing.
type Range struct {
	Min, Max       float64
	HasMin, HasMax bool

	// MinExclusive puts Min itself out of range: "> 0" rather than ">= 0".
	MinExclusive bool
}

func (r Range) contains(x float64) bool {
	switch {
	case r.HasMin && r.MinExclusive && x <= r.Min:
		return false
	case r.HasMin && x < r.Min:
		return false
	case r.HasMax && x > r.Max:
		return false
	}
	return true
}

func (r Range) String() string {
	num := func(f float64) string { return strconv.FormatFloat(f, 'f', -1, 64) }
	switch {
	case r.HasMin && r.HasMax:
		return num(r.Min) + ".." + num(r.Max)
	case r.HasMin && r.MinExclusive:
		return "> " + num(r.Min)
	case r.HasMin:
		return ">= " + num(r.Min)
	case r.HasMax:
		return "<= " + num(r.Max)
	}
	return ""
}

func between(lo, hi float64) Range { return Range{Min: lo, Max: hi, HasMin: true, HasMax: true} }
func atLeast(lo float64) Range     { return Range{Min: lo, HasMin: true} }
func positive() Range              { return Range{Min: 0, HasMin: true, MinExclusive: true} }

type Key struct {
	Path string // dotted; a dynamic table has a placeholder segment
	Type Type

	// Default: an empty string also allows an empty value where Enum or
	// Type would otherwise refuse one.
	Default any

	Enum  []string
	Range Range
	Env   string

	Doc    string
	Status Status

	Tasks []string // limits a ai.tasks.TASK key to these; nil allows every task

	segs []string
}

var tasks = []string{"shot", "title", "ask", "filter", "last", "triage", "url", "digest"}

var builtinProfiles = []string{"claude", "codex"} // each runs on the engine of its own name

// builtinModels: a built-in profile not listed leaves the model to its engine.
var builtinModels = map[string]string{"claude": "sonnet"}

func Tasks() []string { return slices.Clone(tasks) } // a copy

func BuiltinProfiles() []string { return slices.Clone(builtinProfiles) } // a copy

const EngineCommand = "command"

var captureTasks = []string{"shot", "title"}

var schema = []Key{
	{Path: "vault.root", Type: TypePath, Default: "", Env: "NN_ROOT",
		Doc: "Vault directory; required. NN_ROOT is stronger."},
	{Path: "vault.inbox", Type: TypeString, Default: "nn",
		Doc: "Directory for new notes, relative to vault.root."},

	{Path: "editor.command", Type: TypeString, Default: "",
		Doc: "Editor command line; empty: VISUAL, EDITOR, nvim, vi."},

	{Path: "capture.ocr", Type: TypeBool, Default: true,
		Doc: "OCR images on capture."},
	{Path: "capture.similar_threshold", Type: TypeFloat, Default: 0.6, Range: between(0, 1),
		Doc: "Score a note needs to be listed as similar to a capture."},
	{Path: "capture.similar_limit", Type: TypeInt, Default: int64(3), Range: atLeast(0),
		Doc: "Most similar notes listed after a capture."},

	{Path: "hooks.post_save", Type: TypeString, Default: "",
		Doc: "Shell command run after a note is saved (sh -c); empty: none."},
	{Path: "hooks.post_save_timeout", Type: TypeDuration, Default: "10s", Range: positive(),
		Doc: "How long post_save may run."},

	{Path: "shot.copy_text", Type: TypeBool, Default: false,
		Doc: "Copy the recognized text of a screenshot to the clipboard."},
	{Path: "shot.tool", Type: TypeEnum, Default: "auto",
		Enum: []string{"auto", "grim", "spectacle", "gnome-screenshot", "maim", "import"},
		Doc:  "Screenshot tool on Linux; auto picks one for the session."},

	{Path: "ocr.langs", Type: TypeList, Default: []string{},
		Doc: "OCR languages; empty: OMARCHY_OCR_LANGS, then Russian and English."},
	{Path: "ocr.tesseract_psm", Type: TypeInt, Default: int64(11), Range: between(0, 13),
		Doc: "Tesseract page segmentation mode; changing it needs nn ocr --reindex --force (the cache keys on sha+langs, not psm)."},

	{Path: "search.limit", Type: TypeInt, Default: int64(20), Range: atLeast(1),
		Doc: "Results nn s prints."},
	{Path: "search.layout_fallback", Type: TypeBool, Default: true,
		Doc: "Retry a query typed in the other keyboard layout."},

	{Path: "ls.sort", Type: TypeEnum, Default: "modified",
		Enum: []string{"modified", "date", "opened", "title"},
		Doc:  "Order of nn ls."},
	{Path: "ls.limit", Type: TypeInt, Default: int64(0), Range: atLeast(0),
		Doc: "Rows nn ls prints; 0 is all."},

	{Path: "show.images", Type: TypeEnum, Default: "auto", Enum: []string{"auto", "never"},
		Doc: "Inline images in nn show."},
	{Path: "show.ocr", Type: TypeBool, Default: false,
		Doc: "Print the OCR text under each image in nn show."},

	{Path: "graph.format", Type: TypeEnum, Default: "mermaid", Enum: []string{"mermaid", "dot", "json"},
		Doc: "Format of nn graph."},

	{Path: "stats.by", Type: TypeEnum, Default: "week", Enum: []string{"tag", "week", "month", "repo", "via"},
		Doc: "Breakdown of nn stats."},

	{Path: "output.color", Type: TypeEnum, Default: "auto", Enum: []string{"auto", "always", "never"},
		Doc: "Color in output; NO_COLOR is stronger."},

	{Path: "obsidian.check", Type: TypeBool, Default: true,
		Doc: "nn doctor checks that the vault is registered in Obsidian."},

	{Path: "state.track_opens", Type: TypeBool, Default: true,
		Doc: "Record note opens, for ranking and --sort opened."},
	{Path: "state.half_life", Type: TypeDuration, Default: "14d", Range: positive(),
		Doc: "Half-life of a recorded open in ranking."},

	{Path: "notify.enabled", Type: TypeBool, Default: true,
		Doc: "Desktop notifications."},

	{Path: "ai.mode", Type: TypeEnum, Default: "background", Enum: []string{"auto", "wait", "background"},
		Doc: "Capture AI: wait before saving, or background after; auto waits on a terminal."},
	{Path: "ai.profile", Type: TypeString, Default: "claude",
		Doc: "Default AI profile."},
	{Path: "ai.timeout", Type: TypeDuration, Default: "120s", Range: positive(),
		Doc: "Default AI run timeout."},

	{Path: "ai.context.notes", Type: TypeInt, Default: int64(8), Range: atLeast(1),
		Doc: "Most notes whose excerpts the model gets."},
	{Path: "ai.context.chars", Type: TypeInt, Default: int64(12000), Range: atLeast(1000),
		Doc: "Most characters of excerpts the model gets."},
	{Path: "ai.context.search_rounds", Type: TypeInt, Default: int64(1), Range: between(0, 3),
		Doc: "Follow-up searches the model may ask nn for."},

	{Path: "ai.profiles.NAME.engine", Type: TypeEnum, Default: "claude",
		Enum: []string{"claude", "codex", EngineCommand},
		Doc:  "Engine; built-in profiles claude and codex keep their own."},
	{Path: "ai.profiles.NAME.model", Type: TypeString, Default: "",
		Doc: "Model; empty: engine default. The built-in claude profile uses sonnet."},
	{Path: "ai.profiles.NAME.effort", Type: TypeEnum, Default: "",
		Enum: []string{"low", "medium", "high", "max"},
		Doc:  "Reasoning effort; empty: engine default."},
	{Path: "ai.profiles.NAME.command", Type: TypeList, Default: []string{},
		Doc: "Argv for engine command, with {prompt} {image} {model} {effort}."},
	{Path: "ai.profiles.NAME.timeout", Type: TypeDuration, Default: "", Range: positive(),
		Doc: "Run timeout; empty: ai.timeout."},

	{Path: "ai.tasks.TASK.profile", Type: TypeString, Default: "",
		Doc: "Profile for the task; empty: ai.profile."},
	{Path: "ai.tasks.TASK.run", Type: TypeEnum, Default: "always",
		Enum: []string{"always", "flag", "never"}, Tasks: captureTasks,
		Doc: "shot and title only: always, only with --ai (flag), or never."},
	{Path: "ai.tasks.TASK.prompt_file", Type: TypePath, Default: "",
		Doc: "File whose text replaces the built-in prompt."},
	{Path: "ai.tasks.TASK.image", Type: TypeEnum, Default: "discard",
		Enum: []string{"discard", "embed"}, Tasks: []string{"shot"},
		Doc: "shot only: keep the screenshot in the note (embed) or not."},

	{Path: "ai.consent.KEY", Type: TypeEnum, Default: "ask",
		Enum: []string{"ask", "always", "never"},
		Doc:  "Consent to run an engine; KEY: claude, codex or a command profile."},

	{Path: "tui", Type: TypeTable, Status: StatusReserved,
		Doc: "Reserved for the terminal UI; its keys are ignored."},
}

var sectionDocs = map[string]string{
	"capture":     "nn add and nn shot.",
	"ai.context":  "What ask, digest and triage send: nn searches, the model gets excerpts only.",
	"ai.profiles": "A named engine setup. claude and codex exist without being declared.",
	"ai.tasks":    "Per-task settings; TASK: " + strings.Join(tasks, ", ") + ".",
	"ai.consent":  "Written by nn when you answer its consent question.",
}

// moved lists each key of the first config format with its replacement.
var moved = []struct{ old, new string }{
	{"root", "vault.root"},
	{"inbox", "vault.inbox"},
	{"editor", "editor.command"},
	{"ocr_langs", "ocr.langs"},
}

func init() {
	for i := range schema {
		k := &schema[i]
		k.segs = strings.Split(k.Path, ".")
		if k.Status == "" {
			k.Status = StatusActive
		}
		if k.Status == StatusReserved {
			continue
		}
		if _, kind, want := convert(k, k.Default); kind != "" {
			panic(fmt.Sprintf("config: default of %s: want %s", k.Path, want))
		}
	}
}

func Schema() []Key {
	out := make([]Key, len(schema))
	for i, k := range schema {
		k.Enum = slices.Clone(k.Enum)
		k.Tasks = slices.Clone(k.Tasks)
		k.segs = nil
		out[i] = k
	}
	return out
}

// lookup: a key limited to some tasks does not match any other task.
func lookup(path []string) (*Key, bool) {
	for i := range schema {
		k := &schema[i]
		if k.Status == StatusReserved || !matchSegs(k.segs, path) {
			continue
		}
		if k.Tasks != nil && !slices.Contains(k.Tasks, taskOf(k.segs, path)) {
			continue
		}
		return k, true
	}
	return nil, false
}

func lookupAnyTask(path []string) (*Key, bool) {
	for i := range schema {
		k := &schema[i]
		if k.Status != StatusReserved && matchSegs(k.segs, path) {
			return k, true
		}
	}
	return nil, false
}

func isTable(path []string) bool {
	for i := range schema {
		k := &schema[i]
		if k.Status == StatusReserved || len(path) >= len(k.segs) {
			continue
		}
		if matchSegs(k.segs[:len(path)], path) {
			return true
		}
	}
	return false
}

func matchSegs(pattern, path []string) bool {
	if len(pattern) != len(path) {
		return false
	}
	for i, p := range pattern {
		switch p {
		case placeholderName, placeholderKey:
		case placeholderTask:
			if !slices.Contains(tasks, path[i]) {
				return false
			}
		default:
			if p != path[i] {
				return false
			}
		}
	}
	return true
}

func taskOf(pattern, path []string) string {
	for i, p := range pattern {
		if p == placeholderTask && i < len(path) {
			return path[i]
		}
	}
	return ""
}

func section(path []string) []string { return path[:len(path)-1] }

// suggest: "" when nothing is within an edit distance of 2.
func suggest(path []string) string {
	want := joinKey(path)
	best, bestDist := "", 3
	consider := func(segs []string) {
		cand := joinKey(segs)
		if cand == want {
			return
		}
		if d := levenshtein(want, cand); d < bestDist {
			best, bestDist = cand, d
		}
	}
	for i := range schema {
		k := &schema[i]
		for n := 1; n <= len(k.segs); n++ {
			pattern := k.segs[:n]
			if n > len(path) {
				break
			}
			if !slices.ContainsFunc(pattern, isPlaceholder) {
				consider(pattern)
				continue
			}
			for _, inst := range instantiate(pattern, path) {
				consider(inst)
			}
		}
	}
	return best
}

func isPlaceholder(s string) bool {
	return s == placeholderName || s == placeholderTask || s == placeholderKey
}

func instantiate(pattern, path []string) [][]string {
	out := [][]string{slices.Clone(pattern)}
	for i, p := range pattern {
		if !isPlaceholder(p) {
			continue
		}
		var next [][]string
		for _, segs := range out {
			if p == placeholderTask {
				for _, task := range tasks {
					c := slices.Clone(segs)
					c[i] = task
					next = append(next, c)
				}
				continue
			}
			if i < len(path) {
				segs[i] = path[i]
				next = append(next, segs)
			}
		}
		out = next
	}
	return out
}

func levenshtein(a, b string) int {
	ra, rb := []rune(a), []rune(b)
	prev := make([]int, len(rb)+1)
	cur := make([]int, len(rb)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(ra); i++ {
		cur[0] = i
		for j := 1; j <= len(rb); j++ {
			cost := 1
			if ra[i-1] == rb[j-1] {
				cost = 0
			}
			cur[j] = min(prev[j]+1, cur[j-1]+1, prev[j-1]+cost)
		}
		prev, cur = cur, prev
	}
	return prev[len(rb)]
}

func joinKey(segs []string) string {
	parts := make([]string, len(segs))
	for i, s := range segs {
		parts[i] = TOMLKey(s)
	}
	return strings.Join(parts, ".")
}

func TOMLKey(s string) string {
	if s != "" && strings.IndexFunc(s, func(r rune) bool {
		return !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '-')
	}) < 0 {
		return s
	}
	return TOMLString(s)
}
