//go:build integration

package streams

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
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
	gobrickstrace "github.com/gaborage/go-bricks/trace"
)

const (
	itStream       = "it-orders"
	itConsumerName = "it-group"
	itSuperStream  = "it-orders-super"
	itSuperGroup   = "it-super-group"
	itPartitions   = 3
	itWaitTimeout  = 20 * time.Second
	itPollInterval = 50 * time.Millisecond
)

// recorder collects the messages a handler saw, in arrival order. A super-stream
// handler is called concurrently across partitions, so every field is guarded.
type recorder struct {
	mu         sync.Mutex
	offsets    []int64
	bodies     []string
	streams    []string
	properties []map[string]any
	failAt     int64
	failures   int
}

func (r *recorder) handle(_ context.Context, msg *Message) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.offsets = append(r.offsets, msg.Offset)
	r.bodies = append(r.bodies, string(msg.Data))
	r.streams = append(r.streams, msg.Stream)
	r.properties = append(r.properties, msg.Properties)
	if r.failAt >= 0 && msg.Offset == r.failAt {
		r.failures++
		return errors.New("deliberate handler failure")
	}
	return nil
}

// perStream splits what arrived by the stream it arrived on, which for a super
// stream is the partition.
func (r *recorder) perStream() (offsets map[string][]int64, bodies map[string][]string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	offsets, bodies = map[string][]int64{}, map[string][]string{}
	for i, name := range r.streams {
		offsets[name] = append(offsets[name], r.offsets[i])
		bodies[name] = append(bodies[name], r.bodies[i])
	}
	return offsets, bodies
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

// propertiesSnapshot reports the application properties of every delivery, in
// arrival order.
func (r *recorder) propertiesSnapshot() []map[string]any {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]map[string]any(nil), r.properties...)
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
		AddressResolverHost: container.Host(),
		AddressResolverPort: container.StreamPort(),
		OffsetStoreCount:    1,
		OffsetStoreInterval: 100 * time.Millisecond,
		Logger:              logger.New("error", false),
	}
}

// testEnvironment dials the broker the way the manager does, for the test's own
// producing and querying.
func testEnvironment(t *testing.T, opts ManagerOptions) *stream.Environment {
	t.Helper()

	envOpts := stream.NewEnvironmentOptions().
		SetUri(opts.URI).
		SetAddressResolver(stream.AddressResolver{Host: opts.AddressResolverHost, Port: opts.AddressResolverPort})
	env, err := stream.NewEnvironment(envOpts)
	require.NoError(t, err)
	return env
}

// publish writes n messages through a test-only producer environment. Producers
// are deliberately outside the framework surface, so tests drive the client directly.
func publish(t *testing.T, opts ManagerOptions, streamName string, bodies []string) {
	t.Helper()

	env := testEnvironment(t, opts)
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

// partitionName is how the broker names a super stream's partitions.
func partitionName(superStream string, index int) string {
	return fmt.Sprintf("%s-%d", superStream, index)
}

// publishAcrossPartitions writes bodies round-robin into the super stream's
// partitions and reports what went where. A partition is itself a plain stream, so
// publishing into it directly is exactly what a routing strategy would do — and it
// keeps which body lands on which partition deterministic, which a hash strategy
// would not.
func publishAcrossPartitions(t *testing.T, opts ManagerOptions, bodies []string) map[string][]string {
	t.Helper()

	perPartition := make(map[string][]string, itPartitions)
	for i, body := range bodies {
		name := partitionName(itSuperStream, i%itPartitions)
		perPartition[name] = append(perPartition[name], body)
	}
	for name, batch := range perPartition {
		publish(t, opts, name, batch)
	}
	return perPartition
}

// startSuperStreamManager starts one super-stream consumer group member.
func startSuperStreamManager(t *testing.T, opts ManagerOptions, handler Handler) *Manager {
	t.Helper()

	decls := NewDeclarations()
	decls.DeclareSuperStream(itSuperStream, itPartitions, &StreamSpec{MaxLengthBytes: 10 * 1024 * 1024})
	decls.DeclareSuperStreamConsumer(&SuperStreamConsumerOptions{
		SuperStream: itSuperStream,
		Name:        itSuperGroup,
		Start:       OffsetFirst(),
		Handler:     handler,
	})

	m := NewManager(opts)
	require.NoError(t, m.Start(context.Background(), decls))
	return m
}

func stopManager(t *testing.T, m *Manager) {
	t.Helper()
	m.StopConsumers()
	require.NoError(t, m.Close())
}

// partitionBaselines snapshots how many messages each member has handled per
// partition, so the next round's deliveries can be read as a delta.
func partitionBaselines(members []*recorder) []map[string]int {
	baselines := make([]map[string]int, len(members))
	for i, m := range members {
		_, bodies := m.perStream()
		counts := make(map[string]int, len(bodies))
		for name, handled := range bodies {
			counts[name] = len(handled)
		}
		baselines[i] = counts
	}
	return baselines
}

// roundOwnership derives the partition -> member index map of a single warm-up round
// from the delta since baselines. Ownership is read per round on purpose: a
// cumulative view would keep listing an outgoing member as a co-owner of a partition
// it has already handed over, and could never stabilize. It reports ok only when
// every partition was served, by exactly one member.
func roundOwnership(members []*recorder, baselines []map[string]int) (owners map[string]int, ok bool) {
	owners = make(map[string]int, itPartitions)
	for i, m := range members {
		_, bodies := m.perStream()
		for name, handled := range bodies {
			if len(handled) <= baselines[i][name] {
				continue
			}
			if _, contested := owners[name]; contested {
				// Two members served one partition: the handover is still in flight.
				return nil, false
			}
			owners[name] = i
		}
	}
	return owners, len(owners) == itPartitions
}

// warmUpGroup publishes throwaway rounds until the group's partition assignment has
// demonstrably stopped moving: every partition served within one round, and the same
// partition -> member map derived from two consecutive rounds. One round proves only
// that *a* rebalance landed, never that *the* rebalance finished — itPartitions
// partitions over two members settle unevenly, so the member that ends up owning two
// of them starts handling messages the moment its first handover lands, with the
// second still in flight. A round published into a partition mid-handover is consumed
// by the member losing it and may then be replayed to the member gaining it (the
// consumer-connection vs locator-connection window ADR-059 accepts), which is why the
// gate needs a second agreeing round and why the whole thing retries. The returned
// bodies must stay out of whatever the caller measures.
func warmUpGroup(t *testing.T, opts ManagerOptions, members ...*recorder) []string {
	t.Helper()

	warmUp := bodiesFrom("warmup", 0, itPartitions)

	const rounds = 6
	var previous map[string]int
	for range rounds {
		baselines := partitionBaselines(members)
		publishAcrossPartitions(t, opts, warmUp)

		var current map[string]int
		deadline := time.Now().Add(itWaitTimeout / rounds)
		for time.Now().Before(deadline) {
			if owners, ok := roundOwnership(members, baselines); ok {
				current = owners
				break
			}
			time.Sleep(itPollInterval)
		}
		if current == nil {
			// An inconclusive round breaks the chain: the next pair must agree on its own.
			previous = nil
			continue
		}
		if maps.Equal(previous, current) {
			return warmUp
		}
		previous = current
	}
	require.FailNow(t, "the group never converged",
		"no two consecutive rounds derived the same owner for all %d partitions; the last round derived %v (empty: a partition went unserved or was served by two members)",
		itPartitions, previous)
	return nil
}

// handledSince returns the bodies a member handled after baseline that belong to
// want, which keeps the warm-up traffic out of what a test measures.
func handledSince(r *recorder, baseline int, want []string) []string {
	_, bodies := r.snapshot()
	out := make([]string, 0, len(bodies)-baseline)
	for _, body := range bodies[baseline:] {
		if slices.Contains(want, body) {
			out = append(out, body)
		}
	}
	return out
}

// TestStreamsManagerConsumesSuperStreamPartitionsIntegration proves the whole
// per-partition contract against a broker: every partition is consumed, order is
// preserved within each one, offsets are committed per partition, and a restarted
// group member resumes on each partition independently instead of replaying it.
func TestStreamsManagerConsumesSuperStreamPartitionsIntegration(t *testing.T) {
	ctx := context.Background()
	opts := streamsTestEnv(ctx, t)

	first := &recorder{failAt: -1}
	m := startSuperStreamManager(t, opts, first.handle)

	published := publishAcrossPartitions(t, opts, bodiesFrom("msg", 0, 30))
	waitForCount(t, first, 30)

	offsets, bodies := first.perStream()
	require.Len(t, bodies, itPartitions, "every partition delivered")
	for i := range itPartitions {
		name := partitionName(itSuperStream, i)
		assert.Equal(t, published[name], bodies[name], "partition %s keeps its publish order", name)
		for j := 1; j < len(offsets[name]); j++ {
			assert.Greater(t, offsets[name][j], offsets[name][j-1],
				"offsets are monotonic within partition %s", name)
		}
	}

	require.Eventually(t, func() bool {
		stored, ok := m.Stats()["stored_offsets"].(map[string]int64)
		if !ok || len(stored) != itPartitions {
			return false
		}
		for i := range itPartitions {
			if stored[partitionName(itSuperStream, i)+"/"+itSuperGroup] != 9 {
				return false
			}
		}
		return true
	}, itWaitTimeout, itPollInterval, "each partition commits its own last handled offset")

	stopManager(t, m)

	// A fresh member under the same group name must resume on every partition.
	second := &recorder{failAt: -1}
	m2 := startSuperStreamManager(t, opts, second.handle)
	t.Cleanup(func() { stopManager(t, m2) })

	resumed := publishAcrossPartitions(t, opts, bodiesFrom("msg", 30, 9))
	waitForCount(t, second, 9)

	_, secondBodies := second.perStream()
	assert.Equal(t, resumed, secondBodies, "the stored offset wins per partition, not per super stream")
	assert.Equal(t, 9, second.count(), "no partition replays what the previous member committed")
}

// TestStreamsManagerSuperStreamDistributesPartitionsIntegration exercises the
// group: two members of one super-stream consumer group share the partitions, each
// message is handled by exactly one of them, and stopping the first hands its
// partitions to the second, resumed from the offsets it had committed.
func TestStreamsManagerSuperStreamDistributesPartitionsIntegration(t *testing.T) {
	ctx := context.Background()
	opts := streamsTestEnv(ctx, t)

	firstMember := &recorder{failAt: -1}
	m1 := startSuperStreamManager(t, opts, firstMember.handle)
	secondMember := &recorder{failAt: -1}
	m2 := startSuperStreamManager(t, opts, secondMember.handle)
	t.Cleanup(func() { stopManager(t, m2) })

	// Exclusivity is a property of a settled group, so nothing is measured until the
	// join's rebalance has demonstrably landed on both members.
	warmUp := warmUpGroup(t, opts, firstMember, secondMember)
	firstBase, secondBase := firstMember.count(), secondMember.count()

	measured := bodiesFrom("msg", 0, 30)
	publishAcrossPartitions(t, opts, measured)
	// Gate on the measured bodies themselves: a raw count is satisfied by duplicates.
	require.Eventually(t, func() bool {
		seen := slices.Concat(handledSince(firstMember, firstBase, measured),
			handledSince(secondMember, secondBase, measured))
		for _, body := range measured {
			if !slices.Contains(seen, body) {
				return false
			}
		}
		return true
	}, itWaitTimeout, itPollInterval, "the group as a whole consumes every partition")

	firstBodies := handledSince(firstMember, firstBase, measured)
	secondBodies := handledSince(secondMember, secondBase, measured)
	assert.ElementsMatch(t, measured, slices.Concat(firstBodies, secondBodies),
		"each message reaches exactly one member: a partition has one active consumer")
	assert.NotEmpty(t, firstBodies, "partitions are spread across the group, not served by one member")
	assert.NotEmpty(t, secondBodies)

	// The surviving member takes over the partitions the leaving one was active on.
	stopManager(t, m1)
	handedOver := secondMember.count()

	newBodies := bodiesFrom("msg", 30, 15)
	publishAcrossPartitions(t, opts, newBodies)
	// Gate on the new bodies themselves: a raw count is satisfied by 15 replays alone.
	require.Eventually(t, func() bool {
		_, seen := secondMember.snapshot()
		for _, body := range newBodies {
			if !slices.Contains(seen[handedOver:], body) {
				return false
			}
		}
		return true
	}, itWaitTimeout, itPollInterval, "the survivor is promoted on the orphaned partitions")

	_, afterTakeover := secondMember.snapshot()
	// Not exact equality: the leaver commits over its consumer connection while the
	// survivor's promotion queries the locator, an ordering window ADR-059 accepts as replay.
	assert.Subset(t, afterTakeover[handedOver:], newBodies,
		"promotion resumes the orphaned partitions and delivers everything published after it")
	assert.Subset(t, slices.Concat(warmUp, bodiesFrom("msg", 0, 45)), afterTakeover[handedOver:],
		"any extra is a replay of an already-published body, never a fabricated one")
}

// TestStreamsManagerSuperStreamPartitionMismatchIsSilentIntegration pins an
// asymmetry with the plain lane that operators need to know about: DeclareStream
// surfaces a retention mismatch as precondition-failed, but the client swallows
// StreamAlreadyExists for super streams, so re-declaring one with a different
// partition count is accepted and the existing topology is kept.
func TestStreamsManagerSuperStreamPartitionMismatchIsSilentIntegration(t *testing.T) {
	ctx := context.Background()
	opts := streamsTestEnv(ctx, t)

	first := NewManager(opts)
	firstDecls := NewDeclarations()
	firstDecls.DeclareSuperStream(itSuperStream, itPartitions, nil)
	require.NoError(t, first.Start(ctx, firstDecls))
	stopManager(t, first)

	second := NewManager(opts)
	conflicting := NewDeclarations()
	conflicting.DeclareSuperStream(itSuperStream, itPartitions+2, nil)

	err := second.Start(ctx, conflicting)

	require.NoError(t, err, "the broker does not reject the wider declaration")
	t.Cleanup(func() { stopManager(t, second) })

	env := testEnvironment(t, opts)
	defer func() { require.NoError(t, env.Close()) }()
	partitions, err := env.QueryPartitions(itSuperStream)
	require.NoError(t, err)
	assert.Len(t, partitions, itPartitions,
		"the declared widening never reached the topology - the first declaration still stands")
}

// startSACManager starts a single active consumer, whose promotion callback is the
// code path the client invokes from its own read-loop goroutine.
func startSACManager(t *testing.T, opts ManagerOptions, handler Handler) *Manager {
	t.Helper()

	decls := NewDeclarations()
	decls.DeclareStream(itStream, &StreamSpec{MaxLengthBytes: 10 * 1024 * 1024})
	decls.DeclareConsumer(&ConsumerOptions{
		Stream:  itStream,
		Name:    itConsumerName,
		Start:   OffsetFirst(),
		SAC:     true,
		Handler: handler,
	})

	m := NewManager(opts)
	require.NoError(t, m.Start(context.Background(), decls))
	return m
}

// TestStreamsManagerSingleActiveConsumerIntegration exercises the SAC promotion
// callback the client fires outside the manager's lock. Run with -race, it covers
// the environment snapshot that callback closes over: reading m.env there instead
// would be an unsynchronized read of a field Close nils.
func TestStreamsManagerSingleActiveConsumerIntegration(t *testing.T) {
	ctx := context.Background()
	opts := streamsTestEnv(ctx, t)

	first := &recorder{failAt: -1}
	m := startSACManager(t, opts, first.handle)

	publish(t, opts, itStream, bodiesFrom("msg", 0, 6))
	waitForCount(t, first, 6)

	_, bodies := first.snapshot()
	assert.Equal(t, bodiesFrom("msg", 0, 6), bodies, "the promoted consumer starts at the declared position")

	require.Eventually(t, func() bool {
		stored, ok := m.Stats()["stored_offsets"].(map[string]int64)
		return ok && stored[itStream+"/"+itConsumerName] == 5
	}, itWaitTimeout, itPollInterval, "the promoted consumer commits its offsets")

	m.StopConsumers()
	require.NoError(t, m.Close())

	// A new group member is promoted in turn and must resume from the stored offset.
	second := &recorder{failAt: -1}
	m2 := startSACManager(t, opts, second.handle)
	t.Cleanup(func() {
		m2.StopConsumers()
		require.NoError(t, m2.Close())
	})

	publish(t, opts, itStream, bodiesFrom("msg", 6, 2))
	waitForCount(t, second, 2)

	_, resumed := second.snapshot()
	assert.Equal(t, bodiesFrom("msg", 6, 2), resumed,
		"promotion resolves the stored offset, not the declared start position")
}

// TestStreamsManagerDisposesEnvironmentOnDeclareFailureIntegration exercises a real
// post-dial failure: re-declaring an existing stream with different retention makes
// the broker answer precondition-failed, so Start fails with the environment already
// dialed. Manager is exported API, so a caller that treats that error as fatal
// without calling Close must not leak the connection pool.
func TestStreamsManagerDisposesEnvironmentOnDeclareFailureIntegration(t *testing.T) {
	ctx := context.Background()
	opts := streamsTestEnv(ctx, t)

	first := NewManager(opts)
	firstDecls := NewDeclarations()
	firstDecls.DeclareStream(itStream, &StreamSpec{MaxAge: time.Hour})
	require.NoError(t, first.Start(ctx, firstDecls))
	first.StopConsumers()
	require.NoError(t, first.Close())

	// Same stream, different retention: the broker rejects the declaration.
	second := NewManager(opts)
	conflicting := NewDeclarations()
	conflicting.DeclareStream(itStream, &StreamSpec{MaxAge: 48 * time.Hour})

	err := second.Start(ctx, conflicting)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to declare stream")
	assert.Nil(t, second.env, "the dialed environment is disposed by the failed Start itself")
	assert.False(t, second.started)
	require.NoError(t, second.Close(), "the caller's follow-up Close short-circuits instead of double-closing")
}

// TestStreamsPublisherRoundTripIntegration is the proof of the correlation
// assumption the publisher is built on: Publish blocks until the confirmation for
// ITS OWN message arrives, matched by the pointer the client handed back. If
// ConfirmationStatus.GetMessage ever stopped returning the exact message passed to
// Send, no waiter would ever resolve and every Publish below would fail on its
// deadline instead.
//
// It is also the round trip: what a declared publisher sends is what a declared
// consumer receives, application properties included.
func TestStreamsPublisherRoundTripIntegration(t *testing.T) {
	ctx := context.Background()
	opts := streamsTestEnv(ctx, t)

	received := &recorder{failAt: -1}

	decls := NewDeclarations()
	decls.DeclareStream(itStream, &StreamSpec{MaxLengthBytes: 10 * 1024 * 1024})
	decls.DeclareConsumer(&ConsumerOptions{
		Stream:  itStream,
		Name:    itConsumerName,
		Start:   OffsetFirst(),
		Handler: received.handle,
	})
	publisher := decls.DeclarePublisher(&PublisherOptions{Stream: itStream})
	require.NoError(t, decls.Validate())

	m := NewManager(opts)
	require.NoError(t, m.Start(ctx, decls))
	t.Cleanup(func() {
		m.StopConsumers()
		require.NoError(t, m.Close())
	})

	assert.Equal(t, 1, m.Stats()["publishers"])
	assert.True(t, m.Ready(), "a bound publisher counts towards readiness")

	bodies := bodiesFrom("published", 0, 5)
	for i, body := range bodies {
		// The per-publish deadline is the latency bound: a confirmation that never
		// arrived, or arrived correlated to the wrong message, surfaces here.
		publishCtx, cancel := context.WithTimeout(ctx, itWaitTimeout)
		err := publisher.Publish(publishCtx, &PublishMessage{
			Data:       []byte(body),
			Properties: map[string]any{"index": int64(i)},
		})
		cancel()
		require.NoError(t, err, "publish %d must be confirmed by the broker", i)
	}

	waitForCount(t, received, len(bodies))

	_, got := received.snapshot()
	assert.Equal(t, bodies, got, "every published body arrives, in order")

	for i, props := range received.propertiesSnapshot() {
		assert.Equal(t, int64(i), props["index"], "the caller's application properties survive the round trip")
		assert.NotEmpty(t, props[gobrickstrace.HeaderTraceParent], "the framework injects W3C trace context")
		assert.NotEmpty(t, props[gobrickstrace.HeaderXRequestID])
	}
}

// hashPartitionIndex is the partition index the client's own murmur3 strategy
// picks for a routing key. It is computed over placeholder partition names, so it
// depends on the key alone and not on the order the broker reports the real
// partitions in — which is what lets a test compare GROUPINGS rather than names.
func hashPartitionIndex(t *testing.T, key string) int {
	t.Helper()

	names := make([]string, itPartitions)
	for i := range names {
		names[i] = fmt.Sprintf("p%d", i)
	}
	routed, err := stream.NewHashRoutingStrategy(func(message.StreamMessage) string { return key }).
		Route(amqp.NewMessage(nil), names)
	require.NoError(t, err)
	require.Len(t, routed, 1)
	return slices.Index(names, routed[0])
}

// TestStreamsSuperStreamPublisherPartitionsIntegration is the super-stream half of
// the publish proof: every message is confirmed and consumed exactly once, and the
// partition a message lands on is decided by its RoutingKey.
//
// The grouping assertion is what an extractor answering "" would fail — every
// message would pile onto one partition, so keys the hash separates would stop
// being separated. It compares which keys share a partition against which keys the
// client's own strategy sends to one index, never partition names, so it holds
// whatever order the broker reports partitions in.
func TestStreamsSuperStreamPublisherPartitionsIntegration(t *testing.T) {
	ctx := context.Background()
	opts := streamsTestEnv(ctx, t)

	received := &recorder{failAt: -1}

	decls := NewDeclarations()
	decls.DeclareSuperStream(itSuperStream, itPartitions, &StreamSpec{MaxLengthBytes: 10 * 1024 * 1024})
	decls.DeclareSuperStreamConsumer(&SuperStreamConsumerOptions{
		SuperStream: itSuperStream,
		Name:        itSuperGroup,
		Start:       OffsetFirst(),
		Handler:     received.handle,
	})
	publisher := decls.DeclareSuperStreamPublisher(&SuperStreamPublisherOptions{SuperStream: itSuperStream})
	require.NoError(t, decls.Validate())

	m := NewManager(opts)
	require.NoError(t, m.Start(ctx, decls))
	t.Cleanup(func() { stopManager(t, m) })

	assert.Equal(t, 1, m.Stats()["publishers"])
	assert.True(t, m.Ready(), "a bound super-stream publisher counts towards readiness")

	keys := []string{"customer-a", "customer-b", "customer-c", "customer-d", "customer-e", "customer-f"}
	indexOf := make(map[string]int, len(keys))
	distinct := map[int]bool{}
	for _, key := range keys {
		indexOf[key] = hashPartitionIndex(t, key)
		distinct[indexOf[key]] = true
	}
	require.Greater(t, len(distinct), 1,
		"the chosen routing keys must not all hash to one partition, or routing everything to one would pass")

	const perKey = 3
	keyOf := make(map[string]string, len(keys)*perKey)
	for _, key := range keys {
		for i := range perKey {
			body := fmt.Sprintf("%s-%d", key, i)
			keyOf[body] = key

			// The per-publish deadline is the latency bound: a confirmation that never
			// arrived, or arrived correlated to the wrong message, surfaces here.
			publishCtx, cancel := context.WithTimeout(ctx, itWaitTimeout)
			err := publisher.Publish(publishCtx, &PublishMessage{Data: []byte(body), RoutingKey: key})
			cancel()
			require.NoError(t, err, "publish of %q must be confirmed by the broker", body)
		}
	}

	// Gate on the bodies themselves: a raw count is satisfied by duplicates.
	require.Eventually(t, func() bool {
		_, arrived := received.snapshot()
		for body := range keyOf {
			if !slices.Contains(arrived, body) {
				return false
			}
		}
		return true
	}, itWaitTimeout, itPollInterval, "every published message must reach the consumer")

	// Invert the per-partition view into the partition each key's messages arrived on.
	partitionOf := make(map[string]string, len(keys))
	arrivals := make(map[string]int, len(keyOf))
	_, perPartition := received.perStream()
	for partition, bodies := range perPartition {
		for _, body := range bodies {
			arrivals[body]++
			key, published := keyOf[body]
			require.True(t, published, "an unpublished body arrived: %q", body)
			if existing, seen := partitionOf[key]; seen {
				require.Equal(t, existing, partition, "routing key %q was split across partitions", key)
				continue
			}
			partitionOf[key] = partition
		}
	}

	require.Len(t, arrivals, len(keyOf), "every published message arrives")
	for body, count := range arrivals {
		assert.Equal(t, 1, count, "%q arrived more than once", body)
	}

	for _, a := range keys {
		for _, b := range keys {
			assert.Equal(t, indexOf[a] == indexOf[b], partitionOf[a] == partitionOf[b],
				"keys %q and %q are grouped differently than the client's hash routes them", a, b)
		}
	}
}

// TestStreamsPublisherRejectedAfterStopIntegration pins the shutdown contract
// against a real broker: once the manager stopped, the publisher refuses rather
// than publishing into an environment about to be disposed.
func TestStreamsPublisherRejectedAfterStopIntegration(t *testing.T) {
	ctx := context.Background()
	opts := streamsTestEnv(ctx, t)

	decls := NewDeclarations()
	decls.DeclareStream(itStream, &StreamSpec{MaxLengthBytes: 10 * 1024 * 1024})
	publisher := decls.DeclarePublisher(&PublisherOptions{Stream: itStream})
	require.NoError(t, decls.Validate())

	m := NewManager(opts)
	require.NoError(t, m.Start(ctx, decls))

	publishCtx, cancel := context.WithTimeout(ctx, itWaitTimeout)
	require.NoError(t, publisher.Publish(publishCtx, &PublishMessage{Data: []byte("before-stop")}))
	cancel()

	m.StopConsumers()
	require.NoError(t, m.Close())

	err := publisher.Publish(ctx, &PublishMessage{Data: []byte("after-stop")})

	assert.ErrorIs(t, err, ErrPublisherClosed)
}
