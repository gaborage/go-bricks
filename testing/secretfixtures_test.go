package testing_test

import (
	"encoding/pem"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gaborage/go-bricks/config"
	testconsts "github.com/gaborage/go-bricks/testing"
)

// pemBegin is split so this file carries no contiguous PEM marker either —
// the rule the fixtures under test exist to enforce applies here too.
const pemBegin = "-----BEGIN "

// TestPEMFixtureMatchesTheLiteralItReplaces is the load-bearing pin for the
// swap that introduced PEMFixture: call sites gave up hand-written PEM
// literals on the promise that the bytes are unchanged, and they feed
// key-material detection that a malformed block would silently stop
// exercising. Drift here has to fail loudly rather than weaken those tests.
func TestPEMFixtureMatchesTheLiteralItReplaces(t *testing.T) {
	for _, blockType := range []string{"PRIVATE KEY", "CERTIFICATE"} {
		t.Run(blockType, func(t *testing.T) {
			want := pemBegin + blockType + "-----\nZm9v\n-----END " + blockType + "-----\n"
			got := testconsts.PEMFixture(blockType)
			assert.Equal(t, want, string(got), "bytes must match the literal this replaced")

			block, rest := pem.Decode(got)
			require.NotNil(t, block, "a real parser must still find a block")
			assert.Empty(t, rest)
			assert.Equal(t, blockType, block.Type)
			assert.Equal(t, "foo", string(block.Bytes))
		})
	}
}

// TestFakePasswordHoldsThePropertiesFixturesRelyOn pins the three properties
// callers depend on, none of which are visible at a call site.
func TestFakePasswordHoldsThePropertiesFixturesRelyOn(t *testing.T) {
	got := testconsts.FakePassword("mig-idem")

	// Below this floor migration's redactPassword suppresses Flyway output
	// wholesale instead of redacting the credential out of it.
	assert.GreaterOrEqual(t, len(got), config.MinDatabasePasswordLength)

	// ADR-061: PGRoleSpec.Validate rejects these outright.
	for _, ctrl := range []string{"\r", "\n", "\x00"} {
		assert.NotContains(t, got, ctrl)
	}

	// Role-rotation tests assert the old credential stops working, which is
	// vacuous unless distinct labels yield distinct passwords.
	assert.NotEqual(t, got, testconsts.FakePassword("mig-idem-rotated"))
}
