package mocks

import (
	"fmt"
	"reflect"

	"github.com/stretchr/testify/mock"
)

// arg returns the return value at index as T.
//
// A mock configured with the wrong return type is a defect in the test that set
// it up, so this panics at the offending call instead of handing back a zero
// value that would explode later somewhere unrelated. The message names the
// mocked method, the return index, and the actual type so the misconfigured
// .Return(...) is identifiable without a stack walk.
func arg[T any](arguments mock.Arguments, method string, index int) T {
	raw := arguments.Get(index)
	value, ok := raw.(T)
	if !ok {
		panic(fmt.Sprintf("mocks: %s return value %d is %T, want %s",
			method, index, raw, reflect.TypeFor[T]()))
	}
	return value
}
