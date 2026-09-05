package deferred

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var (
	errFirst  = errors.New("first violation")
	errSecond = errors.New("second violation")
)

// TestErrorZeroValueReportsNoError pins that a builder embedding Error needs no
// constructor: the zero value is intact, which is what lets every builder be
// created as a plain struct literal.
func TestErrorZeroValueReportsNoError(t *testing.T) {
	var d Error

	require.NoError(t, d.Err())
}

// TestErrorFailKeepsFirst pins ADR-031's rule in the one place it now lives: a
// later violation never displaces an earlier one, so ToSQL() names the argument
// the caller has to fix rather than whichever door happened to run last.
func TestErrorFailKeepsFirst(t *testing.T) {
	var d Error

	d.Fail(errFirst)
	d.Fail(errSecond)

	assert.Same(t, errFirst, d.Err())
}

// TestErrorFailIgnoresNil pins that handing over a funnel's result unchecked is
// safe — a nil failure leaves an intact builder intact, and leaves a recorded
// one recorded, so no door has to guard the call.
func TestErrorFailIgnoresNil(t *testing.T) {
	t.Run("on_an_intact_error", func(t *testing.T) {
		var d Error

		d.Fail(nil)

		require.NoError(t, d.Err())
	})

	t.Run("after_a_failure_is_recorded", func(t *testing.T) {
		var d Error
		d.Fail(errFirst)

		d.Fail(nil)

		assert.Same(t, errFirst, d.Err())
	})
}
