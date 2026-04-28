package testing

import (
	"testing"

	"github.com/gaborage/go-bricks/jose"
)

// SealForTest is a convenience wrapper over jose.Seal that fails the test on error.
// Use it in test setup when producing a JOSE-wrapped payload to feed into the system
// under test (e.g., posting to an httptest server with a JOSE-tagged handler).
func SealForTest(t testing.TB, payload []byte, p *jose.Policy, r jose.KeyResolver) string {
	t.Helper()
	compact, err := jose.Seal(payload, p, r)
	if err != nil {
		t.Fatalf("jositest: Seal failed: %v", err)
	}
	return compact
}

// OpenForTest is a convenience wrapper over jose.Open that fails the test on error.
// Use it in assertions when consuming a JOSE response from the system under test
// (e.g., decrypting + verifying a response body to assert on its plaintext shape).
//
// Returns the verified plaintext and the extracted claims; the diagnostic header is
// dropped because tests rarely care about it. If you need the header, call jose.Open
// directly.
func OpenForTest(t testing.TB, compact string, p *jose.Policy, r jose.KeyResolver) (plaintext []byte, claims *jose.Claims) {
	t.Helper()
	pt, c, _, err := jose.Open(compact, p, r)
	if err != nil {
		t.Fatalf("jositest: Open failed: %v", err)
	}
	return pt, c
}
