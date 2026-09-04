package sealed_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gaborage/go-bricks/jose/sealed"
)

func TestCheckLogicalKid(t *testing.T) {
	cases := []struct {
		name string
		kid  string
		want string // substring of the error, "" for valid
	}{
		{name: "valid_service_family", kid: "svc-payments-sign", want: ""},
		{name: "valid_underscore_and_digits", kid: "acme_core-enc2", want: ""},
		{name: "valid_64_chars", kid: strings.Repeat("a", 64), want: ""},
		{name: "valid_v_without_dash", kid: "svcv2", want: ""},
		{name: "valid_dash_v_letters", kid: "svc-vault", want: ""},
		{name: "invalid_empty", kid: "", want: "must match"},
		{name: "invalid_dot", kid: "svc.payments", want: "must match"},
		{name: "invalid_space", kid: "svc payments", want: "must match"},
		{name: "invalid_65_chars", kid: strings.Repeat("a", 65), want: "exceeds 64"},
		{name: "invalid_generation_suffix", kid: "svc-sign-v3", want: "must not end in -v<digits>"},
		{name: "invalid_generation_suffix_leading_zero", kid: "svc-sign-v01", want: "must not end in -v<digits>"},
		{name: "invalid_generation_suffix_zero", kid: "svc-sign-v0", want: "must not end in -v<digits>"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := sealed.CheckLogicalKid(tc.kid)
			if tc.want == "" {
				assert.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.want)
		})
	}
}

func TestSplitGenerationKid(t *testing.T) {
	cases := []struct {
		name       string
		kid        string
		family     string
		generation int
		ok         bool
	}{
		{name: "v1", kid: "svc-payments-sign-v1", family: "svc-payments-sign", generation: 1, ok: true},
		{name: "v10", kid: "acme-core-enc-v10", family: "acme-core-enc", generation: 10, ok: true},
		{name: "v207", kid: "k-v207", family: "k", generation: 207, ok: true},
		{name: "logical_only", kid: "svc-payments-sign", ok: false},
		{name: "zero_generation", kid: "svc-v0", ok: false},
		{name: "leading_zero", kid: "svc-v01", ok: false},
		{name: "double_generation_illegal_family", kid: "x-v1-v2", ok: false},
		{name: "family_too_long", kid: strings.Repeat("a", 65) + "-v1", ok: false},
		{name: "family_bad_alphabet", kid: "svc.x-v1", ok: false},
		{name: "no_family", kid: "-v1", ok: false},
		{name: "generation_overflow", kid: "svc-v99999999999999999999", ok: false},
		{name: "uppercase_v", kid: "svc-V1", ok: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			family, generation, ok := sealed.SplitGenerationKid(tc.kid)
			assert.Equal(t, tc.ok, ok)
			assert.Equal(t, tc.family, family)
			assert.Equal(t, tc.generation, generation)
		})
	}
}
