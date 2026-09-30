package app

import (
	"context"
	"fmt"

	"github.com/gaborage/go-bricks/config"
	"github.com/gaborage/go-bricks/messaging"
)

// Every plan DECISION in this file is written as ==/!= comparisons, never a tagged switch: gremlins
// derives no mutant from a tagged switch, so a decision spelled as one is invisible to the
// mutation gate. Display-only String methods may stay switches.

// resourcePlan is the Resource plan (CONTEXT.md, ADR-126): for each resource kind, its Tenancy,
// what the control-plane key "" holds for it and what the tenant keys hold, fixed once when the application is built.
// Startup readers ask its answers instead of re-deriving them from config and Options. It is
// computed once, in appBootstrap.dependencies, before any manager exists, and copied from
// there.
//
// The zero value is inert: every row reads single-tenant with "" and the tenant keys knowable
// only at runtime, so
// nothing is unavailable, nothing is leased at build, and every kind reads configured.
type resourcePlan struct {
	database  kindPlan
	messaging kindPlan // streams and sealing read this row too
	cache     kindPlan
}

// kindTenancy is where a kind resolves. singleTenant, the zero value, doubles as the deployment
// fact: it is the only arm without multitenant.enabled, where messaging.tenancy: shared is the
// ADR-041 env-parity no-op.
type kindTenancy uint8

const (
	singleTenant kindTenancy = iota
	// sharedTenancy: multitenant, and the kind resolves and replays once on "". Only messaging,
	// under messaging.tenancy: shared (ADR-087).
	sharedTenancy
	// perTenantTenancy: multitenant, and modules resolve the kind per tenant. The database and
	// cache always; messaging under messaging.tenancy: per-tenant. The readiness probe and a
	// shared ledger still reach "" (ADR-047, ADR-041).
	perTenantTenancy
)

// String spells the two multitenant arms as config does, so the messaging row can feed
// MultiTenantResourceProvider.SetMessagingTenancy.
func (t kindTenancy) String() string {
	switch t {
	case sharedTenancy:
		return config.TenancyShared
	case perTenantTenancy:
		return config.TenancyPerTenant
	default:
		return "single-tenant"
	}
}

// keyPresence is what "", or the tenant keys, hold for one kind. keyAtRuntime, the zero value,
// is never assumed present or absent, so a zero row never refuses and never leases at build.
type keyPresence uint8

const (
	keyAtRuntime keyPresence = iota // the store serving "" is dynamic
	keyPresent
	keyAbsent
)

func (p keyPresence) String() string {
	switch p {
	case keyPresent:
		return "known present"
	case keyAbsent:
		return "known absent"
	default:
		return "knowable only at runtime"
	}
}

// kindPlan is one kind's row: three facts, and answers derived from them on every call and
// never stored, so no two answers can disagree about the facts.
type kindPlan struct {
	kind     string // componentDatabase, componentMessaging or componentCache; "" in the zero row
	tenancy  kindTenancy
	presence keyPresence // what "" holds
	// tenantKeys is what the tenant keys hold: known only for per-tenant messaging on the
	// built-in store with static tenants, keyAtRuntime everywhere else.
	tenantKeys keyPresence
}

// resolvesOnControlPlane: modules resolve the kind on "".
func (k kindPlan) resolvesOnControlPlane() bool { return k.tenancy != perTenantTenancy }

// unavailable: the kind resolves on "" and "" is known absent, or it resolves per tenant and
// every tenant key is known absent. Definitive, and never true while the deciding fact is
// keyAtRuntime. For the database-absence WARN, the DatabaseRequirer abort, the
// messaging-declarations gate (#366) and ModuleDeps.*Configured, which reads its negation.
func (k kindPlan) unavailable() bool {
	return (k.resolvesOnControlPlane() && k.presence == keyAbsent) ||
		(k.tenancy == perTenantTenancy && k.tenantKeys == keyAbsent)
}

// controlPlaneAbsent: "" is known absent, whatever the Tenancy. For
// ModuleDeps.ControlPlaneMessagingAbsent, which a shared-ledger outbox reads (ADR-128).
func (k kindPlan) controlPlaneAbsent() bool { return k.presence == keyAbsent }

// preInits: lease "" at build under app.startup.<kind>, because the kind resolves on "" and ""
// is known present. Whether a failure is fatal stays the slot's preInitFatal.
func (k kindPlan) preInits() bool {
	return k.resolvesOnControlPlane() && k.presence == keyPresent
}

// preWarms: lease "" once in prepareRuntime, advisory, because the kind resolves on "" and ""
// is not known absent.
func (k kindPlan) preWarms() bool {
	return k.resolvesOnControlPlane() && k.presence != keyAbsent
}

// probe sets the two plan-owned fields of the description a slot built. Only the cache skips
// the lease, and only when "" is known absent: the database and messaging always lease ""
// (ADR-047). perTenant relabels a not-configured "" and follows Tenancy.
func (k kindPlan) probe(d probeDescription) probeDescription {
	d.absent = k.kind == componentCache && k.presence == keyAbsent
	d.perTenant = !k.resolvesOnControlPlane()
	return d
}

// multitenant is the deployment fact.
func (p resourcePlan) multitenant() bool { return p.messaging.tenancy != singleTenant }

// keyedAtRuntime: the store serving "" is dynamic, so no kind's "" is knowable at build and
// no kind pre-inits.
func (p resourcePlan) keyedAtRuntime() bool { return p.database.presence == keyAtRuntime }

// tenantStamps: consumers on both lanes read the x-tenant-id stamp (ADR-087).
func (p resourcePlan) tenantStamps() bool { return p.messaging.tenancy == sharedTenancy }

// refusesStreams: stream consumption would need one Environment per tenant.
func (p resourcePlan) refusesStreams() bool { return p.messaging.tenancy == perTenantTenancy }

// sealTenancy maps the messaging row onto ADR-097's three seal tenancies.
func (p resourcePlan) sealTenancy() messaging.SealTenancy {
	// ==, not a tagged switch: a switch yields zero gremlins mutants, hiding this decision from the mutation gate.
	if p.messaging.tenancy == perTenantTenancy {
		return messaging.SealTenancyPerTenant
	}
	if p.messaging.tenancy == sharedTenancy {
		return messaging.SealTenancyShared
	}
	return messaging.SealTenancyDisabled
}

// planResources plans from a validated cfg, opts (may be nil) and store, the instance
// FactoryResolver.ResourceSource returned (ADR-127); builtInStore says store is the built-in
// one, which the caller knows and planning never infers. A dynamic store is never asked and reads
// keyAtRuntime; a CacheConnector makes the cache present; otherwise the store is asked for ""
// once per kind, a config lookup that dials nothing, under the kind's app.startup budget.
// not_configured reads absent; any other failure, a spent budget included, fails startup
// before any manager exists.
func planResources(ctx context.Context, cfg *config.Config, opts *Options, store TenantStore, builtInStore bool) (resourcePlan, error) {
	var plan resourcePlan
	for _, row := range []struct {
		kind string
		dst  *kindPlan
	}{{componentDatabase, &plan.database}, {componentMessaging, &plan.messaging}, {componentCache, &plan.cache}} {
		presence, err := presenceOf(ctx, cfg, opts, store, row.kind)
		if err != nil {
			return resourcePlan{}, err
		}
		tenancy := tenancyOf(cfg, row.kind)
		*row.dst = kindPlan{kind: row.kind, tenancy: tenancy, presence: presence, tenantKeys: tenantKeysOf(cfg, row.kind, tenancy, builtInStore)}
	}
	return plan, nil
}

// tenantKeysOf reads the static tenants' messaging.url the way the built-in store's BrokerURL
// does, untrimmed: absent when none sets one. Only per-tenant messaging on the built-in store
// is decided, and never from zero tenants.
func tenantKeysOf(cfg *config.Config, kind string, tenancy kindTenancy, builtInStore bool) keyPresence {
	if kind != componentMessaging || tenancy != perTenantTenancy || !builtInStore || len(cfg.Multitenant.Tenants) == 0 {
		return keyAtRuntime
	}
	for id := range cfg.Multitenant.Tenants {
		if cfg.Multitenant.Tenants[id].Messaging.URL != "" {
			return keyPresent
		}
	}
	return keyAbsent
}

func tenancyOf(cfg *config.Config, kind string) kindTenancy {
	if !cfg.Multitenant.Enabled {
		return singleTenant
	}
	if kind == componentMessaging && cfg.Messaging.Tenancy == config.TenancyShared {
		return sharedTenancy
	}
	return perTenantTenancy
}

func presenceOf(ctx context.Context, cfg *config.Config, opts *Options, store TenantStore, kind string) (keyPresence, error) {
	if store.IsDynamic() {
		return keyAtRuntime, nil
	}
	if kind == componentCache && opts != nil && opts.CacheConnector != nil {
		return keyPresent, nil
	}
	err := lookupControlPlaneKey(ctx, cfg, store, kind)
	if err == nil {
		return keyPresent, nil
	}
	if config.IsNotConfigured(err) {
		return keyAbsent, nil
	}
	return keyAtRuntime, fmt.Errorf(`resource plan: %s lookup of the control-plane key "": %w`, kind, err)
}

// lookupControlPlaneKey asks store for kind's "" under the budget that kind's pre-init gets.
func lookupControlPlaneKey(ctx context.Context, cfg *config.Config, store TenantStore, kind string) error {
	// ==, not a tagged switch: a switch yields zero gremlins mutants, hiding this decision from the mutation gate.
	if kind == componentDatabase {
		lookupCtx, cancel := startupContext(ctx, cfg.App.Startup.Database)
		defer cancel()
		_, err := store.DBConfig(lookupCtx, "")
		return err
	}
	if kind == componentMessaging {
		lookupCtx, cancel := startupContext(ctx, cfg.App.Startup.Messaging)
		defer cancel()
		_, err := store.BrokerURL(lookupCtx, "")
		return err
	}
	lookupCtx, cancel := startupContext(ctx, cfg.App.Startup.Cache)
	defer cancel()
	_, err := store.CacheConfig(lookupCtx, "")
	return err
}
