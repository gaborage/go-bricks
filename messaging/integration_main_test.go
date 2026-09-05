//go:build integration

package messaging

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/gaborage/go-bricks/testing/containers"
)

// pkgBroker holds the single RabbitMQ container shared by every integration test
// in this package's test binary (ADR-020). Queues, exchanges and consumers are
// already uniquified per test via uniqueName, so one broker serves all of them
// without cross-test observation. It starts on first use, so a unit-test-only run
// — go test -tags=integration -run SomeUnitTest ./messaging — boots nothing.
var pkgBroker = containers.NewShared("RabbitMQ", 3*time.Minute,
	func(ctx context.Context) (*containers.RabbitMQContainer, bool, error) {
		return containers.StartRabbitMQContainerForTestMain(ctx, nil)
	})

// TestMain tears the shared broker down after the suite. It deliberately never
// exits early when Docker is missing: the package's unit tests still run, and
// each integration test skips itself through setupTestBroker.
func TestMain(m *testing.M) {
	code := m.Run()
	pkgBroker.Close()
	os.Exit(code)
}
