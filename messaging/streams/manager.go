package streams

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"sync"
	"time"

	"github.com/rabbitmq/rabbitmq-stream-go-client/pkg/ha"
	"github.com/rabbitmq/rabbitmq-stream-go-client/pkg/stream"
	"go.opentelemetry.io/otel"

	"github.com/gaborage/go-bricks/logger"
)

const (
	defaultOffsetStoreCount    = 500
	defaultOffsetStoreInterval = 5 * time.Second

	// redactedStreamURI stands in for a URI that could not be parsed, so a
	// malformed value can never reach a log line with its credentials attached.
	// #nosec G101 -- placeholder text, not a credential
	redactedStreamURI = "rabbitmq-stream://****:****@<host>:<port>/<vhost>"
)

// ManagerOptions configures the stream-protocol Manager. The zero value of every
// tuning field applies its default.
type ManagerOptions struct {
	// URI is the stream-protocol endpoint (rabbitmq-stream:// or rabbitmq-stream+tls://).
	URI string
	// AddressResolverHost and AddressResolverPort pin every connection to one
	// entry point. Required behind a load balancer, NAT, or Docker port mapping,
	// where the address the broker advertises is not reachable by the client.
	AddressResolverHost string
	AddressResolverPort int
	// OffsetStoreCount is how many successfully handled messages accumulate
	// before an offset is committed server-side.
	OffsetStoreCount int
	// OffsetStoreInterval is how long after the last commit a pending offset is
	// committed anyway.
	OffsetStoreInterval time.Duration
	// Logger receives consumer lifecycle and handler-failure events. Required.
	Logger logger.Logger
}

// consumerHandle is the subset of the client's reliable consumers the manager
// drives. It keeps stop bookkeeping testable without a broker, and it is
// deliberately narrower than offset storage: *ha.ReliableSuperStreamConsumer has
// no StoreCustomOffset at all, so the flush target is a per-stream function
// instead (see runningConsumer.storerFor).
type consumerHandle interface {
	Close() error
	GetStatus() int
}

// runningConsumer pairs a live client consumer with the offset bookkeeping its
// runner owns. The runner itself is retained by the client through the
// messagesHandler callback, so only the book is kept here.
type runningConsumer struct {
	stream string
	name   string
	handle consumerHandle
	// offsets tracks one committed position per stream this consumer reads.
	offsets *offsetBook
	// storerFor resolves the flush target of one of those streams. Only the
	// shutdown flush needs it; every in-flight commit goes through the consumer
	// the client hands to the delivery callback.
	storerFor func(streamName string) offsetStorer
}

// Manager owns the single stream-protocol Environment of a single-tenant service
// and the consumers started from its declarations. It is a plain struct on
// purpose (ADR-045): app/ consumes it concretely, so there is no interface to
// export and no second implementation to abstract over.
type Manager struct {
	opts ManagerOptions
	log  logger.Logger

	mu        sync.Mutex
	env       *stream.Environment
	consumers []*runningConsumer
	started   bool
	cancel    context.CancelFunc
}

// NewManager creates a Manager. It performs no I/O: the environment is dialed by
// Start, so a service that declares no streams never opens a connection.
//
// Panics on a nil Logger. Every consumer lifecycle, handler-failure and shutdown
// path dereferences it unguarded, so a wiring error would otherwise surface as a
// nil dereference on the first log line — mid-consumption, in production, from a
// goroutine the client owns and nothing recovers. Same fail-fast as
// httpclient.NewBuilder, and it keeps this constructor's single return value.
func NewManager(opts ManagerOptions) *Manager {
	if opts.Logger == nil {
		panic("streams: NewManager requires a non-nil Logger (pass deps.Logger)")
	}
	if opts.OffsetStoreCount <= 0 {
		opts.OffsetStoreCount = defaultOffsetStoreCount
	}
	if opts.OffsetStoreInterval <= 0 {
		opts.OffsetStoreInterval = defaultOffsetStoreInterval
	}
	return &Manager{opts: opts, log: opts.Logger}
}

// Start dials the broker, replays the stream declarations, and starts one
// reliable consumer per declared consumer. Empty declarations are a no-op that
// dials nothing. Any consumer that fails to start stops the ones already
// started and returns an error — the caller makes that fatal. A failure leaves
// nothing to dispose: the connection is closed before the error returns, so a
// caller that never calls Close does not leak it, and a retried Start cannot
// orphan the previous environment.
//
// Start is not a resume: once it has dialed, it refuses to run again until Close
// disposes the environment. StopConsumers deliberately leaves that environment
// open, so redialing over it would orphan a connection the registered closer
// still owns.
//
// ctx contributes its VALUES to every handler invocation, never its cancellation:
// consumers outlive the startup call that created them, and StopConsumers is what
// ends them. See consumeContext.
func (m *Manager) Start(ctx context.Context, decls *Declarations) error {
	if decls == nil || decls.IsEmpty() {
		return nil
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	// Guarded on the environment, not on started: stopLocked clears started but
	// leaves m.env for Close, so a started-only guard would let a Start after
	// StopConsumers dial over the live environment and orphan it.
	if m.env != nil {
		return errors.New("streams manager already started: Close it before starting again")
	}

	// NewEnvironment closes its own locator socket before returning, so a failed
	// dial leaves nothing to clean up here.
	env, err := stream.NewEnvironment(m.environmentOptions())
	if err != nil {
		return fmt.Errorf("failed to connect to stream endpoint %s: %w", redactStreamURI(m.opts.URI), safeEnvError(err))
	}
	m.env = env

	// Stats rather than len(decls.streams), which omits the super streams.
	stats := decls.Stats()
	m.log.Info().
		Str("uri", redactStreamURI(m.opts.URI)).
		Int("streams", stats.Streams).
		Int("super_streams", stats.SuperStreams).
		Int("consumers", stats.Consumers).
		Msg("Connected to RabbitMQ stream endpoint")

	consumeCtx, cancel := consumeContext(ctx)
	m.cancel = cancel

	if err := m.declareStreams(env, decls); err != nil {
		m.abortStartLocked()
		return err
	}

	if err := m.declareSuperStreams(env, decls); err != nil {
		m.abortStartLocked()
		return err
	}

	for _, decl := range decls.consumers {
		if err := m.startConsumer(consumeCtx, env, decl); err != nil {
			m.abortStartLocked()
			return err
		}
	}

	m.started = true
	return nil
}

// consumeContext derives the context the handlers run under: the caller's values
// (trace, tenant) are inherited, its cancellation is severed, and the returned
// cancel func — owned by StopConsumers — becomes the only way to stop consumption.
// Without the detach, a caller whose startup context is canceled after Start
// returns would silently stop consuming; context.Background() would instead drop
// the values the handlers' spans and logs are attributed by.
func consumeContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithCancel(context.WithoutCancel(ctx))
}

// environmentOptions renders the client environment configuration.
func (m *Manager) environmentOptions() *stream.EnvironmentOptions {
	opts := stream.NewEnvironmentOptions().SetUri(m.opts.URI)
	if m.opts.AddressResolverHost != "" {
		opts = opts.SetAddressResolver(stream.AddressResolver{
			Host: m.opts.AddressResolverHost,
			Port: m.opts.AddressResolverPort,
		})
	}
	return opts
}

// declareStreams replays the declared streams. The client treats an identical
// existing stream as success; a retention mismatch surfaces as
// precondition-failed and aborts startup rather than silently consuming a stream
// configured differently from the declaration.
func (m *Manager) declareStreams(env *stream.Environment, decls *Declarations) error {
	for _, s := range decls.streams {
		if err := env.DeclareStream(s.Name, streamOptionsFrom(&s.Spec)); err != nil {
			return fmt.Errorf("failed to declare stream %q: %w", s.Name, err)
		}
	}
	return nil
}

// declareSuperStreams replays the declared super streams. Note the asymmetry with
// declareStreams: the client swallows StreamAlreadyExists here, so a super stream
// that already exists with a DIFFERENT partition count or retention is accepted
// silently — see wiki/streams.md.
func (m *Manager) declareSuperStreams(env *stream.Environment, decls *Declarations) error {
	for _, s := range decls.superStreams {
		if err := env.DeclareSuperStream(s.Name, partitionOptionsFrom(s.Partitions, &s.Spec)); err != nil {
			return fmt.Errorf("failed to declare super stream %q: %w", s.Name, err)
		}
	}
	return nil
}

// startConsumer starts one consumer for a declaration, on the client API its kind
// requires. env is the caller's snapshot of m.env, taken under m.mu.
func (m *Manager) startConsumer(ctx context.Context, env *stream.Environment, decl *consumerDeclaration) error {
	if decl.Super {
		return m.startSuperStreamConsumer(ctx, env, decl)
	}
	return m.startStreamConsumer(ctx, env, decl)
}

// startStreamConsumer starts one reliable consumer on a plain stream.
func (m *Manager) startStreamConsumer(ctx context.Context, env *stream.Environment, decl *consumerDeclaration) error {
	runner := m.newRunner(ctx, decl)

	opts := stream.NewConsumerOptions().
		SetConsumerName(decl.Name).
		SetOffset(m.resolveOffset(env, decl.Name, decl.Stream, decl.Start, runner.offsets))

	if decl.SAC {
		// The promotion callback resolves the offset again: another group member
		// may have advanced it while this one was passive.
		//
		// The client calls it from its own read-loop goroutine, outside m.mu and with
		// no recover() anywhere in its call path. It therefore closes over this
		// snapshot instead of reading m.env: that field is written under m.mu and
		// nil'd by Close, so reading it here would be a data race, and a promotion
		// frame arriving after Close would dereference nil and kill the process
		// mid-shutdown. Taking m.mu here instead would deadlock — stopLocked holds
		// it across a blocking consumer Close.
		opts = opts.SetSingleActiveConsumer(stream.NewSingleActiveConsumer(
			func(streamName string, _ bool) stream.OffsetSpecification {
				return m.resolveOffset(env, decl.Name, streamName, decl.Start, runner.offsets)
			}))
	}

	consumer, err := ha.NewReliableConsumer(env, decl.Stream, opts, runner.messagesHandler)
	if err != nil {
		return fmt.Errorf("failed to start consumer %q on stream %q: %w", decl.Name, decl.Stream, err)
	}

	// One stream, so one flush target: the reliable consumer itself.
	m.trackConsumer(decl, consumer, runner, func(string) offsetStorer { return consumer })
	return nil
}

// startSuperStreamConsumer starts one reliable consumer across every partition of
// a super stream. env is the caller's snapshot of m.env, taken under m.mu.
func (m *Manager) startSuperStreamConsumer(ctx context.Context, env *stream.Environment, decl *consumerDeclaration) error {
	runner := m.newRunner(ctx, decl)

	// Always a single active consumer group. The client attaches every partition
	// with one shared offset specification, so this callback — which the broker
	// fires once per partition, on promotion — is the only place a per-partition
	// stored offset can be restored. See ADR-059.
	//
	// It closes over the env snapshot rather than m.env, for the reason spelled out
	// in startStreamConsumer: the client calls this from its own goroutine, outside
	// m.mu, with no recover() in its call path.
	opts := stream.NewSuperStreamConsumerOptions().
		SetConsumerName(decl.Name).
		SetSingleActiveConsumer(stream.NewSingleActiveConsumer(
			func(partition string, _ bool) stream.OffsetSpecification {
				return m.resolveOffset(env, decl.Name, partition, decl.Start, runner.offsets)
			}))

	consumer, err := ha.NewReliableSuperStreamConsumer(env, decl.Stream, runner.messagesHandler, opts)
	if err != nil {
		return fmt.Errorf("failed to start consumer %q on super stream %q: %w", decl.Name, decl.Stream, err)
	}

	// The shutdown flush goes through the environment, per partition:
	// *ha.ReliableSuperStreamConsumer has no StoreCustomOffset, and the partition
	// consumer that delivered the last message may already have been replaced by a
	// reconnect.
	m.trackConsumer(decl, consumer, runner, func(partition string) offsetStorer {
		return envOffsetStorer{env: env, consumer: decl.Name, stream: partition}
	})
	return nil
}

// newRunner builds the delivery callback state of one declared consumer.
func (m *Manager) newRunner(ctx context.Context, decl *consumerDeclaration) *consumerRunner {
	return &consumerRunner{
		name:    decl.Name,
		handler: decl.Handler,
		offsets: m.newOffsetBook(),
		log:     m.log,
		tracer:  otel.Tracer(tracerName),
		baseCtx: ctx,
	}
}

// trackConsumer records a started consumer for readiness, stats and shutdown.
func (m *Manager) trackConsumer(decl *consumerDeclaration, handle consumerHandle, runner *consumerRunner, storerFor func(streamName string) offsetStorer) {
	m.consumers = append(m.consumers, &runningConsumer{
		stream:    decl.Stream,
		name:      decl.Name,
		handle:    handle,
		offsets:   runner.offsets,
		storerFor: storerFor,
	})

	m.log.Info().
		Str(logFieldStream, decl.Stream).
		Str(logFieldConsumer, decl.Name).
		Bool("single_active", decl.SAC).
		Bool("partitioned", decl.Super).
		Msg("Stream consumer started")
}

// envOffsetStorer commits one stream's offset through the environment's locator
// connection instead of through a consumer.
type envOffsetStorer struct {
	env      *stream.Environment
	consumer string
	stream   string
}

func (s envOffsetStorer) StoreCustomOffset(offset int64) error {
	return s.env.StoreOffset(s.consumer, s.stream, offset)
}

// newOffsetBook builds the offset bookkeeping of one consumer, with the manager's
// commit policy applied to every stream it ends up tracking.
func (m *Manager) newOffsetBook() *offsetBook {
	return newOffsetBook(func() *offsetTracker {
		return newOffsetTracker(m.opts.OffsetStoreCount, m.opts.OffsetStoreInterval, nil)
	})
}

// resolveOffset asks the broker for the consumer's stored offset and falls back
// to the declared start position when it has none. A stored offset always wins,
// which is what makes restart behavior deterministic. committed is the consumer's
// own bookkeeping, consulted only when the broker cannot be asked.
//
// The environment is a parameter, never m.env, so that no call path — including
// the SAC promotion callback the client invokes from its own goroutine — can read
// that guarded field without m.mu. m.log is immutable after NewManager, and the
// book takes only its own lock, so neither is a route back to m.mu.
func (m *Manager) resolveOffset(env *stream.Environment, consumerName, streamName string, start OffsetStart, committed *offsetBook) stream.OffsetSpecification {
	stored, err := env.QueryOffset(consumerName, streamName)
	localOffset, hasLocal := committed.stored()[streamName]
	m.reportOffsetQuery(err, consumerName, streamName, hasLocal)
	return offsetSpecFor(stored, err, start, localOffset, hasLocal)
}

// reportOffsetQuery reports a failed offset query at ERROR. A missing offset is
// routine first-run behavior and stays silent; anything else means the attach
// position was chosen without the broker's answer, which replays messages — a
// data-affecting event an operator should see, not a warning among warnings.
func (m *Manager) reportOffsetQuery(err error, consumerName, streamName string, hasLocal bool) {
	if err == nil || errors.Is(err, stream.OffsetNotFoundError) {
		return
	}
	m.log.Error().Err(err).
		Str(logFieldStream, streamName).
		Str(logFieldConsumer, consumerName).
		Bool("resumed_from_local_commit", hasLocal).
		Msg("Could not query the stored stream offset; attaching at a position that replays rather than skips")
}

// safeEnvError strips the URI out of an environment-construction failure.
//
// SECURITY: the client parses the endpoint with url.Parse and returns its
// *url.Error verbatim, whose Error() renders the raw URI — credentials included.
// Only the cause is kept; the endpoint is reported separately, redacted. This is
// reachable when config.Validate never ran (app.NewWithConfig, see
// app/streams_setup.go).
func safeEnvError(err error) error {
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		return fmt.Errorf("invalid stream URI: %w", urlErr.Err)
	}
	return err
}

// offsetSpecFor picks the position to attach at, given the broker's answer and
// whatever this process has committed itself.
//
// Only a MISSING offset falls back to the declared start. Any other query failure
// must not: with the zero-value Start meaning "next message written from now", a
// transient RPC error would attach past everything written since the last commit,
// and streams have no redelivery to get it back. Delivery is at-least-once and
// handlers are documented idempotent, so replaying is the affordable mistake and
// skipping is not.
func offsetSpecFor(stored int64, queryErr error, start OffsetStart, localOffset int64, hasLocal bool) stream.OffsetSpecification {
	switch {
	case queryErr == nil:
		return stream.OffsetSpecification{}.Offset(stored + 1)
	case errors.Is(queryErr, stream.OffsetNotFoundError):
		// Nothing was ever committed under this name: a first run, which is exactly
		// what the declared start position is for.
		return start.specification()
	case hasLocal:
		// The broker could not be asked, but this process committed a position for
		// this stream itself. It is no older than the broker's, so it is the closest
		// truth available.
		return stream.OffsetSpecification{}.Offset(localOffset + 1)
	default:
		// Nothing is known at all. Replaying what retention still holds is bounded
		// and idempotent; guessing forward is neither.
		return stream.OffsetSpecification{}.First()
	}
}

// retentionOptions is the setter triple both client option types expose, each
// returning its own type. The self-reference on T is what lets one renderer serve
// both, so a StreamSpec field cannot reach the broker on one kind and be dropped on
// the other.
type retentionOptions[T any] interface {
	SetMaxAge(time.Duration) T
	SetMaxLengthBytes(*stream.ByteCapacity) T
	SetMaxSegmentSizeBytes(*stream.ByteCapacity) T
}

// applyRetention renders a StreamSpec onto either client option type. Zero-value
// fields are left unset so the broker's own defaults apply.
func applyRetention[T retentionOptions[T]](opts T, spec *StreamSpec) T {
	if spec == nil {
		return opts
	}
	if spec.MaxAge > 0 {
		opts = opts.SetMaxAge(clampedMaxAge(spec.MaxAge))
	}
	if spec.MaxLengthBytes > 0 {
		opts = opts.SetMaxLengthBytes(stream.ByteCapacity{}.B(spec.MaxLengthBytes))
	}
	if spec.MaxSegmentSizeBytes > 0 {
		opts = opts.SetMaxSegmentSizeBytes(stream.ByteCapacity{}.B(spec.MaxSegmentSizeBytes))
	}
	return opts
}

// streamOptionsFrom renders a StreamSpec as client stream options.
func streamOptionsFrom(spec *StreamSpec) *stream.StreamOptions {
	return applyRetention(stream.NewStreamOptions(), spec)
}

// partitionOptionsFrom renders a StreamSpec as super-stream partition options; the
// retention applies to every partition.
func partitionOptionsFrom(partitions int, spec *StreamSpec) *stream.PartitionsOptions {
	return applyRetention(stream.NewPartitionsOptions(partitions), spec)
}

// clampedMaxAge renders a retention age the way both client renderers and the AMQP
// lane agree on: truncated to whole seconds, floored at 1s.
//
// Second granularity is RabbitMQ's, so a sub-second value is inexpressible — the
// super-stream renderer truncates it (int(MaxAge.Seconds())) and the plain-stream
// one rounds it, so without the floor the same 500ms would disable retention on one
// and keep a second of it on the other. Truncating first also keeps 1500ms from
// reaching the broker as 2s here and 1s in the AMQP lane.
func clampedMaxAge(maxAge time.Duration) time.Duration {
	return max(maxAge.Truncate(time.Second), time.Second)
}

// StopConsumers stops every consumer. Each one flushes its pending offset BEFORE
// closing, so a clean shutdown does not replay successfully handled messages.
// Idempotent.
//
// This is shutdown phase one, not a pause: the environment stays open for Close
// to dispose, and Start stays refused until then.
func (m *Manager) StopConsumers() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.stopLocked()
}

func (m *Manager) stopLocked() {
	if m.cancel != nil {
		m.cancel()
		m.cancel = nil
	}

	for _, rc := range m.consumers {
		for _, failure := range rc.offsets.flush(rc.storerFor) {
			m.log.Warn().Err(failure.err).
				Str(logFieldStream, failure.stream).
				Str(logFieldConsumer, rc.name).
				Msg("Failed to flush stream offset on shutdown")
		}
		if err := rc.handle.Close(); err != nil {
			m.log.Warn().Err(err).
				Str(logFieldStream, rc.stream).
				Str(logFieldConsumer, rc.name).
				Msg("Failed to close stream consumer")
		}
	}

	m.consumers = nil
	m.started = false
}

// abortStartLocked unwinds a Start that failed after the dial: it stops whatever
// consumers came up and disposes the environment, so a caller that treats the
// error as fatal without calling Close leaks nothing and a retried Start cannot
// orphan the previous connection pool.
func (m *Manager) abortStartLocked() {
	m.stopLocked()
	if err := m.closeEnvLocked(); err != nil {
		m.log.Warn().Err(err).Msg("Failed to close stream environment after a failed start")
	}
}

// closeEnvLocked is the single disposal path for the environment. Nils the field
// so a later Close short-circuits instead of closing twice.
func (m *Manager) closeEnvLocked() error {
	if m.env == nil {
		return nil
	}
	env := m.env
	m.env = nil
	return env.Close()
}

// Close stops the consumers and closes the environment. Idempotent.
func (m *Manager) Close() error {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.stopLocked()
	return m.closeEnvLocked()
}

// Stats reports the manager state for the readiness probe and /ready body.
func (m *Manager) Stats() map[string]any {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Keyed by the stream a position belongs to, which for a super stream is the
	// partition rather than the declared name.
	offsets := make(map[string]int64, len(m.consumers))
	for _, rc := range m.consumers {
		for streamName, offset := range rc.offsets.stored() {
			offsets[streamName+"/"+rc.name] = offset
		}
	}

	return map[string]any{
		"started":               m.started,
		"consumers":             len(m.consumers),
		"ready":                 m.readyLocked(),
		"stored_offsets":        offsets,
		"offset_store_count":    m.opts.OffsetStoreCount,
		"offset_flush_interval": m.opts.OffsetStoreInterval.String(),
	}
}

// Ready reports whether every started consumer is currently connected.
func (m *Manager) Ready() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.readyLocked()
}

func (m *Manager) readyLocked() bool {
	if !m.started {
		return false
	}
	for _, rc := range m.consumers {
		if rc.handle.GetStatus() != ha.StatusOpen {
			return false
		}
	}
	return true
}

// redactStreamURI masks the credentials in a stream URI so it can be logged.
//
// SECURITY: messaging.streams.uri carries a broker password. Everything that
// logs the endpoint goes through this function, and an unparseable value degrades
// to a fixed placeholder rather than echoing the raw input.
func redactStreamURI(uri string) string {
	u, err := url.Parse(uri)
	if err != nil || u.Host == "" {
		return redactedStreamURI
	}

	username := "****"
	if u.User != nil && u.User.Username() != "" {
		username = u.User.Username()
	}

	redacted := u.Scheme + "://" + username + ":****@" + u.Host + u.EscapedPath()
	if u.RawQuery != "" {
		redacted += "?<redacted>"
	}
	return redacted
}
