// Package configdecode holds mapstructure decode hooks shared by the go-bricks config
// and migration packages, so the decode guards have a single source of truth
// inside the module. The tools/migration CLI is a separate module and cannot import
// go-bricks/internal, so it keeps a byte-identical local copy in sync with this one.
package configdecode

import (
	"fmt"
	"reflect"
	"strings"
	"time"

	"github.com/go-viper/mapstructure/v2"
)

// durationType is time.Duration's reflect.Type, computed once for the hook's fast path.
var durationType = reflect.TypeOf(time.Duration(0))

// NumericToDurationGuardHookFunc rejects a bare numeric bound to a time.Duration field:
// WeaklyTypedInput would otherwise coerce it to that many raw nanoseconds (300 -> 300ns),
// booting a broken config. Edge cases: an explicit zero (int/uint/float, incl. -0.0) is the
// framework-wide "unset -> use default" idiom and passes; a bool is never a duration and is
// always rejected; a source that is already time.Duration (e.g. a typed default) passes.
// Guards exact time.Duration only, matching StringToTimeDurationHookFunc's scope.
//
// Mirrored in tools/migration/internal/commands/common.go (a separate module that cannot
// import go-bricks/internal) — keep the two in sync.
func NumericToDurationGuardHookFunc() mapstructure.DecodeHookFunc {
	return func(f, t reflect.Type, data any) (any, error) {
		if t != durationType {
			return data, nil
		}
		// A source already time.Duration (typed default) is not unit-less; its Kind is Int64
		// and would otherwise be rejected, so pass it through before the numeric checks.
		if f == durationType {
			return data, nil
		}
		v := reflect.ValueOf(data)
		var isZero bool
		switch f.Kind() {
		case reflect.Bool:
			// A boolean is never a duration: reject with no zero exemption.
		case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
			isZero = v.Int() == 0
		case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
			isZero = v.Uint() == 0
		case reflect.Float32, reflect.Float64:
			isZero = v.Float() == 0 // == 0 (not IsZero): keeps -0.0 exempt
		default:
			return data, nil
		}
		if isZero {
			return data, nil
		}
		return nil, fmt.Errorf(
			"unit-less numeric duration %v — use a duration string with an explicit unit (e.g. \"300s\", \"5m\", \"1h30m\")",
			data,
		)
	}
}

// EmptyStringToScalarGuardHookFunc rejects an empty (or whitespace-only) string bound to a
// numeric or bool field. WeaklyTypedInput would otherwise coerce it to that target's zero
// value — 0 for a numeric, false for a bool — so a set-but-empty environment variable
// (FOO=) or an empty YAML string decodes as a legal zero and boots a config nobody wrote:
// the tri-states in ADR-065 and ADR-046 are defeated exactly this way, and
// database.pool.keepalive.enabled flips from its default true to false. Pointer targets are
// guarded too: the zero arrives as a non-nil *0 / *false, which normalization reads as
// "operator set it".
//
// time.Duration is exempt: StringToTimeDurationHookFunc owns that target and already fails
// loudly on an empty string. Every other target is untouched — an empty string is a legal
// string, and the database-identity subset is ADR-051's to judge.
//
// Mirrored in tools/migration/internal/commands/common.go — keep the two in sync.
func EmptyStringToScalarGuardHookFunc() mapstructure.DecodeHookFunc {
	return func(f, t reflect.Type, data any) (any, error) {
		if f.Kind() != reflect.String {
			return data, nil
		}
		// No pointer walk: mapstructure recurses into a pointer target and re-runs the
		// hook chain against the element type, so *int arrives here as int.
		if t == durationType || !isWeakScalarKind(t.Kind()) {
			return data, nil
		}
		// reflect rather than a concrete type assertion: a named string type (type Env
		// string) has Kind String but fails data.(string), and passing it through hands it
		// straight to the weak "" -> zero conversion this guard exists to stop.
		if strings.TrimSpace(reflect.ValueOf(data).String()) != "" {
			return data, nil
		}
		// The message stays inline rather than in a package var: the mirror-drift test
		// compares function bodies, so a message hoisted out would drift unchecked. Only
		// the remedy differs by kind — the part an operator acts on.
		kind, remedy := "numeric", "an explicit value"
		if t.Kind() == reflect.Bool {
			kind, remedy = "boolean", "an explicit true/false"
		}
		return nil, fmt.Errorf(
			"%s value delivered empty — set %s (empty secretKeyRef / unset envsubst variable?) "+
				"or remove the key entirely to take its default", kind, remedy,
		)
	}
}

// isWeakScalarKind reports whether k is one mapstructure's WeaklyTypedInput would fill from
// a string, i.e. the SCALAR kinds where "" silently becomes that kind's zero value — 0 for
// the numerics, false for a bool. Slice and map elements re-enter the hook individually, so
// they are judged by this same predicate on their element kind.
func isWeakScalarKind(k reflect.Kind) bool {
	switch k {
	case reflect.Bool,
		reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64,
		reflect.Float32, reflect.Float64:
		return true
	default:
		return false
	}
}
