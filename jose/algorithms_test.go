package jose

import (
	"testing"

	jose "github.com/go-jose/go-jose/v4"
	"github.com/stretchr/testify/assert"
)

func TestAllowlistRejectsNoneAlg(t *testing.T) {
	// "none" is the canonical JOSE downgrade attack; it must never appear in any allowlist.
	for _, alg := range AllowedSigAlgs() {
		assert.NotEqual(t, jose.SignatureAlgorithm("none"), alg)
	}
}

func TestAllowlistRejectsHmac(t *testing.T) {
	// HMAC algorithms imply a shared secret; incompatible with our asymmetric keystore model.
	for _, alg := range AllowedSigAlgs() {
		assert.NotEqual(t, jose.HS256, alg)
		assert.NotEqual(t, jose.HS384, alg)
		assert.NotEqual(t, jose.HS512, alg)
	}
}

func TestAllowlistRejectsRsa15(t *testing.T) {
	// RSA-PKCS1v1.5 has padding-oracle history; allowlist excludes it in favor of RSA-OAEP-256.
	for _, alg := range AllowedKeyAlgs() {
		assert.NotEqual(t, jose.RSA1_5, alg)
		assert.NotEqual(t, jose.RSA_OAEP, alg)
	}
}

func TestAllowlistAllowsExpectedAlgs(t *testing.T) {
	assert.True(t, IsAllowedSigAlg(jose.RS256))
	assert.True(t, IsAllowedSigAlg(jose.PS256))
	assert.True(t, IsAllowedKeyAlg(jose.RSA_OAEP_256))
	assert.True(t, IsAllowedEnc(jose.A256GCM))
}

// TestAllowlistRejectsES256 guards against re-adding ES256 to the allowlist
// before keystore.KeyStore is extended to return ECDSA keys. See the comment
// on allowedSigAlgs in algorithms.go and the "JOSE: ECDSA Keystore Support"
// backlog entry. Mirrors TestAllowlistRejectsRsa15's loop-and-NotEqual pattern
// so the security guard cluster reads as a set.
func TestAllowlistRejectsES256(t *testing.T) {
	for _, alg := range AllowedSigAlgs() {
		assert.NotEqual(t, jose.ES256, alg)
	}
}

func TestAllowedSigAlgsReturnsCopy(t *testing.T) {
	a := AllowedSigAlgs()
	a[0] = jose.SignatureAlgorithm("none")
	// External mutation must not affect the package-level allowlist.
	assert.True(t, IsAllowedSigAlg(jose.RS256))
}

func TestIsAllowedEncFor(t *testing.T) {
	tests := []struct {
		name string
		mode SealMode
		enc  jose.ContentEncryption
		want bool
	}{
		{"nested_a256gcm", SealModeJWEofJWS, jose.A256GCM, true},
		{"nested_a128gcm", SealModeJWEofJWS, jose.A128GCM, false},
		{"nested_a128cbc", SealModeJWEofJWS, jose.A128CBC_HS256, false},
		{"bare_a256gcm", SealModeBareJWE, jose.A256GCM, true},
		{"bare_a128gcm", SealModeBareJWE, jose.A128GCM, true},
		{"bare_a128cbc", SealModeBareJWE, jose.A128CBC_HS256, false},
		{"unknown_mode_a256gcm", SealMode(99), jose.A256GCM, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, IsAllowedEncFor(tt.mode, tt.enc))
		})
	}
}

func TestAllowedContentEncsForReturnsCopy(t *testing.T) {
	assert.Equal(t, []jose.ContentEncryption{jose.A256GCM}, AllowedContentEncsFor(SealModeJWEofJWS))
	assert.ElementsMatch(t, []jose.ContentEncryption{jose.A128GCM, jose.A256GCM}, AllowedContentEncsFor(SealModeBareJWE))
	assert.Empty(t, AllowedContentEncsFor(SealMode(99)))

	bare := AllowedContentEncsFor(SealModeBareJWE)
	bare[0] = jose.ContentEncryption("A128CBC-HS256")
	assert.True(t, IsAllowedEncFor(SealModeBareJWE, jose.A128GCM))
	assert.False(t, IsAllowedEncFor(SealModeBareJWE, jose.A128CBC_HS256))
}

// TestIsAllowedEncKeepsNestedMeaning pins the un-suffixed predicate to JWE-of-JWS:
// widening it would silently admit A128GCM on the nested path.
func TestIsAllowedEncKeepsNestedMeaning(t *testing.T) {
	assert.False(t, IsAllowedEnc(jose.A128GCM))
	assert.Equal(t, AllowedContentEncsFor(SealModeJWEofJWS), AllowedContentEncs())
}
