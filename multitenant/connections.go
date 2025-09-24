package multitenant

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"

	"github.com/gaborage/go-bricks/database"
	"github.com/gaborage/go-bricks/logger"
)

// ConnectionOption configures the tenant connection manager.
type ConnectionOption func(*connectionConfig)

type connectionConfig struct {
	maxActiveTenants int
	idleTTL          time.Duration
}

// TenantConnectionManager manages tenant-specific database connections.
type TenantConnectionManager struct {
	provider      TenantConfigProvider
	cache         *TenantConfigCache
	logger        logger.Logger
	cfg           connectionConfig
	mu            sync.RWMutex
	resources     map[string]*tenantResource
	sfg           singleflight.Group
	cleanupTicker *time.Ticker
	cleanupStop   chan bool
}

type tenantResource struct {
	conn     database.Interface
	lastUsed time.Time
}

// NewTenantConnectionManager creates a new manager with the provided cache and options.
func NewTenantConnectionManager(provider TenantConfigProvider, cache *TenantConfigCache, log logger.Logger, opts ...ConnectionOption) *TenantConnectionManager {
	cfg := connectionConfig{
		maxActiveTenants: 100,
		idleTTL:          15 * time.Minute,
	}
	for _, opt := range opts {
		opt(&cfg)
	}
	if cache == nil {
		cache = NewTenantConfigCache(provider)
	}
	return &TenantConnectionManager{
		provider:  provider,
		cache:     cache,
		logger:    log,
		cfg:       cfg,
		resources: make(map[string]*tenantResource),
	}
}

// GetDatabase returns a tenant-specific database interface.
func (m *TenantConnectionManager) GetDatabase(ctx context.Context, tenantID string) (database.Interface, error) {
	if tenantID == "" {
		return nil, fmt.Errorf("tenant id is required")
	}

	// Check for existing connection
	m.mu.RLock()
	resource, exists := m.resources[tenantID]
	m.mu.RUnlock()
	if exists {
		// Update lastUsed under write lock
		m.mu.Lock()
		if resource2, ok := m.resources[tenantID]; ok {
			resource2.lastUsed = time.Now()
			resource = resource2
		}
		m.mu.Unlock()
		return resource.conn, nil
	}

	// Use singleflight to prevent concurrent initialization
	result, err, _ := m.sfg.Do(tenantID, func() (interface{}, error) {
		return m.initializeTenantConnection(ctx, tenantID)
	})

	if err != nil {
		return nil, fmt.Errorf("failed to initialize tenant connection: %w", err)
	}

	db, ok := result.(database.Interface)
	if !ok || db == nil {
		return nil, fmt.Errorf("invalid database connection type for tenant %s", tenantID)
	}

	return db, nil
}

// initializeTenantConnection creates a new database connection for the tenant
func (m *TenantConnectionManager) initializeTenantConnection(ctx context.Context, tenantID string) (database.Interface, error) {
	// Get tenant database configuration
	dbConfig, err := m.cache.GetDatabase(ctx, tenantID)
	if err != nil {
		return nil, fmt.Errorf("failed to get database config for tenant %s: %w", tenantID, err)
	}

	// Create database connection using go-bricks database package
	conn, err := database.NewConnection(dbConfig, m.logger)
	if err != nil {
		return nil, fmt.Errorf("failed to create database connection for tenant %s: %w", tenantID, err)
	}

	// Test the connection
	if err := conn.Health(ctx); err != nil {
		conn.Close()
		return nil, fmt.Errorf("tenant database health check failed for %s: %w", tenantID, err)
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	// Evict old connections if we've reached the limit
	if len(m.resources) >= m.cfg.maxActiveTenants {
		m.evictOldestLocked()
	}

	// Store the connection
	m.resources[tenantID] = &tenantResource{
		conn:     conn,
		lastUsed: time.Now(),
	}

	m.logger.WithFields(map[string]interface{}{
		"tenant_id": tenantID,
		"db_type":   dbConfig.Type,
		"db_host":   dbConfig.Host,
	}).Info().Msg("Initialized database connection for tenant")

	return conn, nil
}

// evictOldestLocked removes the least recently used connection (must be called with lock held)
func (m *TenantConnectionManager) evictOldestLocked() {
	var oldestTenant string
	var oldestTime time.Time

	for tenantID, resource := range m.resources {
		if oldestTenant == "" || resource.lastUsed.Before(oldestTime) {
			oldestTenant = tenantID
			oldestTime = resource.lastUsed
		}
	}

	if oldestTenant != "" {
		resource := m.resources[oldestTenant]
		delete(m.resources, oldestTenant)

		// Close connection in background to avoid blocking
		go func(conn database.Interface, tenant string) {
			if err := conn.Close(); err != nil {
				m.logger.WithFields(map[string]interface{}{
					"tenant_id": tenant,
					"error":     err.Error(),
				}).Error().Msg("Failed to close evicted tenant database connection")
			} else {
				m.logger.WithFields(map[string]interface{}{
					"tenant_id": tenant,
				}).Debug().Msg("Evicted idle tenant database connection")
			}
		}(resource.conn, oldestTenant)
	}
}

// CleanupIdleConnections removes connections that have been idle for too long
func (m *TenantConnectionManager) CleanupIdleConnections() {
	now := time.Now()
	var toEvict []string

	m.mu.RLock()
	for tenantID, resource := range m.resources {
		if now.Sub(resource.lastUsed) > m.cfg.idleTTL {
			toEvict = append(toEvict, tenantID)
		}
	}
	m.mu.RUnlock()

	var toClose []struct {
		tenantID string
		conn     database.Interface
	}
	m.mu.Lock()
	for _, tenantID := range toEvict {
		if resource, ok := m.resources[tenantID]; ok {
			// Re-check liveness under write lock to avoid race conditions
			if resource.lastUsed.Add(m.cfg.idleTTL).After(time.Now()) {
				continue
			}
			toClose = append(toClose, struct {
				tenantID string
				conn     database.Interface
			}{
				tenantID: tenantID,
				conn:     resource.conn,
			})
			delete(m.resources, tenantID)
		}
	}
	m.mu.Unlock()

	for _, item := range toClose {
		// Close connection in background
		go func(conn database.Interface, tenant string) {
			if err := conn.Close(); err != nil {
				m.logger.WithFields(map[string]interface{}{
					"tenant_id": tenant,
					"error":     err.Error(),
				}).Error().Msg("Failed to close idle tenant database connection")
			} else {
				m.logger.WithFields(map[string]interface{}{
					"tenant_id": tenant,
				}).Debug().Msg("Cleaned up idle tenant database connection")
			}
		}(item.conn, item.tenantID)
	}
}

// RefreshTenant closes and recreates the connection for a specific tenant
func (m *TenantConnectionManager) RefreshTenant(_ context.Context, tenantID string) error {
	m.mu.Lock()
	resource, exists := m.resources[tenantID]
	if exists {
		delete(m.resources, tenantID)
	}
	m.mu.Unlock()

	// Close old connection if it exists
	if exists {
		if err := resource.conn.Close(); err != nil {
			m.logger.WithFields(map[string]interface{}{
				"tenant_id": tenantID,
				"error":     err.Error(),
			}).Error().Msg("Failed to close tenant connection during refresh")
		}
	}

	// Force a new connection on next access
	return nil
}

// Close closes all tenant connections
func (m *TenantConnectionManager) Close() error {
	// Stop cleanup first
	m.StopCleanup()

	m.mu.Lock()
	res := m.resources
	m.resources = make(map[string]*tenantResource)
	m.mu.Unlock()

	var errs []error
	for tenantID, resource := range res {
		if err := resource.conn.Close(); err != nil {
			errs = append(errs, fmt.Errorf("failed to close connection for tenant %s: %w", tenantID, err))
		}
	}

	if len(errs) > 0 {
		return errors.Join(errs...)
	}
	return nil
}

// StartCleanup starts the periodic cleanup of idle connections.
// If interval is 0 or negative, cleanup is disabled.
func (m *TenantConnectionManager) StartCleanup(interval time.Duration) {
	if interval <= 0 {
		m.logger.Debug().Msg("Connection cleanup disabled (interval <= 0)")
		return
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	// Stop any existing cleanup
	if m.cleanupTicker != nil {
		m.cleanupTicker.Stop()
		if m.cleanupStop != nil {
			close(m.cleanupStop)
		}
	}

	m.cleanupTicker = time.NewTicker(interval)
	m.cleanupStop = make(chan bool, 1)

	go func(ticker *time.Ticker, stop chan bool) {
		defer func() {
			if r := recover(); r != nil {
				m.logger.Error().
					Interface("panic", r).
					Msg("Panic in connection cleanup goroutine")
			}
		}()

		for {
			select {
			case <-ticker.C:
				m.CleanupIdleConnections()
			case <-stop:
				m.logger.Debug().Msg("Connection cleanup stopped")
				return
			}
		}
	}(m.cleanupTicker, m.cleanupStop)

	m.logger.Info().
		Dur("interval", interval).
		Msg("Started periodic connection cleanup")
}

// StopCleanup stops the periodic cleanup of idle connections.
func (m *TenantConnectionManager) StopCleanup() {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.cleanupTicker != nil {
		m.cleanupTicker.Stop()
		m.cleanupTicker = nil
	}

	if m.cleanupStop != nil {
		select {
		case m.cleanupStop <- true:
		default:
			// Channel already closed or full
		}
		m.cleanupStop = nil
	}

	m.logger.Debug().Msg("Connection cleanup stopped")
}

// WithMaxTenants sets the max number of cached tenant connections.
func WithMaxTenants(maxTenants int) ConnectionOption {
	return func(cfg *connectionConfig) {
		if maxTenants > 0 {
			cfg.maxActiveTenants = maxTenants
		}
	}
}

// WithIdleTTL sets the idle time-to-live before eviction.
func WithIdleTTL(ttl time.Duration) ConnectionOption {
	return func(cfg *connectionConfig) {
		if ttl > 0 {
			cfg.idleTTL = ttl
		}
	}
}
