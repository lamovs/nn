package config

import (
	"fmt"
	"reflect"
	"slices"
	"sort"
	"strings"
)

type checker struct {
	cfg      Config
	problems []Problem

	badConsent map[int]string // ai.consent problem index -> entry name
}

// check does not apply the environment and does not require vault.root.
func check(doc map[string]any) *checker {
	c := &checker{cfg: Default(), badConsent: map[int]string{}}
	c.cfg.sources = map[string]Source{}
	c.declareProfiles(doc)
	c.walk(nil, doc)
	c.checkRefs()
	return c
}

func (c *checker) add(p Problem) { c.problems = append(c.problems, p) }

func (c *checker) addValue(kind Kind, key, detail, outcome, fix string) {
	c.add(Problem{Kind: kind, Key: key, Message: detail + "; " + outcome, Fix: fix, detail: detail})
}

// declareProfiles runs before any profile key is read, so a key the file
// leaves out gets the profile's default, not a zero value.
func (c *checker) declareProfiles(doc map[string]any) {
	ai, _ := doc["ai"].(map[string]any)
	profiles, _ := ai["profiles"].(map[string]any)
	for name, v := range profiles {
		if _, ok := v.(map[string]any); !ok {
			continue
		}
		if _, ok := c.cfg.AI.Profiles[name]; !ok {
			c.cfg.AI.Profiles[name] = defaultProfile(name)
		}
	}
}

func (c *checker) walk(prefix []string, m map[string]any) {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, name := range keys {
		v := m[name]
		path := append(slices.Clone(prefix), name)
		if len(prefix) == 0 && c.topLevel(name, v) {
			continue
		}
		if k, ok := lookup(path); ok {
			c.setKey(k, path, v)
			continue
		}
		if isTable(path) {
			sub, ok := v.(map[string]any)
			if !ok {
				c.add(Problem{
					Kind:    KindType,
					Key:     joinKey(path),
					Message: fmt.Sprintf("%s = %s: want a table [%s]; ignored", joinKey(path), literal(v), joinKey(path)),
					Fix:     fmt.Sprintf("replace it with a [%s] section", joinKey(path)),
				})
				continue
			}
			c.walk(path, sub)
			continue
		}
		c.unknown(path)
	}
}

func (c *checker) topLevel(name string, v any) bool {
	if name == "tui" {
		c.add(Problem{Kind: KindReserved, Key: "tui", Message: "[tui]: reserved, ignored"})
		return true
	}
	for _, m := range moved {
		if name != m.old {
			continue
		}
		// [editor] is the new section; only the old string moved.
		if _, table := v.(map[string]any); table {
			return false
		}
		segs := strings.Split(m.new, ".")
		c.add(Problem{
			Kind:    KindMoved,
			Key:     m.old,
			Message: fmt.Sprintf("%s was renamed to %s and is ignored", m.old, m.new),
			Fix:     fmt.Sprintf("move it under [%s]: %s = %s", joinKey(section(segs)), segs[len(segs)-1], literal(v)),
		})
		return true
	}
	return false
}

func (c *checker) setKey(k *Key, path []string, v any) {
	key := joinKey(path)
	val, kind, want := convert(k, v)
	if kind == "" && k.Path == "vault.inbox" && strings.TrimSpace(val.(string)) == "" {
		kind, want = KindRange, "a directory name"
	}
	if kind != "" {
		// A built-in profile keeps its own default, not the schema's generic one.
		def := defaultOf(k.Path)
		switch k.Path {
		case "ai.profiles.NAME.engine":
			def = c.cfg.AI.Profiles[path[2]].Engine
		case "ai.profiles.NAME.model":
			def = c.cfg.AI.Profiles[path[2]].Model
		}
		if k.Path == "ai.consent.KEY" {
			c.badConsent[len(c.problems)] = path[2]
		}
		c.addValue(kind, key,
			fmt.Sprintf("%s = %s: want %s", key, literal(v), want),
			"using "+literal(def),
			setFix(k, path, def))
		return
	}
	mustSet(reflect.ValueOf(&c.cfg).Elem(), path, val)
	c.cfg.sources[key] = SourceFile
}

func setFix(k *Key, path []string, value any) string {
	if k.Type == TypeList {
		return handFix(path, value)
	}
	return configFix(path, value)
}

// configFix falls back to a by-hand line for a key nn config cannot take
// as TOML spells it, such as ai.consent."a.b".
func configFix(path []string, value any) string {
	if joinKey(path) != strings.Join(path, ".") {
		return handFix(path, value)
	}
	return fmt.Sprintf("nn config %s %s", joinKey(path), shellWord(value))
}

func handFix(path []string, value any) string {
	return fmt.Sprintf("set it by hand under [%s]: %s = %s", joinKey(section(path)), TOMLKey(path[len(path)-1]), literal(value))
}

func (c *checker) unknown(path []string) {
	key := joinKey(path)
	p := Problem{Kind: KindUnknown, Key: key}
	k, ok := lookupAnyTask(path)
	hint := suggest(path)
	switch {
	case ok:
		p.Message = fmt.Sprintf("%s: %s applies to %s only; ignored", key, path[len(path)-1], strings.Join(k.Tasks, ", "))
		p.Fix = "remove it"
	case hint != "":
		p.Message = fmt.Sprintf("unknown key %s; ignored", key)
		p.Fix = "did you mean " + hint + "?"
	default:
		p.Message = fmt.Sprintf("unknown key %s; ignored", key)
		p.Fix = `remove it; "nn config --defaults" lists every key`
	}
	c.add(p)
}

// checkRefs runs once every key is read, so every name it checks exists.
func (c *checker) checkRefs() {
	ai := &c.cfg.AI
	names := make([]string, 0, len(ai.Profiles))
	for name := range ai.Profiles {
		names = append(names, name)
	}
	sort.Strings(names)

	for _, name := range names {
		p := ai.Profiles[name]
		prefix := joinKey([]string{"ai", "profiles", name})
		if slices.Contains(builtinProfiles, name) && p.Engine != name {
			c.addValue(KindRef, prefix+".engine",
				fmt.Sprintf("%s.engine = %s: built-in profile %s runs on %s", prefix, literal(p.Engine), name, name),
				"using "+literal(name),
				fmt.Sprintf("remove engine from [%s], or name the profile differently", prefix))
			p.Engine = name
			ai.Profiles[name] = p
			delete(c.cfg.sources, prefix+".engine")
		}
		switch {
		case p.Engine == EngineCommand && len(p.Command) == 0:
			c.addValue(KindRef, prefix+".command",
				fmt.Sprintf("%s: engine %s needs a command", prefix, literal(EngineCommand)),
				"the profile cannot run",
				fmt.Sprintf(`set it by hand under [%s]: command = ["my-model", "{prompt}"]`, prefix))
		case p.Engine != EngineCommand && len(p.Command) > 0:
			fix := configFix([]string{"ai", "profiles", name, "engine"}, EngineCommand)
			if slices.Contains(builtinProfiles, name) {
				fix = fmt.Sprintf("remove command from [%s]", prefix)
			}
			c.addValue(KindRef, prefix+".command",
				fmt.Sprintf("%s.command is set, but engine is %s", prefix, literal(p.Engine)),
				"ignored",
				fix)
			p.Command = defaultProfile(name).Command
			ai.Profiles[name] = p
			delete(c.cfg.sources, prefix+".command")
		}
	}

	known := strings.Join(names, ", ")
	if _, ok := ai.Profiles[ai.Profile]; !ok {
		def := defaultOf("ai.profile")
		c.addValue(KindRef, "ai.profile",
			fmt.Sprintf("ai.profile = %s: no such profile (profiles: %s)", literal(ai.Profile), known),
			"using "+literal(def),
			fmt.Sprintf("declare [ai.profiles.%s], or: nn config ai.profile %s", TOMLKey(ai.Profile), shellWord(def)))
		ai.Profile = def.(string)
		delete(c.cfg.sources, "ai.profile")
	}
	for _, task := range tasks {
		t := ai.Tasks[task]
		if t.Profile == "" {
			continue
		}
		if _, ok := ai.Profiles[t.Profile]; ok {
			continue
		}
		key := "ai.tasks." + task + ".profile"
		c.addValue(KindRef, key,
			fmt.Sprintf("%s = %s: no such profile (profiles: %s)", key, literal(t.Profile), known),
			"using ai.profile",
			fmt.Sprintf("declare [ai.profiles.%s], or: nn config %s %s", TOMLKey(t.Profile), key, shellWord("")))
		t.Profile = ""
		ai.Tasks[task] = t
		delete(c.cfg.sources, key)
	}

	// A name consent is never asked for gets no fix but removal.
	for i, name := range c.badConsent {
		if !consentKnown(ai.Profiles, name) {
			c.problems[i].Fix = "remove it"
		}
	}

	consentKeys := make([]string, 0, len(ai.Consent))
	for k := range ai.Consent {
		consentKeys = append(consentKeys, k)
	}
	sort.Strings(consentKeys)
	for _, k := range consentKeys {
		if consentKnown(ai.Profiles, k) {
			continue
		}
		key := joinKey([]string{"ai", "consent", k})
		c.addValue(KindRef, key,
			fmt.Sprintf("%s: not an engine (%s) or a profile with engine %s", key, strings.Join(builtinProfiles, ", "), literal(EngineCommand)),
			"ignored",
			"remove it")
		delete(ai.Consent, k)
		delete(c.cfg.sources, key)
	}
	def := defaultOf("ai.consent.KEY").(string)
	for _, name := range names {
		if !consentKnown(ai.Profiles, name) {
			continue
		}
		if _, ok := ai.Consent[name]; !ok {
			ai.Consent[name] = def
		}
	}
}

func consentKnown(profiles map[string]Profile, key string) bool {
	if slices.Contains(builtinProfiles, key) {
		return true
	}
	p, ok := profiles[key]
	return ok && p.Engine == EngineCommand
}
