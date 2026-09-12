package auth

import (
	"encoding/base64"
	"fmt"
	"math/big"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// jwk renders one JWKS entry for the parser tests.
func jwk(members map[string]string) string {
	parts := make([]string, 0, len(members))
	for _, key := range []string{"kty", "kid", "use", "n", "e", "crv"} {
		if value, ok := members[key]; ok {
			parts = append(parts, fmt.Sprintf("%q:%q", key, value))
		}
	}
	return "{" + strings.Join(parts, ",") + "}"
}

// modulusBytes renders a 2048-bit modulus whose first and last bytes are lead
// and last, in the base64url encoding RFC 7518 mandates.
func modulusBytes(lead, last byte) string {
	raw := make([]byte, 256)
	raw[0] = lead
	raw[255] = last
	return base64.RawURLEncoding.EncodeToString(raw)
}

// validModulus is a 2048-bit odd modulus, the shape parseRSAKey accepts.
func validModulus() string {
	return modulusBytes(0xC0, 0x01)
}

// evenModulus is a 2048-bit modulus that is even, which no product of two odd
// primes can be.
func evenModulus() string {
	return modulusBytes(0xC0, 0x00)
}

func TestParseJWKSDropsUnusableEntries(t *testing.T) {
	tests := []struct {
		name    string
		members map[string]string
	}{
		{name: "non_rsa_kty", members: map[string]string{"kty": "EC", "kid": "k", "crv": "P-256"}},
		{name: "missing_kid", members: map[string]string{"kty": "RSA", "n": validModulus(), "e": "AQAB"}},
		{name: "encryption_use", members: map[string]string{"kty": "RSA", "kid": "k", "use": "enc", "n": validModulus(), "e": "AQAB"}},
		{name: "missing_modulus", members: map[string]string{"kty": "RSA", "kid": "k", "e": "AQAB"}},
		{name: "padded_modulus", members: map[string]string{"kty": "RSA", "kid": "k", "n": "AAAA=", "e": "AQAB"}},
		{name: "modulus_below_the_floor", members: map[string]string{"kty": "RSA", "kid": "k", "n": "AQAB", "e": "AQAB"}},
		{name: "even_modulus", members: map[string]string{"kty": "RSA", "kid": "k", "n": evenModulus(), "e": "AQAB"}},
		{name: "missing_exponent", members: map[string]string{"kty": "RSA", "kid": "k", "n": validModulus()}},
		{name: "even_exponent", members: map[string]string{"kty": "RSA", "kid": "k", "n": validModulus(), "e": "BAAA"}},
		{name: "unit_exponent", members: map[string]string{"kty": "RSA", "kid": "k", "n": validModulus(), "e": "AQ"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			keys, dropped, err := parseJWKS([]byte(`{"keys":[` + jwk(tc.members) + `]}`))

			require.NoError(t, err)
			assert.Empty(t, keys)
			assert.Len(t, dropped, 1)
		})
	}
}

func TestParseJWKSKeepsTheFirstOfADuplicateKid(t *testing.T) {
	first := jwk(map[string]string{"kty": "RSA", "kid": "dup", "n": validModulus(), "e": "AQAB"})
	second := jwk(map[string]string{"kty": "RSA", "kid": "dup", "n": modulusBytes(0xFF, 0x01), "e": "AQAB"})

	keys, dropped, err := parseJWKS([]byte(`{"keys":[` + first + "," + second + `]}`))

	require.NoError(t, err)
	require.Len(t, keys, 1)
	assert.Equal(t, []string{"dup"}, dropped)
	assert.Equal(t, byte(0xC0), keys["dup"].N.Bytes()[0], "the first entry must win")
}

// TestParseJWKSKeepsAUsableEntryAfterAnUnusableDuplicate pins that first-writer-wins
// guards only entries that were actually inserted: an unusable first entry never
// reaches the map, so a later usable entry sharing its kid still lands.
func TestParseJWKSKeepsAUsableEntryAfterAnUnusableDuplicate(t *testing.T) {
	unusable := jwk(map[string]string{"kty": "RSA", "kid": "dup", "n": evenModulus(), "e": "AQAB"})
	usable := jwk(map[string]string{"kty": "RSA", "kid": "dup", "n": modulusBytes(0xFF, 0x01), "e": "AQAB"})

	keys, dropped, err := parseJWKS([]byte(`{"keys":[` + unusable + "," + usable + `]}`))

	require.NoError(t, err)
	require.Len(t, keys, 1)
	require.NotNil(t, keys["dup"])
	assert.Equal(t, []string{"dup"}, dropped)
	assert.Equal(t, byte(0xFF), keys["dup"].N.Bytes()[0], "the later usable entry must land")
}

func TestParseJWKSNamesAnEntryWithoutAKid(t *testing.T) {
	body := []byte(`{"keys":[` + jwk(map[string]string{"kty": "EC", "crv": "P-256"}) + `]}`)

	_, dropped, err := parseJWKS(body)

	require.NoError(t, err)
	assert.Equal(t, []string{noKidPlaceholder}, dropped)
}

func TestParseJWKSRejectsANonDocument(t *testing.T) {
	_, _, err := parseJWKS([]byte("not json"))

	require.Error(t, err)
	assert.Contains(t, err.Error(), "not valid json")
}

func TestParseJWKSKeepsTheSmallestUsableExponent(t *testing.T) {
	// "Aw" is the minimal base64url encoding of 3, the smallest exponent RFC
	// 8017 allows; the entry must survive rather than be dropped as too small.
	body := []byte(`{"keys":[` + jwk(map[string]string{"kty": "RSA", "kid": "e3", "n": validModulus(), "e": "Aw"}) + `]}`)

	keys, dropped, err := parseJWKS(body)

	require.NoError(t, err)
	assert.Empty(t, dropped)
	require.Len(t, keys, 1)
	assert.Equal(t, 3, keys["e3"].E)
}

// bigEndianOfBitLen renders the minimal big-endian byte string whose big.Int bit
// length is exactly bits.
func bigEndianOfBitLen(bits int) []byte {
	return new(big.Int).Lsh(big.NewInt(1), uint(bits-1)).Bytes()
}

func TestDecodeUintBoundsTheBitLengthInclusively(t *testing.T) {
	tests := []struct {
		name string
		bits int
		want bool
	}{
		{name: "one_bit_below_the_floor", bits: minRSAModulusBits - 1},
		{name: "exactly_at_the_floor", bits: minRSAModulusBits, want: true},
		{name: "one_bit_above_the_floor", bits: minRSAModulusBits + 1, want: true},
		{name: "one_bit_below_the_ceiling", bits: maxRSAModulusBits - 1, want: true},
		{name: "exactly_at_the_ceiling", bits: maxRSAModulusBits, want: true},
		{name: "one_bit_above_the_ceiling", bits: maxRSAModulusBits + 1},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			encoded := base64.RawURLEncoding.EncodeToString(bigEndianOfBitLen(tc.bits))

			value, ok := decodeUint(encoded, minRSAModulusBits, maxRSAModulusBits)

			assert.Equal(t, tc.want, ok)
			if !tc.want {
				assert.Nil(t, value)
				return
			}
			require.NotNil(t, value)
			assert.Equal(t, tc.bits, value.BitLen())
		})
	}
}
