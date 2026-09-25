package config

import (
	"fmt"
	"math"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"time"
)

func convert(k *Key, v any) (val any, kind Kind, want string) {
	emptyOK := k.Default == ""
	switch k.Type {
	case TypeString, TypePath:
		s, ok := v.(string)
		if !ok {
			return nil, KindType, "a string"
		}
		return s, "", ""
	case TypeEnum:
		s, ok := v.(string)
		if !ok {
			return nil, KindType, "a string"
		}
		if (s == "" && emptyOK) || slices.Contains(k.Enum, s) {
			return s, "", ""
		}
		return nil, KindEnum, "one of " + strings.Join(k.Enum, ", ")
	case TypeInt:
		n, ok := v.(int64)
		if !ok {
			return nil, KindType, "an integer"
		}
		if !k.Range.contains(float64(n)) {
			return nil, KindRange, k.Range.String()
		}
		return int(n), "", ""
	case TypeFloat:
		var f float64
		switch x := v.(type) {
		case float64:
			f = x
		case int64:
			f = float64(x)
		default:
			return nil, KindType, "a number"
		}
		if math.IsNaN(f) || math.IsInf(f, 0) {
			return nil, KindType, "a number"
		}
		if !k.Range.contains(f) {
			return nil, KindRange, k.Range.String()
		}
		return f, "", ""
	case TypeBool:
		b, ok := v.(bool)
		if !ok {
			return nil, KindType, "true or false"
		}
		return b, "", ""
	case TypeDuration:
		s, ok := v.(string)
		if !ok {
			return nil, KindType, "a duration string such as \"10s\""
		}
		if s == "" && emptyOK {
			return time.Duration(0), "", ""
		}
		d, err := ParseDuration(s)
		if err != nil {
			return nil, KindType, "a duration such as 10s, 5m, 2h, 14d or 2w"
		}
		if !k.Range.contains(float64(d)) {
			return nil, KindRange, k.Range.String()
		}
		return d, "", ""
	case TypeList:
		switch x := v.(type) {
		case []string:
			return slices.Clone(x), "", ""
		case []any:
			out := make([]string, 0, len(x))
			for _, item := range x {
				s, ok := item.(string)
				if !ok {
					return nil, KindType, "a list of strings"
				}
				out = append(out, s)
			}
			return out, "", ""
		}
		return nil, KindType, "a list of strings"
	}
	return nil, KindType, string(k.Type)
}

// ParseDuration is time.ParseDuration plus whole days and weeks: "14d", "2w".
func ParseDuration(s string) (time.Duration, error) {
	s = strings.TrimSpace(s)
	for suffix, unit := range map[string]time.Duration{"d": 24 * time.Hour, "w": 7 * 24 * time.Hour} {
		num, ok := strings.CutSuffix(s, suffix)
		if !ok {
			continue
		}
		n, err := strconv.ParseInt(num, 10, 64)
		if err != nil || n > math.MaxInt64/int64(unit) || n < math.MinInt64/int64(unit) {
			return 0, fmt.Errorf("invalid duration %q", s)
		}
		return time.Duration(n) * unit, nil
	}
	return time.ParseDuration(s)
}

func formatDuration(d time.Duration) string {
	if d == 0 {
		return ""
	}
	if d%(24*time.Hour) == 0 {
		return strconv.FormatInt(int64(d/(24*time.Hour)), 10) + "d"
	}
	s := d.String()
	if strings.HasSuffix(s, "m0s") {
		s = strings.TrimSuffix(s, "0s")
	}
	if strings.HasSuffix(s, "h0m") {
		s = strings.TrimSuffix(s, "0m")
	}
	return s
}

func parseRaw(k *Key, raw string) (any, error) {
	trimmed := strings.TrimSpace(raw)
	switch k.Type {
	case TypeString, TypePath, TypeEnum, TypeDuration:
		return trimmed, nil
	case TypeInt:
		n, err := strconv.ParseInt(trimmed, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("%s: want an integer, got %q", k.Path, raw)
		}
		return n, nil
	case TypeFloat:
		f, err := strconv.ParseFloat(trimmed, 64)
		if err != nil {
			return nil, fmt.Errorf("%s: want a number, got %q", k.Path, raw)
		}
		return f, nil
	case TypeBool:
		b, err := strconv.ParseBool(trimmed)
		if err != nil {
			return nil, fmt.Errorf("%s: want true or false, got %q", k.Path, raw)
		}
		return b, nil
	case TypeList:
		return nil, fmt.Errorf("%s is a list: edit the file", k.Path)
	}
	return nil, fmt.Errorf("%s cannot be set", k.Path)
}

func literal(v any) string {
	switch x := v.(type) {
	case string:
		return TOMLString(x)
	case int:
		return strconv.Itoa(x)
	case int64:
		return strconv.FormatInt(x, 10)
	case float64:
		switch {
		case math.IsNaN(x):
			return "nan"
		case math.IsInf(x, 1):
			return "inf"
		case math.IsInf(x, -1):
			return "-inf"
		}
		s := strconv.FormatFloat(x, 'f', -1, 64)
		if !strings.Contains(s, ".") {
			s += ".0"
		}
		return s
	case bool:
		return strconv.FormatBool(x)
	case time.Duration:
		return TOMLString(formatDuration(x))
	case time.Time:
		return x.Format(time.RFC3339)
	case []string:
		items := make([]string, len(x))
		for i, s := range x {
			items[i] = TOMLString(s)
		}
		return "[" + strings.Join(items, ", ") + "]"
	case []any:
		items := make([]string, len(x))
		for i, item := range x {
			items[i] = literal(item)
		}
		return "[" + strings.Join(items, ", ") + "]"
	case map[string]any:
		return "{...}"
	case []map[string]any:
		return "[{...}]"
	}
	return fmt.Sprint(v)
}

// shellWord renders v the way it would be typed after nn config KEY.
func shellWord(v any) string {
	switch x := v.(type) {
	case string:
		if x == "" || strings.ContainsAny(x, " \t\"'\\$`") {
			return TOMLString(x)
		}
		return x
	case time.Duration:
		return shellWord(formatDuration(x))
	}
	return literal(v)
}

// TOMLString renders s as a quoted TOML basic string.
func TOMLString(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		case '\b':
			b.WriteString(`\b`)
		case '\t':
			b.WriteString(`\t`)
		case '\n':
			b.WriteString(`\n`)
		case '\f':
			b.WriteString(`\f`)
		case '\r':
			b.WriteString(`\r`)
		default:
			if r < 0x20 || r == 0x7f {
				fmt.Fprintf(&b, `\u%04X`, r)
				continue
			}
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String()
}

// setIn: a struct held in a map is copied out and back, since map
// elements are not addressable.
func setIn(v reflect.Value, path []string, val any) bool {
	if len(path) == 0 {
		rv := reflect.ValueOf(val)
		if !rv.IsValid() || !rv.Type().AssignableTo(v.Type()) {
			return false
		}
		v.Set(rv)
		return true
	}
	switch v.Kind() {
	case reflect.Struct:
		f, ok := fieldByKey(v, path[0])
		return ok && setIn(f, path[1:], val)
	case reflect.Map:
		if v.IsNil() {
			v.Set(reflect.MakeMap(v.Type()))
		}
		mk := reflect.ValueOf(path[0])
		elem := reflect.New(v.Type().Elem()).Elem()
		if cur := v.MapIndex(mk); cur.IsValid() {
			elem.Set(cur)
		}
		if !setIn(elem, path[1:], val) {
			return false
		}
		v.SetMapIndex(mk, elem)
		return true
	}
	return false
}

func getIn(v reflect.Value, path []string) (any, bool) {
	for _, seg := range path {
		switch v.Kind() {
		case reflect.Struct:
			f, ok := fieldByKey(v, seg)
			if !ok {
				return nil, false
			}
			v = f
		case reflect.Map:
			v = v.MapIndex(reflect.ValueOf(seg))
			if !v.IsValid() {
				return nil, false
			}
		default:
			return nil, false
		}
	}
	return v.Interface(), true
}

func fieldByKey(v reflect.Value, key string) (reflect.Value, bool) {
	t := v.Type()
	for i := range t.NumField() {
		if tag, ok := t.Field(i).Tag.Lookup("key"); ok && tag == key {
			return v.Field(i), true
		}
	}
	return reflect.Value{}, false
}
