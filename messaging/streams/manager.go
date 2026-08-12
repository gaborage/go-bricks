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

// consumerHandle is the subset of *ha.ReliableConsumer the manager drives. It
// keeps stop/flush bookkeeping testable without a broker.
type consumerHandle interface {
	offsetStorer
	Close() error
	GetStatus() int
}

// runningConsumer pairs a live client consumer with the offset bookkeeping its
// runner owns. The runner itself is retained by the client through the
// messagesHandler callback, so only the tracker is kept here.
type runningConsumer struct {
	stream  string
	name    string
	handle  consumerHandle
	tracker *offsetTracker
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
func NewManager(opts ManagerOptions) *Manager {
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
// started and returns an error — the caller makes that fatal.
func (m *Manager) Start(ctx context.Context, decls *Declarations) error {
	if decls == nil || decls.IsEmpty() {
		return nil
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	if m.started {
		return errors.New("streams manager already started")
	}

	// NewEnvironment closes its own locator socket before returning, so a failed
	// dial leaves nothing to clean up here.
	env, err := stream.NewEnvironment(m.environmentOptions())
	if err != nil {
		return fmt.Errorf("failed to connect to stream endpoint %s: %w", redactStreamURI(m.opts.URI), safeEnvError(err))
	}
	m.env = env

	m.log.Info().
		Str("uri", redactStreamURI(m.opts.URI)).
		Int("streams", len(decls.streams)).
		Int("consumers", len(decls.consumers)).
		Msg("Connected to RabbitMQ stream endpoint")

	consumeCtx, cancel := context.WithCancel(ctx)
	m.cancel = cancel

	if err := m.declareStreams(env, decls); err != nil {
		m.stopLocked()
		return err
	}

	for _, decl := range decls.consumers {
		if err := m.startConsumer(consumeCtx, env, decl); err != nil {
			m.stopLocked()
			return err
		}
	}

	m.started = true
	return nil
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

// startConsumer starts one reliable consumer for a declaration. env is the
// caller's snapshot of m.env, taken under m.mu.
func (m *Manager) startConsumer(ctx context.Context, env *stream.Environment, decl *consumerDeclaration) error {
	runner := &consumerRunner{
		name:    decl.Name,
		handler: decl.Handler,
		tracker: newOffsetTracker(m.opts.OffsetStoreCount, m.opts.OffsetStoreInterval, nil),
		log:     m.log,
		tracer:  otel.Tracer(tracerName),
		baseCtx: ctx,
	}

	opts := stream.NewConsumerOptions().
		SetConsumerName(decl.Name).
		SetOffset(m.resolveOffset(env, decl.Name, decl.Stream, decl.Start))

	if decl.SAC {
		// The promotion callback resolves the offset again: another group member
		// may have advanced it while this one was passive.
		//
		// SECURITY: the client calls this from its own read-loop goroutine, outside
		// m.mu and with no recover() anywhere in its call path. It therefore closes
		// over this snapshot instead of reading m.env: that field is written under
		// m.mu and nil'd by Close, so reading it here would be a data race, and a
		// promotion frame arriving after Close would dereference nil and kill the
		// process mid-shutdown. Taking m.mu here instead would deadlock — stopLocked
		// holds it across a blocking consumer Close.
		opts = opts.SetSingleActiveConsumer(stream.NewSingleActiveConsumer(
			func(streamName string, _ bool) stream.OffsetSpecification {
				return m.resolveOffset(env, decl.Name, streamName, decl.Start)
			}))
	}

	consumer, err := ha.NewReliableConsumer(env, decl.Stream, opts, runner.messagesHandler)
	if err != nil {
		return fmt.Errorf("failed to start consumer %q on stream %q: %w", decl.Name, decl.Stream, err)
	}

	m.consumers = append(m.consumers, &runningConsumer{
		stream:  decl.Stream,
		name:    decl.Name,
		handle:  consumer,
		tracker: runner.tracker,
	})

	m.log.Info().
		Str(logFieldStream, decl.Stream).
		Str(logFieldConsumer, decl.Name).
		Bool("single_active", decl.SAC).
		Msg("Stream consumer started")

	return nil
}

// resolveOffset asks the broker for the consumer's stored offset and falls back
// to the declared start position when it has none. A stored offset always wins,
// which is what makes restart behavior deterministic.
//
// The environment is a parameter, never m.env, so that no call path — including
// the SAC promotion callback the client invokes from its own goroutine — can read
// that guarded field without m.mu. m.log is immutable after NewManager.
func (m *Manager) resolveOffset(env *stream.Environment, consumerName, streamName string, start OffsetStart) stream.OffsetSpecification {
	stored, err := env.QueryOffset(consumerName, streamName)
	if err != nil && !errors.Is(err, stream.OffsetNotFoundError) {
		m.log.Warn().Err(err).
			Str(logFieldStream, streamName).
			Str(logFieldConsumer, consumerName).
			Msg("Could not query stored stream offset; using the declared start position")
	}
	return offsetSpecFor(stored, err, start)
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

// offsetSpecFor picks between a stored offset and the declared start position.
func offsetSpecFor(stored int64, queryErr error, start OffsetStart) stream.OffsetSpecification {
	if queryErr != nil {
		return start.specification()
	}
	return stream.OffsetSpecification{}.Offset(stored + 1)
}

// streamOptionsFrom renders a StreamSpec as client stream options. Zero-value
// fields are left unset so the broker's own defaults apply.
func streamOptionsFrom(spec *StreamSpec) *stream.StreamOptions {
	opts := stream.NewStreamOptions()
	if spec == nil {
		return opts
	}
	if spec.MaxAge > 0 {
		// Truncate to whole seconds and floor at 1s, matching
		// messaging.StreamQueueSpec: sub-second retention is inexpressible to the
		// broker and would render "0s", and passing whole seconds keeps the
		// client's round-to-nearest formatting from disagreeing with the AMQP
		// lane's truncation on a value like 1500ms.
		opts = opts.SetMaxAge(max(spec.MaxAge.Truncate(time.Second), time.Second))
	}
	if spec.MaxLengthBytes > 0 {
		opts = opts.SetMaxLengthBytes(stream.ByteCapacity{}.B(spec.MaxLengthBytes))
	}
	if spec.MaxSegmentSizeBytes > 0 {
		opts = opts.SetMaxSegmentSizeBytes(stream.ByteCapacity{}.B(spec.MaxSegmentSizeBytes))
	}
	return opts
}

// StopConsumers stops every consumer. Each one flushes its pending offset BEFORE
// closing, so a clean shutdown does not replay successfully handled messages.
// Idempotent.
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
		if err := rc.tracker.flush(rc.handle); err != nil {
			m.log.Warn().Err(err).
				Str(logFieldStream, rc.stream).
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

// Close stops the consumers and closes the environment. Idempotent.
func (m *Manager) Close() error {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.stopLocked()

	if m.env == nil {
		return nil
	}
	env := m.env
	m.env = nil
	return env.Close()
}

// Stats reports the manager state for the readiness probe and /ready body.
func (m *Manager) Stats() map[string]any {
	m.mu.Lock()
	defer m.mu.Unlock()

	offsets := make(map[string]int64, len(m.consumers))
	for _, rc := range m.consumers {
		if offset, ok := rc.tracker.lastStored(); ok {
			offsets[rc.stream+"/"+rc.name] = offset
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
