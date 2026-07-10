package messaging

import (
	"container/list"
	"context"
	"fmt"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"

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

// maxPublisherAttempts bounds the rare retry where a reused publisher entry is closed before
// the caller can take a lease (only under extreme pool churn). A new entry carries a seed
// lease, so the create path always succeeds and the loop converges.
const maxPublisherAttempts = 4

// Manager manages AMQP clients by string keys with different lifecycle strategies.
// Publishers are cached with idle eviction (can be recreated easily).
// Consumers are long-lived (must stay alive to receive messages).
// The manager is key-agnostic - it doesn't know about tenants, just manages named clients.
type Manager struct {
	logger         logger.Logger
	resourceSource BrokerURLProvider
	clientFactory  ClientFactory // Injected for testability

	// Publishers (evictable)
	pubMu      sync.RWMutex
	publishers map[string]*publisherEntry
	pubLru     *list.List
	maxPubs    int
	idleTTL    time.Duration
	// evictions counts cumulative LRU-driven publisher evictions. Guarded by pubMu.
	// Mirrors cache.CacheManager's evictions field/locking (cache/manager.go).
	evictions int
	// idleCleanups counts cumulative idle-TTL-driven publisher removals. Guarded
	// by pubMu. Mirrors cache.CacheManager's idleCleanups field/locking.
	idleCleanups int

	// Consumers (long-lived)
	consMu        sync.RWMutex
	consumers     map[string]*consumerEntry
	replayedHashs map[string]uint64 // Tracks declaration hashes to prevent duplicate replay

	// Cleanup management
	cleanupMu sync.Mutex
	cleanupCh chan struct{}

	// Singleflight for concurrent initialization
	sfg singleflight.Group
}

// publisherEntry represents a cached publisher with LRU metadata.
// refs, seedHeld, detached, and closed are guarded by Manager.pubMu.
type publisherEntry struct {
	client   AMQPClient
	element  *list.Element // for LRU
	lastUsed time.Time
	key      string

	// refs counts outstanding leases (current borrowers); a publisher with refs > 0 is in use.
	refs int
	// seedHeld is true when one of refs is an unclaimed "seed" lease taken at creation. It
	// keeps a brand-new entry alive through the window before its first Publisher caller claims
	// it, so a concurrent evict/idle-cleanup can only detach (never close) it.
	seedHeld bool
	// detached marks an entry removed from the map+LRU whose Close() was deferred because a
	// lease was still outstanding.
	detached bool
	// closed guards against a double Close() once the deferred close has run.
	closed bool
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
		publishers:     make(map[string]*publisherEntry),
		pubLru:         list.New(),
		maxPubs:        opts.MaxPublishers,
		idleTTL:        opts.IdleTTL,
		consumers:      make(map[string]*consumerEntry),
		replayedHashs:  make(map[string]uint64),
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

	// Create AMQP client for consumers (error is already well-formatted from createAMQPClient)
	client, err := m.createAMQPClient(ctx, key)
	if err != nil {
		return err
	}

	// Create registry and replay declarations
	registry := NewRegistry(client, m.logger)
	if err := decls.ReplayToRegistry(registry); err != nil {
		m.closeClientOnRollback(client, key, "replay_declarations")
		return fmt.Errorf("failed to replay messaging declarations: %w", err)
	}

	// Declare infrastructure
	if err := registry.DeclareInfrastructure(ctx); err != nil {
		m.closeClientOnRollback(client, key, "declare_infrastructure")
		return fmt.Errorf("failed to declare messaging infrastructure: %w", err)
	}

	// Start consumers with a tenant-aware context whose lifetime is detached from the
	// caller. In multi-tenant mode consumers start lazily from the HTTP request context
	// (a ~5s-deadline, cancel-on-finish context); threading that into the long-lived
	// supervisor goroutines would stop every consumer when the first request ends, and
	// they would never restart. context.WithoutCancel severs the request's cancellation
	// and deadline while preserving values (trace/tenant), so consumer lifetime is
	// governed solely by StopConsumers/Close. (Setup calls above keep the caller deadline.)
	consumerCtx := multitenant.SetTenant(context.WithoutCancel(ctx), key)
	if err := registry.StartConsumers(consumerCtx); err != nil {
		m.closeClientOnRollback(client, key, "start_consumers")
		return fmt.Errorf("failed to start messaging consumers: %w", err)
	}

	// Store the consumer entry
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
// is evicted while in use from being closed under an active caller (the #606 race). On error
// the returned ReleaseFunc is nil — check err first.
func (m *Manager) Publisher(ctx context.Context, key string) (AMQPClient, ReleaseFunc, error) {
	for attempt := 0; attempt < maxPublisherAttempts; attempt++ {
		// Fast path: getExistingPublisher increments the refcount atomically with the lookup.
		if entry := m.getExistingPublisher(key); entry != nil {
			return entry.client, m.makeRelease(entry), nil
		}

		// Slow path: singleflight collapses concurrent creates for the same key into one. It
		// returns the shared entry (freshly created with a seed lease, or an existing one);
		// every caller then takes its own lease on that pointer via claimOrAcquire — the first
		// claims the seed, the rest increment — so each concurrent borrower is counted.
		v, err, _ := m.sfg.Do("publisher:"+key, func() (any, error) {
			if e := m.peekPublisher(key); e != nil {
				return e, nil
			}
			return m.createPublisher(ctx, key)
		})
		if err != nil {
			return nil, nil, err
		}

		entry := v.(*publisherEntry)
		if m.claimOrAcquire(entry) {
			return entry.client, m.makeRelease(entry), nil
		}
		// The reused entry was closed in the window between lookup and claim; loop to create
		// a fresh one. The create path always succeeds (seed lease), so this converges.
	}

	return nil, nil, fmt.Errorf("failed to acquire publisher for key %s after %d attempts (pool churn)", key, maxPublisherAttempts)
}

// getExistingPublisher returns an existing entry with a lease acquired (refcount incremented)
// and updates LRU, or nil if not found. The increment happens under the same lock as the
// lookup so the entry cannot be evicted-and-closed before the lease is taken.
func (m *Manager) getExistingPublisher(key string) *publisherEntry {
	m.pubMu.Lock()
	defer m.pubMu.Unlock()

	entry, exists := m.publishers[key]
	if !exists {
		return nil
	}

	// Update LRU and last used time
	entry.lastUsed = time.Now()
	m.pubLru.MoveToFront(entry.element)
	entry.refs++

	return entry
}

// peekPublisher reports whether an entry exists without taking a lease or touching LRU.
func (m *Manager) peekPublisher(key string) *publisherEntry {
	m.pubMu.RLock()
	defer m.pubMu.RUnlock()
	return m.publishers[key]
}

// claimOrAcquire takes one lease on entry, operating on the shared pointer so it can never
// "miss" via a map lookup. Returns false only when the entry has already been fully closed
// (a reused entry that lost a race), signaling the caller to retry with a fresh entry.
func (m *Manager) claimOrAcquire(entry *publisherEntry) bool {
	m.pubMu.Lock()
	defer m.pubMu.Unlock()
	if entry.closed {
		return false
	}
	if entry.seedHeld {
		entry.seedHeld = false // claim the seed: that ref becomes this caller's lease
	} else {
		entry.refs++
	}
	return true
}

// makeRelease returns an idempotent ReleaseFunc bound to a single lease on entry.
func (m *Manager) makeRelease(entry *publisherEntry) ReleaseFunc {
	var once sync.Once
	return func() {
		once.Do(func() { m.releasePublisher(entry) })
	}
}

// releasePublisher drops one lease. If the entry was detached (evicted/idle-cleaned) while
// leased and this was the final lease, it closes the publisher now — outside the lock.
func (m *Manager) releasePublisher(entry *publisherEntry) {
	m.pubMu.Lock()
	entry.refs--
	shouldClose := entry.detached && entry.refs <= 0 && !entry.closed
	if shouldClose {
		entry.closed = true
	}
	m.pubMu.Unlock()

	if shouldClose {
		m.closeEvictedPublisher(entry, "Error closing evicted publisher client (deferred until lease release)")
	}
}

// createPublisher creates a new publisher client for the given key and registers it with a
// single seed lease (refs == 1, seedHeld). The seed keeps the entry alive through the window
// before the caller claims it via claimOrAcquire. Returns an existing entry unchanged on a
// double-create race.
func (m *Manager) createPublisher(ctx context.Context, key string) (*publisherEntry, error) {
	// Create the AMQP client (error is already well-formatted from createAMQPClient)
	client, err := m.createAMQPClient(ctx, key)
	if err != nil {
		return nil, err
	}

	wrapped := newTenantAwarePublisher(client, key)

	// Store in cache with LRU tracking
	m.pubMu.Lock()

	// Check if publisher was created by another goroutine while we were waiting
	if existing, exists := m.publishers[key]; exists {
		existing.lastUsed = time.Now()
		m.pubLru.MoveToFront(existing.element)
		m.pubMu.Unlock()

		// Close our new client outside the lock and return the existing one.
		m.closeClientOnRollback(client, key, "publisher_double_create_race")
		return existing, nil
	}

	// Ensure we don't exceed max size (returns the evicted entry to close outside the lock)
	evicted := m.evictPublisherIfNeeded()

	// Add to cache with a seed lease.
	element := m.pubLru.PushFront(key)
	entry := &publisherEntry{
		client:   wrapped,
		element:  element,
		lastUsed: time.Now(),
		key:      key,
		refs:     1,
		seedHeld: true,
	}
	m.publishers[key] = entry
	m.pubMu.Unlock()

	// Close the evicted client outside the lock so a slow Close() on the LRU victim
	// does not block concurrent Publisher() calls for other keys.
	if evicted != nil {
		m.closeEvictedPublisher(evicted, "Error closing evicted publisher client")
	}

	m.logger.Info().
		Str("key", key).
		Msg("Created new publisher client")

	return entry, nil
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
// the close (e.g. "replay_declarations", "publisher_double_create_race").
func (m *Manager) closeClientOnRollback(client AMQPClient, key, phase string) {
	if err := client.Close(); err != nil {
		m.logger.Error().
			Err(err).
			Str("key", key).
			Str("phase", phase).
			Msg("Error closing AMQP client during rollback")
	}
}

// evictPublisherIfNeeded removes the least recently used publisher from the
// manager's bookkeeping if at capacity. Must be called with m.pubMu held. It
// returns the evicted entry (or nil) so the caller can close it OUTSIDE the lock.
func (m *Manager) evictPublisherIfNeeded() *publisherEntry {
	if len(m.publishers) < m.maxPubs {
		return nil
	}

	// Remove the least recently used publisher
	oldest := m.pubLru.Back()
	if oldest == nil {
		return nil
	}

	key := oldest.Value.(string)
	entry := m.publishers[key]

	// Detach from cache (close happens outside the lock, and only when unleased)
	delete(m.publishers, key)
	m.pubLru.Remove(oldest)
	entry.detached = true
	m.evictions++

	m.logger.Info().
		Str("key", key).
		Msg("Evicted publisher client due to LRU limit")

	if entry.refs > 0 {
		return nil // still leased — defer the close to the final lease release
	}
	entry.closed = true
	return entry
}

// closeEvictedPublisher closes an evicted/idle publisher client and logs any close
// error. It is always called WITHOUT m.pubMu held so a slow Close() cannot block
// other callers.
func (m *Manager) closeEvictedPublisher(entry *publisherEntry, logMsg string) {
	if err := entry.client.Close(); err != nil {
		m.logger.Error().
			Err(err).
			Str("key", entry.key).
			Msg(logMsg)
	}
}

// StartCleanup starts the background cleanup routine for idle publishers
func (m *Manager) StartCleanup(interval time.Duration) {
	if interval <= 0 {
		interval = 2 * time.Minute // default cleanup interval for publishers
	}

	m.cleanupMu.Lock()
	if m.cleanupCh != nil {
		m.cleanupMu.Unlock()
		return
	}
	done := make(chan struct{})
	m.cleanupCh = done
	m.cleanupMu.Unlock()

	go m.cleanupLoop(interval, done)
}

// StopCleanup stops the background cleanup routine
func (m *Manager) StopCleanup() {
	m.cleanupMu.Lock()
	if m.cleanupCh == nil {
		m.cleanupMu.Unlock()
		return
	}
	close(m.cleanupCh)
	m.cleanupCh = nil
	m.cleanupMu.Unlock()
}

// cleanupLoop runs the periodic cleanup of idle publishers
func (m *Manager) cleanupLoop(interval time.Duration, done <-chan struct{}) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			m.cleanupIdlePublishers()
		case <-done:
			return
		}
	}
}

// cleanupIdlePublishers removes publishers that have been idle longer than idleTTL.
// Idle entries are detached from the manager's bookkeeping under the lock, then their
// Close() calls run outside the lock so a slow teardown cannot block concurrent
// Publisher() calls.
func (m *Manager) cleanupIdlePublishers() {
	m.pubMu.Lock()

	now := time.Now()
	var toClose []*publisherEntry

	// Detach idle publishers from bookkeeping under the lock. A still-leased idle publisher
	// is detached but its Close() is deferred to the final lease release (the #606 race).
	for key, entry := range m.publishers {
		if now.Sub(entry.lastUsed) <= m.idleTTL {
			continue
		}
		delete(m.publishers, key)
		m.pubLru.Remove(entry.element)
		entry.detached = true
		m.idleCleanups++

		m.logger.Info().
			Str("key", key).
			Dur("idle_time", now.Sub(entry.lastUsed)).
			Msg("Cleaned up idle publisher client")

		if entry.refs > 0 {
			continue // still leased — defer close to release
		}
		entry.closed = true
		toClose = append(toClose, entry)
	}
	m.pubMu.Unlock()

	// Close detached publishers outside the lock.
	for _, entry := range toClose {
		m.closeEvictedPublisher(entry, "Error closing idle publisher client")
	}
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

// Close closes all clients and stops cleanup.
func (m *Manager) Close() error {
	m.StopCleanup()

	var errors []error

	// Close all publishers
	m.pubMu.Lock()
	for key, entry := range m.publishers {
		if err := entry.client.Close(); err != nil {
			errors = append(errors, fmt.Errorf("error closing publisher for key %s: %w", key, err))
		}
	}
	m.publishers = make(map[string]*publisherEntry)
	m.pubLru.Init()
	m.pubMu.Unlock()

	// Close all consumers (and their registries)
	m.consMu.Lock()
	for key, entry := range m.consumers {
		if entry.registry != nil {
			entry.registry.StopConsumers()
		}
		if err := entry.client.Close(); err != nil {
			errors = append(errors, fmt.Errorf("error closing consumer for key %s: %w", key, err))
		}
	}
	m.consumers = make(map[string]*consumerEntry)
	m.consMu.Unlock()

	if len(errors) > 0 {
		return fmt.Errorf("errors closing messaging clients: %v", errors)
	}

	return nil
}

// Stats returns statistics about the messaging manager
func (m *Manager) Stats() map[string]any {
	m.pubMu.RLock()
	pubCount := len(m.publishers)
	evictions := m.evictions
	idleCleanups := m.idleCleanups
	m.pubMu.RUnlock()

	m.consMu.RLock()
	consCount := len(m.consumers)
	m.consMu.RUnlock()

	stats := map[string]any{
		"active_publishers": pubCount,
		"max_publishers":    m.maxPubs,
		"active_consumers":  consCount,
		"idle_ttl_seconds":  int(m.idleTTL.Seconds()),
		"evictions":         evictions,
		"idle_cleanups":     idleCleanups,
	}

	return stats
}
