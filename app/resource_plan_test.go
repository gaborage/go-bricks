package app

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gaborage/go-bricks/cache"
	"github.com/gaborage/go-bricks/config"
	"github.com/gaborage/go-bricks/messaging"
)

// planMode is one deployment mode the build accepts. spec switches inputs on: mt, shared
// (messaging.tenancy), dynamic (source.type dynamic beside a dynamic store), caller (a static
// Options.ResourceSource), cacheconn, and the db, broker and cache root blocks. want is today's
// answer per kind (database, messaging, cache); rule is the rule's, where it differs. Beside a
// caller store the rule cells are the root-block reading the plan can compute without a lookup;
// ADR-127 decides those from what the store answers for "".
type planMode struct {
	name, spec string
	want, rule [3]string
}

// Plan cells, spelled as renderPlan renders them: "<tenancy>/<presence>" then every yes answer.
// asToday leaves a rule cell equal to today's.
const (
	asToday              = ""
	stPresentPrewarm     = "st/present configured preinit prewarm"
	stPresent            = "st/present configured preinit"
	stAbsentPrewarm      = "st/absent unavailable prewarm"
	stAbsent             = "st/absent unavailable"
	stAbsentSkip         = "st/absent unavailable skip"
	stAbsentConfigured   = "st/absent unavailable configured prewarm"
	stRuntimePrewarm     = "st/runtime configured prewarm"
	stRuntimeUnavailable = "st/runtime unavailable configured prewarm"
	stRuntime            = "st/runtime configured"
	ptAbsent             = "pt/absent configured per_tenant"
	ptAbsentSkip         = "pt/absent configured skip per_tenant"
	ptPresent            = "pt/present configured per_tenant"
	ptRuntime            = "pt/runtime configured per_tenant"
	sharedPresentPrewarm = "shared/present configured prewarm per_tenant"
	sharedPresentPreinit = "shared/present configured preinit prewarm"
	sharedAbsentPrewarm  = "shared/absent configured prewarm per_tenant"
	sharedAbsent         = "shared/absent unavailable"
	sharedRuntimePrewarm = "shared/runtime configured prewarm per_tenant"
	sharedRuntime        = "shared/runtime configured prewarm"
)

// kinds holds one cell per kind: database, messaging, cache.
type kinds = [3]string

// planModes covers every mode of the fact base the build accepts; the comment names the ones a
// case stands for when their plan inputs coincide.
var planModes = []planMode{
	// ST-root, ST-streams
	{name: "st_root", spec: "db broker cache", want: kinds{stPresentPrewarm, stPresentPrewarm, stPresent}, rule: kinds{asToday, asToday, stPresentPrewarm}},
	{name: "st_db_cache_only", spec: "db cache", want: kinds{stPresentPrewarm, stAbsentPrewarm, stPresent}, rule: kinds{asToday, stAbsent, stPresentPrewarm}},
	// ST-noroot
	{name: "st_noroot", spec: "", want: kinds{stAbsentPrewarm, stAbsentPrewarm, stAbsentSkip}, rule: kinds{stAbsent, stAbsent, asToday}},
	// ST-shared-noroot: the ADR-041 env-parity no-op
	{name: "st_shared_noroot", spec: "shared", want: kinds{stAbsentPrewarm, stAbsentPrewarm, stAbsentSkip}, rule: kinds{stAbsent, stAbsent, asToday}},
	// ST-dynsrc-dynRS-noroot
	{name: "st_dynamic_noroot", spec: "dynamic", want: kinds{stRuntimePrewarm, stRuntimeUnavailable, stRuntime}, rule: kinds{asToday, stRuntimePrewarm, stRuntimePrewarm}},
	{name: "st_dynamic_root", spec: "dynamic db broker cache", want: kinds{stRuntimePrewarm, stRuntimePrewarm, stRuntime}, rule: kinds{asToday, asToday, stRuntimePrewarm}},
	{name: "st_dynamic_cacheconn", spec: "dynamic cacheconn", want: kinds{stRuntimePrewarm, stRuntimeUnavailable, stRuntime}, rule: kinds{asToday, stRuntimePrewarm, stRuntimePrewarm}},
	// ST-staticRS-noroot, ST-customRS-noroot; rule: root-block reading
	{name: "st_caller_noroot", spec: "caller", want: kinds{stAbsentConfigured, stAbsentConfigured, stPresent}, rule: kinds{stAbsent, stAbsent, stAbsentSkip}},
	// ST-staticRS-root; rule: root-block reading
	{name: "st_caller_root", spec: "caller db broker", want: kinds{stPresentPrewarm, stPresentPrewarm, stPresent}, rule: kinds{asToday, asToday, stAbsentSkip}},
	// ST-cacheconn, ST-noroot-cacheconn
	{name: "st_cacheconn_noroot", spec: "cacheconn", want: kinds{stAbsentPrewarm, stAbsentPrewarm, stPresent}, rule: kinds{stAbsent, stAbsent, stPresentPrewarm}},
	// MT-static-tenants, MT-static-notenants-noroot, MT-static-pt
	{name: "mt_noroot", spec: "mt", want: kinds{ptAbsent, ptAbsent, ptAbsentSkip}},
	// MT-static-tenants-rootcache
	{name: "mt_rootcache", spec: "mt cache", want: kinds{ptAbsent, ptAbsent, ptPresent}},
	// MT-static-notenants-rootdb, MT-notenants-pt-root
	{name: "mt_root", spec: "mt db broker", want: kinds{ptPresent, ptPresent, ptAbsentSkip}},
	// MT-dynsrc, MT-dyn-pt
	{name: "mt_dynamic", spec: "mt dynamic db", want: kinds{ptRuntime, ptRuntime, ptRuntime}},
	// MT-cacheconn
	{name: "mt_cacheconn", spec: "mt cacheconn", want: kinds{ptAbsent, ptAbsent, ptPresent}},
	// MT-shared-root, MT-static-shared-root, MT-shared-streams
	{name: "mt_shared_root", spec: "mt shared broker", want: kinds{ptAbsent, sharedPresentPrewarm, ptAbsentSkip}, rule: kinds{asToday, sharedPresentPreinit, asToday}},
	// MT-shared-noroot, MT-static-shared-noroot
	{name: "mt_shared_noroot", spec: "mt shared", want: kinds{ptAbsent, sharedAbsentPrewarm, ptAbsentSkip}, rule: kinds{asToday, sharedAbsent, asToday}},
	// MT-dyn-shared-noroot
	{name: "mt_shared_dynamic", spec: "mt shared dynamic", want: kinds{ptRuntime, sharedRuntimePrewarm, ptRuntime}, rule: kinds{asToday, sharedRuntime, asToday}},
	// MT-customRS-shared-noroot; rule: root-block reading
	{name: "mt_shared_caller_noroot", spec: "mt shared caller", want: kinds{ptAbsent, sharedAbsentPrewarm, ptPresent}, rule: kinds{asToday, sharedAbsent, ptAbsentSkip}},
}

// inputs builds the mode's validated-shape config, Options (nil unless one is set) and the
// store FactoryResolver serves, plus the caller's store so a test can count its lookups.
func (m *planMode) inputs() (planInputs, *dynamicResourceSource) {
	on := map[string]bool{}
	for _, word := range strings.Fields(m.spec) {
		on[word] = true
	}
	cfg := &config.Config{Source: config.SourceConfig{Type: config.SourceTypeStatic}}
	cfg.Multitenant.Enabled = on["mt"]
	cfg.Messaging.Tenancy = config.TenancyPerTenant
	if on["shared"] {
		cfg.Messaging.Tenancy = config.TenancyShared
	}
	if on["db"] {
		cfg.Database.Host = "db.internal"
	}
	if on["broker"] {
		cfg.Messaging.Broker.URL = "amqp://broker/"
	}
	cfg.Cache.Enabled = on["cache"]

	var opts *Options
	var caller *dynamicResourceSource
	if on["dynamic"] || on["caller"] {
		caller = &dynamicResourceSource{dynamic: on["dynamic"]}
		opts = &Options{ResourceSource: caller}
	}
	if on["dynamic"] {
		cfg.Source.Type = config.SourceTypeDynamic
	}
	if on["cacheconn"] {
		if opts == nil {
			opts = &Options{}
		}
		opts.CacheConnector = func(context.Context, string) (cache.Cache, error) { return nil, nil }
	}
	return planInputs{cfg: cfg, opts: opts, store: newFactoryResolverForConfig(opts, cfg).ResourceSource(cfg)}, caller
}

var (
	tenancyCode  = map[kindTenancy]string{singleTenant: "st", sharedTenancy: "shared", perTenantTenancy: "pt"}
	presenceCode = map[keyPresence]string{keyAtRuntime: "runtime", keyPresent: "present", keyAbsent: "absent"}
)

// renderPlan spells each row as "<tenancy>/<presence>" followed by every answer that is yes;
// skip is a probe that leases nothing.
func renderPlan(p resourcePlan) [3]string {
	var out [3]string
	for i, k := range []kindPlan{p.database, p.messaging, p.cache} {
		probe := k.probe(probeDescription{})
		words := []string{tenancyCode[k.tenancy] + "/" + presenceCode[k.presence]}
		for _, a := range []struct {
			word string
			yes  bool
		}{
			{"unavailable", k.unavailable()},
			{"configured", k.configured()},
			{"preinit", k.preInits()},
			{"prewarm", k.preWarms()},
			{"skip", probe.absent},
			{"per_tenant", probe.perTenant},
		} {
			if a.yes {
				words = append(words, a.word)
			}
		}
		out[i] = strings.Join(words, " ")
	}
	return out
}

// TestResourcePlan pins today's answers in every mode, and the rule's under an empty drift
// ledger, and that planning never asks a caller-supplied store for "".
func TestResourcePlan(t *testing.T) {
	for _, m := range planModes {
		t.Run(m.name, func(t *testing.T) {
			in, caller := m.inputs()

			assert.Equal(t, m.want, renderPlan(planResources(in.cfg, in.opts, in.store)), "today")

			rule := m.want
			for i, cell := range m.rule {
				if cell != "" {
					rule[i] = cell
				}
			}
			assert.Equal(t, rule, renderPlan(planUnder(driftLedger{}, in)), "rule")

			if caller != nil {
				assert.Zero(t, caller.dbCalls+caller.msgCalls+caller.cacheCalls, "a caller-supplied store is never asked")
			}
		})
	}
}

// TestResourcePlanDeploymentAnswers pins the answers the messaging row's Tenancy decides alone.
func TestResourcePlanDeploymentAnswers(t *testing.T) {
	tests := []struct {
		tenancy                                           kindTenancy
		name                                              string
		controlPlane, multitenant, stamps, refusesStreams bool
		seal                                              messaging.SealTenancy
	}{
		{tenancy: singleTenant, name: "single-tenant", controlPlane: true, seal: messaging.SealTenancyDisabled},
		{tenancy: sharedTenancy, name: config.TenancyShared, controlPlane: true, multitenant: true, stamps: true, seal: messaging.SealTenancyShared},
		{tenancy: perTenantTenancy, name: config.TenancyPerTenant, multitenant: true, refusesStreams: true, seal: messaging.SealTenancyPerTenant},
	}
	for _, tt := range tests {
		t.Run(strings.ReplaceAll(tt.name, "-", "_"), func(t *testing.T) {
			plan := resourcePlan{messaging: kindPlan{kind: componentMessaging, tenancy: tt.tenancy}}
			assert.Equal(t, tt.name, tt.tenancy.String())
			assert.Equal(t, tt.controlPlane, plan.messaging.resolvesOnControlPlane())
			assert.Equal(t, tt.multitenant, plan.multitenant())
			assert.Equal(t, tt.stamps, plan.tenantStamps())
			assert.Equal(t, tt.refusesStreams, plan.refusesStreams())
			assert.Equal(t, tt.seal, plan.sealTenancy())
		})
	}
	assert.Equal(t, []string{"knowable only at runtime", "known present", "known absent"},
		[]string{keyAtRuntime.String(), keyPresent.String(), keyAbsent.String()})
	assert.True(t, kindPlan{}.probe(probeDescription{critical: true}).critical, "probe keeps the slot's fields")
}

// TestDriftLedgerRowsAreLoadBearing fails when a row changes no answer in any mode: a dead row
// would claim a drift that no longer exists.
func TestDriftLedgerRowsAreLoadBearing(t *testing.T) {
	changesAnAnswer := func(ledger driftLedger) bool {
		for _, m := range planModes {
			in, _ := m.inputs()
			if renderPlan(planUnder(ledger, in)) != renderPlan(planUnder(todaysLedger, in)) {
				return true
			}
		}
		return false
	}

	assert.True(t, changesAnAnswer(driftLedger{rows: todaysLedger.rows}), "D1 caller source exempts the cache")
	for i, row := range todaysLedger.rows {
		without := driftLedger{presence: todaysLedger.presence, rows: slices.Delete(slices.Clone(todaysLedger.rows), i, i+1)}
		assert.True(t, changesAnAnswer(without), row.name)
	}
}

// TestDriftLedgerRowsNeverOverlap fails when two rows pin one answer on one kind in one mode,
// where the answer would hang on which pin resolve reads first.
func TestDriftLedgerRowsNeverOverlap(t *testing.T) {
	for _, m := range planModes {
		in, _ := m.inputs()
		for _, kind := range []string{componentDatabase, componentMessaging, componentCache} {
			k := planKind(driftLedger{presence: todaysLedger.presence}, in, kind)
			var pinned answer
			for _, row := range todaysLedger.rows {
				if row.when(in, k) {
					assert.Zero(t, pinned&row.answer, "%s %s: %s pins an answer another row pins", m.name, kind, row.name)
					pinned |= row.answer
				}
			}
		}
	}
}

// TestResourcePlanPresenceMatchesBuiltInStore is the presence property: with the built-in store
// and no Options, each kind's presence is exactly that store's answer for "".
func TestResourcePlanPresenceMatchesBuiltInStore(t *testing.T) {
	ctx := context.Background()
	asPresence := func(err error) keyPresence {
		if err == nil {
			return keyPresent
		}
		require.True(t, config.IsNotConfigured(err), "unexpected store error: %v", err)
		return keyAbsent
	}
	for _, m := range planModes {
		in, _ := m.inputs()
		if in.opts != nil {
			continue
		}
		plan := planResources(in.cfg, nil, in.store)
		_, dbErr := in.store.DBConfig(ctx, "")
		_, msgErr := in.store.BrokerURL(ctx, "")
		_, cacheErr := in.store.CacheConfig(ctx, "")
		assert.Equal(t, asPresence(dbErr), plan.database.presence, m.name)
		assert.Equal(t, asPresence(msgErr), plan.messaging.presence, m.name)
		assert.Equal(t, asPresence(cacheErr), plan.cache.presence, m.name)
	}
}

// TestResourcePlanMatchesLegacyPredicates is ADR-126's behavior-preservation proof: in every
// mode each answer equals the predicate, or the transcribed inline condition, that answers it
// today. It goes once those readers read the plan.
func TestResourcePlanMatchesLegacyPredicates(t *testing.T) {
	for _, m := range planModes {
		t.Run(m.name, func(t *testing.T) {
			in, _ := m.inputs()
			assert.Equal(t, legacyAnswers(in), planAnswers(planResources(in.cfg, in.opts, in.store)))
		})
	}
}

// legacyAnswers asks each reader the plan replaces, keyed by the answer that replaces it.
func legacyAnswers(in planInputs) map[string]any {
	cfg, opts := in.cfg, in.opts
	a := &App{cfg: cfg}
	b := &Builder{cfg: cfg, opts: opts, bundle: &dependencyBundle{deps: &ModuleDeps{}}, app: &App{}}
	dbAbsent := b.InitializeRegistry().app.registry.rootDBAbsent // the WARN's predicate, as wired
	decls := messaging.NewDeclarations()
	decls.RegisterExchange(&messaging.ExchangeDeclaration{Name: "orders", Type: "topic"})
	skipPreInit := cfg.Multitenant.Enabled || cfg.Source.Type == config.SourceTypeDynamic // ConfigureRuntimeHelpers
	perKey := cfg.Multitenant.Enabled || cfg.Source.Type == config.SourceTypeDynamic ||
		(opts != nil && opts.ResourceSource != nil) // markConfigured before ADR-126
	seal := messaging.SealTenancyDisabled // configureSealing
	switch {
	case a.perTenantMessaging():
		seal = messaging.SealTenancyPerTenant
	case a.multiTenant():
		seal = messaging.SealTenancyShared
	}
	// The slots' describe, preInit and start conditions, transcribed; the #366 gate, asked.
	answers := map[string]any{
		"database.unavailable":             dbAbsent,
		"database.configured":              perKey || !dbAbsent,
		"database.preInits":                !skipPreInit && config.IsDatabaseConfigured(&cfg.Database),
		"database.preWarms":                !a.multiTenant(),
		"database.probe":                   probeDescription{perTenant: a.multiTenant()},
		"messaging.unavailable":            a.assertMessagingConfiguredIfDeclared(decls) != nil,
		"messaging.configured":             perKey || config.IsMessagingConfigured(&cfg.Messaging),
		"messaging.preInits":               !skipPreInit && config.IsMessagingConfigured(&cfg.Messaging),
		"messaging.preWarms":               !a.perTenantMessaging(),
		"messaging.resolvesOnControlPlane": !a.perTenantMessaging(),
		"messaging.probe":                  probeDescription{perTenant: a.multiTenant()},
		"cache.configured":                 perKey || !rootCacheAbsent(cfg, opts),
		"cache.preInits":                   !skipPreInit && !rootCacheAbsent(cfg, opts),
		"cache.preWarms":                   false,
		"cache.probe":                      probeDescription{absent: rootCacheAbsent(cfg, opts), perTenant: a.multiTenant()},
		"multitenant":                      a.multiTenant(),
		"amqpStamps":                       newManagerConfigBuilderFromConfig(cfg).tenantStamps,
		"streamStamps":                     a.multiTenant() && a.sharedMessaging(),
		"refusesStreams":                   a.perTenantMessaging(),
		"sealTenancy":                      seal,
	}
	if a.multiTenant() {
		answers["SetMessagingTenancy"] = cfg.Messaging.Tenancy
	}
	return answers
}

func planAnswers(p resourcePlan) map[string]any {
	answers := map[string]any{
		"database.unavailable":             p.database.unavailable(),
		"database.configured":              p.database.configured(),
		"database.preInits":                p.database.preInits(),
		"database.preWarms":                p.database.preWarms(),
		"database.probe":                   p.database.probe(probeDescription{}),
		"messaging.unavailable":            p.messaging.unavailable(),
		"messaging.configured":             p.messaging.configured(),
		"messaging.preInits":               p.messaging.preInits(),
		"messaging.preWarms":               p.messaging.preWarms(),
		"messaging.resolvesOnControlPlane": p.messaging.resolvesOnControlPlane(),
		"messaging.probe":                  p.messaging.probe(probeDescription{}),
		"cache.configured":                 p.cache.configured(),
		"cache.preInits":                   p.cache.preInits(),
		"cache.preWarms":                   p.cache.preWarms(),
		"cache.probe":                      p.cache.probe(probeDescription{}),
		"multitenant":                      p.multitenant(),
		"amqpStamps":                       p.tenantStamps(),
		"streamStamps":                     p.tenantStamps(),
		"refusesStreams":                   p.refusesStreams(),
		"sealTenancy":                      p.sealTenancy(),
	}
	if p.multitenant() {
		answers["SetMessagingTenancy"] = p.messaging.tenancy.String()
	}
	return answers
}

// TestNewWithConfigCarriesTheResourcePlan pins the wiring: the Builder plans from its Options
// and the store it hands the managers, App carries that plan, and ModuleDeps' flags are its
// configured answers.
func TestNewWithConfigCarriesTheResourcePlan(t *testing.T) {
	cacheConnector := func(context.Context, string) (cache.Cache, error) { return nil, nil }
	tests := []struct {
		name          string
		dynamicSource bool
		opts          *Options
		presence      [3]keyPresence
		configured    [3]bool
	}{
		{name: "built_in_store", presence: [3]keyPresence{keyAbsent, keyAbsent, keyAbsent}},
		{
			name: "caller_static_store", opts: &Options{ResourceSource: &dynamicResourceSource{}},
			presence: [3]keyPresence{keyAbsent, keyAbsent, keyPresent}, configured: [3]bool{true, true, true},
		},
		{
			name: "dynamic_store", dynamicSource: true, opts: &Options{ResourceSource: &dynamicResourceSource{dynamic: true}},
			configured: [3]bool{true, true, true},
		},
		{
			name: "cache_connector", opts: &Options{CacheConnector: cacheConnector},
			presence: [3]keyPresence{keyAbsent, keyAbsent, keyPresent}, configured: [3]bool{false, false, true},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := defaultTestConfig()
			cfg.Database = config.DatabaseConfig{}
			cfg.Messaging = config.MessagingConfig{}
			if tt.dynamicSource {
				cfg.Source.Type = config.SourceTypeDynamic
			}
			var store TenantStore = config.NewTenantStore(cfg)
			if tt.opts != nil && tt.opts.ResourceSource != nil {
				store = tt.opts.ResourceSource
			}

			app := newConfiguredApp(t, cfg, tt.opts)

			assert.Equal(t, planResources(cfg, tt.opts, store), app.plan)
			gotPresence := [3]keyPresence{app.plan.database.presence, app.plan.messaging.presence, app.plan.cache.presence}
			assert.Equal(t, tt.presence, gotPresence)
			deps := app.registry.deps
			gotConfigured := [3]bool{deps.DBConfigured, deps.MessagingConfigured, deps.CacheConfigured}
			assert.Equal(t, tt.configured, gotConfigured)
		})
	}
}
