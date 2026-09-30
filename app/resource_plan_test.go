package app

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gaborage/go-bricks/cache"
	"github.com/gaborage/go-bricks/config"
	"github.com/gaborage/go-bricks/messaging"
	"github.com/gaborage/go-bricks/multitenant"
)

var errLookupOutlastedTest = errors.New("lookup outlasted the test's one-second bound")

// answeringStore is a caller-supplied TenantStore whose answer for "" is set per kind: a kind
// missing from answers is served, anything else is returned as the lookup's error. block makes
// every lookup wait for its context instead, and give up with errLookupOutlastedTest after a
// second so an unbounded lookup fails the test rather than hanging it. It records how often,
// and under what remaining budget, each kind was asked.
type answeringStore struct {
	dynamic   bool
	answers   map[string]error
	block     bool
	calls     map[string]int
	remaining map[string]time.Duration
}

func (s *answeringStore) ask(ctx context.Context, kind string) error {
	if s.calls == nil {
		s.calls = map[string]int{}
		s.remaining = map[string]time.Duration{}
	}
	s.calls[kind]++
	if deadline, ok := ctx.Deadline(); ok {
		s.remaining[kind] = time.Until(deadline)
	}
	if s.block {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Second):
			return errLookupOutlastedTest
		}
	}
	return s.answers[kind]
}

func (s *answeringStore) DBConfig(ctx context.Context, _ string) (*config.DatabaseConfig, error) {
	if err := s.ask(ctx, componentDatabase); err != nil {
		return nil, err
	}
	return &config.DatabaseConfig{Type: config.PostgreSQL, Host: "db.internal"}, nil
}

func (s *answeringStore) BrokerURL(ctx context.Context, _ string) (string, error) {
	if err := s.ask(ctx, componentMessaging); err != nil {
		return "", err
	}
	return "amqp://broker/", nil
}

func (s *answeringStore) CacheConfig(ctx context.Context, _ string) (*config.CacheConfig, error) {
	if err := s.ask(ctx, componentCache); err != nil {
		return nil, err
	}
	return &config.CacheConfig{Enabled: true}, nil
}

func (s *answeringStore) IsDynamic() bool { return s.dynamic }

func (s *answeringStore) asked() int {
	return s.calls[componentDatabase] + s.calls[componentMessaging] + s.calls[componentCache]
}

// servesNothing answers not_configured for every kind's "".
func servesNothing() map[string]error {
	answers := map[string]error{}
	for _, kind := range []string{componentDatabase, componentMessaging, componentCache} {
		answers[kind] = config.NewNotConfiguredError(kind, "", "")
	}
	return answers
}

// planMode is one deployment mode the build accepts. spec switches inputs on: mt, shared
// (messaging.tenancy), dynamic (source.type dynamic beside a dynamic store), caller (a static
// Options.ResourceSource answering not_configured for "") or serves (one serving ""),
// cacheconn, the db, broker and cache root blocks, dbconn (a root database named by its
// connection string alone), and static tenants: tenants (none sets messaging.url), tenanturl
// (one does) or blankurl (each sets it to whitespace). want is the plan's answer per kind
// (database, messaging, cache); tenantKeys is the messaging row's tenant-keys fact.
type planMode struct {
	name, spec string
	want       [3]string
	tenantKeys keyPresence
}

// Plan cells, spelled as renderPlan renders them: "<tenancy>/<presence>" then every yes answer.
const (
	stPresent     = "st/present preinit prewarm"
	stAbsent      = "st/absent unavailable"
	stAbsentSkip  = "st/absent unavailable skip"
	stRuntime     = "st/runtime prewarm"
	ptPresent     = "pt/present per_tenant"
	ptAbsent      = "pt/absent per_tenant"
	ptUnavailable = "pt/absent unavailable per_tenant"
	ptAbsentSkip  = "pt/absent skip per_tenant"
	ptRuntime     = "pt/runtime per_tenant"
	sharedPresent = "shared/present preinit prewarm"
	sharedAbsent  = "shared/absent unavailable"
	sharedRuntime = "shared/runtime prewarm"
)

// kinds holds one cell per kind: database, messaging, cache.
type kinds = [3]string

// planModes covers every mode of the fact base the build accepts; the comment names the ones a
// case stands for when their plan inputs coincide.
var planModes = []planMode{
	// ST-root, ST-streams
	{name: "st_root", spec: "db broker cache", want: kinds{stPresent, stPresent, stPresent}},
	{name: "st_db_cache_only", spec: "db cache", want: kinds{stPresent, stAbsent, stPresent}},
	{name: "st_dbconn_only", spec: "dbconn", want: kinds{stPresent, stAbsent, stAbsentSkip}},
	// ST-noroot
	{name: "st_noroot", spec: "", want: kinds{stAbsent, stAbsent, stAbsentSkip}},
	// ST-shared-noroot: the ADR-041 env-parity no-op
	{name: "st_shared_noroot", spec: "shared", want: kinds{stAbsent, stAbsent, stAbsentSkip}},
	// ST-dynsrc-dynRS-noroot
	{name: "st_dynamic_noroot", spec: "dynamic", want: kinds{stRuntime, stRuntime, stRuntime}},
	{name: "st_dynamic_root", spec: "dynamic db broker cache", want: kinds{stRuntime, stRuntime, stRuntime}},
	{name: "st_dynamic_cacheconn", spec: "dynamic cacheconn", want: kinds{stRuntime, stRuntime, stRuntime}},
	// ST-staticRS-noroot, a store not serving ""
	{name: "st_caller_noroot", spec: "caller", want: kinds{stAbsent, stAbsent, stAbsentSkip}},
	// ST-staticRS-root: the store decides "", not the root blocks
	{name: "st_caller_root", spec: "caller db broker cache", want: kinds{stAbsent, stAbsent, stAbsentSkip}},
	// ST-customRS-noroot
	{name: "st_caller_serves_noroot", spec: "serves", want: kinds{stPresent, stPresent, stPresent}},
	{name: "st_caller_cacheconn", spec: "caller cacheconn", want: kinds{stAbsent, stAbsent, stPresent}},
	// ST-cacheconn, ST-noroot-cacheconn
	{name: "st_cacheconn_noroot", spec: "cacheconn", want: kinds{stAbsent, stAbsent, stPresent}},
	// MT-static-notenants-noroot
	{name: "mt_noroot", spec: "mt", want: kinds{ptAbsent, ptAbsent, ptAbsentSkip}},
	{name: "mt_rootcache", spec: "mt cache", want: kinds{ptAbsent, ptAbsent, ptPresent}},
	// MT-static-tenants, MT-static-pt
	{name: "mt_tenants_url", spec: "mt tenanturl", want: kinds{ptAbsent, ptAbsent, ptAbsentSkip}, tenantKeys: keyPresent},
	// MT-static-tenants-rootcache
	{name: "mt_tenants_url_rootcache", spec: "mt tenanturl cache", want: kinds{ptAbsent, ptAbsent, ptPresent}, tenantKeys: keyPresent},
	// no static tenant can reach a broker (#1853)
	{name: "mt_tenants_no_url", spec: "mt tenants", want: kinds{ptAbsent, ptUnavailable, ptAbsentSkip}, tenantKeys: keyAbsent},
	// the built-in store serves a whitespace URL, so it counts as set
	{name: "mt_tenants_blank_url", spec: "mt blankurl", want: kinds{ptAbsent, ptAbsent, ptAbsentSkip}, tenantKeys: keyPresent},
	// a caller store decides the tenant keys, not the tenants block
	{name: "mt_tenants_no_url_caller", spec: "mt tenants caller", want: kinds{ptAbsent, ptAbsent, ptAbsentSkip}},
	// MT-static-notenants-rootdb, MT-notenants-pt-root
	{name: "mt_root", spec: "mt db broker", want: kinds{ptPresent, ptPresent, ptAbsentSkip}},
	// MT-dynsrc, MT-dyn-pt
	{name: "mt_dynamic", spec: "mt dynamic db", want: kinds{ptRuntime, ptRuntime, ptRuntime}},
	// MT-cacheconn
	{name: "mt_cacheconn", spec: "mt cacheconn", want: kinds{ptAbsent, ptAbsent, ptPresent}},
	// MT-shared-root, MT-static-shared-root, MT-shared-streams
	{name: "mt_shared_root", spec: "mt shared broker", want: kinds{ptAbsent, sharedPresent, ptAbsentSkip}},
	// MT-shared-noroot, MT-static-shared-noroot
	{name: "mt_shared_noroot", spec: "mt shared", want: kinds{ptAbsent, sharedAbsent, ptAbsentSkip}},
	// MT-dyn-shared-noroot
	{name: "mt_shared_dynamic", spec: "mt shared dynamic", want: kinds{ptRuntime, sharedRuntime, ptRuntime}},
	// a store not serving ""
	{name: "mt_shared_caller_noroot", spec: "mt shared caller", want: kinds{ptAbsent, sharedAbsent, ptAbsentSkip}},
	// MT-customRS-shared-noroot
	{name: "mt_shared_caller_serves", spec: "mt shared serves", want: kinds{ptPresent, sharedPresent, ptPresent}},
}

// planModeInputs is what one mode hands planResources: a validated-shape config, Options (nil
// unless one is set), the store FactoryResolver serves, and the caller's store when there is one.
type planModeInputs struct {
	cfg    *config.Config
	opts   *Options
	store  TenantStore
	caller *answeringStore
}

func (m *planMode) inputs() planModeInputs {
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
	if on["dbconn"] {
		cfg.Database.Type = config.PostgreSQL
		cfg.Database.ConnectionString = "postgres://db.internal/app"
	}
	if on["broker"] {
		cfg.Messaging.Broker.URL = "amqp://broker/"
	}
	cfg.Cache.Enabled = on["cache"]
	if on["tenants"] || on["tenanturl"] || on["blankurl"] {
		cfg.Multitenant.Tenants = map[string]config.TenantEntry{"acme": {}, "globex": {}}
	}
	if on["tenanturl"] {
		cfg.Multitenant.Tenants["acme"] = config.TenantEntry{Messaging: config.TenantMessagingConfig{URL: "amqp://acme/"}}
	}
	if on["blankurl"] {
		for id := range cfg.Multitenant.Tenants {
			cfg.Multitenant.Tenants[id] = config.TenantEntry{Messaging: config.TenantMessagingConfig{URL: "  "}}
		}
	}

	var opts *Options
	var caller *answeringStore
	if on["dynamic"] || on["caller"] || on["serves"] {
		caller = &answeringStore{dynamic: on["dynamic"]}
		if on["caller"] {
			caller.answers = servesNothing()
		}
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
	return planModeInputs{cfg: cfg, opts: opts, store: newFactoryResolverForConfig(opts, cfg).ResourceSource(cfg), caller: caller}
}

func (in planModeInputs) plan(t *testing.T) resourcePlan {
	t.Helper()
	plan, err := planResources(context.Background(), in.cfg, in.opts, in.store, in.opts == nil || in.opts.ResourceSource == nil)
	require.NoError(t, err)
	return plan
}

// fixturePlan is the plan a fixture App built from cfg gets: cfg's built-in store beside a
// CacheConnector, which is how the fixtures' cache managers dial. A nil cfg plans nothing.
func fixturePlan(cfg *config.Config) resourcePlan {
	if cfg == nil {
		return resourcePlan{}
	}
	opts := &Options{CacheConnector: func(context.Context, string) (cache.Cache, error) { return nil, nil }}
	plan, err := planResources(context.Background(), cfg, opts, config.NewTenantStore(cfg), true)
	if err != nil {
		panic(err) // the built-in store answers "" with a configuration or not_configured only
	}
	return plan
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

// TestResourcePlan pins the plan's answers in every mode, that only the messaging row knows its
// tenant keys, and who planning asks: a static caller store once per kind (never for the cache
// behind a CacheConnector), a dynamic store never.
func TestResourcePlan(t *testing.T) {
	for _, m := range planModes {
		t.Run(m.name, func(t *testing.T) {
			in := m.inputs()

			plan := in.plan(t)
			assert.Equal(t, m.want, renderPlan(plan))
			assert.Equal(t, [3]keyPresence{keyAtRuntime, m.tenantKeys, keyAtRuntime},
				[3]keyPresence{plan.database.tenantKeys, plan.messaging.tenantKeys, plan.cache.tenantKeys})

			if in.caller == nil {
				return
			}
			if in.caller.dynamic {
				assert.Zero(t, in.caller.asked(), "a dynamic store is never asked")
				return
			}
			assert.Equal(t, 1, in.caller.calls[componentDatabase], "database")
			assert.Equal(t, 1, in.caller.calls[componentMessaging], "messaging")
			wantCache := 1
			if in.opts.CacheConnector != nil {
				wantCache = 0
			}
			assert.Equal(t, wantCache, in.caller.calls[componentCache], "cache")
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

// TestResourcePlanPresenceMatchesTheStore is the presence property: wherever the store serving
// "" is static and no CacheConnector speaks for the cache, each kind's presence is exactly that
// store's answer for "".
func TestResourcePlanPresenceMatchesTheStore(t *testing.T) {
	ctx := context.Background()
	asPresence := func(err error) keyPresence {
		if err == nil {
			return keyPresent
		}
		require.True(t, config.IsNotConfigured(err), "unexpected store error: %v", err)
		return keyAbsent
	}
	for _, m := range planModes {
		in := m.inputs()
		if in.store.IsDynamic() {
			continue
		}
		plan := in.plan(t)
		_, dbErr := in.store.DBConfig(ctx, "")
		_, msgErr := in.store.BrokerURL(ctx, "")
		assert.Equal(t, asPresence(dbErr), plan.database.presence, m.name)
		assert.Equal(t, asPresence(msgErr), plan.messaging.presence, m.name)
		if in.opts == nil || in.opts.CacheConnector == nil {
			_, cacheErr := in.store.CacheConfig(ctx, "")
			assert.Equal(t, asPresence(cacheErr), plan.cache.presence, m.name)
		}
	}
}

// TestResourcePlanLookupFailureFailsStartup pins that a lookup of "" failing for any reason but
// not_configured fails planning, naming the kind and the key, and asks no later kind.
func TestResourcePlanLookupFailureFailsStartup(t *testing.T) {
	cause := errors.New("secrets backend unreachable")
	tests := []struct {
		kind   string
		asked  [3]int
		answer map[string]error
	}{
		{kind: componentDatabase, asked: [3]int{1, 0, 0}, answer: map[string]error{componentDatabase: cause}},
		{kind: componentMessaging, asked: [3]int{1, 1, 0}, answer: map[string]error{componentMessaging: cause}},
		{kind: componentCache, asked: [3]int{1, 1, 1}, answer: map[string]error{componentCache: cause}},
	}
	for _, tt := range tests {
		t.Run(tt.kind, func(t *testing.T) {
			store := &answeringStore{answers: tt.answer}

			plan, err := planResources(context.Background(), &config.Config{}, nil, store, false)

			require.ErrorIs(t, err, cause)
			require.EqualError(t, err, "resource plan: "+tt.kind+` lookup of the control-plane key "": secrets backend unreachable`)
			assert.Equal(t, resourcePlan{}, plan)
			gotAsked := [3]int{store.calls[componentDatabase], store.calls[componentMessaging], store.calls[componentCache]}
			assert.Equal(t, tt.asked, gotAsked)
		})
	}
}

// TestResourcePlanLookupHonorsTheKindBudget pins that each kind's lookup asks its own store
// method under its own app.startup budget and lands on its own row, and that a store
// outlasting the budget fails startup.
func TestResourcePlanLookupHonorsTheKindBudget(t *testing.T) {
	cfg := &config.Config{}
	cfg.App.Startup = config.StartupConfig{Database: time.Hour, Messaging: 2 * time.Hour, Cache: 3 * time.Hour}
	store := &answeringStore{answers: map[string]error{
		componentMessaging: config.NewNotConfiguredError(componentMessaging, "", ""),
	}}

	plan, err := planResources(context.Background(), cfg, nil, store, false)

	require.NoError(t, err)
	startup := cfg.App.Startup
	for kind, budget := range map[string]time.Duration{componentDatabase: startup.Database, componentMessaging: startup.Messaging, componentCache: startup.Cache} {
		assert.InDelta(t, budget.Seconds(), store.remaining[kind].Seconds(), 60, kind)
	}
	assert.Equal(t, [3]keyPresence{keyPresent, keyAbsent, keyPresent},
		[3]keyPresence{plan.database.presence, plan.messaging.presence, plan.cache.presence})

	cfg.App.Startup = config.StartupConfig{Database: 20 * time.Millisecond, Messaging: 20 * time.Millisecond, Cache: 20 * time.Millisecond}
	blocking := &answeringStore{block: true}
	_, err = planResources(context.Background(), cfg, nil, blocking, false)
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.ErrorContains(t, err, `resource plan: database lookup of the control-plane key ""`)
}

// TestNewWithConfigCarriesTheResourcePlan pins the wiring: the Builder plans from its Options
// and the store it hands the managers, App carries that plan, and ModuleDeps' flags read
// configured unless the plan says unavailable.
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
			name: "caller_static_store", opts: &Options{ResourceSource: &answeringStore{answers: servesNothing()}},
			presence: [3]keyPresence{keyAbsent, keyAbsent, keyAbsent},
		},
		{
			name: "dynamic_store", dynamicSource: true, opts: &Options{ResourceSource: &answeringStore{dynamic: true}},
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
			builtInStore := true
			if tt.opts != nil && tt.opts.ResourceSource != nil {
				store = tt.opts.ResourceSource
				builtInStore = false
			}

			app := newConfiguredApp(t, cfg, tt.opts)

			want, err := planResources(context.Background(), cfg, tt.opts, store, builtInStore)
			require.NoError(t, err)
			assert.Equal(t, want, app.plan)
			gotPresence := [3]keyPresence{app.plan.database.presence, app.plan.messaging.presence, app.plan.cache.presence}
			assert.Equal(t, tt.presence, gotPresence)
			deps := app.registry.deps
			gotConfigured := [3]bool{deps.DBConfigured, deps.MessagingConfigured, deps.CacheConfigured}
			assert.Equal(t, tt.configured, gotConfigured)
		})
	}
}

// TestNewWithConfigFailsOnAControlPlaneLookupError pins that a failed lookup of "" aborts the
// build.
func TestNewWithConfigFailsOnAControlPlaneLookupError(t *testing.T) {
	store := &answeringStore{answers: map[string]error{componentMessaging: errors.New("vault sealed")}}

	_, _, err := NewWithConfig(defaultTestConfig(), &Options{ResourceSource: store})

	require.Error(t, err)
	require.ErrorContains(t, err, `resource plan: messaging lookup of the control-plane key "": vault sealed`)
	assert.Zero(t, store.calls[componentCache], "planning stops at the first failed kind")
}

// staticTenantsWithoutMessaging is the #1853 shape: per-tenant messaging with static tenants,
// none of which sets messaging.url.
func staticTenantsWithoutMessaging() *config.Config {
	cfg := defaultTestConfig()
	cfg.Database = config.DatabaseConfig{}
	cfg.Messaging = config.MessagingConfig{Tenancy: config.TenancyPerTenant}
	cfg.Multitenant.Enabled = true
	cfg.Multitenant.Resolver.Type = "header"
	cfg.Multitenant.Tenants = map[string]config.TenantEntry{
		"acme": {Database: config.DatabaseConfig{Type: config.PostgreSQL, Host: "db.internal", Port: 5432, Database: "acme", Username: "acme"}},
	}
	return cfg
}

// TestNewWithConfigStaticTenantsWithoutMessagingURL pins that the built-in store's tenant keys
// decide MessagingConfigured, and that a caller store, which may serve tenants the config does
// not name, keeps it true.
func TestNewWithConfigStaticTenantsWithoutMessagingURL(t *testing.T) {
	tests := []struct {
		name       string
		opts       func(*config.Config) *Options
		configured bool
	}{
		{name: "built_in_store", opts: func(*config.Config) *Options { return nil }},
		{
			name:       "caller_static_store",
			opts:       func(cfg *config.Config) *Options { return &Options{ResourceSource: config.NewTenantStore(cfg)} },
			configured: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := staticTenantsWithoutMessaging()

			app := newConfiguredApp(t, cfg, tt.opts(cfg))

			assert.Equal(t, tt.configured, app.registry.deps.MessagingConfigured)
		})
	}
}

// TestStaticTenantsWithoutMessagingURLFailTheAccessor is the positive control for the false
// flag: with a tenant in context, the accessor fails with config_missing.
func TestStaticTenantsWithoutMessagingURLFailTheAccessor(t *testing.T) {
	app := newConfiguredApp(t, staticTenantsWithoutMessaging(), nil)

	_, err := app.registry.deps.Messaging(multitenant.SetTenant(context.Background(), "acme"))

	var cfgErr *config.ConfigError
	require.ErrorAs(t, err, &cfgErr)
	assert.Equal(t, "missing", cfgErr.Category)
	assert.Contains(t, err.Error(), "config_missing")
}
