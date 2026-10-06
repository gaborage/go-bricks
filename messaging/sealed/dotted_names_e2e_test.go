package sealed_test

import (
	"context"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	amqp "github.com/rabbitmq/amqp091-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gaborage/go-bricks/app"
	"github.com/gaborage/go-bricks/config"
	"github.com/gaborage/go-bricks/internal/sealruntime"
	"github.com/gaborage/go-bricks/jose"
	josesealed "github.com/gaborage/go-bricks/jose/sealed"
	jositest "github.com/gaborage/go-bricks/jose/testing"
	"github.com/gaborage/go-bricks/keystore"
	kstest "github.com/gaborage/go-bricks/keystore/testing"
	"github.com/gaborage/go-bricks/logger"
	"github.com/gaborage/go-bricks/messaging"
)

// dottedPayment declares dotted families (ADR-144): the keystore holds their generations
// as payments.sign.v<N> and payments.encrypt.v<N>, written nested in YAML or reached by a
// POSIX variable.
type dottedPayment struct {
	_       struct{}  `seal:"sign=payments.sign,encrypt=payments.encrypt"`
	OrderID string    `json:"orderId"`
	Card    *cardData `json:"card" seal:"subject"`
}

// hyphenPayment is the same event under the hyphenated look-alike families, which are
// different families with -v<N> generations.
type hyphenPayment struct {
	_       struct{}  `seal:"sign=payments-sign,encrypt=payments-encrypt"`
	OrderID string    `json:"orderId"`
	Card    *cardData `json:"card" seal:"subject"`
}

func derPrivateB64(t *testing.T, k *rsa.PrivateKey) string {
	t.Helper()
	der, err := x509.MarshalPKCS8PrivateKey(k)
	require.NoError(t, err)
	return base64.StdEncoding.EncodeToString(der)
}

func derPublicB64(t *testing.T, k *rsa.PublicKey) string {
	t.Helper()
	der, err := x509.MarshalPKIXPublicKey(k)
	require.NoError(t, err)
	return base64.StdEncoding.EncodeToString(der)
}

// loadDottedKeyStore runs config.Load over a nested YAML keystore plus the variables env
// sets, then builds the keystore module from the result: the path a service takes.
func loadDottedKeyStore(t *testing.T, yaml string, env map[string]string) (*config.Config, app.KeyStore) {
	t.Helper()
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(yaml), 0o600))
	t.Chdir(dir)
	// Pinned so an ambient APP_ENV (empty, or naming an overlay) cannot reach this Load.
	t.Setenv("APP_ENV", "test")
	for name, value := range env {
		t.Setenv(name, value)
	}
	cfg, err := config.Load()
	require.NoError(t, err)
	mod := keystore.NewModule()
	require.NoError(t, mod.Init(&app.ModuleDeps{Logger: logger.New("disabled", true), Config: cfg}))
	return cfg, mod.KeyStore()
}

// dottedSignV1YAML provisions payments.sign.v1 nested in YAML; the encrypt generation
// arrives through POSIX variables (dottedEncryptEnv).
func dottedSignV1YAML(t *testing.T) string {
	keys(t)
	return `
keystore:
  keys:
    payments:
      sign:
        v1:
          public: {value: ` + derPublicB64(t, &signPriv.PublicKey) + `}
          private: {value: ` + derPrivateB64(t, signPriv) + `}
`
}

func dottedEncryptEnv(t *testing.T) map[string]string {
	keys(t)
	return map[string]string{
		"KEYSTORE_KEYS_PAYMENTS_ENCRYPT_V1_PUBLIC_VALUE":  derPublicB64(t, &encPriv.PublicKey),
		"KEYSTORE_KEYS_PAYMENTS_ENCRYPT_V1_PRIVATE_VALUE": derPrivateB64(t, encPriv),
	}
}

// sealDotted publishes one event through the typed door under the runtime the config
// built, and returns the bytes the broker would carry.
func sealDotted(t *testing.T, cfg *config.Config, ks app.KeyStore, orderID string) []byte {
	t.Helper()
	sealruntime.Configure(&sealruntime.Runtime{KeyStore: ks, Active: cfg.Messaging.Seal.Active, Tenancy: sealruntime.TenancyDisabled})
	h := declareAs[dottedPayment](t, eventType)
	client := &capturingClient{}
	require.NoError(t, h.Publish(context.Background(), client, dottedPayment{OrderID: orderID, Card: &cardData{PAN: testPAN}}))
	require.Len(t, client.data, 1)
	return client.data[0]
}

// consumeDotted runs body through the real sealed consume door under the runtime the
// config built, returning the event and the delivery's metadata.
func consumeDotted(t *testing.T, cfg *config.Config, ks app.KeyStore, body []byte) (dottedPayment, messaging.Metadata) {
	t.Helper()
	sealruntime.Configure(&sealruntime.Runtime{KeyStore: ks, Active: cfg.Messaging.Seal.Active, Tenancy: sealruntime.TenancyDisabled})
	decls := messaging.NewDeclarations()
	decls.DeclareQueue("q")
	opts := &messaging.ConsumerOptions{Queue: "q", Consumer: "c", EventType: eventType}
	var got dottedPayment
	var meta messaging.Metadata
	messaging.DeclareTypedConsumerWithMeta(decls, opts, func(ctx context.Context, evt dottedPayment, m messaging.Metadata) error {
		got, meta = evt, m
		require.True(t, messaging.IsSealedDelivery(ctx))
		return nil
	})
	require.NoError(t, decls.Validate())
	require.NoError(t, opts.Handler.Handle(t.Context(), &amqp.Delivery{Body: body, Type: eventType}))
	return got, meta
}

// protectedHeader decodes the protected header of a compact JWS or JWE.
func protectedHeader(t *testing.T, compact string) map[string]any {
	t.Helper()
	raw, err := base64.RawURLEncoding.DecodeString(strings.SplitN(compact, ".", 2)[0])
	require.NoError(t, err)
	var hdr map[string]any
	require.NoError(t, json.Unmarshal(raw, &hdr))
	return hdr
}

// innerHeader decodes the Subject member's compact JWE header out of a sealed body.
func innerHeader(t *testing.T, body []byte) map[string]any {
	t.Helper()
	parts := strings.Split(string(body), ".")
	require.Len(t, parts, 3)
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	require.NoError(t, err)
	var doc map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(payload, &doc))
	var compact string
	require.NoError(t, json.Unmarshal(doc["card"], &compact))
	return protectedHeader(t, compact)
}

// TestSealedRoundTripWithDottedNamesFromConfig is the ADR-144 end to end: nested YAML and a
// POSIX variable provision dotted generations, the producer's wire kids and inner iss are
// those names verbatim, the consumer opens them, and the inbox key is <family>:<jti> with
// the dotted family.
func TestSealedRoundTripWithDottedNamesFromConfig(t *testing.T) {
	cfg, ks := loadDottedKeyStore(t, dottedSignV1YAML(t), dottedEncryptEnv(t))
	require.Contains(t, cfg.KeyStore.Keys, "payments.sign.v1")
	require.Contains(t, cfg.KeyStore.Keys, "payments.encrypt.v1", "reached by KEYSTORE_KEYS_PAYMENTS_ENCRYPT_V1_*")

	body := sealDotted(t, cfg, ks, "o-dotted")

	outer := protectedHeader(t, string(body))
	assert.Equal(t, "payments.sign.v1", outer["kid"], "the outer JWS kid is the entry name")
	inner := innerHeader(t, body)
	assert.Equal(t, "payments.encrypt.v1", inner["kid"], "the inner JWE kid is the entry name")
	assert.Equal(t, "payments.sign.v1", inner[josesealed.HeaderIssuer], "the inner iss is the outer kid")

	evt, meta := consumeDotted(t, cfg, ks, body)
	assert.Equal(t, "o-dotted", evt.OrderID)
	require.NotNil(t, evt.Card)
	assert.Equal(t, testPAN, evt.Card.PAN)
	env, ok := meta.Sealed()
	require.True(t, ok)
	assert.Equal(t, "payments.sign.v1", env.SignKid)
	assert.Equal(t, "payments.sign", env.SignFamily)
	assert.Equal(t, "payments.encrypt.v1", env.EncKid)
	key, err := meta.DedupKey()
	require.NoError(t, err)
	assert.True(t, key.Sealed())
	assert.Equal(t, "payments.sign:"+env.JTI, key.String())
}

// TestSealedRotationInADottedFamily: a second dotted generation and the POSIX selector
// MESSAGING_SEAL_ACTIVE_PAYMENTS_SIGN=v2 move new seals onto payments.sign.v2, while a
// body sealed under v1 still opens.
func TestSealedRotationInADottedFamily(t *testing.T) {
	cfgV1, ksV1 := loadDottedKeyStore(t, dottedSignV1YAML(t), dottedEncryptEnv(t))
	bodyV1 := sealDotted(t, cfgV1, ksV1, "o-v1")
	assert.Equal(t, "payments.sign.v1", protectedHeader(t, string(bodyV1))["kid"])

	env := dottedEncryptEnv(t)
	env["MESSAGING_SEAL_ACTIVE_PAYMENTS_SIGN"] = "v2"
	cfg, ks := loadDottedKeyStore(t, dottedSignV1YAML(t)+`
        v2:
          public: {value: `+derPublicB64(t, &sign2.PublicKey)+`}
          private: {value: `+derPrivateB64(t, sign2)+`}
`, env)
	require.Equal(t, map[string]string{"payments.sign": "v2"}, cfg.Messaging.Seal.Active)

	bodyV2 := sealDotted(t, cfg, ks, "o-v2")
	assert.Equal(t, "payments.sign.v2", protectedHeader(t, string(bodyV2))["kid"])

	for _, body := range [][]byte{bodyV1, bodyV2} {
		_, meta := consumeDotted(t, cfg, ks, body)
		sealedEnv, ok := meta.Sealed()
		require.True(t, ok)
		assert.Equal(t, "payments.sign", sealedEnv.SignFamily)
	}
}

// TestSealedLookAlikeFamilyIsARenameNotARotation pins the two consumer outcomes a mixed
// fleet can meet. A consumer declaring the hyphenated look-alike family refuses a dotted
// body with the non-recoverable SEAL_KID_FAMILY_MISMATCH, so a family rename is a
// drain-then-cutover. An ordinary rotation inside a hyphenated family still reaches a
// consumer that lacks the new generation as the recoverable SEAL_KID_UNKNOWN_GENERATION.
func TestSealedLookAlikeFamilyIsARenameNotARotation(t *testing.T) {
	cfg, ks := loadDottedKeyStore(t, dottedSignV1YAML(t), dottedEncryptEnv(t))
	dottedBody := sealDotted(t, cfg, ks, "o-rename")

	keys(t)
	hyphenSpec, err := josesealed.ScanType(reflect.TypeFor[hyphenPayment]())
	require.NoError(t, err)
	hyphenConsumer := jositest.NewTestResolver(map[string]any{
		"payments-sign-v1":    &signPriv.PublicKey,
		"payments-encrypt-v1": encPriv,
	})
	_, err = josesealed.Open(dottedBody, hyphenSpec, &josesealed.OpenOptions{EventType: eventType, Keys: hyphenConsumer}, &hyphenPayment{})
	requireSealCode(t, err, josesealed.CodeKidFamilyMismatch)

	rotated, _, err := josesealed.Seal(hyphenPayment{OrderID: "o-rot", Card: &cardData{PAN: testPAN}}, hyphenSpec, &josesealed.Options{
		SignKid: "payments-sign-v2", EncryptKid: "payments-encrypt-v1", EventType: eventType,
		Keys: jositest.NewTestResolver(map[string]any{"payments-sign-v2": sign2, "payments-encrypt-v1": &encPriv.PublicKey}),
	})
	require.NoError(t, err)
	_, err = josesealed.Open(rotated, hyphenSpec, &josesealed.OpenOptions{EventType: eventType, Keys: hyphenConsumer}, &hyphenPayment{})
	requireSealCode(t, err, josesealed.CodeKidUnknownGeneration)

	// Through the consume seam the same refusal is the recoverable class.
	sealruntime.Configure(&sealruntime.Runtime{KeyStore: kstest.NewMockKeyStore().
		WithPublicKey("payments-sign-v1", &signPriv.PublicKey).WithGeneration("payments-sign", "v1", keystore.RolePublicOnly).
		WithPrivateKey("payments-encrypt-v1", encPriv).WithGeneration("payments-encrypt", "v1", keystore.RolePrivate)})
	factory, ok := sealruntime.Registered().(sealruntime.OpenerProvider)
	require.True(t, ok)
	hyphenRuntimeSpec, err := sealruntime.Registered().ScanType(reflect.TypeFor[hyphenPayment]())
	require.NoError(t, err)
	opener, err := factory.NewOpener(hyphenRuntimeSpec, eventType, sealruntime.Configured())
	require.NoError(t, err)
	_, err = opener.Open(t.Context(), rotated, sealruntime.TenantRule{}, &hyphenPayment{})
	var refused *sealruntime.OpenRefusedError
	require.ErrorAs(t, err, &refused)
	assert.Equal(t, josesealed.CodeKidUnknownGeneration, refused.Code)
	assert.True(t, refused.Recoverable)
	_, err = opener.Open(t.Context(), dottedBody, sealruntime.TenantRule{}, &hyphenPayment{})
	require.ErrorAs(t, err, &refused)
	assert.Equal(t, josesealed.CodeKidFamilyMismatch, refused.Code)
	assert.False(t, refused.Recoverable)
}

func requireSealCode(t *testing.T, err error, code string) {
	t.Helper()
	var jerr *jose.Error
	require.ErrorAs(t, err, &jerr)
	assert.Equal(t, code, jerr.Code)
}
