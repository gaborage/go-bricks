package app

import (
	"crypto/rsa"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metricnoop "go.opentelemetry.io/otel/metric/noop"

	"github.com/gaborage/go-bricks/config"
	"github.com/gaborage/go-bricks/messaging"
)

type fakeKeyStore struct{}

func (fakeKeyStore) PublicKey(string) (*rsa.PublicKey, error)   { return nil, errors.New("fake") }
func (fakeKeyStore) PrivateKey(string) (*rsa.PrivateKey, error) { return nil, errors.New("fake") }
func (fakeKeyStore) Secret(string) ([]byte, error)              { return nil, errors.New("fake") }

func TestConfigureSealingMapsTenancyAndFacts(t *testing.T) {
	cases := []struct {
		name    string
		cfg     *config.Config
		tenancy messaging.SealTenancy
	}{
		{name: "nil_config", cfg: nil, tenancy: messaging.SealTenancyDisabled},
		{name: "multitenant_disabled", cfg: &config.Config{}, tenancy: messaging.SealTenancyDisabled},
		{name: "multitenant_disabled_shared_is_noop", cfg: &config.Config{Messaging: config.MessagingConfig{Tenancy: config.TenancyShared}}, tenancy: messaging.SealTenancyDisabled},
		{name: "shared", cfg: &config.Config{Multitenant: config.MultitenantConfig{Enabled: true}, Messaging: config.MessagingConfig{Tenancy: config.TenancyShared}}, tenancy: messaging.SealTenancyShared},
		{name: "per_tenant", cfg: &config.Config{Multitenant: config.MultitenantConfig{Enabled: true}}, tenancy: messaging.SealTenancyPerTenant},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a := &App{cfg: tc.cfg}
			a.configureSealing()
			rt := messaging.SealingRuntime()
			require.NotNil(t, rt)
			assert.Equal(t, tc.tenancy, rt.Tenancy)
			assert.Nil(t, rt.KeyStore)
		})
	}
}

func TestConfigureSealingCarriesKeyStoreActiveAndMeter(t *testing.T) {
	ks := fakeKeyStore{}
	mp := metricnoop.NewMeterProvider()
	cfg := &config.Config{}
	cfg.Messaging.Seal.Active = map[string]string{"svc-sign": "v3"}
	a := &App{cfg: cfg, registry: NewModuleRegistry(&ModuleDeps{KeyStore: ks, MeterProvider: mp})}
	a.configureSealing()
	rt := messaging.SealingRuntime()
	require.NotNil(t, rt)
	assert.Equal(t, ks, rt.KeyStore)
	assert.Equal(t, mp, rt.Meter)
	assert.Equal(t, "v3", rt.Active["svc-sign"])
}
