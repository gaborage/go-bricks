package app

import (
	"context"
	"errors"
	"fmt"
	"net/url"

	"github.com/gaborage/go-bricks/messaging/streams"
)

// plaintextStreamScheme is the non-TLS stream-protocol scheme
// warnIfPlaintextStreamURI flags outside development.
const plaintextStreamScheme = "rabbitmq-stream"

// prepareStreamConsumers collects the modules' native stream declarations and,
// when there are any, starts the stream-protocol consumers and binds the
// declared publishers.
//
// Everything happens at RUNTIME on purpose: the manager does not exist until this
// function produces it — see streamsSlot in slot.go for why its probe and closer are
// registered separately from the build-time walks.
func (a *App) prepareStreamConsumers(ctx context.Context) error {
	if a.registry == nil {
		return errors.New("module registry not initialized")
	}

	decls := streams.NewDeclarations()
	if err := a.registry.DeclareStreams(decls); err != nil {
		return err
	}
	if decls.IsEmpty() {
		return nil
	}

	if err := a.assertStreamsSingleTenant(); err != nil {
		return err
	}

	if err := a.assertStreamsConfigured(decls); err != nil {
		return err
	}

	cfg := a.cfg.Messaging.Streams
	a.warnIfPlaintextStreamURI(cfg.URI)
	mgr := streams.NewManager(streams.ManagerOptions{
		URI:                 cfg.URI,
		AddressResolverHost: cfg.AddressResolver.Host,
		AddressResolverPort: cfg.AddressResolver.Port,
		OffsetStoreCount:    cfg.OffsetStore.CountBeforeStorage,
		OffsetStoreInterval: cfg.OffsetStore.FlushInterval,
		Logger:              a.logger,
	})

	// A service that declared streams and cannot start them would serve HTTP while
	// consuming nothing and publishing nowhere, so startup fails rather than
	// booting green.
	// The startup context is handed over for its values only: WithoutCancel makes
	// that literal — the manager's own startup gates honor cancellation for callers
	// that want it, and this caller does not — so shutdownStreamConsumers stays the
	// thing that stops the consumers. Same detachment the AMQP lane applies.
	if err := mgr.Start(context.WithoutCancel(ctx), decls); err != nil {
		if closeErr := mgr.Close(); closeErr != nil {
			a.logger.Warn().Err(closeErr).Msg("Failed to close stream environment after a failed start")
		}
		return fmt.Errorf("failed to start stream consumers: %w", err)
	}

	a.streamsManager = mgr

	return nil
}

// assertStreamsSingleTenant re-asserts the tenancy invariant at runtime.
//
// SECURITY: config.Validate already rejects multitenant.enabled beside a stream URI,
// and Builder.WithConfig now runs it on every app.NewWithConfig call — but an App
// assembled without going through WithConfig (e.g. a hand-built Builder) reaches here
// unchecked. Without this repeat, such a service would boot green and run stream
// handlers against one shared Environment with no tenant in context, which is
// precisely what the gate exists to prevent.
func (a *App) assertStreamsSingleTenant() error {
	if !a.cfg.Multitenant.Enabled {
		return nil
	}
	return errors.New("messaging.streams is single-tenant only; multi-tenant stream consumption " +
		"is not yet supported, but stream declarations were registered with multitenant.enabled: true; " +
		"config.Validate rejects this combination and a config built without it is re-checked here")
}

// warnIfPlaintextStreamURI flags a plaintext stream endpoint outside development:
// the broker credentials in the URI then cross the network in the clear. It warns
// rather than fails because the framework exposes no TLS configuration surface yet
// (ADR-059 future work), so failing closed would leave such deployments no option.
func (a *App) warnIfPlaintextStreamURI(uri string) {
	if a.cfg.App.IsDevelopment() {
		return
	}
	u, err := url.Parse(uri)
	if err != nil || u.Scheme != plaintextStreamScheme {
		return
	}
	a.logger.Warn().
		Str("environment", a.cfg.App.Env).
		Msg("messaging.streams.uri uses the plaintext stream protocol; broker credentials cross the network unencrypted — " +
			"terminate TLS in front of the broker or use rabbitmq-stream+tls://")
}

// assertStreamsConfigured fails fast when a module declared streams but no
// stream endpoint is configured — without it the declarations would be silently
// dropped, exactly as assertMessagingConfiguredIfDeclared prevents for AMQP.
func (a *App) assertStreamsConfigured(decls *streams.Declarations) error {
	if a.cfg.Messaging.Streams.URI != "" {
		return nil
	}
	s := decls.Stats()
	return fmt.Errorf("stream declarations were registered "+
		"(streams=%d, consumers=%d, publishers=%d) "+
		"but the stream protocol is not configured; "+
		"set messaging.streams.uri (or env MESSAGING_STREAMS_URI)",
		s.Streams, s.Consumers, s.Publishers)
}

// shutdownStreamConsumers stops stream consumers before modules are torn down,
// then closes the publishers. Each consumer flushes its pending offset first and
// every publish still awaiting confirmation is failed rather than left hanging;
// the environment itself is closed later by the registered closer. No-op when no
// streams were declared.
func (a *App) shutdownStreamConsumers() {
	if a.streamsManager == nil {
		return
	}
	a.logger.Info().Msg("Stopping stream consumers")
	a.streamsManager.StopConsumers()
}
