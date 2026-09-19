package app

import (
	"context"
	"errors"
	"fmt"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"

	rawbackoff "github.com/gaborage/go-bricks/internal/backoff"
	"github.com/gaborage/go-bricks/messaging"
)

// Retry pacing for the external-exchange startup wait. Each attempt dials a
// fresh connection, so the ceiling keeps a long wait from becoming a dial loop.
const (
	externalWaitFirstBackoff = time.Second
	externalWaitMaxBackoff   = 5 * time.Second
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

	if a.perTenantMessaging() {
		a.logger.Info().Msg("Multi-tenant mode: consumers will be started per tenant on demand")
		return nil
	}

	// Stats reads the consumer index length; Consumers() would allocate a slice
	// copy on every successful boot to answer a boolean.
	hasConsumers := decls.Stats().Consumers > 0

	if err := a.ensureConsumersWithExternalWait(ctx, decls, hasConsumers); err != nil {
		if hasConsumers {
			return fmt.Errorf("failed to start consumers on the control-plane key: %w", err)
		}
		a.logger.Warn().Err(err).Msg("Failed to start consumers on the control-plane key")
		return nil
	}

	a.logger.Info().Msg("Consumers started on the control-plane key")
	return nil
}

// ensureConsumersWithExternalWait runs the control-plane declare pass, re-running
// it while the broker answers 404 until messaging.declare.externalwait elapses,
// so a consumer can deploy before the service owning its external exchange.
//
// The wait may only DELAY an abort that would otherwise happen, never introduce
// one — which is why it is gated on hasConsumers: a publisher-only service warns
// and continues on this failure, so there is no abort to delay. Stopping at 404
// is a separate call: it is the one refusal that plausibly converges, and every
// other failure is more useful fast than slow. See ADR-119.
func (a *App) ensureConsumersWithExternalWait(ctx context.Context, decls *messaging.Declarations, hasConsumers bool) error {
	err := a.messagingManager.EnsureConsumers(ctx, "", decls)
	if err == nil {
		return nil
	}

	// Read below the happy path: a directly-constructed App may carry no config
	// (app.go guards a.cfg the same way), and it never reaches here on success.
	wait := a.cfg.Messaging.Declare.ExternalWait
	if wait <= 0 || !hasConsumers || !isBrokerNotFound(err) {
		return err
	}

	// The broker names the entity in its own reply; this line must not claim
	// which one it was, because a 404 can also come from a bind or a consume.
	a.logger.Warn().Err(err).Dur("externalwait", wait).
		Msg("Broker answered 404, re-running the startup declare pass until it succeeds or externalwait elapses")

	deadline := time.Now().Add(wait)
	// Derived so a wait shorter than the fixed first gap still gets several
	// attempts instead of spending its whole budget asleep.
	base := min(externalWaitFirstBackoff, wait/4)

	for attempt := 0; ; attempt++ {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return err
		}

		// Never sleep past the deadline: the ceiling is 5s, so the last gap
		// could otherwise overshoot the configured budget by nearly that much.
		select {
		case <-ctx.Done():
			return err
		case <-time.After(min(rawbackoff.Saturating(base, externalWaitMaxBackoff, attempt), remaining)):
		}

		if err = a.messagingManager.EnsureConsumers(ctx, "", decls); err == nil || !isBrokerNotFound(err) {
			return err
		}
		a.logger.Debug().Err(err).Int("attempt", attempt+1).Dur("remaining", time.Until(deadline)).
			Msg("External exchange still absent")
	}
}

// isBrokerNotFound reports whether the broker refused with 404 NOT_FOUND. Named
// for the reply code rather than the entity: a passive exchange declare, a bind
// and a consume can all raise it, and this sees only the code.
func isBrokerNotFound(err error) bool {
	var amqpErr *amqp.Error
	return errors.As(err, &amqpErr) && amqpErr.Code == amqp.NotFound
}
