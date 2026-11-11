//go:build !integration

package migration

import (
	"os"
	"testing"

	"github.com/gaborage/go-bricks/internal/testutil"
)

// TestMain is the unit-build twin of integration_main_test.go's: it unsets the PGSSL*
// variables config judges, so an ambient value cannot flip a PostgreSQL section's verdict.
func TestMain(m *testing.M) {
	testutil.UnsetPostgresSSLEnv()
	os.Exit(m.Run())
}
