package app

import (
	"context"
	"crypto/rsa"
	"errors"
	"fmt"
	"reflect"
	"sync"
	"testing"

	amqp "github.com/rabbitmq/amqp091-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"

	"github.com/gaborage/go-bricks/config"
	"github.com/gaborage/go-bricks/internal/sealruntime"
	"github.com/gaborage/go-bricks/logger"
	"github.com/gaborage/go-bricks/messaging"
	obtest "github.com/gaborage/go-bricks/observability/testing"
	"github.com/gaborage/go-bricks/server"
)

// namedKeyStore is a key store known by its name only; no key ever resolves.
type namedKeyStore string

func (namedKeyStore) PublicKey(string) (*rsa.PublicKey, error)   { return nil, errors.New("fake") }
func (namedKeyStore) PrivateKey(string) (*rsa.PrivateKey, error) { return nil, errors.New("fake") }
func (namedKeyStore) Secret(string) ([]byte, error)              { return nil, errors.New("fake") }

// sealedOrder carries a seal tag, so the typed doors build it through the registered codec.
type sealedOrder struct {
	Card string `json:"card" seal:"subject"`
}

type stubSealSpec struct{}

func (stubSealSpec) SignLogical() string    { return sealSignFamily }
func (stubSealSpec) EncryptLogical() string { return "app-enc" }

const sealSignFamily = "app-sign"

// recordingCodec stands in for messaging/sealed, which this package cannot link: it records
// the runtime every handle was built with, and its sealer and opener act on that runtime only.
type recordingCodec struct {
	mu      sync.Mutex
	built   []*sealruntime.Runtime
	openers []*stubOpener
}

func (c *recordingCodec) record(rt *sealruntime.Runtime) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.built = append(c.built, rt)
}

// last is the runtime the most recent handle was built with.
func (c *recordingCodec) last(t *testing.T) *sealruntime.Runtime {
	t.Helper()
	c.mu.Lock()
	defer c.mu.Unlock()
	require.NotEmpty(t, c.built, "no seal-tagged handle was built")
	return c.built[len(c.built)-1]
}

func (*recordingCodec) ScanType(t reflect.Type) (sealruntime.Spec, error) {
	if t == reflect.TypeFor[sealedOrder]() {
		return stubSealSpec{}, nil
	}
	return nil, nil
}

func (c *recordingCodec) NewSealer(_ sealruntime.Spec, _ string, rt *sealruntime.Runtime) (sealruntime.Sealer, error) {
	c.record(rt)
	return stubSealer{rt: rt}, nil
}

func (c *recordingCodec) NewOpener(_ sealruntime.Spec, _ string, rt *sealruntime.Runtime) (sealruntime.Opener, error) {
	c.record(rt)
	opener := &stubOpener{rt: rt}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.openers = append(c.openers, opener)
	return opener, nil
}

// judged is every tid rule the codec's openers were asked to apply.
func (c *recordingCodec) judged() []sealruntime.TenantRule {
	c.mu.Lock()
	defer c.mu.Unlock()
	var rules []sealruntime.TenantRule
	for _, o := range c.openers {
		o.mu.Lock()
		rules = append(rules, o.wants...)
		o.mu.Unlock()
	}
	return rules
}

// stubSealer seals into "<key store>/<active sign generation>" and records on its runtime's meter.
type stubSealer struct{ rt *sealruntime.Runtime }

func (s stubSealer) Seal(ctx context.Context, _ any) (data []byte, jti string, err error) {
	s.rt.Metrics.RecordOperation(ctx, sealruntime.OpSeal, 0)
	return fmt.Appendf(nil, "%v/%s", s.rt.KeyStore, s.rt.Active[sealSignFamily]), "jti", nil
}

// stubOpener records the tid rule each open was judged under and records on its runtime's meter.
type stubOpener struct {
	rt    *sealruntime.Runtime
	mu    sync.Mutex
	wants []sealruntime.TenantRule
}

func (o *stubOpener) Open(ctx context.Context, _ []byte, want sealruntime.TenantRule, _ any) (sealruntime.Envelope, error) {
	o.rt.Metrics.RecordOperation(ctx, sealruntime.OpOpen, 0)
	o.mu.Lock()
	defer o.mu.Unlock()
	o.wants = append(o.wants, want)
	return sealruntime.Envelope{JTI: "jti"}, nil
}

// installRecordingCodec registers a fresh recording codec for one test.
func installRecordingCodec(t *testing.T) *recordingCodec {
	t.Helper()
	sealruntime.Reset()
	t.Cleanup(sealruntime.Reset)
	codec := &recordingCodec{}
	sealruntime.Register(codec)
	return codec
}

// sealedPair is one seal-tagged publisher and one seal-tagged consumer declared on one Declarations.
type sealedPair struct {
	pub      *messaging.Publisher[sealedOrder]
	consumer *messaging.ConsumerOptions
}

func declareSealedPair(decls *messaging.Declarations, name string) sealedPair {
	decls.DeclareTopicExchange(name)
	decls.DeclareQueue(name)
	pub := messaging.DeclareTypedPublisher[sealedOrder](decls, &messaging.PublisherOptions{Exchange: name, RoutingKey: name, EventType: name})
	opts := &messaging.ConsumerOptions{Queue: name, Consumer: name, EventType: name}
	messaging.DeclareTypedConsumerWithMeta(decls, opts, func(context.Context, sealedOrder, messaging.Metadata) error { return nil })
	return sealedPair{pub: pub, consumer: opts}
}

// sealedPairModule declares one sealedPair from DeclareMessaging.
type sealedPairModule struct{ pair sealedPair }

func (*sealedPairModule) Name() string           { return "sealed-orders" }
func (*sealedPairModule) Init(*ModuleDeps) error { return nil }
func (*sealedPairModule) Shutdown() error        { return nil }
func (m *sealedPairModule) DeclareMessaging(d *messaging.Declarations) {
	m.pair = declareSealedPair(d, "orders")
}

// sealOps counts the seal-duration recordings per operation.
func sealOps(t *testing.T, mp *obtest.TestMeterProvider) map[string]uint64 {
	t.Helper()
	ops := map[string]uint64{}
	m := obtest.FindMetric(mp.Collect(t), sealruntime.MetricOperationDuration)
	if m == nil {
		return ops
	}
	hist, ok := m.Data.(metricdata.Histogram[float64])
	require.True(t, ok)
	for _, dp := range hist.DataPoints {
		op, _ := dp.Attributes.Value(sealruntime.AttrOperation)
		ops[op.AsString()] += dp.Count
	}
	return ops
}

// firstApp is App A: multi-tenant under shared messaging tenancy, key store "ks-a", sign
// generation v1 active, its own meter, with one sealedPair declared by a module.
type firstApp struct {
	app  *App
	pair sealedPair
	mp   *obtest.TestMeterProvider
}

func newFirstApp(t *testing.T) firstApp {
	t.Helper()
	cfg := &config.Config{Multitenant: config.MultitenantConfig{Enabled: true}, Messaging: config.MessagingConfig{Tenancy: config.TenancyShared}}
	cfg.Messaging.Seal.Active = map[string]string{sealSignFamily: "v1"}
	mp := obtest.NewTestMeterProvider()
	a := &App{cfg: cfg, registry: NewModuleRegistry(&ModuleDeps{Logger: logger.New("error", false), KeyStore: namedKeyStore("ks-a"), MeterProvider: mp})}
	a.installSlots(fixturePlan(cfg))
	mod := &sealedPairModule{}
	require.NoError(t, a.registry.Register(mod))
	require.NoError(t, a.buildMessagingDeclarations())
	require.NoError(t, a.messagingDeclarations.Validate())
	return firstApp{app: a, pair: mod.pair, mp: mp}
}

// requireOwnFacts declares a second sealedPair on A's Declarations, then drives both pairs:
// every handle seals under A's key store and Activation, judges tid under shared tenancy, and
// records on A's meter only.
func (a firstApp) requireOwnFacts(t *testing.T, codec *recordingCodec, other *obtest.TestMeterProvider) {
	t.Helper()
	later := declareSealedPair(a.app.messagingDeclarations, "refunds")
	require.NoError(t, a.app.messagingDeclarations.Validate())
	for _, pair := range []sealedPair{a.pair, later} {
		data, _, err := pair.pub.Seal(context.Background(), sealedOrder{Card: "c"})
		require.NoError(t, err)
		assert.Equal(t, "ks-a/v1", string(data), "seals under A's key store and Activation")
		require.NoError(t, pair.consumer.Handler.Handle(context.Background(), &amqp.Delivery{Body: []byte("sealed")}))
	}
	assert.Equal(t, []sealruntime.TenantRule{{Required: true}, {Required: true}}, codec.judged(),
		"shared tenancy requires a signed tid; the delivery carries no stamp")
	assert.Equal(t, map[string]uint64{sealruntime.OpSeal: 2, sealruntime.OpOpen: 2}, sealOps(t, a.mp))
	assert.Empty(t, sealOps(t, other), "nothing of A's records on B's meter")
}

// newSecondApp is App B: single-tenant, sign generation v2 active, its own meter, and ks as
// its key store when non-nil.
func newSecondApp(t *testing.T, ks KeyStore, opts *Options) (*App, *obtest.TestMeterProvider) {
	t.Helper()
	cfg := minimalAppConfig("/second")
	cfg.Messaging.Seal.Active = map[string]string{sealSignFamily: "v2"}
	b := newConfiguredApp(t, cfg, opts)
	mp := obtest.NewTestMeterProvider()
	b.registry.deps.MeterProvider = mp
	if ks != nil {
		mod := &MockKeyStoreModule{name: "keystore", keyStore: ks}
		mod.On("Init", mock.Anything).Return(nil)
		require.NoError(t, b.RegisterModule(mod))
	}
	return b, mp
}

// requireSecondAppCannotReAim builds App A's declarations, takes App B through start with
// a key store and without one, and requires A's handles to stay on A's facts.
func requireSecondAppCannotReAim(t *testing.T, start func(t *testing.T, ks KeyStore) (*App, *obtest.TestMeterProvider)) {
	server.DefaultRouteRegistry.Clear()
	t.Cleanup(server.DefaultRouteRegistry.Clear)
	for _, tc := range []struct {
		name string
		ks   KeyStore
	}{
		{name: "second_app_with_key_store", ks: namedKeyStore("ks-b")},
		{name: "second_app_without_key_store"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			codec := installRecordingCodec(t)
			first := newFirstApp(t)
			second, other := start(t, tc.ks)
			require.NotNil(t, second.messagingDeclarations, "B built its declarations")
			first.requireOwnFacts(t, codec, other)
		})
	}
}

func TestSecondAppRunCannotReAimSealedHandles(t *testing.T) {
	requireSecondAppCannotReAim(t, func(t *testing.T, ks KeyStore) (*App, *obtest.TestMeterProvider) {
		second, mp := newSecondApp(t, ks, nil)
		require.NoError(t, second.prepareRuntime(context.Background()))
		return second, mp
	})
}

func TestFailedSecondAppRunCannotReAimSealedHandles(t *testing.T) {
	startErr := errors.New("listener refused")
	requireSecondAppCannotReAim(t, func(t *testing.T, ks KeyStore) (*App, *obtest.TestMeterProvider) {
		srv := newMockServer()
		srv.startErr = startErr
		srv.releaseStart()
		second, mp := newSecondApp(t, ks, &Options{Server: srv, SignalHandler: NewMockSignalHandler()})
		require.ErrorIs(t, second.Run(), startErr)
		return second, mp
	})
}

func TestHandBuiltDeclarationsFailNotConfiguredAfterAnAppRan(t *testing.T) {
	installRecordingCodec(t)
	newFirstApp(t)

	pubDecls := messaging.NewDeclarations()
	pubDecls.DeclareTopicExchange("orders")
	messaging.DeclareTypedPublisher[sealedOrder](pubDecls, &messaging.PublisherOptions{Exchange: "orders", RoutingKey: "orders", EventType: "orders"})
	err := pubDecls.Validate()
	require.ErrorIs(t, err, sealruntime.ErrNotConfigured)
	require.ErrorContains(t, err, "messaging: sealing runtime not configured before declarations were collected")

	consumerDecls := messaging.NewDeclarations()
	consumerDecls.DeclareQueue("orders")
	messaging.DeclareTypedConsumerWithMeta(consumerDecls, &messaging.ConsumerOptions{Queue: "orders", Consumer: "orders", EventType: "orders"},
		func(context.Context, sealedOrder, messaging.Metadata) error { return nil })
	err = consumerDecls.Validate()
	require.ErrorIs(t, err, sealruntime.ErrNotConfigured)
	assert.ErrorContains(t, err, "messaging: sealing runtime not configured before declarations were collected")
}

// boundRuntime declares a seal-tagged publisher on decls and returns the runtime the codec
// built it with: the copy of decls' binding every handle receives.
func boundRuntime(t *testing.T, codec *recordingCodec, decls *messaging.Declarations) *sealruntime.Runtime {
	t.Helper()
	pair := declareSealedPair(decls, "probe")
	require.NoError(t, decls.Validate())
	require.NotNil(t, pair.pub)
	return codec.last(t)
}

func TestConfigureSealingMapsTenancyAndFacts(t *testing.T) {
	cases := []struct {
		name    string
		cfg     *config.Config
		tenancy sealruntime.Tenancy
	}{
		{name: "nil_config", cfg: nil, tenancy: sealruntime.TenancyDisabled},
		{name: "multitenant_disabled", cfg: &config.Config{}, tenancy: sealruntime.TenancyDisabled},
		{name: "multitenant_disabled_shared_is_noop", cfg: &config.Config{Messaging: config.MessagingConfig{Tenancy: config.TenancyShared}}, tenancy: sealruntime.TenancyDisabled},
		{name: "shared", cfg: &config.Config{Multitenant: config.MultitenantConfig{Enabled: true}, Messaging: config.MessagingConfig{Tenancy: config.TenancyShared}}, tenancy: sealruntime.TenancyShared},
		{name: "per_tenant", cfg: &config.Config{Multitenant: config.MultitenantConfig{Enabled: true}}, tenancy: sealruntime.TenancyPerTenant},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			codec := installRecordingCodec(t)
			a := &App{cfg: tc.cfg, registry: NewModuleRegistry(&ModuleDeps{KeyStore: namedKeyStore("ks")})}
			a.installSlots(fixturePlan(tc.cfg))
			decls := messaging.NewDeclarations()
			a.configureSealing(decls)
			assert.Equal(t, tc.tenancy, boundRuntime(t, codec, decls).Tenancy)
		})
	}
}

func TestConfigureSealingWithoutAKeyStoreBindsNone(t *testing.T) {
	installRecordingCodec(t)
	a := &App{cfg: &config.Config{}}
	decls := messaging.NewDeclarations()
	a.configureSealing(decls)
	declareSealedPair(decls, "orders")
	assert.ErrorIs(t, decls.Validate(), sealruntime.ErrKeyStoreMissing)
}

// TestConfigureSealingReadsTheMessagingRow takes the plan as given: the seal tenancy follows the
// installed messaging row even where the App's config says otherwise.
func TestConfigureSealingReadsTheMessagingRow(t *testing.T) {
	mtShared := &config.Config{Multitenant: config.MultitenantConfig{Enabled: true}, Messaging: config.MessagingConfig{Tenancy: config.TenancyShared}}
	cases := []struct {
		name    string
		cfg     *config.Config
		tenancy kindTenancy
		want    sealruntime.Tenancy
	}{
		{name: "single_tenant_config_shared_row", cfg: &config.Config{}, tenancy: sharedTenancy, want: sealruntime.TenancyShared},
		{name: "single_tenant_config_per_tenant_row", cfg: &config.Config{}, tenancy: perTenantTenancy, want: sealruntime.TenancyPerTenant},
		{name: "multitenant_config_single_tenant_row", cfg: mtShared, tenancy: singleTenant, want: sealruntime.TenancyDisabled},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			codec := installRecordingCodec(t)
			a := &App{cfg: tc.cfg, registry: NewModuleRegistry(&ModuleDeps{KeyStore: namedKeyStore("ks")})}
			a.installSlots(resourcePlan{messaging: kindPlan{kind: componentMessaging, tenancy: tc.tenancy}})
			decls := messaging.NewDeclarations()
			a.configureSealing(decls)
			assert.Equal(t, tc.want, boundRuntime(t, codec, decls).Tenancy)
		})
	}
}

func TestConfigureSealingCarriesKeyStoreActiveAndMeter(t *testing.T) {
	codec := installRecordingCodec(t)
	ks := namedKeyStore("ks")
	mp := obtest.NewTestMeterProvider()
	cfg := &config.Config{}
	cfg.Messaging.Seal.Active = map[string]string{"svc-sign": "v3"}
	a := &App{cfg: cfg, registry: NewModuleRegistry(&ModuleDeps{KeyStore: ks, MeterProvider: mp})}
	decls := messaging.NewDeclarations()
	a.configureSealing(decls)
	cfg.Messaging.Seal.Active["svc-sign"] = "v9" // a write to the App's selector after binding
	rt := boundRuntime(t, codec, decls)
	assert.Equal(t, ks, rt.KeyStore)
	assert.Equal(t, mp, rt.Meter)
	assert.NotNil(t, rt.Metrics)
	assert.Equal(t, "v3", rt.Active["svc-sign"], "a handle declared after the write resolves the selector as bound")
}
