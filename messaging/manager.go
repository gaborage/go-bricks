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

// TenantMessagingResourceSource provides per-key AMQP configurations.
// This interface abstracts where tenant-specific messaging configs come from.
type TenantMessagingResourceSource interface {
	// BrokerURL returns the AMQP broker URL for the given key.
	// For single-tenant apps, key will be "". For multi-tenant, key will be the tenant ID.
	BrokerURL(ctx context.Context, key string) (string, error)
}

// ClientFactory creates AMQP clients from URLs
type ClientFactory func(string, logger.Logger) AMQPClient

// Manager manages AMQP clients by string keys with different lifecycle strategies.
// Publishers are cached with idle eviction (can be recreated easily).
// Consumers are long-lived (must stay alive to receive messages).
// The manager is key-agnostic - it doesn't know about tenants, just manages named clients.
type Manager struct {
	logger         logger.Logger
	resourceSource TenantMessagingResourceSource
	clientFactory  ClientFactory // Injected for testability

	// Publishers (evictable)
	pubMu      sync.RWMutex
	publishers map[string]*publisherEntry
	pubLru     *list.List
	maxPubs    int
	idleTTL    time.Duration

	// Consumers (long-lived)
	consMu    sync.RWMutex
	consumers map[string]*consumerEntry

	// Cleanup management
	cleanupMu sync.Mutex
	cleanupCh chan struct{}

	// Singleflight for concurrent initialization
	sfg singleflight.Group
}

// publisherEntry represents a cached publisher with LRU metadata
type publisherEntry struct {
	client   AMQPClient
	element  *list.Element // for LRU
	lastUsed time.Time
	key      string
}

// consumerEntry represents a long-lived consumer
type consumerEntry struct {
	client   AMQPClient
	registry *Registry
	started  bool
	key      string
}

// ManagerOptions configures the MessagingManager
type ManagerOptions struct {
	MaxPublishers int           // Maximum number of publisher clients to keep cached
	IdleTTL       time.Duration // Time after which idle publishers are evicted
}

// NewMessagingManager creates a new messaging manager
func NewMessagingManager(resourceSource TenantMessagingResourceSource, log logger.Logger, opts ManagerOptions, clientFactory ClientFactory) *Manager {
	if opts.MaxPublishers <= 0 {
		opts.MaxPublishers = 50 // sensible default
	}
	if opts.IdleTTL <= 0 {
		opts.IdleTTL = 10 * time.Minute // shorter than DB since publishers are lighter
	}

	// Default to real client factory if none provided
	if clientFactory == nil {
		clientFactory = func(url string, log logger.Logger) AMQPClient {
			return NewAMQPClient(url, log)
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

	// Check if consumers already exist and are started
	if entry, exists := m.consumers[key]; exists {
		if entry.started {
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
		client.Close()
		return fmt.Errorf("failed to replay messaging declarations: %w", err)
	}

	// Declare infrastructure
	if err := registry.DeclareInfrastructure(ctx); err != nil {
		client.Close()
		return fmt.Errorf("failed to declare messaging infrastructure: %w", err)
	}

	// Start consumers with tenant-aware context
	tenantCtx := multitenant.SetTenant(ctx, key)
	if err := registry.StartConsumers(tenantCtx); err != nil {
		client.Close()
		return fmt.Errorf("failed to start messaging consumers: %w", err)
	}

	// Store the consumer entry
	m.consumers[key] = &consumerEntry{
		client:   client,
		registry: registry,
		started:  true,
		key:      key,
	}

	m.logger.Info().
		Str("key", key).
		Int("consumers", len(decls.Consumers)).
		Msg("Consumers started for key")

	return nil
}

// GetPublisher returns a publisher client for the given key.
// Publishers are cached with LRU eviction and lazy initialization.
func (m *Manager) GetPublisher(ctx context.Context, key string) (AMQPClient, error) {
	// Try to get existing publisher first (fast path)
	if client := m.getExistingPublisher(key); client != nil {
		return client, nil
	}

	// Use singleflight to prevent thundering herd on client creation
	result, err, _ := m.sfg.Do("publisher:"+key, func() (any, error) {
		// Double-check after acquiring singleflight lock
		if client := m.getExistingPublisher(key); client != nil {
			return client, nil
		}

		return m.createPublisher(ctx, key)
	})

	if err != nil {
		return nil, err
	}

	return result.(AMQPClient), nil
}

// getExistingPublisher returns an existing publisher and updates LRU, or nil if not found
func (m *Manager) getExistingPublisher(key string) AMQPClient {
	m.pubMu.Lock()
	defer m.pubMu.Unlock()

	entry, exists := m.publishers[key]
	if !exists {
		return nil
	}

	// Update LRU and last used time
	entry.lastUsed = time.Now()
	m.pubLru.MoveToFront(entry.element)

	return entry.client
}

// createPublisher creates a new publisher client for the given key
func (m *Manager) createPublisher(ctx context.Context, key string) (AMQPClient, error) {
	// Create the AMQP client (error is already well-formatted from createAMQPClient)
	client, err := m.createAMQPClient(ctx, key)
	if err != nil {
		return nil, err
	}

	wrapped := newTenantAwarePublisher(client, key)

	// Store in cache with LRU tracking
	m.pubMu.Lock()
	defer m.pubMu.Unlock()

	// Check if publisher was created by another goroutine while we were waiting
	if existing, exists := m.publishers[key]; exists {
		// Close our new client and return the existing one
		client.Close()
		existing.lastUsed = time.Now()
		m.pubLru.MoveToFront(existing.element)
		return existing.client, nil
	}

	// Ensure we don't exceed max size
	m.evictPublisherIfNeeded()

	// Add to cache
	element := m.pubLru.PushFront(key)
	entry := &publisherEntry{
		client:   wrapped,
		element:  element,
		lastUsed: time.Now(),
		key:      key,
	}
	m.publishers[key] = entry

	m.logger.Info().
		Str("key", key).
		Msg("Created new publisher client")

	return wrapped, nil
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
		Str("url", amqpURL).
		Msg("Created AMQP client for key")

	return client, nil
}

// evictPublisherIfNeeded removes the least recently used publisher if at capacity
func (m *Manager) evictPublisherIfNeeded() {
	if len(m.publishers) < m.maxPubs {
		return
	}

	// Remove the least recently used publisher
	oldest := m.pubLru.Back()
	if oldest == nil {
		return
	}

	key := oldest.Value.(string)
	entry := m.publishers[key]

	// Close the client
	if err := entry.client.Close(); err != nil {
		m.logger.Error().
			Err(err).
			Str("key", key).
			Msg("Error closing evicted publisher client")
	}

	// Remove from cache
	delete(m.publishers, key)
	m.pubLru.Remove(oldest)

	m.logger.Debug().
		Str("key", key).
		Msg("Evicted publisher client due to LRU limit")
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

// cleanupIdlePublishers removes publishers that have been idle longer than idleTTL
func (m *Manager) cleanupIdlePublishers() {
	m.pubMu.Lock()
	defer m.pubMu.Unlock()

	now := time.Now()
	var toRemove []string

	// Find publishers to remove
	for key, entry := range m.publishers {
		if now.Sub(entry.lastUsed) > m.idleTTL {
			toRemove = append(toRemove, key)
		}
	}

	// Remove idle publishers
	for _, key := range toRemove {
		entry := m.publishers[key]

		// Close the client
		if err := entry.client.Close(); err != nil {
			m.logger.Error().
				Err(err).
				Str("key", key).
				Msg("Error closing idle publisher client")
		}

		// Remove from cache
		delete(m.publishers, key)
		m.pubLru.Remove(entry.element)

		m.logger.Debug().
			Str("key", key).
			Dur("idle_time", now.Sub(entry.lastUsed)).
			Msg("Cleaned up idle publisher client")
	}
}

// Close closes all clients and stops cleanup
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
	m.pubMu.RUnlock()

	m.consMu.RLock()
	consCount := len(m.consumers)
	m.consMu.RUnlock()

	stats := map[string]any{
		"active_publishers": pubCount,
		"max_publishers":    m.maxPubs,
		"active_consumers":  consCount,
		"idle_ttl_seconds":  int(m.idleTTL.Seconds()),
	}

	return stats
}
