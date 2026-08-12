//go:build integration

package streams

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/rabbitmq/rabbitmq-stream-go-client/pkg/amqp"
	"github.com/rabbitmq/rabbitmq-stream-go-client/pkg/message"
	"github.com/rabbitmq/rabbitmq-stream-go-client/pkg/stream"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gaborage/go-bricks/logger"
	"github.com/gaborage/go-bricks/testing/containers"
)

const (
	itStream       = "it-orders"
	itConsumerName = "it-group"
	itWaitTimeout  = 20 * time.Second
	itPollInterval = 50 * time.Millisecond
)

// recorder collects the messages a handler saw, in arrival order.
type recorder struct {
	mu       sync.Mutex
	offsets  []int64
	bodies   []string
	failAt   int64
	failures int
}

func (r *recorder) handle(_ context.Context, msg *Message) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.offsets = append(r.offsets, msg.Offset)
	r.bodies = append(r.bodies, string(msg.Data))
	if r.failAt >= 0 && msg.Offset == r.failAt {
		r.failures++
		return errors.New("deliberate handler failure")
	}
	return nil
}

func (r *recorder) snapshot() (offsets []int64, bodies []string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]int64(nil), r.offsets...), append([]string(nil), r.bodies...)
}

func (r *recorder) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.offsets)
}

// streamsTestEnv boots RabbitMQ with the stream plugin and returns the manager
// options every test in this file uses.
func streamsTestEnv(ctx context.Context, t *testing.T) ManagerOptions {
	t.Helper()

	cfg := containers.DefaultRabbitMQConfig()
	cfg.EnableStreamPlugin = true
	container := containers.MustStartRabbitMQContainer(ctx, t, cfg).WithCleanup(t)

	return ManagerOptions{
		URI: container.StreamURI(),
		// Without the resolver the broker advertises its container-internal address
		// and the client's follow-up dial from the host fails.
		AddressResolverHost: container.StreamHost(),
		AddressResolverPort: container.StreamPort(),
		OffsetStoreCount:    1,
		OffsetStoreInterval: 100 * time.Millisecond,
		Logger:              logger.New("error", false),
	}
}

// publish writes n messages through a test-only producer environment. Producers
// are deliberately outside the framework surface, so tests drive the client directly.
func publish(t *testing.T, opts ManagerOptions, streamName string, bodies []string) {
	t.Helper()

	envOpts := stream.NewEnvironmentOptions().
		SetUri(opts.URI).
		SetAddressResolver(stream.AddressResolver{Host: opts.AddressResolverHost, Port: opts.AddressResolverPort})
	env, err := stream.NewEnvironment(envOpts)
	require.NoError(t, err)
	defer func() { require.NoError(t, env.Close()) }()

	producer, err := env.NewProducer(streamName, stream.NewProducerOptions())
	require.NoError(t, err)

	batch := make([]message.StreamMessage, 0, len(bodies))
	for _, body := range bodies {
		batch = append(batch, amqp.NewMessage([]byte(body)))
	}
	require.NoError(t, producer.BatchSend(batch))
	require.NoError(t, producer.Close())
}

func bodiesFrom(prefix string, from, count int) []string {
	out := make([]string, 0, count)
	for i := 0; i < count; i++ {
		out = append(out, fmt.Sprintf("%s-%d", prefix, from+i))
	}
	return out
}

func startManager(t *testing.T, opts ManagerOptions, handler Handler) *Manager {
	t.Helper()

	decls := NewDeclarations()
	decls.DeclareStream(itStream, &StreamSpec{MaxLengthBytes: 10 * 1024 * 1024})
	decls.DeclareConsumer(&ConsumerOptions{
		Stream:  itStream,
		Name:    itConsumerName,
		Start:   OffsetFirst(),
		Handler: handler,
	})

	m := NewManager(opts)
	require.NoError(t, m.Start(context.Background(), decls))
	return m
}

func waitForCount(t *testing.T, r *recorder, want int) {
	t.Helper()
	require.Eventually(t, func() bool { return r.count() >= want }, itWaitTimeout, itPollInterval,
		"expected %d messages, saw %d", want, r.count())
}

// TestStreamsManagerConsumesAndRestoresOffsetIntegration proves the server-side
// offset contract end to end: a restarted manager with the same consumer name
// resumes after the last committed offset instead of replaying the stream.
func TestStreamsManagerConsumesAndRestoresOffsetIntegration(t *testing.T) {
	ctx := context.Background()
	opts := streamsTestEnv(ctx, t)

	first := &recorder{failAt: -1}
	m := startManager(t, opts, first.handle)

	publish(t, opts, itStream, bodiesFrom("msg", 0, 10))
	waitForCount(t, first, 10)

	offsets, bodies := first.snapshot()
	require.Len(t, offsets, 10)
	assert.Equal(t, bodiesFrom("msg", 0, 10), bodies, "messages arrive in stream order")
	for i := 1; i < len(offsets); i++ {
		assert.Greater(t, offsets[i], offsets[i-1], "offsets are monotonically increasing")
	}

	require.Eventually(t, func() bool {
		stored, ok := m.Stats()["stored_offsets"].(map[string]int64)
		return ok && stored[itStream+"/"+itConsumerName] == offsets[9]
	}, itWaitTimeout, itPollInterval, "the last handled offset must reach the broker")

	m.StopConsumers()
	require.NoError(t, m.Close())

	// A fresh manager under the same consumer name must not re-read the first 10.
	second := &recorder{failAt: -1}
	m2 := startManager(t, opts, second.handle)
	t.Cleanup(func() {
		m2.StopConsumers()
		require.NoError(t, m2.Close())
	})

	publish(t, opts, itStream, bodiesFrom("msg", 10, 3))
	waitForCount(t, second, 3)

	_, secondBodies := second.snapshot()
	assert.Equal(t, bodiesFrom("msg", 10, 3), secondBodies,
		"the stored offset wins over the declared Start position")
	assert.Equal(t, 3, second.count(), "no message is replayed after a clean stop")
}

// TestStreamsManagerSkipsFailedMessageIntegration pins the documented consequence
// of committing only after success: a failed message is skipped, never redelivered.
func TestStreamsManagerSkipsFailedMessageIntegration(t *testing.T) {
	ctx := context.Background()
	opts := streamsTestEnv(ctx, t)

	// Offset 4 is the fifth message in a fresh stream.
	failing := &recorder{failAt: 4}
	m := startManager(t, opts, failing.handle)

	publish(t, opts, itStream, bodiesFrom("msg", 0, 10))
	waitForCount(t, failing, 10)

	offsets, _ := failing.snapshot()
	assert.Equal(t, 1, failing.failures, "exactly one handler failure")
	assert.Equal(t, int64(9), offsets[9], "the stream continued past the failure")

	require.Eventually(t, func() bool {
		stored, ok := m.Stats()["stored_offsets"].(map[string]int64)
		return ok && stored[itStream+"/"+itConsumerName] == int64(9)
	}, itWaitTimeout, itPollInterval, "a later success commits a HIGHER offset than the failed one")

	m.StopConsumers()
	require.NoError(t, m.Close())

	replay := &recorder{failAt: -1}
	m2 := startManager(t, opts, replay.handle)
	t.Cleanup(func() {
		m2.StopConsumers()
		require.NoError(t, m2.Close())
	})

	publish(t, opts, itStream, bodiesFrom("msg", 10, 2))
	waitForCount(t, replay, 2)

	_, replayed := replay.snapshot()
	assert.Equal(t, bodiesFrom("msg", 10, 2), replayed,
		"the failed message is skipped on restart - streams have no redelivery")
}
