//go:build integration

package postgresql

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/gaborage/go-bricks/testing/containers"
)

// pkgPG holds the single PostgreSQL testcontainer this package's test binary
// shares (ADR-020). Every integration test provisions its own database on it via
// pkgPG.Get(t).NewDatabase(t), so a test that CREATE TABLEs with fixed names stays
// isolated. Starting lazily keeps unit-only runs (e.g. -run SomeUnitTest) from
// booting PostgreSQL at all.
var pkgPG = containers.NewShared("PostgreSQL", 3*time.Minute,
	func(ctx context.Context) (*containers.PostgreSQLContainer, bool, error) {
		return containers.StartPostgreSQLContainerForTestMain(ctx, nil)
	})

// TestMain terminates the shared container after the whole binary has run.
// It never exits early: without Docker the individual tests still skip
// themselves, exactly as they did when each booted its own container.
func TestMain(m *testing.M) {
	code := m.Run()
	pkgPG.Close()
	os.Exit(code)
}
