package sealed_test

import (
	"context"
	"crypto/rsa"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"

	"github.com/gaborage/go-bricks/jose"
	josesealed "github.com/gaborage/go-bricks/jose/sealed"
	jositest "github.com/gaborage/go-bricks/jose/testing"
	"github.com/gaborage/go-bricks/keystore"
	kstest "github.com/gaborage/go-bricks/keystore/testing"
	"github.com/gaborage/go-bricks/messaging"
	"github.com/gaborage/go-bricks/messaging/internal/sealruntime"
	"github.com/gaborage/go-bricks/messaging/sealed"
	"github.com/gaborage/go-bricks/multitenant"
)

const (
	signFamily = "svc-payments-sign"
	encFamily  = "acme-core-enc"
	eventType  = "payment.authorized"
	testPAN    = "4111111111111111"
)

type cardData struct {
	PAN string `json:"pan"`
}

type paymentAuthorized struct {
	_       struct{}  `seal:"sign=svc-payments-sign,encrypt=acme-core-enc"`
	OrderID string    `json:"orderId"`
	Card    *cardData `json:"card" seal:"subject"`
}

type plainEvent struct {
	ID string `json:"id"`
}

var (
	keysOnce sync.Once
	signPriv *rsa.PrivateKey
	encPriv  *rsa.PrivateKey
	sign2    *rsa.PrivateKey
)

func keys(t *testing.T) {
	t.Helper()
	keysOnce.Do(func() {
		signPriv, _ = jositest.GenerateTestKeyPair(t)
		encPriv, _ = jositest.GenerateTestKeyPair(t)
		sign2, _ = jositest.GenerateTestKeyPair(t)
	})
}

// producerStore is the canonical producer: sign PRIVATE v1, encrypt PUBLIC v1, built on
// the keystore module's own test double (generation index included).
func producerStore(t *testing.T) *kstest.MockKeyStore {
	keys(t)
	return kstest.NewMockKeyStore().
		WithPrivateKey(signFamily+"-v1", signPriv).WithGeneration(signFamily, "v1", keystore.RolePrivate).
		WithPublicKey(encFamily+"-v1", &encPriv.PublicKey).WithGeneration(encFamily, "v1", keystore.RolePublicOnly)
}

func withPrivate(s *kstest.MockKeyStore, logical, version string, k *rsa.PrivateKey) *kstest.MockKeyStore {
	return s.WithPrivateKey(logical+"-"+version, k).WithGeneration(logical, version, keystore.RolePrivate)
}

// consumerResolver is the audience: sign PUBLIC to verify, encrypt PRIVATE to decrypt.
func consumerResolver(t *testing.T) jose.KeyResolver {
	keys(t)
	return jositest.NewTestResolver(map[string]any{signFamily + "-v1": &signPriv.PublicKey, encFamily + "-v1": encPriv})
}

// familyless is a KeyStore that cannot enumerate generations (no embedding, so nothing
// is promoted).
type familyless struct{ s *kstest.MockKeyStore }

func (f familyless) PublicKey(n string) (*rsa.PublicKey, error)   { return f.s.PublicKey(n) }
func (f familyless) PrivateKey(n string) (*rsa.PrivateKey, error) { return f.s.PrivateKey(n) }

type capturingClient struct {
	messaging.AMQPClient
	opts []messaging.PublishOptions
	data [][]byte
}

func (c *capturingClient) PublishToExchange(_ context.Context, options messaging.PublishOptions, data []byte) error {
	c.opts = append(c.opts, options)
	c.data = append(c.data, data)
	return nil
}

func spec(t *testing.T) sealruntime.Spec {
	t.Helper()
	sp, err := sealruntime.Registered().ScanType(reflect.TypeOf(paymentAuthorized{}))
	require.NoError(t, err)
	require.NotNil(t, sp)
	return sp
}

type foreignSpec struct{}

func (foreignSpec) SignLogical() string    { return signFamily }
func (foreignSpec) EncryptLogical() string { return encFamily }

func TestNewSealerStartupMatrix(t *testing.T) {
	codec := sealruntime.Registered()
	sp := spec(t)
	cases := []struct {
		name   string
		store  func(*testing.T) sealruntime.KeyStore
		active map[string]string
		event  string
		spec   sealruntime.Spec
		wantIs error
		text   string
	}{
		{name: "happy", store: func(t *testing.T) sealruntime.KeyStore { return producerStore(t) }, event: eventType},
		{name: "encrypt_private_serves_as_public", store: func(t *testing.T) sealruntime.KeyStore {
			keys(t)
			return withPrivate(withPrivate(kstest.NewMockKeyStore(), signFamily, "v1", signPriv), encFamily, "v1", encPriv)
		}, event: eventType},
		{name: "sign_private_missing", store: func(t *testing.T) sealruntime.KeyStore {
			keys(t)
			return kstest.NewMockKeyStore().
				WithPublicKey(signFamily+"-v1", &signPriv.PublicKey).WithGeneration(signFamily, "v1", keystore.RolePublicOnly).
				WithPublicKey(encFamily+"-v1", &encPriv.PublicKey).WithGeneration(encFamily, "v1", keystore.RolePublicOnly)
		}, event: eventType, wantIs: sealed.ErrRoleMismatch, text: "holds no private key"},
		{name: "encrypt_family_unprovisioned", store: func(t *testing.T) sealruntime.KeyStore {
			keys(t)
			return withPrivate(kstest.NewMockKeyStore(), signFamily, "v1", signPriv)
		}, event: eventType, text: "no provisioned generation"},
		{name: "encrypt_generation_is_a_secret", store: func(t *testing.T) sealruntime.KeyStore {
			keys(t)
			return withPrivate(kstest.NewMockKeyStore(), signFamily, "v1", signPriv).WithGeneration(encFamily, "v1", keystore.RoleSecret)
		}, event: eventType, wantIs: sealed.ErrRoleMismatch, text: "symmetric secret"},
		{name: "two_sign_generations_no_selector", store: func(t *testing.T) sealruntime.KeyStore {
			return withPrivate(producerStore(t), signFamily, "v2", sign2)
		}, event: eventType, text: "no messaging.seal.active"},
		{name: "selector_picks_second", store: func(t *testing.T) sealruntime.KeyStore {
			return withPrivate(producerStore(t), signFamily, "v2", sign2)
		}, active: map[string]string{signFamily: "v2"}, event: eventType},
		{
			name: "selector_unprovisioned", store: func(t *testing.T) sealruntime.KeyStore { return producerStore(t) },
			active: map[string]string{signFamily: "v7"}, event: eventType, text: "unprovisioned generation",
		},
		{
			name: "event_type_empty", store: func(t *testing.T) sealruntime.KeyStore { return producerStore(t) },
			event: "", wantIs: josesealed.ErrSealFailed, text: "EventType",
		},
		{
			name: "store_without_families", store: func(t *testing.T) sealruntime.KeyStore { return familyless{producerStore(t)} },
			event: eventType, wantIs: sealed.ErrKeyStoreNoFamilies,
		},
		{
			name: "foreign_spec", store: func(t *testing.T) sealruntime.KeyStore { return producerStore(t) },
			event: eventType, spec: foreignSpec{}, text: "not produced by this codec",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rt := &sealruntime.Runtime{KeyStore: tc.store(t), Active: tc.active}
			sp := sp
			if tc.spec != nil {
				sp = tc.spec
			}
			sealer, err := codec.NewSealer(sp, tc.event, rt)
			if tc.wantIs == nil && tc.text == "" {
				require.NoError(t, err)
				assert.NotNil(t, sealer)
				return
			}
			require.Error(t, err)
			assert.Nil(t, sealer)
			if tc.wantIs != nil {
				assert.ErrorIs(t, err, tc.wantIs)
			}
			if tc.text != "" {
				assert.Contains(t, err.Error(), tc.text)
			}
		})
	}
	_, err := codec.NewSealer(sp, eventType, nil)
	assert.ErrorIs(t, err, sealruntime.ErrKeyStoreMissing)
	_, err = codec.NewSealer(sp, eventType, &sealruntime.Runtime{})
	assert.ErrorIs(t, err, sealruntime.ErrKeyStoreMissing)
}

func configure(t *testing.T, tenancy sealruntime.Tenancy) *sdkmetric.ManualReader {
	t.Helper()
	reader := sdkmetric.NewManualReader()
	messaging.ConfigureSealing(&messaging.SealRuntime{
		KeyStore: producerStore(t),
		Tenancy:  tenancy,
		Meter:    sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader)),
	})
	return reader
}

func declare(t *testing.T) *messaging.Publisher[paymentAuthorized] {
	t.Helper()
	decls := messaging.NewDeclarations()
	decls.DeclareTopicExchange("payments")
	h := messaging.DeclareTypedPublisher[paymentAuthorized](decls, &messaging.PublisherOptions{Exchange: "payments", RoutingKey: "payment.authorized", EventType: eventType})
	require.NoError(t, decls.Validate())
	return h
}

func openWire(t *testing.T, body []byte, tenant josesealed.TenantExpectation) (*josesealed.Envelope, paymentAuthorized) {
	t.Helper()
	sp, err := josesealed.ScanType(reflect.TypeOf(paymentAuthorized{}))
	require.NoError(t, err)
	var out paymentAuthorized
	env, err := josesealed.Open(body, sp, &josesealed.OpenOptions{EventType: eventType, Tenant: tenant, Keys: consumerResolver(t)}, &out)
	require.NoError(t, err)
	return env, out
}

func sealCount(t *testing.T, reader *sdkmetric.ManualReader) uint64 {
	t.Helper()
	var rm metricdata.ResourceMetrics
	require.NoError(t, reader.Collect(context.Background(), &rm))
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			if m.Name != sealruntime.MetricOperationDuration {
				continue
			}
			hist, ok := m.Data.(metricdata.Histogram[float64])
			require.True(t, ok)
			var n uint64
			for _, dp := range hist.DataPoints {
				op, _ := dp.Attributes.Value(sealruntime.AttrOperation)
				if op.AsString() == sealruntime.OpSeal {
					n += dp.Count
				}
			}
			return n
		}
	}
	return 0
}

func TestSealedPublishLandsOneJWSOnTheDeclaredDestination(t *testing.T) {
	reader := configure(t, sealruntime.TenancyShared)
	h := declare(t)
	client := &capturingClient{}
	ctx := multitenant.SetTenant(context.Background(), "tenant-a")
	before := sealCount(t, reader)

	require.NoError(t, h.Publish(ctx, client, paymentAuthorized{OrderID: "o1", Card: &cardData{PAN: testPAN}}))

	require.Len(t, client.data, 1)
	body := client.data[0]
	assert.Len(t, strings.Split(string(body), "."), 3, "one compact JWS")
	assert.NotContains(t, string(body), testPAN)
	assert.Equal(t, "payments", client.opts[0].Exchange)
	assert.Equal(t, "payment.authorized", client.opts[0].RoutingKey)
	_, hasMarker := client.opts[0].Headers["x-sealed"]
	assert.False(t, hasMarker, "no unsigned sealing marker")
	assert.Equal(t, before+1, sealCount(t, reader), "one seal per Publish")

	env, out := openWire(t, body, josesealed.TenantExpectation{Required: true, Expected: "tenant-a"})
	assert.Equal(t, "tenant-a", env.TenantID, "signed tid mirrors the context tenant")
	assert.Equal(t, eventType, env.EventType)
	assert.Equal(t, signFamily+"-v1", env.SignKid)
	assert.Equal(t, signFamily, env.SignFamily)
	assert.Equal(t, encFamily+"-v1", env.EncKid)
	assert.Equal(t, "o1", out.OrderID)
	require.NotNil(t, out.Card)
	assert.Equal(t, testPAN, out.Card.PAN, "the audience reads the Subject back")
}

func TestSealedPublishOmitsTidWithoutATenant(t *testing.T) {
	configure(t, sealruntime.TenancyDisabled)
	h := declare(t)
	client := &capturingClient{}
	require.NoError(t, h.Publish(context.Background(), client, paymentAuthorized{OrderID: "o2", Card: &cardData{PAN: testPAN}}))
	env, _ := openWire(t, client.data[0], josesealed.TenantExpectation{})
	assert.Empty(t, env.TenantID)
	_, err := josesealed.Open(client.data[0], mustSpec(t), &josesealed.OpenOptions{EventType: eventType, Tenant: josesealed.TenantExpectation{Required: true}, Keys: consumerResolver(t)}, &paymentAuthorized{})
	assert.Error(t, err, "a required tid is absent: the opener refuses, proving tid was not written")
}

func mustSpec(t *testing.T) *josesealed.Spec {
	t.Helper()
	sp, err := josesealed.ScanType(reflect.TypeOf(paymentAuthorized{}))
	require.NoError(t, err)
	return sp
}

func TestSealAndPublishProduceTheSameShapeWithFreshJTI(t *testing.T) {
	reader := configure(t, sealruntime.TenancyDisabled)
	h := declare(t)
	evt := paymentAuthorized{OrderID: "o3", Card: &cardData{PAN: testPAN}}
	client := &capturingClient{}
	before := sealCount(t, reader)
	require.NoError(t, h.Publish(context.Background(), client, evt))
	sealedBytes, err := h.Seal(context.Background(), evt)
	require.NoError(t, err)
	assert.Equal(t, before+2, sealCount(t, reader), "Seal is the same one-shot operation Publish runs")
	assert.Empty(t, client.data[1:], "Seal publishes nothing")

	envA, _ := openWire(t, client.data[0], josesealed.TenantExpectation{})
	envB, _ := openWire(t, sealedBytes, josesealed.TenantExpectation{})
	assert.NotEqual(t, envA.JTI, envB.JTI, "seal runs once per call: each call mints its own jti")
	assert.Equal(t, envA.SignKid, envB.SignKid)
	assert.Equal(t, envA.EncKid, envB.EncKid)
	assert.NotEqual(t, client.data[0], sealedBytes)
}

func TestPublishBytesAreWhatTheClientRetriesWith(t *testing.T) {
	// The handle seals once and hands ONE byte slice to the client; the client's retry loop
	// re-sends that slice, so every attempt carries the same jti. A caller-side retry after
	// the client gives up is a NEW Publish and therefore a new seal — by design.
	configure(t, sealruntime.TenancyDisabled)
	h := declare(t)
	client := &capturingClient{}
	evt := paymentAuthorized{OrderID: "o4", Card: &cardData{PAN: testPAN}}
	require.NoError(t, h.Publish(context.Background(), client, evt))
	require.NoError(t, h.Publish(context.Background(), client, evt))
	require.Len(t, client.data, 2)
	envA, _ := openWire(t, client.data[0], josesealed.TenantExpectation{})
	envB, _ := openWire(t, client.data[1], josesealed.TenantExpectation{})
	assert.NotEqual(t, envA.JTI, envB.JTI)
}

func TestSealerRefusesAConflictingTenant(t *testing.T) {
	configure(t, sealruntime.TenancyPerTenant)
	h := declare(t)
	client := &capturingClient{}
	// tenantstamp.Resolve with an empty replay key accepts any context tenant; a conflict
	// can only come from the wrapper below. Here the context carries a tenant and the tid follows it.
	ctx := multitenant.SetTenant(context.Background(), "tenant-b")
	require.NoError(t, h.Publish(ctx, client, paymentAuthorized{OrderID: "o5", Card: &cardData{PAN: testPAN}}))
	env, _ := openWire(t, client.data[0], josesealed.TenantExpectation{Expected: "tenant-b"})
	assert.Equal(t, "tenant-b", env.TenantID)
}

// pooledClient mimics a per-tenant pooled client: it exposes the pool key the stamping
// wrapper would stamp with. The typed door reads it through messaging's unexported
// replayKeyProvider seam via this method name.
type pooledClient struct {
	capturingClient
	key string
}

func (c *pooledClient) ReplayKey() string { return c.key }

func TestSignedTidFollowsThePoolKeyWhenTheContextHasNoTenant(t *testing.T) {
	configure(t, sealruntime.TenancyPerTenant)
	h := declare(t)
	client := &pooledClient{key: "tenant-pool"}
	require.NoError(t, h.Publish(context.Background(), &client.capturingClient, paymentAuthorized{OrderID: "o6", Card: &cardData{PAN: testPAN}}))
	env, _ := openWire(t, client.data[0], josesealed.TenantExpectation{})
	assert.Empty(t, env.TenantID, "a bare capturing client exposes no pool key: nothing to mirror")

	client = &pooledClient{key: "tenant-pool"}
	require.NoError(t, h.Publish(context.Background(), client, paymentAuthorized{OrderID: "o6", Card: &cardData{PAN: testPAN}}))
	env, _ = openWire(t, client.data[0], josesealed.TenantExpectation{Expected: "tenant-pool"})
	assert.Equal(t, "tenant-pool", env.TenantID, "the signed tid mirrors the stamp the wrapper writes from the pool key")
}

func TestPlainTypeIsUntouchedByTheCodec(t *testing.T) {
	configure(t, sealruntime.TenancyDisabled)
	decls := messaging.NewDeclarations()
	decls.DeclareTopicExchange("payments")
	h := messaging.DeclareTypedPublisher[plainEvent](decls, &messaging.PublisherOptions{Exchange: "payments", RoutingKey: "k", EventType: "plain"})
	require.NoError(t, decls.Validate())
	client := &capturingClient{}
	require.NoError(t, h.Publish(context.Background(), client, plainEvent{ID: "p"}))
	assert.JSONEq(t, `{"id":"p"}`, string(client.data[0]))
	_, err := h.Seal(context.Background(), plainEvent{ID: "p"})
	assert.ErrorIs(t, err, messaging.ErrNotSealTagged)
}

// TestSealTagNameMatchesJoseSealed pins the two spellings of the tag key: jose
// must not import messaging and the messaging probe must not import the codec, so
// the literal exists twice by design and this test is what keeps them one.
func TestSealTagNameMatchesJoseSealed(t *testing.T) {
	assert.Equal(t, josesealed.TagName, messaging.SealTagName)
}

type agreementSubject struct {
	Card string `json:"card" seal:"subject"`
}

type agreementSealed struct {
	_    struct{} `seal:"sign=svc-sign,encrypt=aud-enc"`
	ID   string   `json:"id"`
	Card string   `json:"card" seal:"subject"`
}

type agreementPromotedEmbed struct {
	agreementSubject
	ID string `json:"id"`
}

type agreementTaggedEmbed struct {
	agreementSubject `json:"inner"`
}

type agreementNestedField struct {
	Inner agreementSubject `json:"inner"`
}

type agreementEmbeddedSubject struct {
	_                struct{} `seal:"sign=svc-sign,encrypt=aud-enc"`
	agreementSubject `seal:"subject"`
}

// TestIsSealTaggedAgreesWithScanType runs the probe and the codec's scan over one
// fixture set. Wherever ScanType speaks — a spec or a refusal — the probe says true;
// where ScanType is silent because the tag sits on a named nested field or a tagged
// embed, the probe STILL says true, because DeclareTypedPublisher refuses that shape
// (misplaced tag) and the lane guards must fail closed on the same set. Only a type
// with no seal tag anywhere is false.
func TestIsSealTaggedAgreesWithScanType(t *testing.T) {
	cases := map[string]struct {
		t         reflect.Type
		wantProbe bool
		scanSpeak bool // ScanType returns a spec OR an error
	}{
		"own_field":        {reflect.TypeOf(agreementSealed{}), true, true},
		"own_field_ptr":    {reflect.TypeOf((*agreementSealed)(nil)), true, true},
		"promoted_embed":   {reflect.TypeOf(agreementPromotedEmbed{}), true, true},
		"embedded_subject": {reflect.TypeOf(agreementEmbeddedSubject{}), true, true},
		"tagged_embed":     {reflect.TypeOf(agreementTaggedEmbed{}), true, false},
		"nested_field":     {reflect.TypeOf(agreementNestedField{}), true, false},
		"plain":            {reflect.TypeOf(struct{ ID string }{}), false, false},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			assert.Equal(t, tc.wantProbe, messaging.IsSealTagged(tc.t), "IsSealTagged")
			spec, err := josesealed.ScanType(tc.t)
			spoke := spec != nil || err != nil
			require.Equal(t, tc.scanSpeak, spoke, "ScanType spoke (spec=%v err=%v)", spec != nil, err)
			if spoke {
				assert.True(t, messaging.IsSealTagged(tc.t), "the probe must speak wherever ScanType does")
			}
		})
	}
}
