package app

import (
	"context"
	"fmt"

	"github.com/gaborage/go-bricks/database"
	"github.com/gaborage/go-bricks/messaging"
	"github.com/gaborage/go-bricks/multitenant"
)

// ResourceProvider abstracts database and messaging access with support for
// both single-tenant and multi-tenant deployment modes.
type ResourceProvider interface {
	GetDB(ctx context.Context) (database.Interface, error)
	GetMessaging(ctx context.Context) (messaging.AMQPClient, error)
}

// SingleTenantResourceProvider provides database and messaging resources
// for single-tenant deployments using a fixed empty key.
type SingleTenantResourceProvider struct {
	dbManager        *database.DbManager
	messagingManager *messaging.Manager
	declarations     *messaging.Declarations
}

// NewSingleTenantResourceProvider creates a resource provider for single-tenant mode.
func NewSingleTenantResourceProvider(
	dbManager *database.DbManager,
	messagingManager *messaging.Manager,
	declarations *messaging.Declarations,
) *SingleTenantResourceProvider {
	return &SingleTenantResourceProvider{
		dbManager:        dbManager,
		messagingManager: messagingManager,
		declarations:     declarations,
	}
}

// GetDB returns the database interface for single-tenant mode.
func (p *SingleTenantResourceProvider) GetDB(ctx context.Context) (database.Interface, error) {
	if p.dbManager == nil {
		return nil, fmt.Errorf("database not configured")
	}
	return p.dbManager.Get(ctx, "")
}

// GetMessaging returns the messaging client for single-tenant mode.
// It ensures consumers are initialized before returning the publisher.
func (p *SingleTenantResourceProvider) GetMessaging(ctx context.Context) (messaging.AMQPClient, error) {
	if p.messagingManager == nil {
		return nil, fmt.Errorf("messaging not configured")
	}

	// Ensure consumers are set up for single-tenant
	if p.declarations != nil {
		if err := p.messagingManager.EnsureConsumers(ctx, "", p.declarations); err != nil {
			return nil, fmt.Errorf("failed to ensure consumers: %w", err)
		}
	}

	return p.messagingManager.GetPublisher(ctx, "")
}

// SetDeclarations updates the declaration store used for ensuring consumers.
func (p *SingleTenantResourceProvider) SetDeclarations(declarations *messaging.Declarations) {
	p.declarations = declarations
}

// MultiTenantResourceProvider provides database and messaging resources
// for multi-tenant deployments using tenant ID from context.
type MultiTenantResourceProvider struct {
	dbManager        *database.DbManager
	messagingManager *messaging.Manager
	declarations     *messaging.Declarations
}

// NewMultiTenantResourceProvider creates a resource provider for multi-tenant mode.
func NewMultiTenantResourceProvider(
	dbManager *database.DbManager,
	messagingManager *messaging.Manager,
	declarations *messaging.Declarations,
) *MultiTenantResourceProvider {
	return &MultiTenantResourceProvider{
		dbManager:        dbManager,
		messagingManager: messagingManager,
		declarations:     declarations,
	}
}

// GetDB returns the database interface for the tenant specified in context.
func (p *MultiTenantResourceProvider) GetDB(ctx context.Context) (database.Interface, error) {
	if p.dbManager == nil {
		return nil, fmt.Errorf("database not configured")
	}

	tenantID, ok := multitenant.GetTenant(ctx)
	if !ok {
		return nil, ErrNoTenantInContext
	}

	return p.dbManager.Get(ctx, tenantID)
}

// GetMessaging returns the messaging client for the tenant specified in context.
// It ensures tenant-specific consumers are initialized before returning the publisher.
func (p *MultiTenantResourceProvider) GetMessaging(ctx context.Context) (messaging.AMQPClient, error) {
	if p.messagingManager == nil {
		return nil, fmt.Errorf("messaging not configured")
	}

	tenantID, ok := multitenant.GetTenant(ctx)
	if !ok {
		return nil, ErrNoTenantInContext
	}

	// Ensure consumers are set up for this tenant
	if p.declarations != nil {
		if err := p.messagingManager.EnsureConsumers(ctx, tenantID, p.declarations); err != nil {
			return nil, fmt.Errorf("failed to ensure consumers for tenant %s: %w", tenantID, err)
		}
	}

	return p.messagingManager.GetPublisher(ctx, tenantID)
}

// SetDeclarations updates the declaration store used for ensuring consumers.
func (p *MultiTenantResourceProvider) SetDeclarations(declarations *messaging.Declarations) {
	p.declarations = declarations
}
