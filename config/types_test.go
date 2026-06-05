package config

import (
	"reflect"
	"strings"
	"testing"
)

// TestConfigKoanfTagsHaveNoUnderscore guards the whole "config key cannot be
// reached" bug class. Two mechanisms make an underscored leaf tag unreachable:
//   - env vars: the env loader maps "_"->"." (koanf's nesting delimiter), so an
//     underscored koanf tag is unreachable from the environment.
//   - section Unmarshal: plain koanf.Unmarshal binds by koanf tag or the
//     field-name fallback and never honors the mapstructure tag, so an
//     underscored mapstructure tag lies about the key that actually binds (see
//     #554, which hit the observability tree's mapstructure-only fields).
//
// Every koanf AND mapstructure tag in the Config tree MUST therefore be
// underscore-free. The sibling observability tree (mapstructure-tagged, loaded
// via Config.Unmarshal) is guarded by the parallel
// observability.TestObservabilityConfigTagsHaveNoUnderscore.
func TestConfigKoanfTagsHaveNoUnderscore(t *testing.T) {
	offenders := collectUnderscoredKoanfTags(reflect.TypeOf(Config{}), "", map[reflect.Type]bool{})
	if len(offenders) > 0 {
		t.Fatalf("config tags must be underscore-free (env vars nest on '_'; section "+
			"Unmarshal ignores mapstructure tags); offenders:\n  %s",
			strings.Join(offenders, "\n  "))
	}
}

// collectUnderscoredKoanfTags walks the config struct tree (through pointers,
// maps, slices, and nested/embedded structs) and returns the dotted key paths
// whose koanf OR mapstructure tag contains an underscore. seen prevents
// revisiting shared types.
func collectUnderscoredKoanfTags(rt reflect.Type, prefix string, seen map[reflect.Type]bool) []string {
	rt = koanfUnderlyingStruct(rt)
	if rt == nil || seen[rt] {
		return nil
	}
	seen[rt] = true

	var offenders []string
	for i := 0; i < rt.NumField(); i++ {
		f := rt.Field(i)
		// Unexported fields (e.g. the cached *koanf.Koanf handle) never receive
		// config values, so their tags are irrelevant to the binding invariant —
		// and recursing into third-party internals could surface foreign tags.
		if f.PkgPath != "" {
			continue
		}
		koanfName := koanfTagName(&f, "koanf")
		mapName := koanfTagName(&f, "mapstructure")
		// Untagged fields (incl. anonymous/embedded structs) keep the parent
		// prefix so their tagged children are still reached and checked. Prefer
		// the koanf name for the path, falling back to the mapstructure name.
		pathName := koanfName
		if pathName == "" {
			pathName = mapName
		}
		key := koanfChildKey(prefix, pathName)
		if strings.Contains(koanfName, "_") {
			offenders = append(offenders, key+` (koanf:"`+koanfName+`")`)
		}
		if strings.Contains(mapName, "_") {
			offenders = append(offenders, key+` (mapstructure:"`+mapName+`")`)
		}
		offenders = append(offenders, collectUnderscoredKoanfTags(f.Type, key, seen)...)
	}
	return offenders
}

// koanfTagName returns the first segment of the named struct tag, treating the
// explicit "-" skip marker as empty (no key contributed).
func koanfTagName(f *reflect.StructField, tag string) string {
	name := strings.Split(f.Tag.Get(tag), ",")[0]
	if name == "-" {
		return ""
	}
	return name
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
