package app

import (
	"context"
	"errors"
	"fmt"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"

	"github.com/gaborage/go-bricks/messaging"
)

// Retry pacing for the external-exchange startup wait. The first gap is short
// enough that an exchange declared moments later costs almost nothing, and the
// ceiling keeps a long wait from becoming a busy loop.
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

	hasConsumers := len(decls.Consumers()) > 0

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

// ensureConsumersWithExternalWait runs the control-plane declare pass, and when
// messaging.declare.externalwait is set re-runs it while the broker keeps
// answering 404 — an exchange that does not exist yet (ADR-119) — so a consumer
// can deploy before the service that owns its external exchange.
//
// The wait only DELAYS an abort that would otherwise happen; it never introduces
// one. That is why it is gated on hasConsumers: a publisher-only service warns
// and continues on this failure, so holding it at startup would buy nothing and
// cost boot time, and its next channel generation redeclares the topology anyway.
// Every non-404 failure returns immediately, keeping the fail-fast contract
// TestPrepareRuntimeConsumersFailsStartupOnEnsureError pins.
//
// The 404 is returned verbatim when the wait elapses, so the operator reads the
// broker's own reply naming the exchange rather than a bare timeout.
func (a *App) ensureConsumersWithExternalWait(ctx context.Context, decls *messaging.Declarations, hasConsumers bool) error {
	err := a.messagingManager.EnsureConsumers(ctx, "", decls)

	wait := a.cfg.Messaging.Declare.ExternalWait
	if err == nil || wait <= 0 || !hasConsumers || !isExchangeNotFound(err) {
		return err
	}

	a.logger.Warn().Err(err).Dur("externalwait", wait).
		Msg("External exchange is absent, waiting for it before aborting startup")

	deadline := time.Now().Add(wait)
	// Derived rather than fixed: a wait shorter than the default first gap would
	// otherwise spend its whole budget asleep and retry once at the very end.
	backoff := max(min(externalWaitFirstBackoff, wait/4), time.Millisecond)

	for {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return err
		}

		select {
		case <-ctx.Done():
			return err
		case <-time.After(min(backoff, remaining)):
		}

		if err = a.messagingManager.EnsureConsumers(ctx, "", decls); err == nil || !isExchangeNotFound(err) {
			return err
		}
		backoff = min(backoff*2, externalWaitMaxBackoff)
	}
}

// isExchangeNotFound reports whether the broker refused with 404 NOT_FOUND, the
// answer a passive declare gives for an exchange that does not exist and the one
// refusal the startup wait retries.
func isExchangeNotFound(err error) bool {
	var amqpErr *amqp.Error
	return errors.As(err, &amqpErr) && amqpErr.Code == amqp.NotFound
}
