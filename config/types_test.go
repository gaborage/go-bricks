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
	offenders := collectUnderscoredKoanfTags(reflect.TypeOf(Config{}), "", map[reflect.Type]bool{})
	if len(offenders) > 0 {
		t.Fatalf("koanf tags must be underscore-free (env vars nest on '_'); offenders:\n  %s",
			strings.Join(offenders, "\n  "))
	}
}

// collectUnderscoredKoanfTags walks the config struct tree (through pointers,
// maps, slices, and nested/embedded structs) and returns the dotted key paths
// whose koanf tag contains an underscore. seen prevents revisiting shared types.
func collectUnderscoredKoanfTags(rt reflect.Type, prefix string, seen map[reflect.Type]bool) []string {
	rt = koanfUnderlyingStruct(rt)
	if rt == nil || seen[rt] {
		return nil
	}
	seen[rt] = true

	var offenders []string
	for i := 0; i < rt.NumField(); i++ {
		f := rt.Field(i)
		name := strings.Split(f.Tag.Get("koanf"), ",")[0]
		if name == "-" {
			continue
		}
		// Untagged fields (incl. anonymous/embedded structs) keep the parent
		// prefix so their tagged children are still reached and checked.
		key := koanfChildKey(prefix, name)
		if strings.Contains(name, "_") {
			offenders = append(offenders, key)
		}
		offenders = append(offenders, collectUnderscoredKoanfTags(f.Type, key, seen)...)
	}
	return offenders
}

// koanfUnderlyingStruct dereferences pointers and unwraps map/slice element types,
// returning the underlying struct type, or nil if the type bottoms out at a
// non-struct (a scalar leaf).
func koanfUnderlyingStruct(rt reflect.Type) reflect.Type {
	for {
		switch rt.Kind() {
		case reflect.Pointer, reflect.Map, reflect.Slice:
			rt = rt.Elem()
		case reflect.Struct:
			return rt
		default:
			return nil
		}
	}
}

// koanfChildKey joins a parent prefix with a leaf name, treating an empty name
// (untagged/embedded field) as a pass-through of the parent prefix.
func koanfChildKey(prefix, name string) string {
	switch {
	case name == "":
		return prefix
	case prefix == "":
		return name
	default:
		return prefix + "." + name
	}
}
