package multitenant

import (
	"context"
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
	provider  TenantConfigProvider
	cache     *TenantConfigCache
	logger    logger.Logger
	cfg       connectionConfig
	mu        sync.RWMutex
	resources map[string]*tenantResource
	sfg       singleflight.Group
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
	m.mu.Lock()
	if resource, exists := m.resources[tenantID]; exists {
		resource.lastUsed = time.Now()
		conn := resource.conn
		m.mu.Unlock()
		return conn, nil
	}
	m.mu.Unlock()

	// Use singleflight to prevent concurrent initialization
	result, err, _ := m.sfg.Do(tenantID, func() (interface{}, error) {
		return m.initializeTenantConnection(ctx, tenantID)
	})

	if err != nil {
		return nil, fmt.Errorf("failed to initialize tenant connection: %w", err)
	}

	return result.(database.Interface), nil
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
	m.mu.Lock()
	defer m.mu.Unlock()

	now := time.Now()
	var toEvict []string

	for tenantID, resource := range m.resources {
		if now.Sub(resource.lastUsed) > m.cfg.idleTTL {
			toEvict = append(toEvict, tenantID)
		}
	}

	for _, tenantID := range toEvict {
		resource := m.resources[tenantID]
		delete(m.resources, tenantID)

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
		}(resource.conn, tenantID)
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
	m.mu.Lock()
	defer m.mu.Unlock()

	var errs []error
	for tenantID, resource := range m.resources {
		if err := resource.conn.Close(); err != nil {
			errs = append(errs, fmt.Errorf("failed to close connection for tenant %s: %w", tenantID, err))
		}
	}

	// Clear the map
	m.resources = make(map[string]*tenantResource)

	if len(errs) > 0 {
		return fmt.Errorf("errors closing tenant connections: %v", errs)
	}

	return nil
}

// WithMaxActiveTenants sets the max number of cached tenant connections.
func WithMaxActiveTenants(maxTenants int) ConnectionOption {
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
