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
	// readinessTimeout bounds PreWarmMessaging's wait for a freshly created
	// publisher to report IsReady(). Threaded from the operator's
	// messaging.reconnect.readytimeout by ConfigureRuntimeHelpers (see
	// app_builder.go); zero falls back to defaultPreWarmReadinessTimeout.
	// Unexported and set post-construction so NewConnectionPreWarmer's shipped
	// signature stays byte-identical (apidiff gate).
	readinessTimeout time.Duration
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

// defaultPreWarmReadinessTimeout is the fallback readiness budget when no
// operator value was threaded into readinessTimeout (zero, e.g. direct
// construction in tests). Mirrors config's defaultReadyTimeout for
// messaging.reconnect.readytimeout (see config/validation.go) — pre-warm and
// the first real publish converge on the same "how long is reasonable to wait
// for a cold client" budget.
const defaultPreWarmReadinessTimeout = 5 * time.Second

// preWarmReadinessPollInterval mirrors messaging's unexported
// readinessCheckInterval (see messaging/constants.go) so both readiness-wait
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

	if declarations != nil {
		if err := w.messagingManager.EnsureConsumers(ctx, key, declarations); err != nil {
			return fmt.Errorf("failed to ensure consumers: %w", err)
		}
		w.logger.Info().Msg("Ensured messaging consumers")
	}

	client, release, err := w.messagingManager.Publisher(ctx, key)
	if err != nil {
		return fmt.Errorf("failed to get publisher: %w", err)
	}
	defer release() // pre-warm only verifies connectivity; release the lease when done

	switch w.awaitPublisherReady(ctx, client) {
	case preWarmReady:
		w.logger.Info().Msg("Pre-warmed messaging publisher")
	case preWarmCanceled:
		// Startup abort / shutdown in flight — propagate the cancellation
		// instead of mislabeling it as a broker-readiness problem.
		return fmt.Errorf("publisher readiness wait canceled: %w", ctx.Err())
	default: // preWarmNotReadyInTime
		// Non-fatal: PublishToExchange's own readytimeout pre-flight (see
		// messaging/amqp_client.go) will still absorb a slow first real publish.
		w.logger.Warn().
			Dur("ready_timeout", w.publisherReadinessTimeout()).
			Msg("Messaging publisher not ready within pre-warm window; continuing startup")
	}

	return nil
}

// preWarmReadyOutcome reports why awaitPublisherReady's bounded wait ended.
// Mirrors messaging's unexported readyWaitOutcome (see messaging/amqp_client.go)
// minus the shutdown-channel case pre-warm has no equivalent for, so ctx
// cancellation is never conflated with a readiness timeout.
type preWarmReadyOutcome int

const (
	preWarmReady preWarmReadyOutcome = iota
	preWarmNotReadyInTime
	preWarmCanceled
)

// publisherReadinessTimeout resolves the readiness budget: the operator's
// messaging.reconnect.readytimeout when threaded (positive), the
// defaultPreWarmReadinessTimeout fallback otherwise.
func (w *ConnectionPreWarmer) publisherReadinessTimeout() time.Duration {
	if w.readinessTimeout > 0 {
		return w.readinessTimeout
	}
	return defaultPreWarmReadinessTimeout
}

// awaitPublisherReady polls client.IsReady() until it reports ready, the
// bounded publisherReadinessTimeout elapses, or ctx is canceled — whichever
// comes first — and reports which of the three ended the wait. A readiness
// timeout never fails startup (PreWarmMessaging logs a WARN and continues);
// cancellation propagates so shutdown isn't mislabeled as not-ready.
func (w *ConnectionPreWarmer) awaitPublisherReady(ctx context.Context, client messaging.AMQPClient) preWarmReadyOutcome {
	if client.IsReady() {
		return preWarmReady
	}

	timeout := time.NewTimer(w.publisherReadinessTimeout())
	defer timeout.Stop()
	ticker := time.NewTicker(preWarmReadinessPollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return preWarmCanceled
		case <-timeout.C:
			return preWarmNotReadyInTime
		case <-ticker.C:
			if client.IsReady() {
				return preWarmReady
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
