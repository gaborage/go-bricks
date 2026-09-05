package messaging

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gaborage/go-bricks/messaging/internal/sealruntime"
	"github.com/gaborage/go-bricks/messaging/internal/tenantstamp"
	"github.com/gaborage/go-bricks/multitenant"
)

// sealedEvt is a seal-tagged consumer type with one validated clear member.
type sealedEvt struct {
	_      struct{} `seal:"sign=svc-sign,encrypt=aud-enc"`
	Card   string   `json:"card" seal:"subject"`
	Amount int      `json:"amount" validate:"gte=0"`
}

// fakeOpener records the tid rule of every call and answers with a fixed
// envelope or error, writing a value into out when asked.
type fakeOpener struct {
	mu    sync.Mutex
	rules []sealruntime.TenantRule
	env   sealruntime.Envelope
	err   error
	write func(out any)
}

func (o *fakeOpener) Open(_ context.Context, _ []byte, want sealruntime.TenantRule, out any) (sealruntime.Envelope, error) {
	o.mu.Lock()
	o.rules = append(o.rules, want)
	o.mu.Unlock()
	if o.err != nil {
		return sealruntime.Envelope{}, o.err
	}
	if o.write != nil {
		o.write(out)
	}
	return o.env, nil
}

type consumerSpec struct{}

func (consumerSpec) SignLogical() string    { return "svc-sign" }
func (consumerSpec) EncryptLogical() string { return "aud-enc" }

// consumerCodec is a Codec with a consume side.
type consumerCodec struct {
	opener  *fakeOpener
	newErr  error
	scanned int
}

func (c *consumerCodec) ScanType(t reflect.Type) (sealruntime.Spec, error) {
	c.scanned++
	if hasSealTag(t) {
		return consumerSpec{}, nil
	}
	return nil, nil
}

func (*consumerCodec) NewSealer(sealruntime.Spec, string, *sealruntime.Runtime) (sealruntime.Sealer, error) {
	return nil, errors.New("not the side under test")
}

func (c *consumerCodec) NewOpener(sealruntime.Spec, string, *sealruntime.Runtime) (sealruntime.Opener, error) {
	if c.newErr != nil {
		return nil, c.newErr
	}
	return c.opener, nil
}

// producerOnlyCodec is a Codec without the consume side (no embedding, so no
// promoted NewOpener).
type producerOnlyCodec struct{}

func (producerOnlyCodec) ScanType(t reflect.Type) (sealruntime.Spec, error) {
	if hasSealTag(t) {
		return consumerSpec{}, nil
	}
	return nil, nil
}

func (producerOnlyCodec) NewSealer(sealruntime.Spec, string, *sealruntime.Runtime) (sealruntime.Sealer, error) {
	return nil, errors.New("not the side under test")
}

func installConsumerCodec(t *testing.T, opener *fakeOpener, tenancy sealruntime.Tenancy) {
	t.Helper()
	sealruntime.Reset()
	t.Cleanup(sealruntime.Reset)
	sealruntime.Register(&consumerCodec{opener: opener})
	sealruntime.Configure(&sealruntime.Runtime{KeyStore: stubKeyStore{}, Tenancy: tenancy})
}

func consumerOpts(optional bool) *ConsumerOptions {
	return &ConsumerOptions{Queue: "q", Consumer: "c", EventType: "payment.authorized", TenantOptional: optional}
}

func declareSealed(t *testing.T, opener *fakeOpener, tenancy sealruntime.Tenancy, optional bool, fn func(context.Context, sealedEvt, Metadata) error) MessageHandler {
	t.Helper()
	installConsumerCodec(t, opener, tenancy)
	decls := NewDeclarations()
	decls.DeclareQueue("q")
	opts := consumerOpts(optional)
	DeclareTypedConsumerWithMeta(decls, opts, fn)
	require.NoError(t, decls.Validate())
	require.IsType(t, &sealedHandler[sealedEvt]{}, opts.Handler)
	return opts.Handler
}

func TestDeclareTypedConsumerRefusesSealTaggedTOnTheMetaLessDoor(t *testing.T) {
	installConsumerCodec(t, &fakeOpener{}, sealruntime.TenancyDisabled)
	decls := NewDeclarations()
	decls.DeclareQueue("q")
	DeclareTypedConsumer(decls, consumerOpts(false), func(context.Context, sealedEvt) error { return nil })

	err := decls.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "DeclareTypedConsumerWithMeta")
	assert.Contains(t, err.Error(), "event_type=payment.authorized")

	// A plain T on the same door is untouched.
	plain := NewDeclarations()
	plain.DeclareQueue("q")
	DeclareTypedConsumer(plain, consumerOpts(false), func(context.Context, plainEvent) error { return nil })
	assert.NoError(t, plain.Validate())
}

func TestDeclareTypedConsumerWithMetaSealedStartupMatrix(t *testing.T) {
	newOpenerErr := errors.New("families not provisioned")
	cases := []struct {
		name  string
		setup func(t *testing.T)
		want  error
		text  string
	}{
		{name: "codec_not_linked", setup: func(*testing.T) {}, want: ErrSealingNotLinked},
		{name: "runtime_not_configured", setup: func(*testing.T) { sealruntime.Register(&consumerCodec{}) }, want: sealruntime.ErrNotConfigured},
		{name: "keystore_missing", setup: func(*testing.T) {
			sealruntime.Register(&consumerCodec{})
			sealruntime.Configure(&sealruntime.Runtime{})
		}, want: sealruntime.ErrKeyStoreMissing},
		{name: "codec_without_consume_side", setup: func(*testing.T) {
			sealruntime.Register(producerOnlyCodec{})
			sealruntime.Configure(&sealruntime.Runtime{KeyStore: stubKeyStore{}})
		}, want: ErrSealingNotLinked, text: "no consume side"},
		{name: "opener_startup_failure", setup: func(*testing.T) {
			sealruntime.Register(&consumerCodec{newErr: newOpenerErr})
			sealruntime.Configure(&sealruntime.Runtime{KeyStore: stubKeyStore{}})
		}, want: newOpenerErr, text: "sealed consumer for"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sealruntime.Reset()
			t.Cleanup(sealruntime.Reset)
			tc.setup(t)
			decls := NewDeclarations()
			decls.DeclareQueue("q")
			opts := consumerOpts(false)
			DeclareTypedConsumerWithMeta(decls, opts, func(context.Context, sealedEvt, Metadata) error { return nil })

			err := decls.Validate()
			require.ErrorIs(t, err, tc.want)
			if tc.text != "" {
				assert.Contains(t, err.Error(), tc.text)
			}
			assert.IsType(t, &typedHandler[sealedEvt]{}, opts.Handler, "the declaration still registers so Validate can name it")
		})
	}
}

// nestedSealEvt hides a seal tag where the codec never looks.
type nestedSealEvt struct {
	Inner struct {
		Card string `json:"card" seal:"subject"`
	} `json:"inner"`
}

func TestDeclareTypedConsumerDoorsRefuseANestedSealTag(t *testing.T) {
	installConsumerCodec(t, &fakeOpener{}, sealruntime.TenancyDisabled)

	withMeta := NewDeclarations()
	withMeta.DeclareQueue("q")
	opts := consumerOpts(false)
	DeclareTypedConsumerWithMeta(withMeta, opts, func(context.Context, nestedSealEvt, Metadata) error { return nil })
	err := withMeta.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "nested member Inner.Card")
	assert.IsType(t, &typedHandler[nestedSealEvt]{}, opts.Handler)

	metaLess := NewDeclarations()
	metaLess.DeclareQueue("q")
	DeclareTypedConsumer(metaLess, consumerOpts(false), func(context.Context, nestedSealEvt) error { return nil })
	err = metaLess.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "nested member Inner.Card")

	// The publisher's cycle-guard case: a struct met first as an untagged embed and
	// again as a named field is still refused on the consumer side.
	again := NewDeclarations()
	again.DeclareQueue("q")
	DeclareTypedConsumerWithMeta(again, consumerOpts(false), func(context.Context, embeddedThenNamed, Metadata) error { return nil })
	err = again.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "nested member Inner.Card")
}

func TestDeclareTypedConsumerWithMetaPlainTypeNeverTouchesTheCodec(t *testing.T) {
	sealruntime.Reset()
	t.Cleanup(sealruntime.Reset)
	decls := NewDeclarations()
	decls.DeclareQueue("q")
	opts := consumerOpts(false)
	DeclareTypedConsumerWithMeta(decls, opts, func(context.Context, plainEvent, Metadata) error { return nil })
	require.NoError(t, decls.Validate())
	assert.IsType(t, &typedHandler[plainEvent]{}, opts.Handler)
}

func sealedDelivery(headers amqp.Table) *amqp.Delivery {
	return &amqp.Delivery{Body: []byte("eyJ.eyJ.sig"), Headers: headers, Type: "payment.authorized"}
}

func TestSealedHandlerOpensThenValidatesThenRunsFn(t *testing.T) {
	env := sealruntime.Envelope{
		JTI: "jti-1", IssuedAt: time.Unix(1_800_000_000, 0).UTC(), EventType: "payment.authorized",
		TenantID: "acme", SignKid: "svc-sign-v2", SignFamily: "svc-sign", EncKid: "aud-enc-v1",
	}
	opener := &fakeOpener{env: env, write: func(out any) { *out.(*sealedEvt) = sealedEvt{Card: "4111", Amount: 12} }}

	var got sealedEvt
	var meta Metadata
	var sealedCtx bool
	handler := declareSealed(t, opener, sealruntime.TenancyDisabled, false, func(ctx context.Context, evt sealedEvt, m Metadata) error {
		got, meta, sealedCtx = evt, m, IsSealedDelivery(ctx)
		return nil
	})

	require.NoError(t, handler.Handle(t.Context(), sealedDelivery(nil)))
	assert.Equal(t, sealedEvt{Card: "4111", Amount: 12}, got)
	sealed, ok := meta.Sealed()
	assert.True(t, ok)
	assert.Equal(t, SealedEnvelope(env), sealed)
	key, err := meta.DedupKey()
	require.NoError(t, err)
	assert.Equal(t, "svc-sign:jti-1", key)
	assert.True(t, sealedCtx, "fn runs under the sealed-delivery marker")
	assert.False(t, IsSealedDelivery(t.Context()), "the marker never leaks outside the handler")
	assert.Equal(t, "payment.authorized", handler.EventType())
	assert.Equal(t, "payment.authorized", meta.EventType())
}

func TestSealedHandlerRefusalIsPayloadStageOpenAndNacksWithoutRequeue(t *testing.T) {
	refused := &sealruntime.OpenRefusedError{Code: "SEAL_SIGNATURE_INVALID", Details: map[string]string{"len": "3"}, Cause: errors.New("inner")}
	opener := &fakeOpener{err: refused}
	calls := 0
	handler := declareSealed(t, opener, sealruntime.TenancyDisabled, false, func(context.Context, sealedEvt, Metadata) error {
		calls++
		return nil
	})

	err := handler.Handle(t.Context(), sealedDelivery(nil))
	require.Error(t, err)
	assert.Zero(t, calls, "fn never runs for a refused message")

	var pe *PayloadError
	assert.Contains(t, err.Error(), "open failed")
	assert.Contains(t, err.Error(), "SEAL_SIGNATURE_INVALID (len=3)")
	assert.NotContains(t, err.Error(), "inner", "the opener's cause is in the chain, never in the text")
	require.ErrorAs(t, err, &pe)
	assert.Equal(t, PayloadStageOpen, pe.Stage)
	assert.Equal(t, "payment.authorized", pe.EventType)
	assert.ErrorIs(t, err, ErrPayloadOpenRefused)
	assert.NotErrorIs(t, err, ErrPayloadUndecodable)
	var got *sealruntime.OpenRefusedError
	require.ErrorAs(t, err, &got)
	assert.Same(t, refused, got)

	// Through the classic lane: settled by a nack that does not requeue.
	registry := NewRegistry(&simpleMockAMQPClient{}, &stubLogger{})
	acker := &mockAcknowledger{}
	delivery := sealedDelivery(nil)
	delivery.DeliveryTag, delivery.Acknowledger = 7, acker
	registry.processMessage(context.Background(), &ConsumerDeclaration{Queue: "q", EventType: "payment.authorized", Handler: handler}, delivery, &stubLogger{})
	assert.Equal(t, 1, acker.nackCount)
	assert.Equal(t, 0, acker.ackCount)
	assert.False(t, acker.nackRequeue, "poison never requeues")
}

func TestSealedHandlerValidatesThePlaintext(t *testing.T) {
	opener := &fakeOpener{write: func(out any) { *out.(*sealedEvt) = sealedEvt{Card: "4111", Amount: -1} }}
	handler := declareSealed(t, opener, sealruntime.TenancyDisabled, false, func(context.Context, sealedEvt, Metadata) error {
		t.Error("fn must not run for an invalid plaintext")
		return nil
	})

	err := handler.Handle(t.Context(), sealedDelivery(nil))
	var pe *PayloadError
	require.ErrorAs(t, err, &pe)
	assert.Equal(t, PayloadStageValidate, pe.Stage)
	assert.Equal(t, []string{"sealedEvt.Amount"}, pe.Fields())
	require.ErrorIs(t, err, ErrPayloadInvalid)
}

func TestSealedHandlerNilDeliveryIsADecodeFailure(t *testing.T) {
	opener := &fakeOpener{}
	handler := declareSealed(t, opener, sealruntime.TenancyDisabled, false, func(context.Context, sealedEvt, Metadata) error { return nil })
	err := handler.Handle(t.Context(), nil)
	assert.Empty(t, opener.rules, "the opener is never asked for a nil delivery")
	require.ErrorIs(t, err, ErrPayloadUndecodable)
}

// TestSealedHandlerTenantRuleMatrix pins the tid expectation the door derives per
// tenancy (#1309 G2/G10, #1307): the opener judges the signed tid against it.
func TestSealedHandlerTenantRuleMatrix(t *testing.T) {
	cases := []struct {
		name     string
		tenancy  sealruntime.Tenancy
		optional bool
		headers  amqp.Table
		ctx      func() context.Context
		want     sealruntime.TenantRule
	}{
		{"shared_stamped", sealruntime.TenancyShared, false, amqp.Table{tenantstamp.Header: "acme"}, t.Context, sealruntime.TenantRule{Required: true, Expected: "acme"}},
		// An unusable stamp never reaches the handler (the pipeline refused it); if it
		// did, the door would compare against nothing rather than trust it.
		{"shared_stamp_unusable", sealruntime.TenancyShared, false, amqp.Table{tenantstamp.Header: 7}, t.Context, sealruntime.TenantRule{Required: true}},
		{"shared_unstamped_required", sealruntime.TenancyShared, false, nil, t.Context, sealruntime.TenantRule{Required: true}},
		{"shared_optional_unstamped", sealruntime.TenancyShared, true, nil, t.Context, sealruntime.TenantRule{Required: false}},
		{"shared_optional_stamped", sealruntime.TenancyShared, true, amqp.Table{tenantstamp.Header: "acme"}, t.Context, sealruntime.TenantRule{Required: false, Expected: "acme"}},
		{"shared_carrier_rewritten", sealruntime.TenancyShared, false, amqp.Table{tenantstamp.Header: "mallory"}, t.Context, sealruntime.TenantRule{Required: true, Expected: "mallory"}},
		{"per_tenant_context", sealruntime.TenancyPerTenant, false, nil, func() context.Context { return multitenant.SetTenant(t.Context(), "tenant-b") }, sealruntime.TenantRule{Expected: "tenant-b"}},
		{
			"per_tenant_ignores_carrier", sealruntime.TenancyPerTenant, false,
			amqp.Table{tenantstamp.Header: "acme"},
			func() context.Context { return multitenant.SetTenant(t.Context(), "tenant-b") },
			sealruntime.TenantRule{Expected: "tenant-b"},
		},
		{"disabled", sealruntime.TenancyDisabled, false, amqp.Table{tenantstamp.Header: "acme"}, func() context.Context { return multitenant.SetTenant(t.Context(), "acme") }, sealruntime.TenantRule{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			opener := &fakeOpener{}
			handler := declareSealed(t, opener, tc.tenancy, tc.optional, func(context.Context, sealedEvt, Metadata) error { return nil })
			require.NoError(t, handler.Handle(tc.ctx(), sealedDelivery(tc.headers)))
			require.Len(t, opener.rules, 1)
			assert.Equal(t, tc.want, opener.rules[0])
		})
	}
}

func TestValidateDedupKeyAdmitsSealedKeysOnlyFromSealedDeliveries(t *testing.T) {
	plain := t.Context()
	sealed := context.WithValue(plain, sealedDeliveryKey{}, true)

	require.NoError(t, ValidateDedupKey(sealed, "svc-sign:jti-1"))
	require.ErrorIs(t, ValidateDedupKey(plain, "svc-sign:jti-1"), ErrInvalidEventID, "the same spelling from a header is refused")
	assert.NoError(t, ValidateDedupKey(plain, "jti-1"))
	assert.NoError(t, ValidateDedupKey(sealed, "jti-1"))
	require.ErrorIs(t, ValidateDedupKey(sealed, "a:b:c"), ErrInvalidEventID)
	assert.ErrorIs(t, ValidateDedupKey(sealed, ""), ErrInvalidEventID)
}

func TestIsSealedDedupKeyVariesTheGrammar(t *testing.T) {
	long := func(n int) string { return repeat("k", n) }
	cases := map[string]bool{
		"svc-sign:jti-1":         true,
		"svc-sign:" + long(128):  true,
		long(64) + ":jti":        true,
		"svc-sign:" + long(129):  false,
		long(65) + ":jti":        false,
		"svc-sign:":              false,
		":jti":                   false,
		"svc-sign:jti:extra":     false,
		"svc-sign:has space":     false,
		"jti-only":               false,
		"":                       false,
		"svc-sign:jti\n":         false,
		"svc.sign:jti":           false,
		"svc-sign:jti-1-v2":      true,
		"svc-payments-sign-v2:x": true,
	}
	for key, want := range cases {
		assert.Equal(t, want, IsSealedDedupKey(key), "%q", key)
	}
}

func repeat(s string, n int) string {
	out := make([]byte, 0, n*len(s))
	for range n {
		out = append(out, s...)
	}
	return string(out)
}
