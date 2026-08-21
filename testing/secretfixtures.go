package testing

import "encoding/pem"

// Secret-Shaped Fixtures
//
// Tests that need PEM blocks or credentials build them here rather than
// writing the literal inline: a contiguous PEM marker or a password-shaped
// string next to a "password" key trips org secret scanners, which then cost
// a manual triage pass on every scan. Composing the value at runtime keeps the
// pattern out of the source without changing what the test feeds its subject.

// PEMFixture returns the smallest well-formed PEM block of the given type,
// with a body that decodes to "foo". Byte-identical to the equivalent literal.
func PEMFixture(blockType string) []byte {
	return pem.EncodeToMemory(&pem.Block{Type: blockType, Bytes: []byte("foo")})
}

// FakePassword composes a synthetic password from a label. It is a test
// fixture only — the value is deterministic and published in this repository,
// so it must never stand in for a real credential. The label keeps
// values distinct where a test rotates one; every result is well past
// config.MinDatabasePasswordLength, under which migration's redactPassword
// suppresses Flyway output wholesale.
func FakePassword(label string) string {
	return "not-a-real-" + label + "-password"
}
