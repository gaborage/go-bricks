package app

import (
	"context"
	"fmt"
	"time"

	"github.com/gaborage/go-bricks/messaging"
)

// defaultPreWarmReadinessTimeout is the fallback readiness budget when
// messaging.reconnect.readytimeout carries no positive value (a directly
// constructed App in tests — Builder.Build always resolves a config, via
// WithConfig, before prepareRuntime is reachable). Mirrors config's
// defaultReadyTimeout (see config/validation.go) — pre-warm and the first
// real publish converge on the same "how long is reasonable to wait for a
// cold client" budget.
const defaultPreWarmReadinessTimeout = 5 * time.Second

// preWarmReadinessPollInterval mirrors messaging's unexported
// readinessCheckInterval (see messaging/constants.go) so both readiness-wait
// call sites share one poll cadence, without exporting an internal messaging
// constant just for this.
const preWarmReadinessPollInterval = 100 * time.Millisecond

// preWarmDatabase leases the fixed "" key to verify connectivity and releases it
// immediately. databaseSlot.start holds the manager nil check and the deployment-mode gate.
func (a *App) preWarmDatabase(ctx context.Context) error {
	_, release, err := a.dbManager.Get(ctx, "")
	if err != nil {
		return err
	}
	release() // pre-warm only verifies connectivity; release the lease immediately
	return nil
}

// preWarmMessaging ensures consumers for the fixed "" key and waits, bounded, for the
// publisher to report ready. messagingSlot.start holds the manager nil check and the
// deployment-mode gate.
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
