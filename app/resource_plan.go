package app

import (
	"github.com/gaborage/go-bricks/config"
	"github.com/gaborage/go-bricks/messaging"
)

// resourcePlan is the Resource plan (CONTEXT.md, ADR-126): for each resource kind, its Tenancy
// and what the control-plane key "" holds for it, fixed once when the application is built.
// Startup readers ask its answers instead of re-deriving them from config and Options. It is
// computed once, in appBootstrap.dependencies, before any manager exists, and copied from
// there.
//
// The zero value is inert: every row reads single-tenant with "" knowable only at runtime, so
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
	if t == sharedTenancy {
		return config.TenancyShared
	}
	if t == perTenantTenancy {
		return config.TenancyPerTenant
	}
	return "single-tenant"
}

// keyPresence is what "" holds for one kind. keyAtRuntime, the zero value, is never assumed
// present or absent, so a zero row never refuses and never leases at build.
type keyPresence uint8

const (
	keyAtRuntime keyPresence = iota // the store serving "" is dynamic
	keyPresent
	keyAbsent
)

func (p keyPresence) String() string {
	if p == keyPresent {
		return "known present"
	}
	if p == keyAbsent {
		return "known absent"
	}
	return "knowable only at runtime"
}

// kindPlan is one kind's row: two facts, and answers derived from them on every call and never
// stored, so no two answers can disagree about the facts.
type kindPlan struct {
	kind     string // componentDatabase, componentMessaging or componentCache; "" in the zero row
	tenancy  kindTenancy
	presence keyPresence
	forced   forcedAnswers // today's answers where they differ from the rule; ADR-127 deletes it
}

// resolvesOnControlPlane: modules resolve the kind on "".
func (k kindPlan) resolvesOnControlPlane() bool { return k.tenancy != perTenantTenancy }

// unavailable: the kind resolves on "" and "" is known absent. Definitive, and never true while
// presence is keyAtRuntime. For the database-absence WARN, the DatabaseRequirer abort and the
// messaging-declarations gate (#366).
func (k kindPlan) unavailable() bool {
	return k.forced.resolve(answerUnavailable, k.tenancy != perTenantTenancy && k.presence == keyAbsent)
}

// configured feeds ModuleDeps.*Configured. Its rule is !unavailable(); it stays a separate
// answer until ADR-127 because today's flags read true where the kind is also unavailable.
func (k kindPlan) configured() bool {
	return k.forced.resolve(answerConfigured, k.tenancy == perTenantTenancy || k.presence != keyAbsent)
}

// preInits: lease "" at build under app.startup.<kind>, because the kind resolves on "" and ""
// is known present. Whether a failure is fatal stays the slot's preInitFatal.
func (k kindPlan) preInits() bool {
	return k.forced.resolve(answerPreInit, k.tenancy != perTenantTenancy && k.presence == keyPresent)
}

// preWarms: lease "" once in prepareRuntime, advisory, because the kind resolves on "" and ""
// is not known absent.
func (k kindPlan) preWarms() bool {
	return k.forced.resolve(answerPreWarm, k.tenancy != perTenantTenancy && k.presence != keyAbsent)
}

// probe sets the two plan-owned fields of the description a slot built. Only the cache skips
// the lease, and only when "" is known absent: the database and messaging always lease ""
// (ADR-047). perTenant relabels a not-configured "" and follows Tenancy.
func (k kindPlan) probe(d probeDescription) probeDescription {
	d.absent = k.kind == componentCache && k.presence == keyAbsent
	d.perTenant = k.forced.resolve(answerPerTenantLabel, k.tenancy == perTenantTenancy)
	return d
}

// multitenant is the deployment fact.
func (p resourcePlan) multitenant() bool { return p.messaging.tenancy != singleTenant }

// tenantStamps: consumers on both lanes read the x-tenant-id stamp (ADR-087).
func (p resourcePlan) tenantStamps() bool { return p.messaging.tenancy == sharedTenancy }

// refusesStreams: stream consumption would need one Environment per tenant.
func (p resourcePlan) refusesStreams() bool { return p.messaging.tenancy == perTenantTenancy }

// sealTenancy maps the messaging row onto ADR-097's three seal tenancies.
func (p resourcePlan) sealTenancy() messaging.SealTenancy {
	if p.messaging.tenancy == perTenantTenancy {
		return messaging.SealTenancyPerTenant
	}
	if p.messaging.tenancy == sharedTenancy {
		return messaging.SealTenancyShared
	}
	return messaging.SealTenancyDisabled
}

// planResources plans from a validated cfg, opts (may be nil) and store, the instance
// FactoryResolver.ResourceSource returned. It never asks a store for "": a dynamic store reads
// keyAtRuntime, a CacheConnector makes the cache present, and otherwise "" is judged as the
// built-in config.TenantStore answers it, from the root blocks — a caller-supplied static
// store included, as today's readers judge it.
func planResources(cfg *config.Config, opts *Options, store TenantStore) resourcePlan {
	return planUnder(todaysLedger, planInputs{cfg: cfg, opts: opts, store: store})
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

func presenceOf(ledger driftLedger, in planInputs, kind string) keyPresence {
	if in.store.IsDynamic() {
		return keyAtRuntime
	}
	if kind == componentCache && in.opts != nil && in.opts.CacheConnector != nil {
		return keyPresent
	}
	if ledger.presence != nil {
		if p, ok := ledger.presence(in, kind); ok {
			return p
		}
	}
	return rootBlockPresence(in.cfg, kind)
}

// rootBlockPresence is config.TenantStore's answer for "": the content tests it applies before
// answering not_configured.
func rootBlockPresence(cfg *config.Config, kind string) keyPresence {
	present := cfg.Cache.Enabled
	if kind == componentDatabase {
		present = config.IsDatabaseConfigured(&cfg.Database)
	}
	if kind == componentMessaging {
		present = cfg.Messaging.Broker.URL != ""
	}
	if present {
		return keyPresent
	}
	return keyAbsent
}

// ---- Transitional: ADR-127 deletes the drift ledger below and every answer's pin. ----

type planInputs struct {
	cfg   *config.Config
	opts  *Options
	store TenantStore
}

// planUnder is planResources with the drift ledger as a parameter; the rule is
// planUnder(driftLedger{}, …).
func planUnder(ledger driftLedger, in planInputs) resourcePlan {
	return resourcePlan{
		database:  planKind(ledger, in, componentDatabase),
		messaging: planKind(ledger, in, componentMessaging),
		cache:     planKind(ledger, in, componentCache),
	}
}

func planKind(ledger driftLedger, in planInputs, kind string) kindPlan {
	k := kindPlan{kind: kind, tenancy: tenancyOf(in.cfg, kind), presence: presenceOf(ledger, in, kind)}
	for _, row := range ledger.rows {
		if !row.when(in, k) {
			continue
		}
		if row.to {
			k.forced.toTrue |= row.answer
		} else {
			k.forced.toFalse |= row.answer
		}
	}
	return k
}

// answer names one derived answer a drift row can pin.
type answer uint8

const (
	answerUnavailable answer = 1 << iota
	answerConfigured
	answerPreInit
	answerPreWarm
	answerPerTenantLabel
)

// forcedAnswers holds the answers the ledger pinned on one row.
type forcedAnswers struct{ toTrue, toFalse answer }

// resolve returns the pinned value when a is pinned, and the rule otherwise.
func (f forcedAnswers) resolve(a answer, rule bool) bool {
	if f.toTrue&a != 0 {
		return true
	}
	if f.toFalse&a != 0 {
		return false
	}
	return rule
}

// driftRow is one named way a startup reader answers differently from the rule today: where
// when holds, answer is pinned to `to`.
type driftRow struct {
	name   string
	answer answer
	to     bool
	when   func(in planInputs, k kindPlan) bool
}

// driftLedger is today's behavior written as its difference from the rule. presence
// overrides what "" holds for a kind before the root blocks are read.
type driftLedger struct {
	presence func(in planInputs, kind string) (keyPresence, bool)
	rows     []driftRow
}

func callerSource(in planInputs) bool { return in.opts != nil && in.opts.ResourceSource != nil }

// todaysLedger pins every answer where a reader differs from the rule today, each row naming
// the reader it reproduces, so planning under it changes no behavior (ADR-126).
var todaysLedger = driftLedger{
	// D1: rootCacheAbsent, installed as cacheSlot.absent (app_builder.go:228), exempts ANY
	// caller-supplied ResourceSource, so the cache probe and pre-init lease "" through it
	// whatever cache.enabled says.
	presence: func(in planInputs, kind string) (keyPresence, bool) {
		return keyPresent, kind == componentCache && callerSource(in)
	},
	rows: []driftRow{
		// D2: the ModuleDeps flags read true in every per-key mode — multitenant, a dynamic
		// store, any caller-supplied ResourceSource (module.go:324-326).
		{name: "D2 per-key flags", answer: answerConfigured, to: true, when: func(in planInputs, k kindPlan) bool {
			return k.tenancy != singleTenant || callerSource(in)
		}},
		// D3: ConfigureRuntimeHelpers skips every kind's pre-init under multitenant
		// (app_builder.go:278), messaging under shared tenancy included.
		{name: "D3 pre-init skips multitenant", answer: answerPreInit, to: false, when: func(_ planInputs, k kindPlan) bool {
			return k.tenancy != singleTenant
		}},
		// D4: databaseSlot.start and messagingSlot.start pre-warm on the tenancy alone
		// (slot.go:208, 294), leasing a known-absent "" for a Debug skip.
		{name: "D4 pre-warm ignores absence", answer: answerPreWarm, to: true, when: func(_ planInputs, k kindPlan) bool {
			return k.kind != componentCache && k.tenancy != perTenantTenancy && k.presence == keyAbsent
		}},
		// D5: cacheSlot.start never pre-warms (slot.go:365).
		{name: "D5 cache never pre-warms", answer: answerPreWarm, to: false, when: func(_ planInputs, k kindPlan) bool {
			return k.kind == componentCache
		}},
		// D6: assertMessagingConfiguredIfDeclared skips on multitenant.enabled (lifecycle.go:245),
		// shared tenancy included.
		{name: "D6 messaging gate skips multitenant", answer: answerUnavailable, to: false, when: func(_ planInputs, k kindPlan) bool {
			return k.kind == componentMessaging && k.tenancy != singleTenant
		}},
		// D7: the same gate tests the root broker URL, never the store (lifecycle.go:248), so
		// it refuses a dynamic store that serves "".
		{name: "D7 messaging gate reads the root broker", answer: answerUnavailable, to: true, when: func(in planInputs, k kindPlan) bool {
			return k.kind == componentMessaging && k.tenancy == singleTenant && k.presence == keyAtRuntime &&
				in.cfg.Messaging.Broker.URL == ""
		}},
		// D8: messagingSlot.describe labels per_tenant on multitenant.enabled (slot.go:237),
		// shared tenancy included.
		{name: "D8 messaging label follows multitenant", answer: answerPerTenantLabel, to: true, when: func(_ planInputs, k kindPlan) bool {
			return k.kind == componentMessaging && k.tenancy == sharedTenancy
		}},
	},
}
