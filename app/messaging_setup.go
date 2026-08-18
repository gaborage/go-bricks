package app

import (
	"context"
	"fmt"

	"github.com/gaborage/go-bricks/messaging"
)

// prepareRuntimeConsumers starts AMQP consumers according to the deployment mode.
// No-op when no messaging manager was built or no declarations were collected.
//
// Multi-tenant: consumers start per tenant on demand, so nothing runs at startup.
//
// Single-tenant: EnsureConsumers also declares the exchanges, queues, and bindings
// publishers rely on, so it runs regardless; only the failure is graded. A service that
// declared consumers and cannot start them would serve HTTP while consuming nothing, so
// it fails fast. One that declared none — including a service with no messaging
// configured at all — keeps the historical warn-and-continue.
func (a *App) prepareRuntimeConsumers(ctx context.Context, decls *messaging.Declarations) error {
	if a.messagingManager == nil || decls == nil {
		return nil
	}

	if a.cfg.Multitenant.Enabled {
		a.logger.Info().Msg("Multi-tenant mode: consumers will be started per tenant on demand")
		return nil
	}

	if err := a.messagingManager.EnsureConsumers(ctx, "", decls); err != nil {
		if len(decls.Consumers()) > 0 {
			return fmt.Errorf("failed to start single-tenant consumers: %w", err)
		}
		a.logger.Warn().Err(err).Msg("Failed to start single-tenant consumers")
		return nil
	}

	a.logger.Info().Msg("Single-tenant consumers started successfully")
	return nil
}
