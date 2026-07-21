package messaging

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"

	"github.com/gaborage/go-bricks/internal/resourcepool"
	"github.com/gaborage/go-bricks/logger"
	"github.com/gaborage/go-bricks/multitenant"
)

// BrokerURLProvider provides per-key AMQP configurations.
// This interface abstracts where tenant-specific messaging configs come from.
type BrokerURLProvider interface {
	// BrokerURL returns the AMQP broker URL for the given key.
	// For single-tenant apps, key will be "". For multi-tenant, key will be the tenant ID.
	BrokerURL(ctx context.Context, key string) (string, error)
}

// ClientFactory creates AMQP clients from URLs
type ClientFactory func(string, logger.Logger) AMQPClient

// ReleaseFunc releases a lease obtained from Publisher. Callers must invoke it (typically
// deferred) when finished with the publisher for the current unit of work. It is idempotent.
// Release does NOT close the shared publisher; it signals this borrower is done, so a
// publisher evicted while leased is closed only once its last lease is released. See ADR-032.
type ReleaseFunc func()

// errManagerClosed is returned by Publisher after Close has been called, rather than
// resurrecting a publisher on a shut-down manager (backlog F22). It is unexported: Manager
// exposed no closed-state error before the resourcepool rewire, so this closes F22 while
// keeping the public surface unchanged.
var errManagerClosed = errors.New("messaging: manager closed")

// Manager manages AMQP clients by string keys with different lifecycle strategies.
// Publishers are cached with idle eviction (can be recreated easily).
// Consumers are long-lived (must stay alive to receive messages).
// The manager is key-agnostic - it doesn't know about tenants, just manages named clients.
//
// The publisher side is a thin adapter over internal/resourcepool.Pool, which owns the
// ADR-032 lease/evict/close protocol (seed leases, LRU eviction, idle cleanup, and the
// closed-pool guard). The manager keeps only the AMQP-specific client creation. The consumer
// side is long-lived and managed directly (not pool-shaped).
type Manager struct {
	logger         logger.Logger
	resourceSource BrokerURLProvider
	clientFactory  ClientFactory // Injected for testability

	// Publishers (evictable) — the resourcepool owns lease/LRU/idle-cleanup bookkeeping.
	pubPool *resourcepool.Pool[AMQPClient]

	// Consumers (long-lived)
	consMu        sync.RWMutex
	consumers     map[string]*consumerEntry
	replayedHashs map[string]uint64 // Tracks declaration hashes to prevent duplicate replay

	// Singleflight for concurrent consumer initialization
	sfg singleflight.Group
}

// consumerEntry represents a long-lived consumer
type consumerEntry struct {
	client   AMQPClient
	registry *Registry
	started  bool
	key      string
}

// ManagerOptions configures the Manager
type ManagerOptions struct {
	MaxPublishers int           // Maximum number of publisher clients to keep cached
	IdleTTL       time.Duration // Time after which idle publishers are evicted
	// ConnectionTimeout is the per-publish broker confirmation timeout applied to
	// clients created by the default factory. Zero leaves the client default (30s).
	ConnectionTimeout time.Duration
	// MaxPublishAttempts bounds the per-publish retry loop for clients created by the
	// default factory. Zero (or negative) leaves the client default (5).
	MaxPublishAttempts int
	// ReadyTimeout bounds the pre-flight readiness wait for clients created by the
	// default factory. Zero (or negative) leaves the client default (5s).
	ReadyTimeout time.Duration
	// Reconnect delays for clients created by the default factory. Zero (or negative)
	// leaves the client defaults (5s/60s/2s/5s). See the WithReconnect*/WithResendDelay
	// option docs for each knob's exact scope (jitter semantics, publish-error-only).
	ReconnectDelay    time.Duration
	ReconnectMaxDelay time.Duration
	ReinitDelay       time.Duration
	ResendDelay       time.Duration
}

// NewMessagingManager creates a new messaging manager
func NewMessagingManager(resourceSource BrokerURLProvider, log logger.Logger, opts ManagerOptions, clientFactory ClientFactory) *Manager {
	if opts.MaxPublishers <= 0 {
		opts.MaxPublishers = 50 // sensible default
	}
	if opts.IdleTTL <= 0 {
		// Single-tenant default only: this package has no deployment-mode signal (a bare
		// caller passes only ManagerOptions), so it cannot apply the multi-tenant 10m default.
		// The real production path always resolves IdleTTL before it reaches here — via
		// config.Validate() (config/validation.go: applyMessagingDefaults, mode-aware) and
		// app.ManagerConfigBuilder.BuildMessagingOptions (app/managers.go, same mode split) —
		// so this branch only serves bare/direct callers that bypass both.
		opts.IdleTTL = 1 * time.Hour // kept in sync with app.defaultPublisherIdleTTL (see app/managers.go)
	}

	// Default to real client factory if none provided
	if clientFactory == nil {
		clientFactory = func(url string, log logger.Logger) AMQPClient {
			return NewAMQPClient(url, log,
				WithConnectionTimeout(opts.ConnectionTimeout),
				WithMaxPublishAttempts(opts.MaxPublishAttempts),
				WithReadyTimeout(opts.ReadyTimeout),
				WithReconnectDelay(opts.ReconnectDelay),
				WithReconnectMaxDelay(opts.ReconnectMaxDelay),
				WithReinitDelay(opts.ReinitDelay),
				WithResendDelay(opts.ResendDelay),
			)
		}
	}

	return &Manager{
		logger:         log,
		resourceSource: resourceSource,
		clientFactory:  clientFactory,
		// The pool closer surfaces each publisher's raw Close() error; pool.Close() joins them.
		// Unlike the consumer loop below, these are not wrapped with the per-key label — the
		// closer receives only the AMQPClient value (and key=="" uses a bare client), matching
		// the database rewire's deliberate tradeoff: error coverage and the aggregate prefix are
		// preserved, only the per-publisher-key context is dropped.
		pubPool: resourcepool.New[AMQPClient](opts.MaxPublishers, opts.IdleTTL, func(client AMQPClient) error {
			return client.Close()
		}),
		consumers:     make(map[string]*consumerEntry),
		replayedHashs: make(map[string]uint64),
	}
}

// EnsureConsumers creates and starts consumers for the given key using the provided declarations.
// This should be called once per key to set up long-lived consumers.
// Subsequent calls for the same key are idempotent.
func (m *Manager) EnsureConsumers(ctx context.Context, key string, decls *Declarations) error {
	// Use singleflight to prevent concurrent consumer setup for the same key
	_, err, _ := m.sfg.Do("consumer:"+key, func() (any, error) {
		return nil, m.ensureConsumersInternal(ctx, key, decls)
	})
	return err
}

// ensureConsumersInternal performs the actual consumer setup
func (m *Manager) ensureConsumersInternal(ctx context.Context, key string, decls *Declarations) error {
	m.consMu.Lock()
	defer m.consMu.Unlock()

	// Compute hash of incoming declarations for idempotency check
	declHash := decls.Hash()

	// Check if we've already replayed these exact declarations
	if existingHash, exists := m.replayedHashs[key]; exists {
		if existingHash == declHash {
			// Idempotency: same declarations already replayed, skip
			m.logger.Debug().
				Str("key", key).
				Uint64("hash", declHash).
				Msg("Declarations already replayed for key - skipping (idempotent)")
			return nil
		}
		// Different declarations for same key - this is an error
		return fmt.Errorf(
			"messaging: attempt to replay different declarations for key %s (existing hash=%d, new hash=%d)",
			key, existingHash, declHash,
		)
	}

	// Check if consumers already exist and are started
	if entry, exists := m.consumers[key]; exists {
		if entry.started {
			// Record hash for future idempotency checks
			m.replayedHashs[key] = declHash
			return nil // Already set up
		}
	}

	// Setup runs on its own best-effort budget, detached from the caller's
	// deadline: a lazy-start request's ~5s deadline expiring mid-declare
	// would abort and roll back an otherwise-successful setup (values —
	// trace/tenant — are preserved by WithoutCancel). The budget is soft —
	// see infraSetupTimeout: amqp091 declares aren't ctx-cancelable on the wire.
	setupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), infraSetupTimeout)
	defer cancel()

	// Create AMQP client for consumers (error is already well-formatted from createAMQPClient)
	client, err := m.createAMQPClient(setupCtx, key)
	if err != nil {
		return err
	}

	// Create registry and replay declarations
	registry := NewRegistry(client, m.logger)
	if err := decls.ReplayToRegistry(registry); err != nil {
		m.closeClientOnRollback(client, key, "replay_declarations")
		return fmt.Errorf("failed to replay messaging declarations: %w", err)
	}

	if err := registry.DeclareInfrastructure(setupCtx); err != nil {
		m.closeClientOnRollback(client, key, "declare_infrastructure")
		return fmt.Errorf("failed to declare messaging infrastructure: %w", err)
	}

	// Start consumers with a tenant-aware context whose lifetime is detached from the
	// caller. In multi-tenant mode consumers start lazily from the HTTP request context
	// (a ~5s-deadline, cancel-on-finish context); threading that into the long-lived
	// supervisor goroutines would stop every consumer when the first request ends, and
	// they would never restart. context.WithoutCancel severs the request's cancellation
	// and deadline while preserving values (trace/tenant), so consumer lifetime is
	// governed solely by StopConsumers/Close.
	consumerCtx := multitenant.SetTenant(context.WithoutCancel(ctx), key)
	if err := registry.StartConsumers(consumerCtx); err != nil {
		m.closeClientOnRollback(client, key, "start_consumers")
		return fmt.Errorf("failed to start messaging consumers: %w", err)
	}

	m.consumers[key] = &consumerEntry{
		client:   client,
		registry: registry,
		started:  true,
		key:      key,
	}

	// Record hash for future idempotency checks
	m.replayedHashs[key] = declHash

	m.logger.Info().
		Str("key", key).
		Int("consumers", len(decls.Consumers())).
		Uint64("declaration_hash", declHash).
		Msg("Consumers started for key")

	return nil
}

// Publisher returns a publisher client for the given key plus a ReleaseFunc the caller must
// invoke when finished with it for the current unit of work (typically deferred). Publishers
// are cached with LRU eviction and lazy initialization; the lease prevents a publisher that
// is evicted while in use from being closed under an active caller (the #606 race). Once Close
// has run, Publisher fails closed rather than resurrecting a publisher (F22). On error the
// returned ReleaseFunc is nil — check err first.
func (m *Manager) Publisher(ctx context.Context, key string) (AMQPClient, ReleaseFunc, error) {
	if m.pubPool == nil {
		// Zero-value manager (never built via NewMessagingManager): unusable, fail closed
		// rather than panic — consistent with the Stats()/Close()/StartCleanup zero-value guards.
		return nil, nil, errManagerClosed
	}
	client, release, err := m.pubPool.GetOrCreate(ctx, key, func(ctx context.Context) (AMQPClient, error) {
		return m.createPublisher(ctx, key)
	})
	if err != nil {
		if errors.Is(err, resourcepool.ErrPoolClosed) {
			return nil, nil, errManagerClosed
		}
		return nil, nil, err
	}
	return client, ReleaseFunc(release), nil
}

// createPublisher creates a new AMQP client for the given key and wraps it so publishes carry
// the tenant header. It performs only client creation — the pool owns all lease/LRU/eviction
// bookkeeping. Invoked inside the pool's create callback, so singleflight guarantees one call
// per key per creation.
func (m *Manager) createPublisher(ctx context.Context, key string) (AMQPClient, error) {
	// Create the AMQP client (error is already well-formatted from createAMQPClient)
	client, err := m.createAMQPClient(ctx, key)
	if err != nil {
		return nil, err
	}
	return newTenantAwarePublisher(client, key), nil
}

// createAMQPClient creates a new AMQP client for the given key
func (m *Manager) createAMQPClient(ctx context.Context, key string) (AMQPClient, error) {
	// Get AMQP URL for this key (error is already well-formatted from tenant store)
	amqpURL, err := m.resourceSource.BrokerURL(ctx, key)
	if err != nil {
		return nil, err
	}

	// Create AMQP client using injected factory
	client := m.clientFactory(amqpURL, m.logger)

	m.logger.Info().
		Str("key", key).
		Str("broker_url", redactAMQPURL(amqpURL)).
		Msg("Created AMQP client for key")

	return client, nil
}

// closeClientOnRollback closes an AMQP client during an error-rollback path
// and logs (but does not propagate) any close failure. The primary error is
// what the caller cares about; we keep the close failure observable for
// forensics. The `phase` argument identifies which rollback site triggered
// the close (e.g. "replay_declarations", "declare_infrastructure").
func (m *Manager) closeClientOnRollback(client AMQPClient, key, phase string) {
	if err := client.Close(); err != nil {
		m.logger.Error().
			Err(err).
			Str("key", key).
			Str("phase", phase).
			Msg("Error closing AMQP client during rollback")
	}
}

// StartCleanup starts the background cleanup routine for idle publishers. A non-positive
// interval substitutes the documented 2-minute default.
func (m *Manager) StartCleanup(interval time.Duration) {
	if m.pubPool == nil {
		return // zero-value manager: nothing to run, consistent with the other nil-pool guards
	}
	if interval <= 0 {
		interval = 2 * time.Minute // default cleanup interval for publishers
	}
	m.pubPool.StartCleanup(interval)
}

// StopCleanup stops the background cleanup routine
func (m *Manager) StopCleanup() {
	if m.pubPool == nil {
		return // zero-value manager: nothing to stop
	}
	m.pubPool.StopCleanup()
}

// StopConsumers stops every consumer registry from accepting new messages (canceling their
// consume contexts) WITHOUT closing the underlying AMQP connections — Close does that. The
// framework calls this during shutdown before tearing down modules so it stops delivering
// fresh messages to modules that are about to shut down. Cancellation propagates to in-flight
// handlers via their context, but they are not synchronously joined here. Idempotent:
// Registry.StopConsumers guards on its active flag, so a subsequent Close (which also stops
// consumers) is safe.
func (m *Manager) StopConsumers() {
	m.consMu.Lock()
	defer m.consMu.Unlock()
	for _, entry := range m.consumers {
		if entry.registry != nil {
			entry.registry.StopConsumers()
		}
	}
}

// Close closes all clients and stops cleanup. Publisher closes go through the pool (which
// stops its own cleanup loop and joins every per-publisher close failure); consumer closes
// are handled directly. Every failure from BOTH sides is surfaced under the historical
// "errors closing messaging clients" prefix.
func (m *Manager) Close() error {
	var allErrs []error

	// Close all publishers via the pool. pool.Close stops the publisher cleanup loop (exactly
	// once, via its closeOnce) and returns errors.Join of every per-publisher close failure.
	if m.pubPool != nil {
		if err := m.pubPool.Close(); err != nil {
			allErrs = append(allErrs, err)
		}
	}

	// Close all consumers (and their registries)
	m.consMu.Lock()
	for key, entry := range m.consumers {
		if entry.registry != nil {
			entry.registry.StopConsumers()
		}
		if err := entry.client.Close(); err != nil {
			allErrs = append(allErrs, fmt.Errorf("error closing consumer for key %s: %w", key, err))
		}
	}
	m.consumers = make(map[string]*consumerEntry)
	m.consMu.Unlock()

	if len(allErrs) > 0 {
		return fmt.Errorf("errors closing messaging clients: %w", errors.Join(allErrs...))
	}

	return nil
}

// Stats returns statistics about the messaging manager. Publisher counters come from the
// pool; active_consumers comes from the directly-managed consumer map.
func (m *Manager) Stats() map[string]any {
	m.consMu.RLock()
	consCount := len(m.consumers)
	m.consMu.RUnlock()

	// A zero-value Manager (not built via NewMessagingManager, e.g. the lightweight stand-in
	// the debug/health endpoint uses) reports zero publisher stats rather than panicking.
	stats := map[string]any{
		"active_publishers": 0,
		"max_publishers":    0,
		"active_consumers":  consCount,
		"idle_ttl_seconds":  0,
		"evictions":         0,
		"idle_cleanups":     0,
	}

	if m.pubPool != nil {
		ps := m.pubPool.Stats()
		stats["active_publishers"] = ps.Size
		stats["max_publishers"] = ps.MaxSize
		stats["idle_ttl_seconds"] = int(ps.IdleTTL.Seconds())
		stats["evictions"] = ps.Evictions
		stats["idle_cleanups"] = ps.IdleCleanups
	}

	return stats
}
