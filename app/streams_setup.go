package app

import (
	"context"
	"errors"
	"fmt"
	"net/url"

	"github.com/gaborage/go-bricks/internal/streamruntime"
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
//
// The lane itself is link-time opt-in (ADR-091). A configured URI with no
// registered runtime is a startup error naming the missing import; an absent
// URI with no runtime is a clean start.
func (a *App) prepareStreamConsumers(ctx context.Context) error {
	rt := registeredStreamRuntime()
	if rt == nil {
		if a.streamsURIConfigured() {
			return ErrStreamsNotLinked
		}
		return nil
	}

	if a.registry == nil {
		return errors.New("module registry not initialized")
	}

	decls, collectErr := rt.CollectDeclarations(runtimeModules(a.registry.modules), a.logger)
	if collectErr != nil {
		return collectErr
	}
	if decls.IsEmpty() {
		return nil
	}

	if perTenantErr := a.assertStreamsNotPerTenant(); perTenantErr != nil {
		return perTenantErr
	}

	if configuredErr := a.assertStreamsConfigured(decls); configuredErr != nil {
		return configuredErr
	}

	hold, holdErr := a.holdLedger()
	if holdErr != nil {
		return holdErr
	}

	cfg := a.cfg.Messaging.Streams
	a.warnIfPlaintextStreamURI(cfg.URI)
	mgr := rt.NewManager(&streamruntime.ManagerOptions{
		URI:                 cfg.URI,
		AddressResolverHost: cfg.AddressResolver.Host,
		AddressResolverPort: cfg.AddressResolver.Port,
		OffsetStoreCount:    cfg.OffsetStore.CountBeforeStorage,
		OffsetStoreInterval: cfg.OffsetStore.FlushInterval,
		Logger:              a.logger,
		Hold:                hold,
	})
	mgr.SetTenantStamps(a.multiTenant() && a.sharedMessaging())

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

func runtimeModules(mods []Module) []streamruntime.ModuleNamer {
	out := make([]streamruntime.ModuleNamer, len(mods))
	for i, m := range mods {
		out[i] = m
	}
	return out
}

func (a *App) streamsURIConfigured() bool {
	return a.cfg != nil && a.cfg.Messaging.Streams.URI != ""
}

// assertStreamsNotPerTenant re-asserts the tenancy invariant at runtime: streams
// are consumed on one key, which is the deployment's own under single-tenant and the
// control-plane key under messaging.tenancy: shared. Only per-tenant tenancy would
// need one Environment per tenant, and that is what stays refused.
//
// SECURITY: config.Validate already rejects multitenant.enabled beside a stream URI,
// and Builder.WithConfig now runs it on every app.NewWithConfig call — but an App
// assembled without going through WithConfig (e.g. a hand-built Builder) reaches here
// unchecked. Without this repeat, such a service would boot green and run stream
// handlers against one shared Environment with no tenant in context, which is
// precisely what the gate exists to prevent.
func (a *App) assertStreamsNotPerTenant() error {
	if !a.perTenantMessaging() {
		return nil
	}
	return errors.New("messaging.streams needs single-tenant mode or messaging.tenancy: shared; " +
		"per-tenant stream consumption would need one Environment per tenant and is not supported, " +
		"but stream declarations were registered with multitenant.enabled: true and per-tenant " +
		"messaging tenancy; config.Validate rejects this combination and a config built without it " +
		"is re-checked here")
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
func (a *App) assertStreamsConfigured(decls streamruntime.Declarations) error {
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

// holdLedger is the one ledger stream consumers park into, or nil when no module
// offers one. Two providers is a configuration error rather than a choice: the
// hold's order guarantee is per tenant per consumer, and two ledgers would split
// one consumer's held set between them.
func (a *App) holdLedger() (HoldLedger, error) {
	var found HoldLedger
	for _, provider := range a.holdLedgers {
		ledger := provider.HoldLedger()
		if ledger == nil {
			continue
		}
		if found != nil {
			return nil, errors.New("two modules provide a hold ledger; stream consumers can park into only one")
		}
		found = ledger
	}
	return found, nil
}
