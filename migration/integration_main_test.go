//go:build integration

package migration

import (
	"os"
	"testing"

	"github.com/gaborage/go-bricks/testing/containers"
)

// pgCfg is what pgPool boots with, so the helpers read its credentials.
var pgCfg = containers.DefaultPostgreSQLConfig()

// pgPool hands each integration test its own never-used PostgreSQL container and
// boots the next two while the current test runs. A container per test, not a
// shared one with per-test databases (ADR-020): the roles these tests create are
// cluster-global, so on one server they would leak from test to test.
var pgPool = containers.NewPostgreSQLPool(pgCfg, 2)

// TestMain terminates the containers booted for tests that never came. It never
// exits early: without Docker each test still skips itself.
func TestMain(m *testing.M) {
	code := m.Run()
	pgPool.Close()
	os.Exit(code)
}
