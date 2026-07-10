// Package configdecode holds mapstructure decode hooks shared by the go-bricks config
// and migration packages, so the numeric-duration guard has a single source of truth
// inside the module. The tools/migration CLI is a separate module and cannot import
// go-bricks/internal, so it keeps a byte-identical local copy in sync with this one.
package configdecode

import (
	"fmt"
	"reflect"
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
