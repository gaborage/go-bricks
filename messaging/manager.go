package messaging

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
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

// ErrManagerClosed is returned by Manager's EnsureConsumers and Publisher methods once
// Close has been called, rather than resurrecting a consumer or publisher on a shut-down
// manager (backlog F22). Publisher additionally returns it from a zero-value Manager that
// was never built via NewMessagingManager. Callers can use errors.Is(err, ErrManagerClosed)
// to distinguish "manager is gone" from a per-key failure and decide whether to abort or
// fall back to a non-messaging path.
var ErrManagerClosed = errors.New("messaging: manager closed")

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

	// closed flips to true the moment Close begins, mirroring resourcepool.Pool's flag
	// (the publisher side gets its closed state from the pool; the consumer side has no
	// pool to ask). It is atomic rather than consMu-guarded because EnsureConsumers must
	// read it without ever blocking on a lock an in-flight setup pass holds across a
	// broker dial — see consumersReplayed's TryRLock rationale.
	closed atomic.Bool

	// Singleflight for concurrent consumer initialization
	sfg singleflight.Group
	// tenantStamps is ManagerOptions.TenantStamps, handed to every registry this
	// manager builds so the consume side knows whether a delivery's tenant stamp
	// is the authority for the handler's tenant.
	tenantStamps bool
}

// consumerEntry represents a long-lived consumer
type consumerEntry struct {
	client   AMQPClient
	registry *Registry
	started  bool
	key      string
}

// defaultPublisherCleanupInterval is the documented idle-sweep frequency
// (messaging.publisher.cleanupinterval) applied when the caller supplies none.
const defaultPublisherCleanupInterval = 2 * time.Minute

// ManagerOptions configures the Manager
type ManagerOptions struct {
	MaxPublishers int           // Maximum number of publisher clients to keep cached
	IdleTTL       time.Duration // Time after which idle publishers are evicted
	// CleanupInterval is how often the idle-publisher sweep runs; <=0 uses the documented
	// 2-minute default. The manager starts that sweep itself at construction (ADR-067).
	CleanupInterval time.Duration
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
	// TenantStamps makes consumers read the tenant stamp off each delivery and seed
	// the handler context with it. True only under multitenant.enabled together with
	// messaging.tenancy: shared — under per-tenant tenancy the replay key is already
	// the tenant, and in single-tenant mode there is none to read.
	TenantStamps bool
}

// NewMessagingManager creates a new messaging manager. The idle-publisher sweep starts here and
// stops in Close, so callers need not drive it (ADR-067); StartCleanup remains available and
// idempotent.
//
//nolint:gocritic // hugeParam: every manager constructor takes its options by value (shipped signature); it runs once at startup
func NewMessagingManager(resourceSource BrokerURLProvider, log logger.Logger, opts ManagerOptions, clientFactory ClientFactory) *Manager {
	if opts.MaxPublishers <= 0 {
		opts.MaxPublishers = 50 // sensible default
	}
	if opts.IdleTTL <= 0 {
		// Interface default for bare callers constructing a manager without the app
		// builder; single-tenant value — a bare caller supplies no deployment-mode
		// signal. The app path always arrives with IdleTTL already stamped by
		// config.Validate (ADR-064).
		opts.IdleTTL = 1 * time.Hour
	}
	if opts.CleanupInterval <= 0 {
		opts.CleanupInterval = defaultPublisherCleanupInterval
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

	m := &Manager{
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
		tenantStamps:  opts.TenantStamps,
	}

	resourcepool.WarnIfCleanupIntervalTooLate(log, "messaging.publisher", opts.CleanupInterval, opts.IdleTTL)
	m.pubPool.StartCleanup(opts.CleanupInterval)

	return m
}

// EnsureConsumers creates and starts consumers for the given key using the provided declarations.
// This should be called once per key to set up long-lived consumers.
// Subsequent calls for the same key are idempotent.
//
// Concurrent calls for the same key collapse onto one setup pass, but each caller waits on ITS OWN
// context. DoChan (not Do) is what makes that possible: Do blocks uncancelably, so a caller whose
// budget was already spent still sat through the full setup — bounded by infraSetupTimeout (45s)
// while the realistic caller is a lazy first-touch request carrying a ~5s deadline. Giving up does
// NOT cancel the setup: ensureConsumersInternal runs on a WithoutCancel budget, so it completes and
// installs the consumers for whoever asks next.
//
// One testing caveat: singleflight neither recovers nor forwards a runtime.Goexit from the
// shared call, so a test double that calls t.Fatal or require.* inside the setup path hangs
// every waiter. Use t.Errorf and return, as the repo's httptest handlers do.
func (m *Manager) EnsureConsumers(ctx context.Context, key string, decls *Declarations) error {
	// Nil declarations would nil-deref in Hash() below — on the caller's goroutine, outside the
	// closure's recover. Every current caller already guards decls == nil before calling, but
	// EnsureConsumers is exported, so reject it here too rather than trust every caller to.
	if decls == nil {
		return fmt.Errorf("messaging: nil declarations for key %q", key)
	}
	if m.closed.Load() {
		return ErrManagerClosed
	}
	declHash := decls.Hash()

	// Fast path: an already-replayed key needs no setup pass, so it must not depend on the
	// caller's context at all. Mirrors resourcepool.GetOrCreate's getExisting-before-DoChan.
	// Without it every warm messaging resolution allocates a channel and spawns a goroutine,
	// and a caller whose budget is already spent fails on work that would have been a no-op.
	if m.consumersReplayed(key, declHash) {
		return nil
	}

	// Singleflight prevents concurrent consumer setup for the same key.
	ch := m.sfg.DoChan("consumer:"+key, func() (v any, err error) {
		// A panic must not escape through DoChan: x/sync re-panics on a NEW goroutine once any
		// caller used DoChan (`go panic(e)` in doCall), which no recover — including Echo's
		// middleware.Recover — can catch, so one tenant's bad broker config would kill the
		// process instead of failing one request. Converting it here restores the pre-DoChan
		// blast radius and improves on it: collapsed callers get an error, not a re-raised panic.
		defer func() {
			if r := recover(); r != nil {
				err = fmt.Errorf("messaging: panic during consumer setup for key %q (type: %T)", key, r)
			}
		}()
		return nil, m.ensureConsumersInternal(ctx, key, decls, declHash)
	})

	select {
	case res := <-ch:
		return res.Err
	case <-ctx.Done():
		// Nothing to settle on the way out: unlike resourcepool.GetOrCreate a collapsed caller here
		// holds no lease and receives no handle, so there is no seed to hand back (no
		// releaseAbandoned analog), and singleflight's result channel is buffered (capacity 1) so
		// the abandoned send never blocks.
		return fmt.Errorf("messaging: caller context ended while consumer setup for key %q was in flight (setup is not canceled): %w", key, ctx.Err())
	}
}

// ensureConsumersInternal performs the actual consumer setup. declHash is computed once by
// EnsureConsumers and threaded through, so the fast path and this path provably compare the
// same value and the declarations are walked once per setup rather than twice.
func (m *Manager) ensureConsumersInternal(ctx context.Context, key string, decls *Declarations, declHash uint64) error {
	m.consMu.Lock()
	defer m.consMu.Unlock()

	// Re-check under the lock: 1.3's guard is a pre-lock read, so a Close that lands
	// between it and this Lock would otherwise let the setup pass install a consumer into
	// the map Close just drained — the very connection leak this change exists to close.
	if m.closed.Load() {
		return ErrManagerClosed
	}

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
	registry.tenantStamps = m.tenantStamps
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

	m.replayedHashs[key] = declHash

	m.logger.Info().
		Str("key", key).
		Int("consumers", len(decls.Consumers())).
		Uint64("declaration_hash", declHash).
		Msg("Consumers started for key")

	return nil
}

// consumersReplayed reports whether key's consumers were already set up from exactly these
// declarations — i.e. whether ensureConsumersInternal would return nil without doing any work.
//
// TryRLock, not RLock: ensureConsumersInternal holds consMu in WRITE mode for its whole pass,
// including the broker dial, and consMu is manager-wide rather than per-key. A blocking RLock here
// would therefore park any caller — even one for an unrelated key — behind an in-flight setup,
// before the select that is supposed to honor its context, reinstating the very uncancelable wait
// this path exists to remove and widening it across tenants. Failing to acquire only costs the
// fast path: the caller falls through to DoChan, where ensureConsumersInternal re-checks
// idempotency under the real lock, so the answer is never wrong — just not free.
func (m *Manager) consumersReplayed(key string, declHash uint64) bool {
	if !m.consMu.TryRLock() {
		return false
	}
	defer m.consMu.RUnlock()
	existing, ok := m.replayedHashs[key]
	return ok && existing == declHash
}

// Publisher returns a publisher client for the given key plus a ReleaseFunc the caller must
// invoke when finished with it for the current unit of work (typically deferred). Publishers
// are cached with LRU eviction and lazy initialization; the lease prevents a publisher that
// is evicted while in use from being closed under an active caller (the #606 race). Once Close
// begins, Publisher fails closed rather than resurrecting a publisher (F22) — except a
// caller already mid-Publisher on a fresh client another borrower holds, who may still
// receive that live handle after Close returns; it closes exactly once, at its final
// release. On error the returned ReleaseFunc is nil — check err first.
func (m *Manager) Publisher(ctx context.Context, key string) (AMQPClient, ReleaseFunc, error) {
	if m.pubPool == nil {
		// Zero-value manager (never built via NewMessagingManager): unusable, fail closed
		// rather than panic — consistent with the Stats()/Close()/StartCleanup zero-value guards.
		return nil, nil, ErrManagerClosed
	}
	if m.closed.Load() {
		// Close flips this flag before closing the pool (see Close), so without this check a
		// caller landing in that window would reach a still-open pool and could get back a live
		// publisher — new or cached — on a manager that has begun shutting down. The
		// ErrPoolClosed translation below only catches callers arriving once the pool itself
		// has finished closing.
		return nil, nil, ErrManagerClosed
	}
	client, release, err := m.pubPool.GetOrCreate(ctx, key, func(ctx context.Context) (AMQPClient, error) {
		return m.createPublisher(ctx, key)
	})
	if err != nil {
		if errors.Is(err, resourcepool.ErrPoolClosed) {
			return nil, nil, ErrManagerClosed
		}
		return nil, nil, err
	}
	return client, ReleaseFunc(release), nil
}

// createPublisher creates a new AMQP client for the given key and tells it which key it
// was pooled under, so its publish doors can resolve the tenant stamp. It performs only
// client creation — the pool owns all lease/LRU/eviction bookkeeping. Invoked inside the
// pool's create callback, so singleflight guarantees one call per key per creation.
//
// The key reaches the client through an optional interface rather than a concrete
// type, so the manager stays coupled to AMQPClient and a client that has no publish
// door to stamp from — a test double, an adapter — simply does not implement it.
func (m *Manager) createPublisher(ctx context.Context, key string) (AMQPClient, error) {
	// Create the AMQP client (error is already well-formatted from createAMQPClient)
	client, err := m.createAMQPClient(ctx, key)
	if err != nil {
		return nil, err
	}
	if setter, ok := client.(replayKeySetter); ok {
		setter.setReplayKey(key)
	}
	return client, nil
}

// replayKeySetter is implemented by clients that stamp the tenant onto their
// publishes and therefore need to know which key they were pooled under.
type replayKeySetter interface {
	setReplayKey(key string)
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
// interval substitutes the documented 2-minute default. The constructor already started a
// sweep, so this is a no-op unless StopCleanup ran first (the pool's loop is single-instance).
func (m *Manager) StartCleanup(interval time.Duration) {
	if m.pubPool == nil {
		return // zero-value manager: nothing to run, consistent with the other nil-pool guards
	}
	if interval <= 0 {
		interval = defaultPublisherCleanupInterval
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
// consumers) is safe. Unlike Close it does not mark the manager closed and leaves the replay
// state intact, so a Stop is recoverable while a Close is terminal.
func (m *Manager) StopConsumers() {
	m.consMu.Lock()
	defer m.consMu.Unlock()
	for _, entry := range m.consumers {
		if entry.registry != nil {
			entry.registry.StopConsumers()
		}
	}
}

// Close closes all clients and stops the idle-publisher sweep the constructor started. Publisher
// closes go through the pool (which stops its own cleanup loop and joins every per-publisher
// close failure); consumer closes are handled directly. A publisher client still borrowed by
// in-flight work is closed at its final release instead of by this call, and that deferred close
// failure (if any) is excluded from this return value — it is counted in Stats()["errors"]
// instead (wiki/migrations.md C581.3). Every failure returned here, from BOTH sides, is surfaced
// under the historical "errors closing messaging clients" prefix.
func (m *Manager) Close() error {
	// Flip closed BEFORE any teardown, matching resourcepool.Close: it establishes the
	// happens-before that lets ensureConsumersInternal's re-check see the flag once it
	// acquires consMu after this drain releases it.
	m.closed.Store(true)

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
	m.replayedHashs = make(map[string]uint64)
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
		"errors":            0,
	}

	if m.pubPool != nil {
		ps := m.pubPool.Stats()
		stats["active_publishers"] = ps.Size
		stats["max_publishers"] = ps.MaxSize
		stats["idle_ttl_seconds"] = int(ps.IdleTTL.Seconds())
		stats["evictions"] = ps.Evictions
		stats["idle_cleanups"] = ps.IdleCleanups
		// Publisher create/close failures (including a deferred close on a client still
		// borrowed when Close ran, C581.3) — excluded from Close()'s returned error, so
		// this is the only way a caller observes them.
		stats["errors"] = ps.Errors
	}

	return stats
}
