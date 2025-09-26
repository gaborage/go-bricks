package app

import (
	"context"
	"fmt"

	"github.com/gaborage/go-bricks/logger"
	"github.com/gaborage/go-bricks/messaging"
)

// MessagingInitializer handles messaging system initialization including
// declaration collection and consumer setup for different deployment modes.
type MessagingInitializer struct {
	logger      logger.Logger
	manager     *messaging.Manager
	multiTenant bool
}

// NewMessagingInitializer creates a new messaging initializer.
func NewMessagingInitializer(
	log logger.Logger,
	manager *messaging.Manager,
	multiTenant bool,
) *MessagingInitializer {
	return &MessagingInitializer{
		logger:      log,
		manager:     manager,
		multiTenant: multiTenant,
	}
}

// CollectDeclarations collects messaging declarations from all registered modules.
// This builds the unified declaration store used for all tenant registries.
func (m *MessagingInitializer) CollectDeclarations(registry *ModuleRegistry) (*messaging.Declarations, error) {
	declarations := messaging.NewDeclarations()
	if err := registry.DeclareMessaging(declarations); err != nil {
		return nil, fmt.Errorf("failed to collect messaging declarations: %w", err)
	}
	return declarations, nil
}

// SetupLazyConsumerInit modifies the resource provider's GetMessaging function
// to include lazy consumer initialization. This ensures consumers are started
// on-demand when messaging is first accessed.
func (m *MessagingInitializer) SetupLazyConsumerInit(
	provider ResourceProvider,
	declarations *messaging.Declarations,
) error {
	if m.manager == nil {
		return fmt.Errorf("messaging manager not configured")
	}

	// Check if provider is one of our known implementations
	switch p := provider.(type) {
	case *SingleTenantResourceProvider:
		return m.setupSingleTenantLazyInit(p, declarations)
	case *MultiTenantResourceProvider:
		return m.setupMultiTenantLazyInit(p, declarations)
	default:
		// For unknown provider types, we can't modify the behavior
		m.logger.Warn().Msg("Unknown resource provider type, skipping lazy consumer initialization")
		return nil
	}
}

// setupSingleTenantLazyInit configures lazy consumer initialization for single-tenant mode.
func (m *MessagingInitializer) setupSingleTenantLazyInit(
	provider *SingleTenantResourceProvider,
	declarations *messaging.Declarations,
) error {
	// Update the provider's declarations so it can ensure consumers
	provider.declarations = declarations
	return nil
}

// setupMultiTenantLazyInit configures lazy consumer initialization for multi-tenant mode.
func (m *MessagingInitializer) setupMultiTenantLazyInit(
	provider *MultiTenantResourceProvider,
	declarations *messaging.Declarations,
) error {
	// Update the provider's declarations so it can ensure consumers per tenant
	provider.declarations = declarations
	return nil
}

// PrepareRuntimeConsumers prepares consumers based on deployment mode.
// For single-tenant: starts consumers immediately.
// For multi-tenant: logs that consumers will start on-demand.
func (m *MessagingInitializer) PrepareRuntimeConsumers(
	ctx context.Context,
	declarations *messaging.Declarations,
) error {
	if m.manager == nil {
		return fmt.Errorf("messaging manager not configured")
	}

	if m.multiTenant {
		// Multi-tenant: consumers will be started on-demand per tenant
		m.logger.Info().Msg("Multi-tenant mode: consumers will be started per tenant on demand")
		return nil
	}

	// Single-tenant: pre-start consumers
	if err := m.manager.EnsureConsumers(ctx, "", declarations); err != nil {
		m.logger.Warn().Err(err).Msg("Failed to start single-tenant consumers")
		// Don't fail the app startup for messaging issues
		return nil
	}

	m.logger.Info().Msg("Single-tenant consumers started successfully")
	return nil
}

// IsAvailable returns true if the messaging manager is available.
func (m *MessagingInitializer) IsAvailable() bool {
	return m.manager != nil
}

// LogDeploymentMode logs the current deployment mode for messaging.
func (m *MessagingInitializer) LogDeploymentMode() {
	if m.multiTenant {
		m.logger.Info().Msg("Messaging initialized for multi-tenant deployment")
	} else {
		m.logger.Info().Msg("Messaging initialized for single-tenant deployment")
	}
}
