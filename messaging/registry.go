package messaging

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"sync"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"
	"go.opentelemetry.io/otel/attribute"
	semconv "go.opentelemetry.io/otel/semconv/v1.32.0"

	"github.com/gaborage/go-bricks/logger"
	pipeline "github.com/gaborage/go-bricks/messaging/internal/delivery"
	"github.com/gaborage/go-bricks/messaging/internal/tracking"
)

// defaultConsumerResubscribeDelay is the wait between consumer re-subscribe
// attempts after the broker closes the delivery channel (a connection or
// channel flap). It mirrors the AMQP client's reconnect delay so consumers do
// not hammer ConsumeFromQueue while the client is still re-establishing its
// connection. Overridable per-registry (see Registry.resubscribeDelay) for
// fast test iteration.
const defaultConsumerResubscribeDelay = 5 * time.Second

// consumerResubscribeWarnFromAttempt is the first consecutive failed
// re-subscribe attempt logged at WARN instead of Debug. It is also the streak at
// which ConsumerState.GivenUp reports the outage, so moving it moves a readiness
// verdict as well as a log level.
const consumerResubscribeWarnFromAttempt = 5

// RegistryInterface defines the contract for messaging infrastructure management.
// This interface allows for easy mocking and testing of messaging infrastructure.
type RegistryInterface interface {
	// Registration methods
	RegisterExchange(declaration *ExchangeDeclaration)
	RegisterQueue(declaration *QueueDeclaration)
	RegisterBinding(declaration *BindingDeclaration)
	RegisterPublisher(declaration *PublisherDeclaration)
	RegisterConsumer(declaration *ConsumerDeclaration)

	// Infrastructure lifecycle
	DeclareInfrastructure(ctx context.Context) error
	StartConsumers(ctx context.Context) error
	StopConsumers()

	// Accessor methods for testing/monitoring
	Exchanges() map[string]*ExchangeDeclaration
	Queues() map[string]*QueueDeclaration
	Bindings() []*BindingDeclaration
	Publishers() []*PublisherDeclaration
	Consumers() []*ConsumerDeclaration

	// Validation methods
	ValidatePublisher(exchange, routingKey string) bool
	ValidateConsumer(queue string) bool
}

// Registry manages messaging infrastructure declarations across modules.
// It ensures queues, exchanges, and bindings are properly declared before use.
// It also manages consumer lifecycle and handles message routing to handlers.
type Registry struct {
	client     AMQPClient
	logger     logger.Logger
	exchanges  map[string]*ExchangeDeclaration
	queues     map[string]*QueueDeclaration
	bindings   []*BindingDeclaration
	publishers []*PublisherDeclaration
	// Mutex protects: exchanges, queues, bindings, publishers, consumerIndex, consumerOrder, consumerStates, consumersActive, declared, redeclareObserverDone
	// NOTE: GoBricks startup is single-threaded, but multi-tenant scenarios
	// may have concurrent registry access during tenant initialization.
	mu              sync.RWMutex
	consumerIndex   map[consumerKey]*ConsumerDeclaration // Defense-in-depth deduplication
	consumerOrder   []consumerKey                        // Deterministic iteration order
	consumerStates  map[consumerKey]*consumerState       // Runtime subscription state, seeded when a consumer starts
	declared        bool
	consumersActive bool
	cancelConsumers context.CancelFunc
	// tenantStamps makes every delivery's tenant stamp seed the handler context:
	// true only under multitenant.enabled together with messaging.tenancy: shared.
	// Written once by setTenantStamps before the registry leaves the manager and
	// never again, so it needs no mutex and is deliberately absent from the "Mutex
	// protects" list above.
	//
	// It is not a NewRegistry parameter because that function is exported and
	// shipped: adding one is an incompatible change (apidiff), and no consumer
	// builds a Registry — the manager is the only caller.
	tenantStamps bool
	// resubscribeDelay is the backoff between consumer re-subscribe attempts
	// after a delivery-channel close. Defaults to defaultConsumerResubscribeDelay;
	// tests lower it for fast iteration.
	resubscribeDelay time.Duration
	// declaredGenerations is the channel generation the topology was last declared
	// on, PER redeclare source. It is keyed per source and not once for the
	// registry because sources number their channels independently — each starts
	// at generation 1 — so a rotation only one of them saw would otherwise be
	// swallowed by a number another source already used. Keys are always
	// pointers — channelGeneration is unexported, so only this package's client
	// types can be a source — and never an uncomparable value.
	// Guarded by redeclareMu.
	declaredGenerations map[channelGenerationer]uint64
	// redeclareMu serializes redeclare passes, so two sources never declare the
	// same generation twice and neither declaredGenerations nor redeclareSkip is
	// written concurrently. Lock order is redeclareMu before mu, including in
	// DeclareInfrastructure, which seeds declaredGenerations.
	redeclareMu sync.Mutex
	// redeclareSkip holds declarations refused with PRECONDITION_FAILED. Guarded by redeclareMu.
	redeclareSkip map[string]struct{}
	// redeclareStop is closed once, by StopConsumers, and ends every redeclare
	// driver this registry has: the observer it runs on its own client parks on
	// it. Created by NewRegistry; nil on a zero-value Registry, which the two
	// helpers below tolerate the way the manager's zero-value guards do.
	redeclareStop     chan struct{}
	redeclareStopOnce sync.Once
	// redeclareObserverDone is closed when the observer this registry runs on its
	// own client returns, as the client's reconnectDone is, so a test can confirm
	// the exit instead of waiting on a leak. Nil until DeclareInfrastructure
	// starts an observer, and it starts at most one.
	redeclareObserverDone chan struct{}
}

// stopRedeclaring ends every redeclare driver, idempotently.
func (r *Registry) stopRedeclaring() {
	if r.redeclareStop == nil {
		return
	}
	r.redeclareStopOnce.Do(func() { close(r.redeclareStop) })
}

// setTenantStamps records whether this registry's consumers read a tenant stamp.
// Called by the manager immediately after NewRegistry, before the registry is
// stored or any consumer starts, so no delivery can observe it unset.
func (r *Registry) setTenantStamps(enabled bool) {
	r.tenantStamps = enabled
}

// setResubscribeDelay overrides the backoff floor between re-subscribe attempts.
// Called by the manager immediately after NewRegistry, before any consumer starts;
// a non-positive delay leaves the default in place.
func (r *Registry) setResubscribeDelay(delay time.Duration) {
	if delay > 0 {
		r.resubscribeDelay = delay
	}
}

// ExchangeDeclaration defines an exchange to be declared
type ExchangeDeclaration struct {
	Name       string         // Exchange name
	Type       string         // Exchange type: an ExchangeType* constant or an "x-" plugin type
	Durable    bool           // Survive server restart
	AutoDelete bool           // Delete when no longer used
	Internal   bool           // Internal exchange
	NoWait     bool           // Do not wait for server confirmation
	Args       map[string]any // Additional arguments
}

// QueueDeclaration defines a queue to be declared
type QueueDeclaration struct {
	Name       string         // Queue name
	Durable    bool           // Survive server restart
	AutoDelete bool           // Delete when no consumers
	Exclusive  bool           // Only accessible by declaring connection
	NoWait     bool           // Do not wait for server confirmation
	Args       map[string]any // Additional arguments
}

// BindingDeclaration defines a queue-to-exchange binding
type BindingDeclaration struct {
	Queue      string         // Queue name
	Exchange   string         // Exchange name
	RoutingKey string         // Routing key pattern
	NoWait     bool           // Do not wait for server confirmation
	Args       map[string]any // Additional arguments
}

// PublisherDeclaration defines what a module publishes
type PublisherDeclaration struct {
	Exchange    string         // Target exchange
	RoutingKey  string         // Default routing key
	EventType   string         // Event type identifier
	Description string         // Human-readable description
	Mandatory   bool           // Message must be routed to a queue
	Immediate   bool           // Message must be delivered immediately
	Headers     map[string]any // Default headers
}

// ConsumerDeclaration defines what a module consumes and how to handle messages
type ConsumerDeclaration struct {
	Queue         string         // Queue to consume from
	Consumer      string         // Consumer tag
	AutoAck       bool           // Automatically acknowledge messages
	Exclusive     bool           // Exclusive consumer
	NoLocal       bool           // Do not deliver to the connection that published
	NoWait        bool           // Do not wait for server confirmation
	EventType     string         // Event type identifier
	Description   string         // Human-readable description
	Handler       MessageHandler // Message handler (optional for documentation-only declarations)
	Workers       int            // Number of concurrent workers (0 = auto-scale to NumCPU*4, >0 = explicit)
	PrefetchCount int            // RabbitMQ prefetch count (0 = auto-scale to Workers*10, capped at 500)
	Args          map[string]any // Per-consumer arguments forwarded to basic.consume (x-stream-offset, x-priority, ...)
	// TenantOptional lets this consumer run a delivery that carries no tenant stamp,
	// for a control-plane consumer whose events belong to no tenant. The default is
	// false — fail closed — and it never admits a stamp that is present but unusable.
	TenantOptional bool
}

// NewRegistry creates a new messaging registry
func NewRegistry(client AMQPClient, log logger.Logger) *Registry {
	return &Registry{
		client:              client,
		logger:              log,
		exchanges:           make(map[string]*ExchangeDeclaration),
		queues:              make(map[string]*QueueDeclaration),
		bindings:            make([]*BindingDeclaration, 0),
		publishers:          make([]*PublisherDeclaration, 0),
		consumerIndex:       make(map[consumerKey]*ConsumerDeclaration),
		consumerOrder:       make([]consumerKey, 0),
		consumerStates:      make(map[consumerKey]*consumerState),
		resubscribeDelay:    defaultConsumerResubscribeDelay,
		redeclareSkip:       make(map[string]struct{}),
		redeclareStop:       make(chan struct{}),
		declaredGenerations: make(map[channelGenerationer]uint64),
	}
}

// RegisterExchange registers an exchange for declaration
func (r *Registry) RegisterExchange(declaration *ExchangeDeclaration) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.declared {
		r.logger.Warn().
			Str("exchange", declaration.Name).
			Msg("Cannot register exchange after infrastructure has been declared")
		return
	}

	r.exchanges[declaration.Name] = declaration
	r.logger.Debug().
		Str("exchange", declaration.Name).
		Str("type", declaration.Type).
		Msg("Registered exchange for declaration")
}

// RegisterQueue registers a queue for declaration
func (r *Registry) RegisterQueue(declaration *QueueDeclaration) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.declared {
		r.logger.Warn().
			Str("queue", declaration.Name).
			Msg("Cannot register queue after infrastructure has been declared")
		return
	}

	r.queues[declaration.Name] = declaration
	r.logger.Debug().
		Str("queue", declaration.Name).
		Msg("Registered queue for declaration")
}

// RegisterBinding registers a binding for declaration
func (r *Registry) RegisterBinding(declaration *BindingDeclaration) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.declared {
		r.logger.Warn().
			Str("queue", declaration.Queue).
			Str("exchange", declaration.Exchange).
			Msg("Cannot register binding after infrastructure has been declared")
		return
	}

	r.bindings = append(r.bindings, declaration)
	r.logger.Debug().
		Str("queue", declaration.Queue).
		Str("exchange", declaration.Exchange).
		Str("routing_key", declaration.RoutingKey).
		Msg("Registered binding for declaration")
}

// RegisterPublisher registers a publisher declaration
func (r *Registry) RegisterPublisher(declaration *PublisherDeclaration) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.publishers = append(r.publishers, declaration)
	r.logger.Debug().
		Str("exchange", declaration.Exchange).
		Str("routing_key", declaration.RoutingKey).
		Str("event_type", declaration.EventType).
		Msg("Registered publisher")
}

// RegisterConsumer registers a consumer declaration
func (r *Registry) RegisterConsumer(declaration *ConsumerDeclaration) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.consumerIndex == nil {
		r.consumerIndex = make(map[consumerKey]*ConsumerDeclaration)
	}

	key := consumerKeyFor(declaration)

	// Defense-in-depth: warn and skip if duplicate detected during replay
	if _, exists := r.consumerIndex[key]; exists {
		r.logger.Warn().
			Str("queue", key.Queue).
			Str("consumer", key.Consumer).
			Str("event_type", key.EventType).
			Msg("duplicate consumer encountered during registry replay - skipping second registration")
		return
	}

	r.consumerIndex[key] = declaration
	r.consumerOrder = append(r.consumerOrder, key)

	r.logger.Debug().
		Str("queue", declaration.Queue).
		Str("consumer", declaration.Consumer).
		Str("event_type", declaration.EventType).
		Msg("Registered consumer")
}

// DeclareInfrastructure declares all registered messaging infrastructure
func (r *Registry) DeclareInfrastructure(ctx context.Context) error {
	// The first declare IS a topology pass: it seeds declaredGenerations and must
	// not interleave with one a redeclare source drives. redeclareMu before mu is
	// the documented order — topologySteps reads the declarations through the
	// accessors, which take mu. A source that wakes during startup therefore
	// queues behind this whole body, readiness wait included, and that wait is
	// bounded by reconnect.readytimeout.
	r.redeclareMu.Lock()
	defer r.redeclareMu.Unlock()
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.declared {
		return nil // Already declared
	}

	if r.client == nil {
		return errors.New("AMQP client is not available")
	}

	// Wait for AMQP client to be ready with timeout. Shares its poll loop with
	// AMQPClientImpl.waitForReady via pollUntilReady (see amqp_client.go); this
	// call site has no shutdown channel (done is nil, so readyWaitDone never
	// fires) and — unlike waitForReady — does not pre-check IsReady() before the
	// first tick, preserving this method's historical behavior of always waiting
	// at least one readinessCheckInterval before its first readiness check.
	switch pollUntilReady(ctx, readyTimeoutDuration, readinessCheckInterval, r.client.IsReady, nil, func() {
		r.logger.Debug().Msg("Waiting for AMQP client to be ready...")
	}) {
	case readyWaitBecameReady:
		r.logger.Info().Msg("AMQP client is ready, proceeding with infrastructure declaration")
	case readyWaitCanceled:
		return fmt.Errorf("context canceled while waiting for AMQP client: %w", ctx.Err())
	default: // readyWaitTimedOut (readyWaitDone is unreachable: done is nil)
		return errors.New("timeout waiting for AMQP client to be ready")
	}

	if source, ok := r.client.(channelGenerationer); ok {
		generation, _ := source.channelGeneration()
		r.declaredGenerations[source] = generation
	}

	r.logger.Info().
		Int("exchanges", len(r.exchanges)).
		Int("queues", len(r.queues)).
		Int("bindings", len(r.bindings)).
		Msg("Declaring messaging infrastructure")

	// Declare exchanges first
	for name, exchange := range r.exchanges {
		if err := r.client.DeclareExchange(ctx, exchange); err != nil {
			return fmt.Errorf("failed to declare exchange %s: %w", name, err)
		}
		r.logger.Info().
			Str("exchange", name).
			Str("type", exchange.Type).
			Msg("Exchange declared successfully")
	}

	// Declare queues
	for name, queue := range r.queues {
		if err := r.client.DeclareQueue(ctx, queue); err != nil {
			return fmt.Errorf("failed to declare queue %s: %w", name, err)
		}
		r.logger.Info().
			Str("queue", name).
			Msg("Queue declared successfully")
	}

	// Create bindings
	for _, binding := range r.bindings {
		if err := r.client.BindQueue(ctx, binding); err != nil {
			return fmt.Errorf("failed to bind queue %s to exchange %s: %w", binding.Queue, binding.Exchange, err)
		}
		r.logger.Info().
			Str("queue", binding.Queue).
			Str("exchange", binding.Exchange).
			Str("routing_key", binding.RoutingKey).
			Msg("Queue binding created successfully")
	}

	r.declared = true
	r.startRedeclareObserver(ctx)
	r.logger.Info().Msg("All messaging infrastructure declared successfully")

	return nil
}

// StartConsumers starts all registered consumers with handlers.
// This should be called after DeclareInfrastructure and before starting the main application.
func (r *Registry) StartConsumers(ctx context.Context) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.consumersActive {
		return nil // Already started
	}

	if r.client == nil || !r.client.IsReady() {
		return errors.New("AMQP client is not ready")
	}

	consumersWithHandlers := 0
	for _, key := range r.consumerOrder {
		consumer := r.consumerIndex[key]
		if consumer.Handler != nil {
			consumersWithHandlers++
		}
	}

	r.logger.Info().
		Int("total_consumers", len(r.consumerIndex)).
		Int("consumers_with_handlers", consumersWithHandlers).
		Msg("Starting message consumers")

	// Diagnostic: Detect duplicate queue consumers (warning only, not blocking)
	queueCounts := make(map[string]int)
	for _, key := range r.consumerOrder {
		queueCounts[key.Queue]++
	}
	for queue, count := range queueCounts {
		if count > 1 {
			r.logger.Warn().
				Str("queue", queue).
				Int("consumer_count", count).
				Msg("Multiple consumers registered for same queue - may indicate duplicate declarations")
		}
	}

	consumerCtx, cancel := context.WithCancel(ctx)
	r.cancelConsumers = cancel

	// Start each consumer with a handler
	for _, key := range r.consumerOrder {
		consumer := r.consumerIndex[key]
		if consumer.Handler == nil {
			r.logger.Debug().
				Str("queue", consumer.Queue).
				Str("event_type", consumer.EventType).
				Msg("Consumer has no handler, skipping (documentation only)")
			continue
		}

		r.logger.Info().
			Str("queue", consumer.Queue).
			Str("consumer", consumer.Consumer).
			Str("event_type", consumer.EventType).
			Msg("Starting consumer")

		if err := r.startSingleConsumer(consumerCtx, consumer); err != nil {
			cancel() // Cancel all consumers on error
			return fmt.Errorf("failed to start consumer for queue %s: %w", consumer.Queue, err)
		}
	}

	r.consumersActive = true
	r.logger.Info().Msg("All consumers started successfully")
	return nil
}

// StopConsumers gracefully stops all running consumers.
func (r *Registry) StopConsumers() {
	r.mu.Lock()
	defer r.mu.Unlock()

	// The redeclare drivers are not consumers: a publisher-only registry has
	// them without ever activating consumers, so they have to stop ahead of the
	// guard below or they outlive every registry that has no consumer to stop.
	r.stopRedeclaring()

	if !r.consumersActive {
		return
	}

	r.logger.Info().Msg("Stopping all consumers")

	if r.cancelConsumers != nil {
		r.cancelConsumers()
		r.cancelConsumers = nil
	}

	r.consumersActive = false
	r.logger.Info().Msg("All consumers stopped")
}

// consumerState is one consumer session's runtime subscription state. The
// supervisor goroutine owns the pointer for the session's whole life — the same way
// it owns streamResume. The mutex below is taken under r.mu by the readers, and the
// only lock ever taken while it is held is the history's leaf mutex.
type consumerState struct {
	mu         sync.Mutex
	subscribed bool
	failStreak int
	// history belongs to the consumer, not to this session: every session of the same
	// consumer writes the same record, so a session still unwinding when the next one
	// starts still has its successes counted. Its mutex is a leaf taken under mu.
	history *consumerHistory
}

// consumerHistory is a consumer's cumulative record, outliving each session that writes it.
// Its mutex is a leaf and is taken UNDER a session's: every writer holds consumerState.mu
// first, so nothing may ever take a consumerState.mu while holding this one.
type consumerHistory struct {
	mu                sync.Mutex
	resubscribes      uint64
	lastResubscribeAt time.Time
}

// recordResubscribe counts one successful re-subscribe, whichever session landed it.
func (h *consumerHistory) recordResubscribe(at time.Time) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.resubscribes++
	h.lastResubscribeAt = at
}

// read returns the record so far.
func (h *consumerHistory) read() (resubscribes uint64, lastResubscribeAt time.Time) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.resubscribes, h.lastResubscribeAt
}

// markSubscribed records the subscription a consumer session opens with.
func (s *consumerState) markSubscribed() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.subscribed = true
}

// markUnsubscribed records that the broker closed the delivery channel.
func (s *consumerState) markUnsubscribed() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.subscribed = false
}

// markSupervisorStopped records that the consumer's supervisor has gone: it is not
// subscribed, and no outage is in progress for a streak to describe. Nothing else
// can re-subscribe the consumer, so a streak left behind would read as a supervisor
// still failing to — forever, since none is running.
func (s *consumerState) markSupervisorStopped() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.subscribed = false
	s.failStreak = 0
}

// setFailStreak records how many attempts the current re-subscribe loop has lost.
// The loop's own attempt counter is the streak, so the number the WARN escalation
// branches on and the number GivenUp judges are one number.
func (s *consumerState) setFailStreak(attempts int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.failStreak = attempts
}

// markResubscribed records a successful re-subscribe at the given time.
func (s *consumerState) markResubscribed(at time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.subscribed = true
	s.failStreak = 0
	s.history.recordResubscribe(at)
}

// givenUp is ConsumerState.GivenUp asked of the live state in place, with no snapshot
// allocated: the readiness probe wants one bool per poll, not a row per consumer. The
// predicate itself stays defined in exactly one place — this builds the two fields it reads
// on the stack and asks the exported method. A nil receiver is a consumer declared but never
// started, which has no streak and so never reads as given up.
func (s *consumerState) givenUp() bool {
	if s == nil {
		return false
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	live := ConsumerState{Subscribed: s.subscribed, FailStreak: s.failStreak}
	return live.GivenUp()
}

// snapshot renders the state of the consumer identified by key. A nil receiver is one that was
// declared but never started — a documentation-only one, or any consumer before
// StartConsumers — and reports the zero value. When consumersActive is false the
// registry's consumers are stopped: no supervisor is trying, so the live flags have
// no subject and read as "not subscribed, no streak", while the cumulative counters,
// which are history, pass through.
func (s *consumerState) snapshot(key consumerKey, consumersActive bool) ConsumerState {
	snapshot := ConsumerState{Queue: key.Queue, Consumer: key.Consumer, EventType: key.EventType}
	if s == nil {
		return snapshot
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	snapshot.Resubscribes, snapshot.LastResubscribeAt = s.history.read()
	if consumersActive {
		snapshot.Subscribed = s.subscribed
		snapshot.FailStreak = s.failStreak
	}
	return snapshot
}

// ConsumerState is a snapshot of one declared consumer's subscription state.
//
// The identity fields — Key, Queue, Consumer, EventType — are for an operator reading
// /_sys/health-debug or a caller of ConsumerStates, never for the unauthenticated /ready
// body or a log line: Manager.Stats() reduces these rows to counts precisely so no
// tenant key, queue name, consumer tag or event type leaves through them.
type ConsumerState struct {
	// Key is the manager key the consumer's registry was leased under: the tenant id
	// under per-tenant replay, "" for the control plane. Registry.ConsumerStates leaves
	// it empty — a registry does not know the key it was leased under.
	Key       string
	Queue     string
	Consumer  string // consumer tag
	EventType string
	// Subscribed flips false only once the session has fully ended: the handler
	// pool drains first, so a consumer whose delivery channel the broker already
	// closed still reads subscribed while its slowest handler runs.
	Subscribed        bool
	Resubscribes      uint64
	LastResubscribeAt time.Time // zero until the first successful re-subscribe
	// FailStreak counts failed re-subscribe attempts in the CURRENT outage only:
	// the next success clears it and a restarted consumer starts a fresh count, so
	// it never carries a previous outage's, or a previous session's, total.
	FailStreak int
}

// GivenUp reports a consumer whose outage stopped looking like a routine flap: it
// is unsubscribed and its consecutive re-subscribe failures have reached
// consumerResubscribeWarnFromAttempt, the same threshold that escalates the
// re-subscribe log to WARN. The supervisor keeps retrying; this is the point at
// which the outage is worth reporting.
//
// The receiver is a pointer because the identity fields make the struct too heavy to
// copy per call; a snapshot read out of a slice is addressable, so callers write
// states[i].GivenUp() unchanged.
func (s *ConsumerState) GivenUp() bool {
	return !s.Subscribed && s.FailStreak >= consumerResubscribeWarnFromAttempt
}

// ConsumerStates returns a snapshot of every declared consumer's subscription state
// in declaration order. A consumer declared without a handler (documentation only)
// never subscribes, so it reports Subscribed false forever.
func (r *Registry) ConsumerStates() []ConsumerState {
	r.mu.RLock()
	defer r.mu.RUnlock()

	states := make([]ConsumerState, 0, len(r.consumerOrder))
	for _, key := range r.consumerOrder {
		states = append(states, r.consumerStates[key].snapshot(key, r.consumersActive))
	}
	return states
}

// anyGivenUp reports whether any declared consumer's supervisor has given up re-subscribing.
// It reads the same state ConsumerStates renders and under the same mask — a stopped registry
// has no supervisor trying, so nothing there reads as given up — but allocates nothing and
// stops at the first hit.
func (r *Registry) anyGivenUp() bool {
	r.mu.RLock()
	defer r.mu.RUnlock()

	if !r.consumersActive {
		return false
	}
	for _, key := range r.consumerOrder {
		if r.consumerStates[key].givenUp() {
			return true
		}
	}
	return false
}

// consumeOptionsFor builds the ConsumeOptions for a consumer declaration. It is
// shared by the initial subscription and every re-subscription so the broker
// re-applies identical settings (QoS/prefetch, consumer tag, ack mode) on the
// new channel after a reconnect. The one deliberate per-session difference is a
// stream consumer's resume offset: a non-nil resume that has seen a delivery
// overrides x-stream-offset so the new session continues past it.
func (r *Registry) consumeOptionsFor(consumer *ConsumerDeclaration, resume *streamResume) ConsumeOptions {
	opts := ConsumeOptions{
		Queue:         consumer.Queue,
		Consumer:      consumer.Consumer,
		AutoAck:       consumer.AutoAck,
		Exclusive:     consumer.Exclusive,
		NoLocal:       consumer.NoLocal,
		NoWait:        consumer.NoWait,
		PrefetchCount: consumer.PrefetchCount,
		Args:          consumer.Args,
	}

	// SECURITY: amqp091 encodes a Go int as a 32-bit AMQP field ('I', write.go)
	// and an int64 as 64-bit ('l'), so a declared offset above math.MaxInt32
	// would silently truncate on the wire — 1<<32 arrives as 0 and replays the
	// whole stream, 3000000000 arrives negative. Widening to int64 is lossless
	// and is what the operator meant.
	offset, hasOffset := int64(0), false
	if declared, isInt := consumer.Args[argStreamOffset].(int); isInt {
		offset, hasOffset = int64(declared), true
	}
	if resume != nil && resume.seen {
		offset, hasOffset = resume.last+1, true
	}
	if hasOffset {
		opts.Args = withStreamOffset(consumer.Args, offset)
	}

	return opts
}

// withStreamOffset returns a copy of args carrying offset as x-stream-offset.
// The declaration's own map is shared with the registry state and every other
// session, so the override is never applied in place.
func withStreamOffset(args map[string]any, offset int64) map[string]any {
	next := make(map[string]any, len(args))
	maps.Copy(next, args)
	next[argStreamOffset] = offset
	return next
}

// streamResume carries the last stream offset handed to the worker pool across
// a re-subscribe, so a broker flap resumes just past it instead of re-reading
// the whole stream from the declared start position. Best-effort: messages
// already handed to workers may be redelivered, and a process restart
// re-attaches at the declared offset — handlers must be idempotent.
// The feed loop in handleMessages is the only writer, and the supervisor reads
// it only after handleMessages returns, so no synchronization is needed.
// handleMessages closes the jobs channel and waits for its workers before
// returning, so last is the last offset fully PROCESSED, not merely received.
// Hence the resume is last+1 and not last: nothing unprocessed can be skipped,
// while an inclusive resume would re-deliver that message on every reconnect.
type streamResume struct {
	last int64
	seen bool
}

// observe records a delivery's stream offset. The nil receiver is the
// non-stream consumer case, so the feed loop can call it unconditionally.
func (s *streamResume) observe(headers amqp.Table) {
	if s == nil {
		return
	}
	if offset, found := streamOffsetFromHeaders(headers); found {
		s.last = offset
		s.seen = true
	}
}

// consumerStateFor installs the state a starting consumer session writes to. A restart
// always gets a FRESH struct: StopConsumers cancels its supervisors without waiting for
// them, so one still unwinding would otherwise share the new session's state and could
// revive its subscribed flag or leave its failure streak behind. It keeps the consumer's
// history, though — the same record, not a copy — because a success that lands late is
// still a success this consumer had, and a copy would drop it. History is per consumer,
// session state is per start. Callers must hold r.mu; only StartConsumers reaches this.
func (r *Registry) consumerStateFor(consumer *ConsumerDeclaration) *consumerState {
	if r.consumerStates == nil {
		r.consumerStates = make(map[consumerKey]*consumerState)
	}

	key := consumerKeyFor(consumer)
	state := &consumerState{history: &consumerHistory{}}
	if previous, ok := r.consumerStates[key]; ok {
		state.history = previous.history
	}
	r.consumerStates[key] = state
	return state
}

// startSingleConsumer starts a consumer for a specific queue and routes messages to the handler.
// The first subscription is established synchronously so an unreachable broker
// fails startup (fail-fast); the supervisor goroutine then keeps the consumer
// alive across broker reconnects (see superviseConsumer).
func (r *Registry) startSingleConsumer(ctx context.Context, consumer *ConsumerDeclaration) error {
	deliveries, err := r.client.ConsumeFromQueue(ctx, r.consumeOptionsFor(consumer, nil))
	if err != nil {
		return fmt.Errorf("failed to start consuming from queue %s: %w", consumer.Queue, err)
	}

	// Stream-ness comes from the declared queue table, never from a delivery
	// header, so a publisher-forged x-stream-offset cannot enable the resume
	// path on a classic queue. Read r.queues directly: StartConsumers holds r.mu
	// across its whole body, so the exported Queues() accessor would deadlock.
	var resume *streamResume
	if isStreamQueue(r.queues[consumer.Queue]) {
		resume = &streamResume{}
	}

	// StartConsumers holds r.mu across its whole body, so the state map is seeded
	// directly here (as r.queues is read above). From here the supervisor owns the
	// pointer and never takes r.mu again.
	state := r.consumerStateFor(consumer)
	state.markSubscribed()

	// Supervise the subscription so it survives AMQP reconnects: when the broker
	// closes the delivery channel (connection/channel flap), superviseConsumer
	// re-subscribes on the client's new channel instead of leaving the queue
	// with zero consumers until a process restart.
	go r.superviseConsumer(ctx, consumer, deliveries, resume, state)

	return nil
}

// superviseConsumer runs consumer sessions back-to-back, re-subscribing after
// the broker drops the delivery channel, until the consumer context is
// canceled (StopConsumers / shutdown). This is the consumer-side counterpart
// to the client's reconnection supervisor: the publisher path recovers because
// every publish re-reads the live channel under lock, whereas a consumer
// captures its delivery channel once, so it needs an explicit re-subscribe.
func (r *Registry) superviseConsumer(ctx context.Context, consumer *ConsumerDeclaration, deliveries <-chan amqp.Delivery, resume *streamResume, state *consumerState) {
	// Nothing else can hold this consumer subscribed or re-subscribe it, so every
	// exit ends the session — cancellation as much as a channel the broker closed.
	// Without this a caller that cancels the context it passed to StartConsumers,
	// rather than calling StopConsumers, would leave the flags behind with no
	// supervisor to justify them: subscribed forever, or given up forever.
	defer state.markSupervisorStopped()

	for {
		// Run one subscription session until the delivery channel closes
		// (reconnect needed) or the context is canceled (stop for good).
		sessionStart := time.Now()
		if !r.handleMessages(ctx, consumer, deliveries, resume) {
			return // context canceled → stop for good
		}
		state.markUnsubscribed()

		// Rapid-flap guard: if the session barely lasted, the broker is handing
		// back channels that close almost immediately. Pace re-subscribes by the
		// backoff floor so this can't become a tight loop. A healthy (long-lived)
		// session falls through and re-subscribes immediately for fast recovery.
		if elapsed := time.Since(sessionStart); elapsed < r.resubscribeDelay {
			if !sleepCtx(ctx, r.resubscribeDelay-elapsed) {
				return
			}
		}

		next, ok := r.resubscribe(ctx, consumer, resume, state)
		if !ok {
			return // context canceled while waiting to re-subscribe
		}
		deliveries = next
	}
}

// sleepCtx waits for d, or until ctx is canceled, whichever comes first. It
// returns true if the full duration elapsed and false if ctx was canceled.
func sleepCtx(ctx context.Context, d time.Duration) bool {
	if d <= 0 {
		return ctx.Err() == nil
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

// consumerLogFields returns the base structured-log fields identifying a
// consumer, shared by the session, worker, and re-subscribe log contexts so the
// field keys are defined in exactly one place.
func consumerLogFields(consumer *ConsumerDeclaration) map[string]any {
	return map[string]any{
		genericQueue:     consumer.Queue,
		genericConsumer:  consumer.Consumer,
		genericEventType: consumer.EventType,
	}
}

// resubscribe re-establishes a consumer subscription after the delivery channel
// closed. It attempts immediately so a routine channel-only flap (the client's
// connection is still up) recovers without added downtime, then on failure
// backs off with full jitter before retrying, until ConsumeFromQueue succeeds
// or the context is canceled. Returns (channel, true) on success and
// (nil, false) on cancellation.
//
// A success-then-immediately-closing channel cannot become a tight spin: the
// client only hands out a fresh usable channel via its own reconnect supervisor
// (handleReInit, paced by reInitDelay), and while the client is not ready
// ConsumeFromQueue returns errNotConnected, which takes the backoff path below.
func (r *Registry) resubscribe(ctx context.Context, consumer *ConsumerDeclaration, resume *streamResume, state *consumerState) (<-chan amqp.Delivery, bool) {
	log := r.logger.WithFields(consumerLogFields(consumer))

	opts := r.consumeOptionsFor(consumer, resume)

	for attempt := 1; ; attempt++ {
		// Stop promptly if we're shutting down before trying again.
		if ctx.Err() != nil {
			return nil, false
		}

		r.redeclareTopology(ctx)
		deliveries, err := r.client.ConsumeFromQueue(ctx, opts)
		if err == nil {
			state.markResubscribed(time.Now())
			log.Info().Int("attempt", attempt).
				Msg("Consumer re-subscribed after delivery channel closed")
			return deliveries, true
		}

		state.setFailStreak(attempt)

		// errNotConnected is expected while the client is still reconnecting;
		// early attempts log at debug to avoid noise during a flap. Full-jitter
		// backoff (the client's own computeBackoff) bounds the loop and, on a
		// broker restart that drops every consumer at once, spreads the herd of
		// re-subscribe attempts instead of having all consumers retry in lockstep.
		backoff := computeBackoff(r.resubscribeDelay, defaultReconnectMaxDelay, attempt)
		var event logger.LogEvent
		if attempt < consumerResubscribeWarnFromAttempt {
			event = log.Debug()
		} else {
			event = log.Warn()
		}
		withAMQPReply(event, err).Err(err).Int("attempt", attempt).Dur("backoff", backoff).
			Msg("Consumer re-subscribe attempt failed, will retry")
		select {
		case <-ctx.Done():
			return nil, false
		case <-time.After(backoff):
		}
	}
}

// withAMQPReply adds the broker's reply code and text to event when err is an
// *amqp.Error, the only form in which the broker says why it refused.
func withAMQPReply(event logger.LogEvent, err error) logger.LogEvent {
	var amqpErr *amqp.Error
	if errors.As(err, &amqpErr) {
		return event.Int("amqp_reply_code", amqpErr.Code).Str("amqp_reply_text", amqpErr.Reason)
	}
	return event
}

// channelGenerationer is the optional client capability the reconnect
// redeclare pass keys on. AMQPClientImpl implements it; a client without it (a
// custom ClientFactory wrapper, an external AMQPClient) never redeclares.
type channelGenerationer interface {
	channelGeneration() (generation uint64, ready bool)
}

var _ channelGenerationer = (*AMQPClientImpl)(nil)

// channelReadyNotifier is the optional client capability that announces a new
// channel. It is what turns a client into a redeclare DRIVER: without it only a
// consumer re-subscribe runs a pass, so a registry that declares but consumes
// nothing would never recover topology the broker lost. AMQPClientImpl
// implements it; a client without it (a custom ClientFactory wrapper, an
// external AMQPClient) is never observed.
type channelReadyNotifier interface {
	channelReadyNotify() (ready <-chan struct{}, open bool)
}

// redeclareSource is a client whose channel rotations drive a registry's
// redeclare pass. The registry observes its own client; the pass itself takes
// any source, because the generation guard is keyed per source rather than per
// registry.
type redeclareSource interface {
	channelGenerationer
	channelReadyNotifier
}

var _ redeclareSource = (*AMQPClientImpl)(nil)

// startRedeclareObserver starts the one goroutine that redeclares on every new
// channel generation of the registry's OWN client, for as long as the registry
// lives, consumers or not. Called from DeclareInfrastructure with the pass locks
// held, and only there, so a registry never runs two.
func (r *Registry) startRedeclareObserver(ctx context.Context) {
	source, ok := r.client.(redeclareSource)
	if !ok {
		return
	}
	// ctx is a setup budget that expires; this goroutine lives as long as the
	// registry, so it keeps the values (trace, tenant) and drops the deadline.
	observerCtx := context.WithoutCancel(ctx)
	done := make(chan struct{})
	r.redeclareObserverDone = done
	go func() {
		defer close(done)
		observeChannelReady(source, r.redeclareStop, func(s redeclareSource) {
			r.redeclareTopologyFrom(observerCtx, s)
		})
	}()
}

// observeChannelReady calls redeclare every time source becomes ready on a fresh
// channel. It takes the broadcast BEFORE running the pass, so a generation that
// rotates while that pass runs is caught by the pass that follows the wait
// instead of being lost. It returns when stop fires or source closes, whichever
// comes first: a failed StartConsumers closes the client and drops the registry
// without ever calling StopConsumers, so the client's own end has to be an exit
// too. A nil stop leaves the client's end as the only one.
func observeChannelReady(source redeclareSource, stop <-chan struct{}, redeclare func(redeclareSource)) {
	for {
		ready, open := source.channelReadyNotify()
		if !open {
			return
		}
		redeclare(source)
		select {
		case <-ready:
		case <-stop:
			return
		}
	}
}

// topologyStep is one recorded declaration, keyed for logs and the skip set.
type topologyStep struct {
	key     string
	declare func(context.Context) error
}

// topologySteps snapshots the declarations in order: exchanges, queues, bindings.
// A binding's key carries its index in the append-only registration order, so
// two bindings whose names join to the same text or which differ only in Args
// never share a skip-set entry.
func (r *Registry) topologySteps() []topologyStep {
	exchanges, queues, bindings := r.Exchanges(), r.Queues(), r.Bindings()
	steps := make([]topologyStep, 0, len(exchanges)+len(queues)+len(bindings))
	for name, exchange := range exchanges {
		steps = append(steps, topologyStep{key: "exchange:" + name, declare: func(ctx context.Context) error {
			return r.client.DeclareExchange(ctx, exchange)
		}})
	}
	for name, queue := range queues {
		steps = append(steps, topologyStep{key: "queue:" + name, declare: func(ctx context.Context) error {
			return r.client.DeclareQueue(ctx, queue)
		}})
	}
	for i, binding := range bindings {
		steps = append(steps, topologyStep{
			key:     fmt.Sprintf("binding[%d]:%s|%s|%s", i, binding.Queue, binding.Exchange, binding.RoutingKey),
			declare: func(ctx context.Context) error { return r.client.BindQueue(ctx, binding) },
		})
	}
	return steps
}

// redeclareTopology runs a pass driven by the registry's own client, which is
// what a consumer re-subscribe has in hand. It is a no-op for a client without
// channelGeneration.
func (r *Registry) redeclareTopology(ctx context.Context) {
	source, ok := r.client.(channelGenerationer)
	if !ok {
		return
	}
	r.redeclareTopologyFrom(ctx, source)
}

// redeclareTopologyFrom re-runs the recorded declarations once per channel
// generation of source. It is a no-op for a source not ready or on a generation
// already declared. A pass the channel was replaced during is repeated on the
// new generation, so a restart mid-pass cannot leave the consumer on topology
// the pass never saw. The first failure ends a pass; the next channel retries. A
// declaration refused with PRECONDITION_FAILED is skipped by every later pass
// until the process restarts: the operator fixes the server-side definition and
// restarts.
//
// source only says WHEN to declare. Declaring always goes through r.client:
// topology is broker-global, so repairing it over the registry's own connection
// is both correct and the only connection the registry owns — a borrowed channel
// belongs to whoever is publishing on it.
func (r *Registry) redeclareTopologyFrom(ctx context.Context, source channelGenerationer) {
	r.redeclareMu.Lock()
	defer r.redeclareMu.Unlock()

	// DeclareInfrastructure is the latch: before it, a "redeclare" would be the
	// FIRST declare, running the startup topology off a background sighting and
	// outside the error path that makes a failed startup fatal. Recording
	// nothing here matters as much as declaring nothing — an adopted generation
	// would never earn its pass once the latch does open.
	r.mu.RLock()
	declared := r.declared
	r.mu.RUnlock()
	if !declared {
		return
	}

	for !r.redeclareHalted() && ctx.Err() == nil {
		generation, ready := source.channelGeneration()
		if !ready || generation == r.declaredGenerations[source] {
			return
		}
		r.declaredGenerations[source] = generation
		r.replayTopology(ctx, generation)
	}
}

// forgetRedeclareSource drops the generation recorded for a source whose
// channels can no longer rotate. A registry outlives its sources, so without
// this the map keeps one entry — and one dead client pointer — per retired
// source, for the process lifetime. A source forgotten and then seen again is
// unseen: its next sighting declares.
func (r *Registry) forgetRedeclareSource(source channelGenerationer) {
	r.redeclareMu.Lock()
	defer r.redeclareMu.Unlock()
	delete(r.declaredGenerations, source)
}

// redeclareHalted reports whether StopConsumers has ended this registry's
// redeclare drivers. A source that outlives the registry's consumers has no
// other way to learn the registry is done, so the pass refuses rather than
// relying on every driver having stopped.
func (r *Registry) redeclareHalted() bool {
	if r.redeclareStop == nil {
		return false
	}
	select {
	case <-r.redeclareStop:
		return true
	default:
		return false
	}
}

// replayTopology runs one pass of the recorded declarations on generation.
func (r *Registry) replayTopology(ctx context.Context, generation uint64) {
	for _, step := range r.topologySteps() {
		if _, skipped := r.redeclareSkip[step.key]; skipped {
			continue
		}
		err := step.declare(ctx)
		if err == nil {
			continue
		}
		if ctx.Err() != nil {
			return
		}
		msg := "Messaging topology redeclare failed, the next channel retries"
		var amqpErr *amqp.Error
		if errors.As(err, &amqpErr) && amqpErr.Code == amqp.PreconditionFailed {
			r.redeclareSkip[step.key] = struct{}{}
			msg = "Messaging declaration rejected with PRECONDITION_FAILED, skipped until restart: " +
				"fix the server-side definition and restart the process"
		}
		withAMQPReply(r.logger.Warn(), err).Err(err).Str("declaration", step.key).
			Uint64("channel_generation", generation).Msg(msg)
		return
	}
	r.logger.Info().Uint64("channel_generation", generation).Msg("Messaging topology redeclared on new channel")
}

// handleMessages runs a single consumer session: it spawns a worker pool
// (v0.17+) and feeds deliveries to it until the session ends. It returns true
// when the broker closed the delivery channel (the caller should re-subscribe)
// and false when the context was canceled (shut down for good).
func (r *Registry) handleMessages(ctx context.Context, consumer *ConsumerDeclaration, deliveries <-chan amqp.Delivery, resume *streamResume) bool {
	workers := consumer.Workers
	if workers <= 0 {
		workers = 1 // Fallback (should not happen with smart defaults)
	}

	fields := consumerLogFields(consumer)
	fields["workers"] = workers
	fields["prefetch"] = consumer.PrefetchCount
	log := r.logger.WithFields(fields)

	log.Info().Msg("Message handler started with worker pool")

	defer func() {
		log.Info().Msg("Message handler stopped")
	}()

	// Buffered jobs channel for work distribution (size = workers * 2 for backpressure)
	jobs := make(chan *amqp.Delivery, workers*2)

	// Start worker pool
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go r.worker(ctx, consumer, jobs, i, &wg)
	}

	// Main loop: feed jobs to worker pool. reconnect=true means the broker
	// closed the delivery channel (the caller should re-subscribe); false means
	// the context was canceled (shut down for good).
	reconnect := func() bool {
		for {
			select {
			case <-ctx.Done():
				log.Info().Msg("Consumer context canceled, stopping message handler")
				return false

			case delivery, ok := <-deliveries:
				if !ok {
					log.Warn().Msg("Delivery channel closed, will re-subscribe")
					return true
				}

				resume.observe(delivery.Headers)

				// Create local copy to avoid pointer capture bug (loop variable reuse)
				d := delivery
				// Send to the worker pool, but also honor cancellation: without
				// the ctx.Done() arm the feed loop could block forever on a full
				// buffer after workers have already exited on shutdown, leaking
				// this goroutine (and the supervisor that owns it).
				select {
				case jobs <- &d:
				case <-ctx.Done():
					log.Info().Msg("Consumer context canceled, stopping message handler")
					return false
				}
			}
		}
	}()

	close(jobs)
	wg.Wait()
	log.Info().Msg("All workers stopped gracefully")
	return reconnect
}

// streamOffsetFromHeaders reads a delivery's x-stream-offset header. amqp091
// decodes AMQP longs as int64, but the narrower integer types are accepted too
// rather than assuming one wire encoding.
func streamOffsetFromHeaders(headers amqp.Table) (int64, bool) {
	switch v := headers[argStreamOffset].(type) {
	case int64:
		return v, true
	case int32:
		return int64(v), true
	case int16:
		return int64(v), true
	case int8:
		return int64(v), true
	case int:
		return int64(v), true
	default:
		return 0, false
	}
}

// worker processes messages from the jobs channel concurrently.
// Each worker runs in its own goroutine and processes messages independently.
func (r *Registry) worker(ctx context.Context, consumer *ConsumerDeclaration, jobs <-chan *amqp.Delivery, workerID int, wg *sync.WaitGroup) {
	defer wg.Done()

	fields := consumerLogFields(consumer)
	fields["worker_id"] = workerID
	log := r.logger.WithFields(fields)

	log.Debug().Msg("Worker started")

	defer func() {
		log.Debug().Msg("Worker stopped")
	}()

	for {
		select {
		case <-ctx.Done():
			log.Debug().Msg("Worker context canceled")
			return

		case delivery, ok := <-jobs:
			if !ok {
				log.Debug().Msg("Jobs channel closed, worker exiting")
				return
			}

			r.processMessage(ctx, consumer, delivery, log)
		}
	}
}

// processMessage runs one delivery through the delivery pipeline. Settlement is
// this lane's policy — ack on success, nack-without-requeue on a handler error
// or a panic, nothing under AutoAck — but WHEN it happens is the pipeline's: it
// calls Settle after the span closed and the lease drained, and it guarantees
// at most one call even if the delivery tail panicked (ADR-069).
func (r *Registry) processMessage(ctx context.Context, consumer *ConsumerDeclaration, delivery *amqp.Delivery, log logger.Logger) {
	id := identifyDelivery(delivery)
	pipeline.Run(ctx, &pipeline.Request{
		Carrier:        amqpHeaderAccessor{headers: delivery.Headers},
		TenantStamps:   r.tenantStamps,
		TenantOptional: consumer.TenantOptional,
		Destination:    consumer.Queue,
		BodySize:       len(delivery.Body),
		SpanExtras:     consumeSpanExtras(id),
		Metrics:        consumeMetrics(consumer.Queue, id),
		Log:            log,
		Handle: func(msgCtx context.Context, msgLog logger.Logger, traceID string) error {
			logProcessing(msgLog, traceID, delivery, id)
			return consumer.Handler.Handle(msgCtx, delivery)
		},
		LogOutcome: func(res *pipeline.Result) {
			logOutcome(res, consumer, delivery, id)
		},
		Settle: func(res *pipeline.Result) {
			settleDelivery(res, consumer, delivery)
		},
	})
}

// settleDelivery is this lane's broker action for a finished delivery.
func settleDelivery(res *pipeline.Result, consumer *ConsumerDeclaration, delivery *amqp.Delivery) {
	if consumer.AutoAck {
		return // the broker already considers it delivered
	}
	if res.Outcome == pipeline.Succeeded {
		ackMessage(delivery, res.Log, res.TraceID)
		return
	}
	nackMessage(delivery, res.Log, res.TraceID)
}

// consumeSpanExtras renders this lane's span attributes, on top of the four the
// pipeline sets for both lanes. A field the delivery did not carry is omitted
// rather than reported empty, which is what the receive span has always done —
// see deliveryIdentity for what "did not carry" now includes.
func consumeSpanExtras(id deliveryIdentity) []attribute.KeyValue {
	extras := make([]attribute.KeyValue, 0, 4)
	if id.exchange != "" {
		extras = append(extras, attribute.String(attrMessagingRabbitMQExchange, id.exchange))
	}
	if id.routingKey != "" {
		extras = append(extras, semconv.MessagingRabbitMQDestinationRoutingKey(id.routingKey))
	}
	if id.messageID != "" {
		extras = append(extras, semconv.MessagingMessageID(id.messageID))
	}
	if id.correlationID != "" {
		extras = append(extras, semconv.MessagingMessageConversationID(id.correlationID))
	}
	return extras
}

// logProcessing writes the per-delivery DEBUG line. The whole field chain is
// skipped when the event is dropped: DEBUG is below WarnLevel, so the adapter's
// Msg -> trackSeverity hook is a no-op and skipping Msg changes nothing.
func logProcessing(log logger.Logger, traceID string, delivery *amqp.Delivery, id deliveryIdentity) {
	dbg := log.Debug()
	if !dbg.Enabled() {
		return
	}
	dbg = dbg.Str(logger.FieldCorrelationID, traceID)
	dbg = strIfSet(dbg, "message_id", id.messageID)
	dbg = strIfSet(dbg, "routing_key", id.routingKey)
	dbg = strIfSet(dbg, "exchange", id.exchange)
	dbg = flagRejected(dbg, id)
	dbg.Uint64("delivery_tag", delivery.DeliveryTag).
		Int("body_size", len(delivery.Body)).
		Msg("Processing message")
}

// logOutcome writes this lane's line for a finished delivery.
func logOutcome(res *pipeline.Result, consumer *ConsumerDeclaration, delivery *amqp.Delivery, id deliveryIdentity) {
	switch res.Outcome {
	case pipeline.Succeeded:
		e := strIfSet(pipeline.AppendOutcome(res.Log.Info(), res), "message_id", id.messageID)
		flagRejected(e, id).Msg("Message processed successfully")
	case pipeline.HandlerError:
		buildFailureLogEvent(res, consumer, delivery, id).
			Err(res.Err).
			Msg("Message processing failed - discarding without requeue")
	case pipeline.Panicked:
		buildFailureLogEvent(res, consumer, delivery, id).
			Msg("Panic recovered in message handler - discarding without requeue")
	}
}

// ackMessage acknowledges a handled message.
// Logs any ack errors but does not propagate them (robustness over strict error handling).
func ackMessage(delivery *amqp.Delivery, log logger.Logger, traceID string) {
	err := delivery.Ack(false)
	tracking.RecordSettlement(tracking.LaneClassic, tracking.OutcomeAcked, err)
	if err != nil {
		log.Error().
			Str(logger.FieldCorrelationID, traceID).
			Err(err).
			Uint64("delivery_tag", delivery.DeliveryTag).
			Msg("Failed to ack message")
	}
}

// nackMessage negatively acknowledges a message WITHOUT requeue, which prevents
// infinite retry loops. Queues declared with x-dead-letter-exchange route the
// nacked message to that exchange (retained only if a binding delivers it to a
// queue); queues without one drop it (logged by logOutcome).
// DeclareQueueWithDLQ declares that full route in one call.
// Logs any nack errors but does not propagate them (robustness over strict error handling).
func nackMessage(delivery *amqp.Delivery, log logger.Logger, traceID string) {
	err := delivery.Nack(false, false)
	tracking.RecordSettlement(tracking.LaneClassic, tracking.OutcomeNacked, err)
	if err != nil {
		log.Error().
			Str(logger.FieldCorrelationID, traceID).
			Err(err).
			Uint64("delivery_tag", delivery.DeliveryTag).
			Msg("Failed to nack message")
	}
}

// buildFailureLogEvent creates a structured log event for failed message processing.
// Provides consistent error logging across panic and error paths.
func buildFailureLogEvent(res *pipeline.Result, consumer *ConsumerDeclaration, delivery *amqp.Delivery, id deliveryIdentity) logger.LogEvent {
	e := pipeline.AppendOutcome(res.Log.Error(), res)
	e = strIfSet(e, "message_id", id.messageID)
	e = e.Str("queue", consumer.Queue).Str("event_type", consumer.EventType)
	e = strIfSet(e, "amqp_correlation_id", id.correlationID)
	e = e.Str("consumer_tag", delivery.ConsumerTag)
	e = strIfSet(e, "routing_key", id.routingKey)
	e = strIfSet(e, "exchange", id.exchange)
	// delivery_tag is the one identifier no publisher supplies, so it is what keeps
	// this line attributable to ONE delivery when every vouched field was dropped.
	e = e.Uint64("delivery_tag", delivery.DeliveryTag)
	return flagRejected(e, id)
}

// Publishers returns all registered publishers (for documentation/monitoring)
func (r *Registry) Publishers() []*PublisherDeclaration {
	r.mu.RLock()
	defer r.mu.RUnlock()

	publishers := make([]*PublisherDeclaration, len(r.publishers))
	copy(publishers, r.publishers)
	return publishers
}

// Consumers returns all registered consumers (for documentation/monitoring)
func (r *Registry) Consumers() []*ConsumerDeclaration {
	r.mu.RLock()
	defer r.mu.RUnlock()

	consumers := make([]*ConsumerDeclaration, 0, len(r.consumerOrder))
	for _, key := range r.consumerOrder {
		consumers = append(consumers, r.consumerIndex[key])
	}
	return consumers
}

// ValidatePublisher checks if a publisher is registered for the given exchange/routing key
func (r *Registry) ValidatePublisher(exchange, routingKey string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()

	for _, pub := range r.publishers {
		if pub.Exchange == exchange && pub.RoutingKey == routingKey {
			return true
		}
	}
	return false
}

// ValidateConsumer checks if a consumer is registered for the given queue
func (r *Registry) ValidateConsumer(queue string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()

	for _, key := range r.consumerOrder {
		cons := r.consumerIndex[key]
		if cons.Queue == queue {
			return true
		}
	}
	return false
}

// Exchanges returns all registered exchanges (for testing/monitoring)
func (r *Registry) Exchanges() map[string]*ExchangeDeclaration {
	r.mu.RLock()
	defer r.mu.RUnlock()

	exchanges := make(map[string]*ExchangeDeclaration, len(r.exchanges))
	maps.Copy(exchanges, r.exchanges)
	return exchanges
}

// Queues returns all registered queues (for testing/monitoring)
func (r *Registry) Queues() map[string]*QueueDeclaration {
	r.mu.RLock()
	defer r.mu.RUnlock()

	queues := make(map[string]*QueueDeclaration, len(r.queues))
	maps.Copy(queues, r.queues)
	return queues
}

// Bindings returns all registered bindings (for testing/monitoring)
func (r *Registry) Bindings() []*BindingDeclaration {
	r.mu.RLock()
	defer r.mu.RUnlock()

	bindings := make([]*BindingDeclaration, len(r.bindings))
	copy(bindings, r.bindings)
	return bindings
}
