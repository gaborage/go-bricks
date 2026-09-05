package sealed_test

import (
	"context"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"

	"github.com/gaborage/go-bricks/jose"
	josesealed "github.com/gaborage/go-bricks/jose/sealed"
	"github.com/gaborage/go-bricks/keystore"
	kstest "github.com/gaborage/go-bricks/keystore/testing"
	"github.com/gaborage/go-bricks/messaging"
	"github.com/gaborage/go-bricks/messaging/internal/sealruntime"
	"github.com/gaborage/go-bricks/messaging/sealed"
)

// The published opener vectors (jose/sealed/testdata) are bound to fixed keys; this
// package replays them through the seam so every rule's code survives the mapping.
const vectorsDir = "../../jose/sealed/testdata"

const (
	vecSignKid   = "svc-payments-sign-v2"
	vecSignKidV1 = "svc-payments-sign-v1"
	vecEncKid    = "acme-core-enc-v1"
	vecTenant    = "tenant-a"
)

type vectorFile struct {
	Positive string `json:"positive"`
	Vectors  []struct {
		Name   string `json:"name"`
		Code   string `json:"code"`
		Layer  string `json:"layer"`
		Slot   string `json:"slot"`
		Tenant *struct {
			Required bool   `json:"required"`
			Expected string `json:"expected"`
		} `json:"tenant"`
		Body string `json:"body"`
	} `json:"vectors"`
}

// vectorConsumerStore is the audience's view of the vector keys: sign PUBLIC (both
// provisioned generations), encrypt PRIVATE.
func vectorConsumerStore(t *testing.T) *kstest.MockKeyStore {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(vectorsDir, "keys.json"))
	require.NoError(t, err)
	var file struct {
		Keys map[string]string `json:"keys"`
	}
	require.NoError(t, json.Unmarshal(raw, &file))
	priv := func(kid string) *rsa.PrivateKey {
		der, err := base64.StdEncoding.DecodeString(file.Keys[kid])
		require.NoError(t, err)
		k, err := x509.ParsePKCS1PrivateKey(der)
		require.NoError(t, err)
		return k
	}
	store := kstest.NewMockKeyStore()
	withPublic(store, signFamily, "v1", &priv(vecSignKidV1).PublicKey)
	withPublic(store, signFamily, "v2", &priv(vecSignKid).PublicKey)
	return withPrivate(store, encFamily, "v1", priv(vecEncKid))
}

func withPublic(s *kstest.MockKeyStore, logical, version string, k *rsa.PublicKey) *kstest.MockKeyStore {
	return s.WithPublicKey(logical+"-"+version, k).WithGeneration(logical, version, keystore.RolePublicOnly)
}

func withSecret(s *kstest.MockKeyStore, logical, version string) *kstest.MockKeyStore {
	return s.WithGeneration(logical, version, keystore.RoleSecret)
}

func loadVectors(t *testing.T) *vectorFile {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(vectorsDir, "vectors.json"))
	require.NoError(t, err)
	var vf vectorFile
	require.NoError(t, json.Unmarshal(raw, &vf))
	require.NotEmpty(t, vf.Vectors)
	return &vf
}

// configureConsumer installs the runtime for a consumer over store and returns the
// metrics reader.
func configureConsumer(t *testing.T, store sealruntime.KeyStore) *sdkmetric.ManualReader {
	t.Helper()
	reader := sdkmetric.NewManualReader()
	messaging.ConfigureSealing(&messaging.SealRuntime{
		KeyStore: store,
		Tenancy:  sealruntime.TenancyShared,
		Meter:    sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader)),
	})
	return reader
}

func newVectorOpener(t *testing.T) (sealruntime.Opener, *sdkmetric.ManualReader) {
	t.Helper()
	reader := configureConsumer(t, vectorConsumerStore(t))
	factory, ok := sealruntime.Registered().(sealruntime.OpenerProvider)
	require.True(t, ok)
	opener, err := factory.NewOpener(spec(t), eventType, sealruntime.Configured())
	require.NoError(t, err)
	return opener, reader
}

// openCount and failureCount read the two open-side instruments.
func openCount(t *testing.T, reader *sdkmetric.ManualReader) uint64 {
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
				if op, _ := dp.Attributes.Value(sealruntime.AttrOperation); op.AsString() == sealruntime.OpOpen {
					n += dp.Count
				}
			}
			return n
		}
	}
	return 0
}

// failureCount sums the open-failure counter for one code, or across every code
// when code is "".
func failureCount(t *testing.T, reader *sdkmetric.ManualReader, code string) int64 {
	t.Helper()
	var rm metricdata.ResourceMetrics
	require.NoError(t, reader.Collect(context.Background(), &rm))
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			if m.Name != sealruntime.MetricOpenFailures {
				continue
			}
			sum, ok := m.Data.(metricdata.Sum[int64])
			require.True(t, ok)
			var n int64
			for _, dp := range sum.DataPoints {
				if c, _ := dp.Attributes.Value(sealruntime.AttrCode); code == "" || c.AsString() == code {
					n += dp.Value
				}
			}
			return n
		}
	}
	return 0
}

// openerCase is one row of the consumer startup matrix.
type openerCase struct {
	name   string
	store  func(*testing.T) sealruntime.KeyStore
	event  string
	spec   sealruntime.Spec
	rt     func(store sealruntime.KeyStore) *sealruntime.Runtime
	wantIs error
	text   string
}

// runtime builds the Runtime the case hands NewOpener: the store, unless the case
// overrides the whole runtime (nil, or one without a key store).
func (tc *openerCase) runtime(store sealruntime.KeyStore) *sealruntime.Runtime {
	if tc.rt != nil {
		return tc.rt(store)
	}
	return &sealruntime.Runtime{KeyStore: store}
}

// check asserts NewOpener's outcome: an opener when nothing is expected to fail,
// otherwise a nil opener and an error matching the sentinel and/or text given.
func (tc *openerCase) check(t *testing.T, opener sealruntime.Opener, err error) {
	t.Helper()
	if tc.wantIs == nil && tc.text == "" {
		require.NoError(t, err)
		assert.NotNil(t, opener)
		return
	}
	require.Error(t, err)
	assert.Nil(t, opener)
	if tc.wantIs != nil {
		require.ErrorIs(t, err, tc.wantIs)
	}
	if tc.text != "" {
		assert.Contains(t, err.Error(), tc.text)
	}
}

func TestNewOpenerStartupMatrix(t *testing.T) {
	codec, ok := sealruntime.Registered().(sealruntime.OpenerProvider)
	require.True(t, ok, "the codec carries its consume side")
	sp := spec(t)
	consumer := func(t *testing.T) sealruntime.KeyStore { return vectorConsumerStore(t) }
	cases := []openerCase{
		{name: "happy", store: consumer, event: eventType},
		{name: "sign_private_serves_as_public", store: func(t *testing.T) sealruntime.KeyStore {
			keys(t)
			return withPrivate(withPrivate(kstest.NewMockKeyStore(), signFamily, "v1", signPriv), encFamily, "v1", encPriv)
		}, event: eventType},
		{name: "sign_family_unprovisioned", store: func(t *testing.T) sealruntime.KeyStore {
			keys(t)
			return withPrivate(kstest.NewMockKeyStore(), encFamily, "v1", encPriv)
		}, event: eventType, wantIs: sealed.ErrFamilyUnprovisioned, text: "sign family"},
		{name: "encrypt_family_unprovisioned", store: func(t *testing.T) sealruntime.KeyStore {
			keys(t)
			return withPublic(kstest.NewMockKeyStore(), signFamily, "v1", &signPriv.PublicKey)
		}, event: eventType, wantIs: sealed.ErrFamilyUnprovisioned, text: "encrypt family"},
		{name: "encrypt_generation_public_only", store: func(t *testing.T) sealruntime.KeyStore {
			keys(t)
			return withPublic(withPublic(kstest.NewMockKeyStore(), signFamily, "v1", &signPriv.PublicKey), encFamily, "v1", &encPriv.PublicKey)
		}, event: eventType, wantIs: sealed.ErrRoleMismatch, text: "holds no private key (the consumer decrypts with it)"},
		{name: "encrypt_second_generation_public_only", store: func(t *testing.T) sealruntime.KeyStore {
			keys(t)
			store := withPublic(kstest.NewMockKeyStore(), signFamily, "v1", &signPriv.PublicKey)
			return withPublic(withPrivate(store, encFamily, "v1", encPriv), encFamily, "v2", &sign2.PublicKey)
		}, event: eventType, wantIs: sealed.ErrRoleMismatch, text: "acme-core-enc-v2"},
		{name: "sign_generation_indexed_without_material", store: func(t *testing.T) sealruntime.KeyStore {
			return vectorConsumerStore(t).WithGeneration(signFamily, "v3", keystore.RolePublicOnly)
		}, event: eventType, wantIs: sealed.ErrGenerationUnresolvable, text: "sign generation svc-payments-sign-v3"},
		{name: "encrypt_generation_indexed_without_material", store: func(t *testing.T) sealruntime.KeyStore {
			return vectorConsumerStore(t).WithGeneration(encFamily, "v2", keystore.RolePrivate)
		}, event: eventType, wantIs: sealed.ErrGenerationUnresolvable, text: "encrypt generation acme-core-enc-v2"},
		{name: "sign_generation_is_a_secret", store: func(t *testing.T) sealruntime.KeyStore {
			keys(t)
			return withPrivate(withSecret(kstest.NewMockKeyStore(), signFamily, "v1"), encFamily, "v1", encPriv)
		}, event: eventType, wantIs: sealed.ErrRoleMismatch, text: "symmetric secret"},
		{name: "event_type_empty", store: consumer, event: "", text: "non-empty EventType"},
		{name: "store_without_families", store: func(t *testing.T) sealruntime.KeyStore { return familyless{vectorConsumerStore(t)} }, event: eventType, wantIs: sealed.ErrKeyStoreNoFamilies},
		{name: "foreign_spec", store: consumer, event: eventType, spec: foreignSpec{}, text: "not produced by this codec"},
		{name: "runtime_nil", store: consumer, event: eventType, rt: func(sealruntime.KeyStore) *sealruntime.Runtime { return nil }, wantIs: sealruntime.ErrKeyStoreMissing},
		{name: "keystore_nil", store: consumer, event: eventType, rt: func(sealruntime.KeyStore) *sealruntime.Runtime { return &sealruntime.Runtime{} }, wantIs: sealruntime.ErrKeyStoreMissing},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := tc.store(t)
			s := tc.spec
			if s == nil {
				s = sp
			}
			opener, err := codec.NewOpener(s, tc.event, tc.runtime(store))
			tc.check(t, opener, err)
		})
	}
}

func TestOpenerOpensThePositiveVector(t *testing.T) {
	opener, reader := newVectorOpener(t)
	vf := loadVectors(t)
	before, failuresBefore := openCount(t, reader), failureCount(t, reader, "")

	var out paymentAuthorized
	env, err := opener.Open(t.Context(), []byte(vf.Positive), sealruntime.TenantRule{Required: true, Expected: vecTenant}, &out)
	require.NoError(t, err)
	assert.Equal(t, paymentAuthorized{OrderID: "ord-1", Card: &cardData{PAN: "4111111111111111"}}, out)
	assert.Equal(t, sealruntime.Envelope{
		JTI: "0f4b7c1e-3d2a-4e8b-9c6d-1a2b3c4d5e6f", IssuedAt: time.Unix(1_800_000_000, 0).UTC(), EventType: eventType,
		TenantID: vecTenant, SignKid: vecSignKid, SignFamily: signFamily, EncKid: vecEncKid,
	}, env)
	assert.Equal(t, before+1, openCount(t, reader), "one open recorded")
	assert.Equal(t, failuresBefore, failureCount(t, reader, ""), "no failure counted")

	// Redelivery: the same bytes yield the same envelope.
	env2, err := opener.Open(t.Context(), []byte(vf.Positive), sealruntime.TenantRule{Expected: vecTenant}, &out)
	require.NoError(t, err)
	assert.Equal(t, env, env2)
}

// TestOpenerMapsEveryPublishedVector drives the published negative set through the
// seam: each refusal keeps its code and details, marks the recoverable class, keeps
// the codec's own error in the chain, and counts once under its code.
func TestOpenerMapsEveryPublishedVector(t *testing.T) {
	opener, reader := newVectorOpener(t)
	vf := loadVectors(t)

	for _, tc := range vf.Vectors {
		t.Run(tc.Name, func(t *testing.T) {
			want := sealruntime.TenantRule{Expected: vecTenant}
			if tc.Tenant != nil {
				want = sealruntime.TenantRule{Required: tc.Tenant.Required, Expected: tc.Tenant.Expected}
			}
			before := failureCount(t, reader, tc.Code)

			var out paymentAuthorized
			env, err := opener.Open(t.Context(), []byte(tc.Body), want, &out)
			require.Error(t, err)
			assert.Zero(t, env)
			assert.Zero(t, out, "nothing decodes on a refused message")

			var refused *sealruntime.OpenRefusedError
			require.ErrorAs(t, err, &refused)
			assert.Equal(t, tc.Code, refused.Code)
			assert.Equal(t, tc.Layer, refused.Details["layer"])
			assert.Equal(t, tc.Slot, refused.Details["slot"])
			assert.Equal(t, tc.Code == josesealed.CodeKidUnknownGeneration, refused.Recoverable)
			var oe *josesealed.OpenError
			require.ErrorAs(t, err, &oe, "the codec's error stays in the chain")
			assert.Equal(t, before+1, failureCount(t, reader, tc.Code), "counted once under its code")
			for _, secret := range []string{"0f4b7c1e", vecTenant, "tenant-b", "payment.voided", "has:colon"} {
				assert.NotContains(t, err.Error(), secret)
			}
		})
	}
}

func TestOpenerRefusalCodeFallsBackToTheJoseCode(t *testing.T) {
	// A bare *jose.Error (no OpenError around it) keeps its code; anything else is
	// the generic open failure. Reached through the exported seam by opening with a
	// resolver-less runtime is impossible, so the mapping is pinned on the error path
	// of an opener whose Open is handed foreign errors via a wrapping opener.
	opener, _ := newVectorOpener(t)
	var out paymentAuthorized
	_, err := opener.Open(t.Context(), []byte("not.a.jws"), sealruntime.TenantRule{}, &out)
	var refused *sealruntime.OpenRefusedError
	require.ErrorAs(t, err, &refused)
	assert.Equal(t, josesealed.CodeNotSealed, refused.Code)
	require.ErrorIs(t, err, josesealed.ErrNotSealed)
	assert.False(t, refused.Recoverable)
	var je *jose.Error
	require.ErrorAs(t, err, &je)
	assert.NotErrorIs(t, err, josesealed.ErrKidUnknownGeneration)
}

// TestNewOpenerTagsEveryProvisionedGenerationAsSeal pins the consumer half of the
// dual-role check: startup tags every provisioned generation of both families
// under "seal" (the accept set IS the local keystore), and a refused consumer
// tags nothing.
func TestNewOpenerTagsEveryProvisionedGenerationAsSeal(t *testing.T) {
	codec, ok := sealruntime.Registered().(sealruntime.OpenerProvider)
	require.True(t, ok)
	store := vectorConsumerStore(t)
	before := len(store.Recorded())
	_, err := codec.NewOpener(spec(t), eventType, &sealruntime.Runtime{KeyStore: store})
	require.NoError(t, err)
	assert.ElementsMatch(t, [][2]string{
		{signFamily + "-v1", keystore.RoleTagSeal}, {signFamily + "-v2", keystore.RoleTagSeal}, {encFamily + "-v1", keystore.RoleTagSeal},
	}, store.Recorded()[before:])

	before = len(store.Recorded())
	_, err = codec.NewOpener(spec(t), "", &sealruntime.Runtime{KeyStore: store})
	require.Error(t, err)
	assert.Len(t, store.Recorded(), before)
}
