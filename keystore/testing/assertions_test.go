package testing

import "testing"

func TestAssertSecretAvailablePasses(t *testing.T) {
	m := NewMockKeyStore().WithSecret("mac", []byte("non-empty-secret"))
	AssertSecretAvailable(t, m, "mac")
}
