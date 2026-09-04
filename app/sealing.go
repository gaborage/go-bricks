package app

import "github.com/gaborage/go-bricks/messaging"

// configureSealing hands the sealing seam the facts only the app knows — the
// registered key store, the messaging.seal.active selector, the deployment's
// tenancy and the meter — BEFORE declarations are collected, so a seal-tagged
// declaration resolves its producer at declaration time and fails Validate,
// never a publish (ADR-097). The order is load-bearing: DeclareTypedPublisher
// reads these facts as the module declares. Unlike the database facts in
// bootstrap.go, none of them is per tenant or per dynamic source: there is one
// KeyStoreProvider per process and the selector is a static messaging key, so
// dynamic-config and per-tenant deployments read the same facts (#1306: per-tenant
// keys are forbidden in v1).
func (a *App) configureSealing() {
	rt := messaging.SealRuntime{Tenancy: messaging.SealTenancyDisabled}
	if a.cfg != nil {
		rt.Active = a.cfg.Messaging.Seal.Active
		switch {
		case a.perTenantMessaging():
			rt.Tenancy = messaging.SealTenancyPerTenant
		case a.multiTenant():
			rt.Tenancy = messaging.SealTenancyShared
		}
	}
	if a.registry != nil && a.registry.deps != nil {
		if a.registry.deps.KeyStore != nil {
			rt.KeyStore = a.registry.deps.KeyStore
		}
		rt.Meter = a.registry.deps.MeterProvider
	}
	messaging.ConfigureSealing(&rt)
}
