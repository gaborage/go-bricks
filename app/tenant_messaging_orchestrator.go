package app

import (
	"context"
	"fmt"

	"github.com/gaborage/go-bricks/logger"
	"github.com/gaborage/go-bricks/messaging"
	"github.com/gaborage/go-bricks/multitenant"
)

// TenantMessagingOrchestrator coordinates multi-tenant messaging setup and access.
type TenantMessagingOrchestrator interface {
	CaptureDeclarations() (*multitenant.MessagingDeclarations, error)
	PublisherForTenant(ctx context.Context, tenantID string, declarations *multitenant.MessagingDeclarations) (messaging.AMQPClient, error)
}

type tenantMessagingOrchestrator struct {
	registry *ModuleRegistry
	manager  *multitenant.TenantMessagingManager
	log      logger.Logger
}

func newTenantMessagingOrchestrator(registry *ModuleRegistry, manager *multitenant.TenantMessagingManager, log logger.Logger) TenantMessagingOrchestrator {
	if registry == nil || manager == nil {
		return nil
	}
	return &tenantMessagingOrchestrator{registry: registry, manager: manager, log: log}
}

func (o *tenantMessagingOrchestrator) CaptureDeclarations() (*multitenant.MessagingDeclarations, error) {
	if o.manager == nil {
		return nil, fmt.Errorf("messaging manager not configured")
	}

	o.log.Info().Msg("Capturing messaging declarations for multi-tenant mode")

	recordingClient := multitenant.NewRecordingAMQPClient()
	recordingRegistry := messaging.NewRegistry(recordingClient, o.log)

	originalRegistry := o.registry.messagingRegistry
	o.registry.messagingRegistry = recordingRegistry
	defer func() {
		o.registry.messagingRegistry = originalRegistry
	}()

	for _, module := range o.registry.modules {
		o.log.Debug().
			Str("module", module.Name()).
			Msg("Capturing messaging declarations from module")
		module.RegisterMessaging(recordingRegistry)
	}

	declarations := multitenant.NewMessagingDeclarations()
	declarations.CaptureFromRegistry(recordingRegistry)

	if err := declarations.Validate(); err != nil {
		return nil, fmt.Errorf("captured messaging declarations are invalid: %w", err)
	}

	declarations.LogSummary(o.log)

	return declarations, nil
}

func (o *tenantMessagingOrchestrator) PublisherForTenant(ctx context.Context, tenantID string, declarations *multitenant.MessagingDeclarations) (messaging.AMQPClient, error) {
	if o.manager == nil {
		return nil, fmt.Errorf("messaging not configured in multi-tenant mode")
	}
	if tenantID == "" {
		return nil, fmt.Errorf("tenant id is required")
	}

	if declarations != nil {
		if err := o.manager.EnsureConsumers(ctx, tenantID, declarations); err != nil {
			return nil, fmt.Errorf("failed to ensure consumers for tenant %s: %w", tenantID, err)
		}
	}

	return o.manager.GetPublisher(ctx, tenantID)
}
