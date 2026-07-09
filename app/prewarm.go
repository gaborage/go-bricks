package app

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/gaborage/go-bricks/config"
	"github.com/gaborage/go-bricks/database"
	"github.com/gaborage/go-bricks/logger"
	"github.com/gaborage/go-bricks/messaging"
)

// ConnectionPreWarmer handles pre-warming of database and messaging connections
// for improved startup performance and health checking.
type ConnectionPreWarmer struct {
	logger           logger.Logger
	dbManager        *database.DbManager
	messagingManager *messaging.Manager
}

// NewConnectionPreWarmer creates a new connection pre-warmer.
func NewConnectionPreWarmer(
	log logger.Logger,
	dbManager *database.DbManager,
	messagingManager *messaging.Manager,
) *ConnectionPreWarmer {
	return &ConnectionPreWarmer{
		logger:           log,
		dbManager:        dbManager,
		messagingManager: messagingManager,
	}
}

// preWarmReadinessTimeout bounds how long PreWarmMessaging waits for a freshly
// created publisher to report IsReady() before giving up and logging a WARN.
// Mirrors messaging.WithReadyTimeout's own 5s default (see
// messaging/amqp_client.go) — pre-warm and the first real publish converge on
// the same "how long is reasonable to wait for a cold client" budget.
const preWarmReadinessTimeout = 5 * time.Second

// preWarmReadinessPollInterval mirrors messaging's unexported
// readinessCheckInterval (see messaging/registry.go) so both readiness-wait
// call sites share one poll cadence, without exporting an internal messaging
// constant just for this.
const preWarmReadinessPollInterval = 100 * time.Millisecond

// PreWarmSingleTenant pre-warms connections for single-tenant deployments.
// It establishes database connections and messaging consumers/publishers upfront.
// Errors are logged as warnings and don't cause startup failure.
func (w *ConnectionPreWarmer) PreWarmSingleTenant(
	ctx context.Context,
	declarations *messaging.Declarations,
) error {
	var errs []error

	errs = w.attemptDatabasePreWarm(ctx, errs)
	errs = w.attemptMessagingPreWarm(ctx, declarations, errs)

	// Return combined errors but don't fail startup
	if len(errs) > 0 {
		return fmt.Errorf("pre-warming issues (non-fatal): %w", errors.Join(errs...))
	}

	return nil
}

// attemptDatabasePreWarm attempts to pre-warm database connection
func (w *ConnectionPreWarmer) attemptDatabasePreWarm(ctx context.Context, errs []error) []error {
	if w.dbManager == nil {
		w.logger.Debug().Msg("Skipping single-tenant database pre-warming: manager unavailable")
		return errs
	}

	// Pre-warm database connection
	if err := w.PreWarmDatabase(ctx, ""); err != nil {
		// Check if error is due to database not being configured
		if config.IsNotConfigured(err) {
			w.logger.Debug().Msg("Skipping single-tenant database pre-warming: not configured")
		} else {
			w.logger.Warn().Err(err).Msg("Failed to pre-warm single-tenant database connection")
			errs = append(errs, fmt.Errorf("database pre-warming failed: %w", err))
		}
	} else {
		w.logger.Info().Msg("Pre-warmed single-tenant database connection")
	}

	return errs
}

// attemptMessagingPreWarm attempts to pre-warm messaging components
func (w *ConnectionPreWarmer) attemptMessagingPreWarm(
	ctx context.Context,
	declarations *messaging.Declarations,
	errs []error,
) []error {
	if w.messagingManager == nil {
		w.logger.Debug().Msg("Skipping single-tenant messaging pre-warming: manager unavailable")
		return errs
	}

	// Pre-warm messaging components
	if err := w.PreWarmMessaging(ctx, "", declarations); err != nil {
		// Check if error is due to messaging not being configured
		if config.IsNotConfigured(err) {
			w.logger.Debug().Msg("Skipping single-tenant messaging pre-warming: not configured")
		} else {
			w.logger.Warn().Err(err).Msg("Failed to pre-warm single-tenant messaging")
			errs = append(errs, fmt.Errorf("messaging pre-warming failed: %w", err))
		}
	} else {
		w.logger.Info().Msg("Pre-warmed single-tenant messaging")
	}

	return errs
}

// PreWarmDatabase attempts to establish a database connection for the given key.
// Returns error but caller determines if it's fatal.
func (w *ConnectionPreWarmer) PreWarmDatabase(ctx context.Context, key string) error {
	if w.dbManager == nil {
		return fmt.Errorf("database manager not available")
	}

	_, release, err := w.dbManager.Get(ctx, key)
	if err != nil {
		return err
	}
	release() // pre-warm only verifies connectivity; release the lease immediately
	return nil
}

// PreWarmMessaging attempts to establish messaging components for the given key.
// This includes ensuring consumers are set up and getting a publisher.
// Returns error but caller determines if it's fatal.
func (w *ConnectionPreWarmer) PreWarmMessaging(
	ctx context.Context,
	key string,
	declarations *messaging.Declarations,
) error {
	if w.messagingManager == nil {
		return fmt.Errorf("messaging manager not available")
	}

	// Ensure consumers are set up
	if declarations != nil {
		if err := w.messagingManager.EnsureConsumers(ctx, key, declarations); err != nil {
			return fmt.Errorf("failed to ensure consumers: %w", err)
		}
		w.logger.Info().Msg("Ensured messaging consumers")
	}

	// Pre-warm publisher
	client, release, err := w.messagingManager.Publisher(ctx, key)
	if err != nil {
		return fmt.Errorf("failed to get publisher: %w", err)
	}
	defer release() // pre-warm only verifies connectivity; release the lease when done

	if w.awaitPublisherReady(ctx, client) {
		w.logger.Info().Msg("Pre-warmed messaging publisher")
	} else {
		// Non-fatal: PublishToExchange's own readytimeout pre-flight (see
		// messaging/amqp_client.go) will still absorb a slow first real publish.
		w.logger.Warn().Msg("Messaging publisher not ready within pre-warm window; continuing startup")
	}

	return nil
}

// awaitPublisherReady polls client.IsReady() until it returns true, the
// bounded preWarmReadinessTimeout elapses, or ctx is canceled — whichever
// comes first. It never fails startup: PreWarmMessaging logs a WARN and
// continues if the client isn't ready in time.
func (w *ConnectionPreWarmer) awaitPublisherReady(ctx context.Context, client messaging.AMQPClient) bool {
	if client.IsReady() {
		return true
	}

	timeout := time.NewTimer(preWarmReadinessTimeout)
	defer timeout.Stop()
	ticker := time.NewTicker(preWarmReadinessPollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return false
		case <-timeout.C:
			return false
		case <-ticker.C:
			if client.IsReady() {
				return true
			}
		}
	}
}

// IsAvailable returns true if both database and messaging managers are available.
func (w *ConnectionPreWarmer) IsAvailable() bool {
	return w.dbManager != nil || w.messagingManager != nil
}

// LogAvailability logs which components are available for pre-warming.
func (w *ConnectionPreWarmer) LogAvailability() {
	if w.dbManager == nil {
		w.logger.Debug().Msg("Database manager not available for pre-warming")
	} else {
		w.logger.Debug().Msg("Database manager available for pre-warming")
	}

	if w.messagingManager == nil {
		w.logger.Debug().Msg("Messaging manager not available for pre-warming")
	} else {
		w.logger.Debug().Msg("Messaging manager available for pre-warming")
	}
}
