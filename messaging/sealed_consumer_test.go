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
// envelope or error, writing a value into out when asked. envFor, when set,
// selects the envelope from the delivery body so concurrent Handles on one
// handler can carry distinct jtis.
type fakeOpener struct {
	mu     sync.Mutex
	rules  []sealruntime.TenantRule
	env    sealruntime.Envelope
	envFor func(body []byte) sealruntime.Envelope
	err    error
	write  func(out any)
}

func (o *fakeOpener) Open(_ context.Context, body []byte, want sealruntime.TenantRule, out any) (sealruntime.Envelope, error) {
	o.mu.Lock()
	o.rules = append(o.rules, want)
	env, envFor, err, write := o.env, o.envFor, o.err, o.write
	o.mu.Unlock()
	if err != nil {
		return sealruntime.Envelope{}, err
	}
	if write != nil {
		write(out)
	}
	if envFor != nil {
		return envFor(body), nil
	}
	return env, nil
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
	assert.Equal(t, "svc-sign:jti-1", key.String())
	assert.True(t, key.Sealed())
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
	assert.ErrorIs(t, err, ErrPayloadOpenRefused)    //nolint:testifylint // peer sentinel probe; the negative claim follows
	assert.NotErrorIs(t, err, ErrPayloadUndecodable) //nolint:testifylint // peer sentinel probe; a second ErrorAs target follows
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

func sealedTestEnv(jti string) sealruntime.Envelope {
	return sealruntime.Envelope{
		JTI: jti, IssuedAt: time.Unix(1_800_000_000, 0).UTC(), EventType: "payment.authorized",
		TenantID: "acme", SignKid: "svc-sign-v2", SignFamily: "svc-sign", EncKid: "aud-enc-v1",
	}
}

// runSealedDoor opens one delivery through the real sealed consume door and
// runs fn with the handler context and the DedupKey that door composed.
func runSealedDoor(t *testing.T, jti string, fn func(ctx context.Context, key DedupKey) error) error {
	t.Helper()
	opener := &fakeOpener{
		env:   sealedTestEnv(jti),
		write: func(out any) { *out.(*sealedEvt) = sealedEvt{Card: "4111", Amount: 12} },
	}
	var result error
	handler := declareSealed(t, opener, sealruntime.TenancyDisabled, false, func(ctx context.Context, _ sealedEvt, m Metadata) error {
		key, err := m.DedupKey()
		require.NoError(t, err)
		result = fn(ctx, key)
		return result
	})
	err := handler.Handle(t.Context(), sealedDelivery(nil))
	require.Equal(t, result, err)
	return result
}

func captureSealedDoor(t *testing.T, jti string) (context.Context, DedupKey) {
	t.Helper()
	var ctx context.Context
	var key DedupKey
	require.NoError(t, runSealedDoor(t, jti, func(c context.Context, k DedupKey) error {
		ctx, key = c, k
		return nil
	}))
	require.True(t, IsSealedDelivery(ctx))
	return ctx, key
}

// TestValidateDedupKeyBindsASealedKeyToItsDelivery pins equality binding
// through the real sealed consume door: a key minted for delivery A is refused
// under B's context, each delivery admits its own key, a plain context still
// refuses, and a wire key under a sealed context still passes. Each refusal
// arm pins its OWN message, so swapping the two sealed messages fails here
// rather than sending an operator to the wrong diagnosis.
func TestValidateDedupKeyBindsASealedKeyToItsDelivery(t *testing.T) {
	ctxA, keyA := captureSealedDoor(t, "jti-a")
	ctxB, keyB := captureSealedDoor(t, "jti-b")
	plain := t.Context()
	wireKey, err := WireDedupKey("evt-1")
	require.NoError(t, err)
	assert.Equal(t, keyA, sealedDedupKey("svc-sign", "jti-a"), "keys minted from the same inputs compare equal")
	assert.NotEqual(t, keyA, keyB)
	assert.False(t, IsSealedDelivery(plain), "the marker never leaks outside the handler")

	// wantMsg is the refusal arm this case must land on, asserted verbatim: the
	// three arms are distinguishable only by this text, so a shared substring
	// would let a swap survive.
	const (
		msgUnbound  = "sealed dedup key outside a sealed delivery"
		msgMismatch = "sealed dedup key belongs to another delivery"
		msgZero     = "zero DedupKey"
	)
	cases := []struct {
		name    string
		ctx     context.Context
		key     DedupKey
		ok      bool
		wantMsg string
	}{
		{name: "a_key_under_b_context", ctx: ctxB, key: keyA, wantMsg: msgMismatch},
		{name: "b_key_under_b_context", ctx: ctxB, key: keyB, ok: true},
		{name: "a_key_under_a_context", ctx: ctxA, key: keyA, ok: true},
		{name: "a_key_under_plain_context", ctx: plain, key: keyA, wantMsg: msgUnbound},
		{name: "wire_key_under_sealed_context", ctx: ctxB, key: wireKey, ok: true},
		{name: "wire_key_under_plain_context", ctx: plain, key: wireKey, ok: true},
		{name: "empty_key_under_sealed_context", ctx: ctxB, key: DedupKey{}, wantMsg: msgZero},
		{name: "empty_key_under_plain_context", ctx: plain, key: DedupKey{}, wantMsg: msgZero},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateDedupKey(tc.ctx, tc.key)
			if tc.ok {
				assert.NoError(t, err)
				return
			}
			require.ErrorIs(t, err, ErrInvalidEventID)
			require.ErrorContains(t, err, tc.wantMsg, "each refusal arm names itself")
			for _, other := range []string{msgUnbound, msgMismatch, msgZero} {
				if other == tc.wantMsg {
					continue
				}
				assert.NotContains(t, err.Error(), other, "arms must not share a message")
			}
			assert.NotContains(t, err.Error(), "jti-a", "the error never carries the key")
			assert.NotContains(t, err.Error(), "jti-b", "the error never carries the key")
		})
	}
}

// TestValidateDedupKeyConcurrentDeliveriesAdmitOnlyTheirOwnKey runs two
// deliveries on one sealed handler at the same time: each worker admits its
// own key and refuses the other's.
func TestValidateDedupKeyConcurrentDeliveriesAdmitOnlyTheirOwnKey(t *testing.T) {
	const jtiA, jtiB = "jti-a", "jti-b"
	opener := &fakeOpener{
		envFor: func(body []byte) sealruntime.Envelope { return sealedTestEnv(string(body)) },
		write:  func(out any) { *out.(*sealedEvt) = sealedEvt{Card: "4111", Amount: 12} },
	}
	handler := declareSealed(t, opener, sealruntime.TenancyDisabled, false, func(ctx context.Context, _ sealedEvt, m Metadata) error {
		own, err := m.DedupKey()
		if err != nil {
			return err
		}
		if !IsSealedDelivery(ctx) {
			return errors.New("expected a sealed delivery context")
		}
		if err := ValidateDedupKey(ctx, own); err != nil {
			return err
		}
		otherJTI := jtiB
		if own == sealedDedupKey("svc-sign", jtiB) {
			otherJTI = jtiA
		}
		if err := ValidateDedupKey(ctx, sealedDedupKey("svc-sign", otherJTI)); err == nil {
			return errors.New("peer delivery's key was admitted")
		} else if !errors.Is(err, ErrInvalidEventID) {
			return err
		}
		return nil
	})

	var wg sync.WaitGroup
	start := make(chan struct{})
	errs := make(chan error, 2)
	for _, jti := range []string{jtiA, jtiB} {
		wg.Add(1)
		go func(jti string) {
			defer wg.Done()
			<-start
			errs <- handler.Handle(t.Context(), &amqp.Delivery{Body: []byte(jti), Type: "payment.authorized"})
		}(jti)
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		assert.NoError(t, err)
	}
}
