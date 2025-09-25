package multitenant

import (
	"context"
	"fmt"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"

	"github.com/gaborage/go-bricks/logger"
	"github.com/gaborage/go-bricks/messaging"
)

// AMQPClientFactory creates AMQP clients for tenants
type AMQPClientFactory interface {
	CreateClient(url string, log logger.Logger) messaging.AMQPClient
}

// DefaultAMQPClientFactory creates real AMQP clients
type DefaultAMQPClientFactory struct{}

func (f *DefaultAMQPClientFactory) CreateClient(url string, log logger.Logger) messaging.AMQPClient {
	return messaging.NewAMQPClient(url, log)
}

// TenantMessagingManager manages per-tenant AMQP clients and registries.
// - Consumers are long-lived (no eviction) to avoid missing inbound messages.
// - Publisher clients are cached with idle eviction.
type TenantMessagingManager struct {
	provider      TenantConfigProvider
	cache         *TenantConfigCache
	log           logger.Logger
	clientFactory AMQPClientFactory

	// publisher cache (evictable)
	pubMu      sync.RWMutex
	publishers map[string]*publisherEntry

	// consumer state (long-lived)
	consMu    sync.RWMutex
	consumers map[string]*consumerEntry

	sfg singleflight.Group

	idleTTL             time.Duration
	maxActivePublishers int

	// Cleanup timer management
	cleanupTimer  *time.Timer
	cleanupStopCh chan struct{}
	cleanupMu     sync.Mutex
}

type publisherEntry struct {
	client   messaging.AMQPClient
	wrapper  *TenantAMQPClient
	lastUsed time.Time
}

type consumerEntry struct {
	client   messaging.AMQPClient
	registry *messaging.Registry
	running  bool
}

func NewTenantMessagingManager(provider TenantConfigProvider, cache *TenantConfigCache, log logger.Logger, idleTTL time.Duration, maxActive int) *TenantMessagingManager {
	return NewTenantMessagingManagerWithFactory(provider, cache, log, idleTTL, maxActive, &DefaultAMQPClientFactory{})
}

func NewTenantMessagingManagerWithFactory(provider TenantConfigProvider, cache *TenantConfigCache, log logger.Logger, idleTTL time.Duration, maxActive int, factory AMQPClientFactory) *TenantMessagingManager {
	if cache == nil {
		cache = NewTenantConfigCache(provider)
	}
	return &TenantMessagingManager{
		provider:            provider,
		cache:               cache,
		log:                 log,
		clientFactory:       factory,
		publishers:          make(map[string]*publisherEntry),
		consumers:           make(map[string]*consumerEntry),
		idleTTL:             idleTTL,
		maxActivePublishers: maxActive,
	}
}

// GetPublisher returns a tenant-aware AMQP client wrapper for publishing.
func (m *TenantMessagingManager) GetPublisher(ctx context.Context, tenantID string) (messaging.AMQPClient, error) {
	if tenantID == "" {
		return nil, fmt.Errorf("tenant id is required")
	}

	// Fast path: cached
	m.pubMu.Lock()
	if entry, ok := m.publishers[tenantID]; ok {
		entry.lastUsed = time.Now()
		wrapper := entry.wrapper
		m.pubMu.Unlock()
		return wrapper, nil
	}
	m.pubMu.Unlock()

	// Initialize with singleflight
	v, err, _ := m.sfg.Do("pub:"+tenantID, func() (interface{}, error) {
		return m.initPublisher(ctx, tenantID)
	})
	if err != nil {
		return nil, err
	}
	return v.(messaging.AMQPClient), nil
}

func (m *TenantMessagingManager) initPublisher(ctx context.Context, tenantID string) (messaging.AMQPClient, error) {
	// Recheck cache inside init
	m.pubMu.Lock()
	if entry, ok := m.publishers[tenantID]; ok {
		entry.lastUsed = time.Now()
		wrapper := entry.wrapper
		m.pubMu.Unlock()
		return wrapper, nil
	}
	m.pubMu.Unlock()

	// Resolve tenant messaging URL
	cfg, err := m.cache.GetMessaging(ctx, tenantID)
	if err != nil {
		return nil, fmt.Errorf("get messaging config: %w", err)
	}

	// Create client
	base := m.clientFactory.CreateClient(cfg.URL, m.log)
	wrapper := NewTenantAMQPClient(base, tenantID)

	// Evict if needed
	m.pubMu.Lock()
	if len(m.publishers) >= m.maxActivePublishers {
		m.evictOldestPublisherLocked()
	}
	m.publishers[tenantID] = &publisherEntry{client: base, wrapper: wrapper, lastUsed: time.Now()}
	m.pubMu.Unlock()

	return wrapper, nil
}

func (m *TenantMessagingManager) evictOldestPublisherLocked() {
	var oldest string
	var oldestTime time.Time
	for k, v := range m.publishers {
		if oldest == "" || v.lastUsed.Before(oldestTime) {
			oldest = k
			oldestTime = v.lastUsed
		}
	}
	if oldest != "" {
		entry := m.publishers[oldest]
		delete(m.publishers, oldest)
		go func(c messaging.AMQPClient, tid string) {
			_ = c.Close()
			m.log.WithFields(map[string]any{"tenant_id": tid}).Debug().Msg("Evicted tenant publisher client")
		}(entry.client, oldest)
	}
}

// EnsureConsumers creates per-tenant consumer infrastructure and starts consumers once.
func (m *TenantMessagingManager) EnsureConsumers(ctx context.Context, tenantID string, decls *Declarations) error {
	if tenantID == "" {
		return fmt.Errorf("tenant id is required")
	}

	m.consMu.Lock()
	if c, ok := m.consumers[tenantID]; ok && c.running {
		m.consMu.Unlock()
		return nil
	}
	m.consMu.Unlock()

	// Singleflight on consumer init
	_, err, _ := m.sfg.Do("cons:"+tenantID, func() (interface{}, error) {
		return nil, m.initConsumers(ctx, tenantID, decls)
	})
	return err
}

func (m *TenantMessagingManager) initConsumers(ctx context.Context, tenantID string, decls *Declarations) error {
	// Re-check if already running
	m.consMu.Lock()
	if c, ok := m.consumers[tenantID]; ok && c.running {
		m.consMu.Unlock()
		return nil
	}
	m.consMu.Unlock()

	cfg, err := m.cache.GetMessaging(ctx, tenantID)
	if err != nil {
		return fmt.Errorf("get messaging config: %w", err)
	}

	client := m.clientFactory.CreateClient(cfg.URL, m.log)
	// Build per-tenant registry
	reg := messaging.NewRegistry(client, m.log)
	// Replay declarations
	decls.Replay(ctx, reg)
	if err := reg.DeclareInfrastructure(ctx); err != nil {
		return fmt.Errorf("declare infra: %w", err)
	}
	if err := reg.StartConsumers(ctx); err != nil {
		return fmt.Errorf("start consumers: %w", err)
	}

	m.consMu.Lock()
	m.consumers[tenantID] = &consumerEntry{client: client, registry: reg, running: true}
	m.consMu.Unlock()
	return nil
}

// CleanupPublishers evicts idle publisher clients.
func (m *TenantMessagingManager) CleanupPublishers() {
	now := time.Now()
	m.pubMu.Lock()
	for tid, entry := range m.publishers {
		if now.Sub(entry.lastUsed) > m.idleTTL {
			delete(m.publishers, tid)
			go func(c messaging.AMQPClient, id string) {
				_ = c.Close()
				m.log.WithFields(map[string]any{"tenant_id": id}).Debug().Msg("Cleaned idle publisher")
			}(entry.client, tid)
		}
	}
	m.pubMu.Unlock()
}

// StartCleanup starts periodic cleanup of idle publisher clients.
func (m *TenantMessagingManager) StartCleanup(interval time.Duration) {
	m.cleanupMu.Lock()
	defer m.cleanupMu.Unlock()

	if interval <= 0 {
		m.log.Debug().Msg("Publisher cleanup disabled (interval <= 0)")
		return
	}

	// Only start if not already running (timer is nil)
	if m.cleanupTimer != nil {
		return
	}

	m.cleanupTimer = time.NewTimer(interval)
	stopCh := make(chan struct{})
	m.cleanupStopCh = stopCh

	go m.cleanupLoop(interval, m.cleanupTimer, stopCh)

	m.log.Info().
		Dur("interval", interval).
		Msg("Started periodic publisher cleanup")
}

// StopCleanup stops the periodic cleanup timer.
func (m *TenantMessagingManager) StopCleanup() {
	m.cleanupMu.Lock()
	defer m.cleanupMu.Unlock()

	// Only stop if currently running (timer is not nil)
	if m.cleanupTimer == nil {
		return
	}

	timer := m.cleanupTimer
	stopCh := m.cleanupStopCh

	timer.Stop()
	m.cleanupTimer = nil
	m.cleanupStopCh = nil

	close(stopCh)

	m.log.Info().Msg("Stopped periodic publisher cleanup")
}

// cleanupLoop runs the periodic cleanup in a separate goroutine.
func (m *TenantMessagingManager) cleanupLoop(interval time.Duration, timer *time.Timer, stopCh <-chan struct{}) {
	for {
		select {
		case <-timer.C:
			m.CleanupPublishers()

			// Reset timer for next cleanup
			// If cleanup is stopped, the goroutine will exit via the stop channel
			timer.Reset(interval)

		case <-stopCh:
			m.log.Debug().Msg("Publisher cleanup loop stopped")
			return
		}
	}
}

// Close stops cleanup and closes all publisher and consumer connections.
func (m *TenantMessagingManager) Close() error {
	m.StopCleanup()

	var errors []error

	// Close all publisher clients
	m.pubMu.Lock()
	for tid, entry := range m.publishers {
		if err := entry.client.Close(); err != nil {
			errors = append(errors, fmt.Errorf("failed to close publisher for tenant %s: %w", tid, err))
		}
	}
	m.publishers = make(map[string]*publisherEntry)
	m.pubMu.Unlock()

	// Close all consumer clients
	m.consMu.Lock()
	for tid, entry := range m.consumers {
		if err := entry.client.Close(); err != nil {
			errors = append(errors, fmt.Errorf("failed to close consumer for tenant %s: %w", tid, err))
		}
	}
	m.consumers = make(map[string]*consumerEntry)
	m.consMu.Unlock()

	if len(errors) > 0 {
		return fmt.Errorf("failed to close messaging manager: %v", errors)
	}

	m.log.Info().Msg("Tenant messaging manager closed successfully")
	return nil
}
