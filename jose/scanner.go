package jose

import (
	"reflect"
)

// SentinelFieldName is the conventional field name applications use for the jose-tagged
// sentinel field. Any blank-named field with a jose tag also matches; this constant
// documents the canonical choice.
const SentinelFieldName = "_"

// ScanType inspects a Go type for a jose: sentinel field. Returns (nil, nil) if the type
// has no jose tag (i.e., the route is not JOSE-protected). Returns (*Policy, nil) on a
// valid declaration. Returns (nil, *Error) on any parse or policy-validation failure.
//
// The caller is responsible for resolving the Policy's kid references against a
// KeyResolver — ScanType is purely structural.
//
// Pointer types are unwrapped: ScanType(reflect.TypeOf(*Foo)) and ScanType(reflect.TypeOf(Foo))
// return the same result.
func ScanType(t reflect.Type, dir Direction) (*Policy, error) {
	if t == nil {
		return nil, nil
	}
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if t.Kind() != reflect.Struct {
		return nil, nil
	}

	for i := 0; i < t.NumField(); i++ {
		field := t.Field(i)
		tag, ok := field.Tag.Lookup(TagName)
		if !ok {
			continue
		}
		policy, err := ParseTag(tag, dir)
		if err != nil {
			return nil, err
		}
		return policy, nil
	}
	return nil, nil
}
