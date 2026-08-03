package validation

import (
	"sync"
	"testing"

	"github.com/go-playground/validator/v10"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	mccTag       = "mcc_code"
	validMCCCode = "5411"
)

type merchant struct {
	Code string `validate:"mcc_code"`
}

func TestNewRegistersMCCCode(t *testing.T) {
	v := New()

	require.NotNil(t, v)
	require.NoError(t, v.Struct(merchant{Code: validMCCCode}))
}

func TestNewMCCCodeRejectsMalformedCodes(t *testing.T) {
	tests := []struct {
		name string
		code string
	}{
		{name: "empty", code: ""},
		{name: "two_digits", code: "54"},
		{name: "three_digits", code: "541"},
		{name: "five_digits", code: "54111"},
		{name: "four_letters", code: "abcd"},
		{name: "four_chars_mixed", code: "54a1"},
		{name: "four_chars_with_space", code: "54 1"},
		{name: "four_digits_with_trailing_newline", code: "5411\n"},
		{name: "four_digits_with_leading_newline", code: "\n5411"},
	}

	v := New()
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := v.Struct(merchant{Code: tc.code})
			require.Error(t, err)

			var verrs validator.ValidationErrors
			require.ErrorAs(t, err, &verrs)
			require.Len(t, verrs, 1)
			assert.Equal(t, "Code", verrs[0].Field())
			assert.Equal(t, mccTag, verrs[0].Tag())
		})
	}
}

// A non-string field yields reflect.Value.String() == "<int Value>", which the
// anchored regex rejects — the same answer the removed length guard gave.
func TestNewMCCCodeRejectsNonStringField(t *testing.T) {
	type numericMerchant struct {
		Code int `validate:"mcc_code"`
	}

	err := New().Struct(numericMerchant{Code: 5411})
	require.Error(t, err)

	var verrs validator.ValidationErrors
	require.ErrorAs(t, err, &verrs)
	require.Len(t, verrs, 1)
	assert.Equal(t, mccTag, verrs[0].Tag())
}

// Each call must yield a private registration set: layer-3 consumers and the
// server each hold their own instance, and a rule added to one must not leak.
func TestNewReturnsIndependentInstances(t *testing.T) {
	a, b := New(), New()
	require.NotSame(t, a, b)

	type probe struct {
		Field string `validate:"probe_rule"`
	}
	require.NoError(t, a.RegisterValidation("probe_rule", func(validator.FieldLevel) bool { return true }))

	assert.NoError(t, a.Struct(probe{}))
	assert.Panics(t, func() { _ = b.Struct(probe{}) }, "rule registered on a must not exist on b")
}

// One instance is shared across every consumer worker and tenant, so pin the
// concurrency safety go-playground documents rather than trusting the doc.
func TestNewInstanceIsConcurrencySafe(t *testing.T) {
	v := New()

	const goroutines = 32
	var wg sync.WaitGroup
	wg.Add(goroutines)
	for i := range goroutines {
		go func(i int) {
			defer wg.Done()

			code, wantErr := validMCCCode, false
			if i%2 == 0 {
				code, wantErr = "54", true
			}

			// t.Errorf, not require: FailNow outside the test goroutine is undefined.
			if err := v.Struct(merchant{Code: code}); (err != nil) != wantErr {
				t.Errorf("code %q: got err %v, wantErr %v", code, err, wantErr)
			}
		}(i)
	}
	wg.Wait()
}
