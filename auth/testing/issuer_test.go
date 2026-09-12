package testing

import (
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
	"time"

	jose "github.com/go-jose/go-jose/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// decode splits a compact JWS and returns its protected header and payload claims.
func decode(t *testing.T, compact string) (header, payload map[string]any) {
	t.Helper()
	parts := strings.Split(compact, ".")
	require.Len(t, parts, 3, "compact JWS must have three segments")

	raw, err := base64.RawURLEncoding.DecodeString(parts[0])
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(raw, &header))

	raw, err = base64.RawURLEncoding.DecodeString(parts[1])
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(raw, &payload))

	return header, payload
}

func decodeHeader(t *testing.T, compact string) map[string]any {
	t.Helper()
	header, _ := decode(t, compact)
	return header
}

func decodePayload(t *testing.T, compact string) map[string]any {
	t.Helper()
	_, payload := decode(t, compact)
	return payload
}

// verify checks the signature against the issuer's public key for kid.
func verify(t *testing.T, iss *Issuer, compact string) []byte {
	t.Helper()
	header := decodeHeader(t, compact)
	kid, _ := header["kid"].(string)
	key := iss.PublicKey(kid)
	require.NotNil(t, key, "no public key for kid %q", kid)

	obj, err := jose.ParseSigned(compact, []jose.SignatureAlgorithm{jose.RS256, jose.PS256})
	require.NoError(t, err)
	payload, err := obj.Verify(key)
	require.NoError(t, err)
	return payload
}

func numericClaim(t *testing.T, payload map[string]any, name string) int64 {
	t.Helper()
	value, ok := payload[name].(float64)
	require.True(t, ok, "claim %q missing or not numeric: %v", name, payload[name])
	return int64(value)
}

func TestNewIssuerDefaults(t *testing.T) {
	iss := NewIssuer()

	assert.Equal(t, DefaultIssuerURL, iss.IssuerURL())
	assert.Equal(t, DefaultAudience, iss.Audience())
	assert.Equal(t, DefaultKeyID, iss.ActiveKeyID())
	require.Len(t, iss.PublicKeys(), 1)
	require.NotNil(t, iss.PublicKey(DefaultKeyID))
	assert.Equal(t, rsaKeyBits, iss.PublicKey(DefaultKeyID).N.BitLen())
	assert.Nil(t, iss.PublicKey(UnknownKeyID))
}

func TestIssuerBuildersOverrideDefaults(t *testing.T) {
	iss := NewIssuer().WithIssuerURL("https://idp.example/").WithAudience("payments-api")

	payload := decodePayload(t, iss.Mint(Claims{}))

	assert.Equal(t, "https://idp.example/", payload["iss"])
	assert.Equal(t, "payments-api", payload["aud"])
	assert.Equal(t, DefaultSubject, payload["sub"])
}

func TestIssuerMintProducesVerifiableCredential(t *testing.T) {
	iss := NewIssuer()
	frozen := time.Unix(1700000000, 0)
	iss.WithClock(func() time.Time { return frozen })

	compact := iss.Mint(Claims{Subject: "user-42"})

	header, payload := decode(t, compact)
	assert.Equal(t, "RS256", header["alg"])
	assert.Equal(t, DefaultKeyID, header["kid"])
	assert.Equal(t, "JWT", header["typ"])
	assert.Equal(t, "user-42", payload["sub"])
	assert.Equal(t, frozen.Unix(), numericClaim(t, payload, "iat"))
	assert.Equal(t, frozen.Add(DefaultLifetime).Unix(), numericClaim(t, payload, "exp"))
	assert.NotContains(t, payload, "nbf")

	verified := verify(t, iss, compact)
	var round map[string]any
	require.NoError(t, json.Unmarshal(verified, &round))
	assert.Equal(t, "user-42", round["sub"])
}

func TestIssuerMintCarriesExtraClaims(t *testing.T) {
	iss := NewIssuer()

	compact := iss.Mint(Claims{Extra: map[string]any{
		"scope":     "accounts:read",
		"tenant_id": "acme",
		"roles":     []string{"admin", "auditor"},
	}})

	payload := decodePayload(t, compact)
	assert.Equal(t, "accounts:read", payload["scope"])
	assert.Equal(t, "acme", payload["tenant_id"])
	assert.Equal(t, []any{"admin", "auditor"}, payload["roles"])
}

func TestIssuerMintStandardClaimsWinOverExtra(t *testing.T) {
	iss := NewIssuer()

	payload := decodePayload(t, iss.Mint(Claims{
		Subject: "real-subject",
		Extra:   map[string]any{"sub": "spoofed"},
	}))

	assert.Equal(t, "real-subject", payload["sub"])
}

func TestIssuerMintAudienceEncoding(t *testing.T) {
	iss := NewIssuer()

	tests := []struct {
		name     string
		audience []string
		expected any
	}{
		{name: "default_audience_as_string", audience: nil, expected: DefaultAudience},
		{name: "single_audience_as_string", audience: []string{"one"}, expected: "one"},
		{name: "multiple_audiences_as_array", audience: []string{"one", "two"}, expected: []any{"one", "two"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			payload := decodePayload(t, iss.Mint(Claims{Audience: tt.audience}))
			assert.Equal(t, tt.expected, payload["aud"])
		})
	}
}

func TestIssuerMintExplicitTimes(t *testing.T) {
	iss := NewIssuer()
	base := time.Unix(1700000000, 0)

	payload := decodePayload(t, iss.Mint(Claims{
		IssuedAt:  base,
		NotBefore: base.Add(time.Minute),
		ExpiresAt: base.Add(time.Hour),
	}))

	assert.Equal(t, base.Unix(), numericClaim(t, payload, "iat"))
	assert.Equal(t, base.Add(time.Minute).Unix(), numericClaim(t, payload, "nbf"))
	assert.Equal(t, base.Add(time.Hour).Unix(), numericClaim(t, payload, "exp"))
}

func TestIssuerMintExpired(t *testing.T) {
	iss := NewIssuer()
	frozen := time.Unix(1700000000, 0)
	iss.WithClock(func() time.Time { return frozen })

	payload := decodePayload(t, iss.MintExpired())

	assert.Less(t, numericClaim(t, payload, "exp"), frozen.Unix())
}

func TestIssuerMintExpiredWithinLeeway(t *testing.T) {
	iss := NewIssuer()
	frozen := time.Unix(1700000000, 0)
	iss.WithClock(func() time.Time { return frozen })
	const leeway = 2 * time.Minute

	payload := decodePayload(t, iss.MintExpiredWithin(leeway))

	exp := numericClaim(t, payload, "exp")
	assert.Less(t, exp, frozen.Unix(), "must already be expired")
	assert.Greater(t, exp, frozen.Add(-leeway).Unix(), "must still sit inside the leeway window")
	assert.Less(t, numericClaim(t, payload, "iat"), exp)
}

func TestIssuerMintUnknownKeyID(t *testing.T) {
	iss := NewIssuer()

	compact := iss.MintUnknownKeyID()

	assert.Equal(t, UnknownKeyID, decodeHeader(t, compact)["kid"])
	assert.NotContains(t, iss.PublicKeys(), UnknownKeyID)
}

// TestIssuerSingleShapeMintersProduceTheirShape covers the helpers that are a lone
// MintWith call. Each case asserts the wire bytes it exists to produce; the loop
// additionally proves every one stays signed by the active key.
func TestIssuerSingleShapeMintersProduceTheirShape(t *testing.T) {
	frozen := time.Unix(1700000000, 0)
	iss := NewIssuer().WithClock(func() time.Time { return frozen })

	tests := []struct {
		name   string
		mint   func(*Issuer) string
		assert func(t *testing.T, header, payload map[string]any)
	}{
		{
			name: "ps256_switches_the_alg_header",
			mint: (*Issuer).MintPS256,
			assert: func(t *testing.T, header, _ map[string]any) {
				assert.Equal(t, "PS256", header["alg"])
				assert.Equal(t, DefaultKeyID, header["kid"])
			},
		},
		{
			name: "wrong_audience_addresses_another_api",
			mint: (*Issuer).MintWrongAudience,
			assert: func(t *testing.T, _, payload map[string]any) {
				assert.Equal(t, WrongAudience, payload["aud"])
				assert.NotEqual(t, DefaultAudience, payload["aud"])
			},
		},
		{
			name: "wrong_issuer_names_another_idp",
			mint: (*Issuer).MintWrongIssuer,
			assert: func(t *testing.T, _, payload map[string]any) {
				assert.Equal(t, WrongIssuer, payload["iss"])
				assert.NotEqual(t, DefaultIssuerURL, payload["iss"])
			},
		},
		{
			name: "missing_key_id_drops_the_kid_header",
			mint: (*Issuer).MintMissingKeyID,
			assert: func(t *testing.T, header, _ map[string]any) {
				assert.NotContains(t, header, "kid")
				assert.Equal(t, "RS256", header["alg"])
			},
		},
		{
			name: "missing_expiry_drops_exp_but_keeps_iat",
			mint: (*Issuer).MintMissingExpiry,
			assert: func(t *testing.T, _, payload map[string]any) {
				assert.NotContains(t, payload, "exp")
				assert.Contains(t, payload, "iat")
			},
		},
		{
			name: "future_not_before_is_not_yet_valid",
			mint: (*Issuer).MintFutureNotBefore,
			assert: func(t *testing.T, _, payload map[string]any) {
				assert.Greater(t, numericClaim(t, payload, "nbf"), frozen.Unix())
			},
		},
		{
			name: "future_issued_at_is_dated_ahead",
			mint: (*Issuer).MintFutureIssuedAt,
			assert: func(t *testing.T, _, payload map[string]any) {
				assert.Greater(t, numericClaim(t, payload, "iat"), frozen.Unix())
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			compact := tt.mint(iss)

			header, payload := decode(t, compact)
			assert.Equal(t, "JWT", header["typ"])
			assert.Equal(t, DefaultSubject, payload["sub"])
			tt.assert(t, header, payload)

			obj, err := jose.ParseSigned(compact, []jose.SignatureAlgorithm{jose.RS256, jose.PS256})
			require.NoError(t, err, "wire shape stays a parseable compact JWS")
			_, err = obj.Verify(iss.PublicKey(DefaultKeyID))
			assert.NoError(t, err, "signed by the active key")
		})
	}
}

func TestIssuerMintAlgNone(t *testing.T) {
	iss := NewIssuer()

	compact := iss.MintAlgNone()

	parts := strings.Split(compact, ".")
	require.Len(t, parts, 3)
	assert.Empty(t, parts[2], "unsigned credential carries an empty signature segment")
	assert.Equal(t, "none", decodeHeader(t, compact)["alg"])
	assert.Equal(t, DefaultSubject, decodePayload(t, compact)["sub"])

	_, err := jose.ParseSigned(compact, []jose.SignatureAlgorithm{jose.RS256, jose.PS256})
	assert.Error(t, err, "the framework allowlist must refuse alg=none")
}

func TestIssuerMintES256(t *testing.T) {
	iss := NewIssuer()

	compact := iss.MintES256()

	assert.Equal(t, "ES256", decodeHeader(t, compact)["alg"])

	obj, err := jose.ParseSigned(compact, []jose.SignatureAlgorithm{jose.ES256})
	require.NoError(t, err)
	_, err = obj.Verify(&iss.ecdsaKey().PublicKey)
	require.NoError(t, err)

	_, err = jose.ParseSigned(compact, []jose.SignatureAlgorithm{jose.RS256, jose.PS256})
	assert.Error(t, err, "the framework allowlist must refuse ES256")
}

func TestIssuerMintWithType(t *testing.T) {
	iss := NewIssuer()

	compact := iss.MintWithType("at+jwt")

	assert.Equal(t, "at+jwt", decodeHeader(t, compact)["typ"])
	verify(t, iss, compact)
}

func TestIssuerMintBadSignature(t *testing.T) {
	iss := NewIssuer()

	compact := iss.MintBadSignature()

	header := decodeHeader(t, compact)
	assert.Equal(t, DefaultKeyID, header["kid"], "claims the active kid")
	obj, err := jose.ParseSigned(compact, []jose.SignatureAlgorithm{jose.RS256})
	require.NoError(t, err, "wire shape stays valid")
	_, err = obj.Verify(iss.PublicKey(DefaultKeyID))
	assert.Error(t, err, "signed by a key the verifier does not hold")
}

func TestIssuerMintCorruptSignature(t *testing.T) {
	iss := NewIssuer()

	compact := iss.MintCorruptSignature()

	obj, err := jose.ParseSigned(compact, []jose.SignatureAlgorithm{jose.RS256})
	require.NoError(t, err)
	_, err = obj.Verify(iss.PublicKey(DefaultKeyID))
	assert.Error(t, err)
}

func TestIssuerRotateSwitchesActiveKeyAndKeepsOld(t *testing.T) {
	iss := NewIssuer()
	before := iss.Mint(Claims{})
	oldKey := iss.PublicKey(DefaultKeyID)
	require.NotNil(t, oldKey)

	iss.Rotate("test-key-2")
	after := iss.Mint(Claims{})

	assert.Equal(t, "test-key-2", iss.ActiveKeyID())
	assert.Equal(t, DefaultKeyID, decodeHeader(t, before)["kid"])
	assert.Equal(t, "test-key-2", decodeHeader(t, after)["kid"])

	keys := iss.PublicKeys()
	require.Len(t, keys, 2)
	assert.Equal(t, oldKey, keys[DefaultKeyID], "old key survives rotation")
	assert.NotEqual(t, keys[DefaultKeyID], keys["test-key-2"])

	verify(t, iss, before)
	verify(t, iss, after)
}

func TestIssuerPublicKeysIsACopy(t *testing.T) {
	iss := NewIssuer()

	keys := iss.PublicKeys()
	keys["injected"] = &rsa.PublicKey{}

	assert.Nil(t, iss.PublicKey("injected"))
	assert.Len(t, iss.PublicKeys(), 1)
}

func TestIssuerMintWithExplicitKeyID(t *testing.T) {
	iss := NewIssuer().Rotate("test-key-2")

	compact := iss.MintWith(MintOptions{KeyID: DefaultKeyID})

	assert.Equal(t, DefaultKeyID, decodeHeader(t, compact)["kid"])
	verify(t, iss, compact)
}

func TestIssuerMintWithUnknownKeyIDPanics(t *testing.T) {
	iss := NewIssuer()

	assert.PanicsWithValue(t,
		`auth/testing: no signing key for kid "nope"; pass MintOptions.SignKey`,
		func() { iss.MintWith(MintOptions{KeyID: "nope"}) })
}

func TestIssuerMintWithUnmarshalableClaimPanics(t *testing.T) {
	iss := NewIssuer()

	assert.Panics(t, func() {
		iss.Mint(Claims{Extra: map[string]any{"bad": make(chan int)}})
	})
}
