package config

import (
	"reflect"
	"strings"
	"testing"
)

// TestConfigKoanfTagsHaveNoUnderscore guards the whole "env var cannot reach an
// underscored koanf key" bug class: the env loader maps "_"->"." (koanf's nesting
// delimiter), so any koanf leaf tag containing "_" is unreachable from the
// environment. Every koanf tag in the Config tree MUST be underscore-free.
func TestConfigKoanfTagsHaveNoUnderscore(t *testing.T) {
	var offenders []string
	seen := map[reflect.Type]bool{}

	var walk func(rt reflect.Type, prefix string)
	walk = func(rt reflect.Type, prefix string) {
		for rt.Kind() == reflect.Pointer {
			rt = rt.Elem()
		}
		if rt.Kind() == reflect.Map || rt.Kind() == reflect.Slice {
			el := rt.Elem()
			for el.Kind() == reflect.Pointer {
				el = el.Elem()
			}
			if el.Kind() == reflect.Struct {
				walk(el, prefix)
			}
			return
		}
		if rt.Kind() != reflect.Struct || seen[rt] {
			return
		}
		seen[rt] = true
		for i := 0; i < rt.NumField(); i++ {
			f := rt.Field(i)
			name := strings.Split(f.Tag.Get("koanf"), ",")[0]
			if name == "-" {
				continue
			}
			// An untagged field (incl. anonymous/embedded structs) keeps the
			// parent prefix so its tagged children are still reached and checked.
			key := prefix
			if name != "" {
				if prefix != "" {
					key = prefix + "." + name
				} else {
					key = name
				}
				if strings.Contains(name, "_") {
					offenders = append(offenders, key)
				}
			}
			// Recurse into every field type; walk is a no-op for scalar leaf kinds.
			walk(f.Type, key)
		}
	}

	walk(reflect.TypeOf(Config{}), "")
	if len(offenders) > 0 {
		t.Fatalf("koanf tags must be underscore-free (env vars nest on '_'); offenders:\n  %s",
			strings.Join(offenders, "\n  "))
	}
}
