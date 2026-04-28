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
	assert.True(t, IsAllowedSigAlg(jose.ES256))
	assert.True(t, IsAllowedKeyAlg(jose.RSA_OAEP_256))
	assert.True(t, IsAllowedEnc(jose.A256GCM))
}

func TestAllowedSigAlgsReturnsCopy(t *testing.T) {
	a := AllowedSigAlgs()
	a[0] = jose.SignatureAlgorithm("none")
	// External mutation must not affect the package-level allowlist.
	assert.True(t, IsAllowedSigAlg(jose.RS256))
}
