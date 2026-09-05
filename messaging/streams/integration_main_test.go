//go:build integration

package streams

import (
	"context"
	"math/rand/v2"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/rabbitmq/rabbitmq-stream-go-client/pkg/stream"

	"github.com/gaborage/go-bricks/testing/containers"
)

const (
	// pkgBrokerStartTimeout bounds the one broker boot, plugin enable included.
	pkgBrokerStartTimeout = 3 * time.Minute
	// itNameBaseMax keeps a derived name well inside RabbitMQ's stream-name limit
	// once the longest suffix this file appends is added.
	itNameBaseMax = 48
)

// pkgBroker is the one stream-enabled broker this test binary shares (ADR-020).
// It boots on first use, so a run that matches no integration test - a unit-test-only
// -run filter, say - never pays for RabbitMQ.
var pkgBroker = containers.NewShared("RabbitMQ", pkgBrokerStartTimeout,
	func(ctx context.Context) (*containers.RabbitMQContainer, bool, error) {
		cfg := containers.DefaultRabbitMQConfig()
		cfg.EnableStreamPlugin = true
		return containers.StartRabbitMQContainerForTestMain(ctx, cfg)
	})

// TestMain terminates the shared broker after the whole binary has run. It never
// exits early on a missing Docker: the package's unit tests must still run, and
// each integration test skips itself through streamsTestEnv.
func TestMain(m *testing.M) {
	code := m.Run()
	pkgBroker.Close()
	os.Exit(code)
}

// itNames are the topology names one test declares. The broker is shared, so
// every one of them must be unique to the test that declares it: a stream carries
// the offsets whoever consumed it last committed, and a consumer group carries its
// membership.
type itNames struct {
	stream      string
	consumer    string
	superStream string
	superGroup  string
}

// sanitizeStreamName maps everything RabbitMQ does not accept in a stream name to
// a dash, which is what a subtest's "/" becomes.
func sanitizeStreamName(name string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			return r
		case r == '-', r == '_', r == '.':
			return r
		default:
			return '-'
		}
	}, name)
}

// newITNames derives this test's topology names from its own name plus a random
// suffix - the suffix is what keeps a -count=2 run from reusing the offsets its
// first pass committed - and deletes what the test declared once it finishes.
func newITNames(t *testing.T, opts ManagerOptions) itNames {
	t.Helper()

	base := sanitizeStreamName(t.Name())
	if len(base) > itNameBaseMax {
		base = base[:itNameBaseMax]
	}
	base += "-" + strconv.FormatUint(uint64(rand.Uint32()), 36)

	names := itNames{
		stream:      base + "-stream",
		consumer:    base + "-group",
		superStream: base + "-super",
		superGroup:  base + "-super-group",
	}
	t.Cleanup(func() { deleteDeclaredStreams(opts, names) })
	return names
}

// deleteDeclaredStreams drops the test's topology from the shared broker so it
// does not accumulate over the run. Best effort throughout: a test that never
// declared its super stream is not a failure.
func deleteDeclaredStreams(opts ManagerOptions, names itNames) {
	env, err := stream.NewEnvironment(streamEnvironmentOptions(opts))
	if err != nil {
		return
	}
	defer func() { _ = env.Close() }()

	_ = env.DeleteSuperStream(names.superStream)
	_ = env.DeleteStream(names.stream)
}
