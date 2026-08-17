package app

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/gaborage/go-bricks/config"
	"github.com/gaborage/go-bricks/messaging"
)

// defaultPreWarmReadinessTimeout is the fallback readiness budget when
// messaging.reconnect.readytimeout carries no positive value (a directly
// constructed App in tests, or a Builder assembled without WithConfig). Mirrors
// config's defaultReadyTimeout (see config/validation.go) — pre-warm and the
// first real publish converge on the same "how long is reasonable to wait for a
// cold client" budget.
const defaultPreWarmReadinessTimeout = 5 * time.Second

// preWarmReadinessPollInterval mirrors messaging's unexported
// readinessCheckInterval (see messaging/constants.go) so both readiness-wait
// call sites share one poll cadence, without exporting an internal messaging
// constant just for this.
const preWarmReadinessPollInterval = 100 * time.Millisecond

// preWarmSingleTenant pre-warms connections for single-tenant deployments.
// It establishes database connections and messaging consumers/publishers upfront.
// Errors are logged as warnings and don't cause startup failure.
func (a *App) preWarmSingleTenant(ctx context.Context, decls *messaging.Declarations) error {
	var errs []error

	errs = a.attemptDatabasePreWarm(ctx, errs)
	errs = a.attemptMessagingPreWarm(ctx, decls, errs)

	// Return combined errors but don't fail startup
	if len(errs) > 0 {
		return fmt.Errorf("pre-warming issues (non-fatal): %w", errors.Join(errs...))
	}

	return nil
}

// attemptDatabasePreWarm attempts to pre-warm the database connection.
func (a *App) attemptDatabasePreWarm(ctx context.Context, errs []error) []error {
	if a.dbManager == nil {
		a.logger.Debug().Msg("Skipping single-tenant database pre-warming: manager unavailable")
		return errs
	}

	if err := a.preWarmDatabase(ctx); err != nil {
		// Check if error is due to database not being configured
		if config.IsNotConfigured(err) {
			a.logger.Debug().Msg("Skipping single-tenant database pre-warming: not configured")
		} else {
			a.logger.Warn().Err(err).Msg("Failed to pre-warm single-tenant database connection")
			errs = append(errs, fmt.Errorf("database pre-warming failed: %w", err))
		}
	} else {
		a.logger.Info().Msg("Pre-warmed single-tenant database connection")
	}

	return errs
}

// attemptMessagingPreWarm attempts to pre-warm messaging components.
func (a *App) attemptMessagingPreWarm(ctx context.Context, decls *messaging.Declarations, errs []error) []error {
	if a.messagingManager == nil {
		a.logger.Debug().Msg("Skipping single-tenant messaging pre-warming: manager unavailable")
		return errs
	}

	if err := a.preWarmMessaging(ctx, decls); err != nil {
		// Check if error is due to messaging not being configured
		if config.IsNotConfigured(err) {
			a.logger.Debug().Msg("Skipping single-tenant messaging pre-warming: not configured")
		} else {
			a.logger.Warn().Err(err).Msg("Failed to pre-warm single-tenant messaging")
			errs = append(errs, fmt.Errorf("messaging pre-warming failed: %w", err))
		}
	} else {
		a.logger.Info().Msg("Pre-warmed single-tenant messaging")
	}

	return errs
}

// preWarmDatabase leases the fixed "" key to verify connectivity and releases it
// immediately. attemptDatabasePreWarm holds the manager nil check.
func (a *App) preWarmDatabase(ctx context.Context) error {
	_, release, err := a.dbManager.Get(ctx, "")
	if err != nil {
		return err
	}
	release() // pre-warm only verifies connectivity; release the lease immediately
	return nil
}

// preWarmMessaging ensures consumers for the fixed "" key and waits, bounded, for the
// publisher to report ready. attemptMessagingPreWarm holds the manager nil check.
func (a *App) preWarmMessaging(ctx context.Context, decls *messaging.Declarations) error {
	if decls != nil {
		if err := a.messagingManager.EnsureConsumers(ctx, "", decls); err != nil {
			return fmt.Errorf("failed to ensure consumers: %w", err)
		}
		a.logger.Info().Msg("Ensured messaging consumers")
	}

	client, release, err := a.messagingManager.Publisher(ctx, "")
	if err != nil {
		return fmt.Errorf("failed to get publisher: %w", err)
	}
	defer release() // pre-warm only verifies connectivity; release the lease when done

	switch a.awaitPublisherReady(ctx, client) {
	case preWarmReady:
		a.logger.Info().Msg("Pre-warmed messaging publisher")
	case preWarmCanceled:
		// Startup abort / shutdown in flight — propagate the cancellation
		// instead of mislabeling it as a broker-readiness problem.
		return fmt.Errorf("publisher readiness wait canceled: %w", ctx.Err())
	default: // preWarmNotReadyInTime
		// Non-fatal: PublishToExchange's own readytimeout pre-flight (see
		// messaging/amqp_client.go) will still absorb a slow first real publish.
		a.logger.Warn().
			Dur("ready_timeout", a.publisherReadinessTimeout()).
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
// messaging.reconnect.readytimeout when positive, the
// defaultPreWarmReadinessTimeout fallback otherwise. Nil-guarded because a
// directly-constructed App may carry no config.
func (a *App) publisherReadinessTimeout() time.Duration {
	if a.cfg != nil && a.cfg.Messaging.Reconnect.ReadyTimeout > 0 {
		return a.cfg.Messaging.Reconnect.ReadyTimeout
	}
	return defaultPreWarmReadinessTimeout
}

// awaitPublisherReady polls client.IsReady() until it reports ready, the
// bounded publisherReadinessTimeout elapses, or ctx is canceled — whichever
// comes first — and reports which of the three ended the wait. A readiness
// timeout never fails startup (preWarmMessaging logs a WARN and continues);
// cancellation propagates so shutdown isn't mislabeled as not-ready.
func (a *App) awaitPublisherReady(ctx context.Context, client messaging.AMQPClient) preWarmReadyOutcome {
	if client.IsReady() {
		return preWarmReady
	}

	timeout := time.NewTimer(a.publisherReadinessTimeout())
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
