//go:build integration

package outbox

import (
	"os"
	"testing"

	"github.com/gaborage/go-bricks/testing/containers"
)

// pgPool hands each integration test its own never-used PostgreSQL container, as
// these tests have always had, and boots the next two while the current test runs.
var pgPool = containers.NewPostgreSQLPool(nil, 2)

// TestMain terminates the containers booted for tests that never came. It never
// exits early: without Docker each test still skips itself.
func TestMain(m *testing.M) {
	code := m.Run()
	pgPool.Close()
	os.Exit(code)
}
