package messaging

import (
	"context"
	"crypto/rsa"
	"errors"
	"reflect"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gaborage/go-bricks/messaging/internal/sealruntime"
)

type sealedEvent struct {
	_    struct{} `seal:"sign=svc-sign,encrypt=aud-enc"`
	ID   string   `json:"id"`
	Card string   `json:"card" seal:"subject"`
}

type plainEvent struct {
	ID string `json:"id"`
}

type fakeSpec struct{}

func (fakeSpec) SignLogical() string    { return "svc-sign" }
func (fakeSpec) EncryptLogical() string { return "aud-enc" }

// fakeCodec records how the door drives it and returns a canned sealer.
type fakeCodec struct {
	scanErr   error
	scanNil   bool
	sealerErr error
	scans     int
	sealer    *fakeSealer
	gotEvent  string
	gotRT     *sealruntime.Runtime
}

func (c *fakeCodec) ScanType(reflect.Type) (sealruntime.Spec, error) {
	c.scans++
	if c.scanErr != nil {
		return nil, c.scanErr
	}
	if c.scanNil {
		return nil, nil
	}
	return fakeSpec{}, nil
}

func (c *fakeCodec) NewSealer(_ sealruntime.Spec, eventType string, rt *sealruntime.Runtime) (sealruntime.Sealer, error) {
	c.gotEvent, c.gotRT = eventType, rt
	if c.sealerErr != nil {
		return nil, c.sealerErr
	}
	return c.sealer, nil
}

type fakeSealer struct {
	calls int
	out   []byte
	err   error
}

func (s *fakeSealer) Seal(context.Context, any) ([]byte, error) {
	s.calls++
	return s.out, s.err
}

type stubKeyStore struct{}

func (stubKeyStore) PublicKey(string) (*rsaPublicKey, error)   { return nil, errors.New("stub") }
func (stubKeyStore) PrivateKey(string) (*rsaPrivateKey, error) { return nil, errors.New("stub") }

func sealedOpts() *PublisherOptions {
	return &PublisherOptions{Exchange: "payments", RoutingKey: "payment.authorized", EventType: "payment.authorized"}
}

// newSealingDecls returns declarations with the fixture exchange declared, so Validate
// judges the sealing rules rather than a dangling publisher.
func newSealingDecls() *Declarations {
	decls := NewDeclarations()
	decls.DeclareTopicExchange("payments")
	return decls
}

type promotedSeal struct {
	Card string `seal:"subject"`
}

type promotedSealEvent struct {
	promotedSeal
	ID string `json:"id"`
}

type namedEmbedEvent struct {
	promotedSeal `json:"inner"`
}

func TestHasSealTag(t *testing.T) {
	cases := map[string]struct {
		t    reflect.Type
		want bool
	}{
		"sealed_value":   {reflect.TypeOf(sealedEvent{}), true},
		"sealed_pointer": {reflect.TypeOf((**sealedEvent)(nil)), true},
		"plain":          {reflect.TypeOf(plainEvent{}), false},
		"non_struct":     {reflect.TypeOf("s"), false},
		"nil":            {nil, false},
		"subject_only": {reflect.TypeOf(struct {
			A string `seal:"subject"`
		}{}), true},
		"jose_tag_is_not": {reflect.TypeOf(struct {
			_ struct{} `jose:"sign=a,encrypt=b"`
		}{}), false},
		"promoted_embed": {reflect.TypeOf(promotedSealEvent{}), true},
		"tagged_embed":   {reflect.TypeOf(namedEmbedEvent{}), false},
		"nested_field":   {reflect.TypeOf(struct{ Inner promotedSeal }{}), false},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) { assert.Equal(t, tc.want, hasSealTag(tc.t)) })
	}
}

func TestDeclareTypedPublisherPlainTypeNeverTouchesTheCodec(t *testing.T) {
	sealruntime.Reset()
	t.Cleanup(sealruntime.Reset)
	codec := &fakeCodec{}
	sealruntime.Register(codec)
	decls := newSealingDecls()
	h := DeclareTypedPublisher[plainEvent](decls, sealedOpts())
	require.NoError(t, decls.Validate())
	assert.Zero(t, codec.scans)
	assert.Nil(t, h.sealer)
	_, err := h.Seal(context.Background(), plainEvent{ID: "x"})
	assert.ErrorIs(t, err, ErrNotSealTagged)
}

func TestDeclareTypedPublisherSealedStartupFailures(t *testing.T) {
	scanErr := errors.New("bad tag")
	sealerErr := errors.New("no active generation")
	cases := []struct {
		name  string
		setup func(*fakeCodec)
		want  error
		text  string
	}{
		{name: "codec_not_linked", setup: func(*fakeCodec) {}, want: ErrSealingNotLinked, text: "messaging/sealed"},
		{name: "runtime_not_configured", setup: func(c *fakeCodec) { sealruntime.Register(c) }, want: sealruntime.ErrNotConfigured},
		{name: "keystore_missing", setup: func(c *fakeCodec) {
			sealruntime.Register(c)
			sealruntime.Configure(&sealruntime.Runtime{})
		}, want: sealruntime.ErrKeyStoreMissing},
		{name: "scan_refused", setup: func(c *fakeCodec) {
			c.scanErr = scanErr
			sealruntime.Register(c)
			sealruntime.Configure(&sealruntime.Runtime{KeyStore: stubKeyStore{}})
		}, want: scanErr, text: "seal declaration"},
		{name: "codec_ignores_tags", setup: func(c *fakeCodec) {
			c.scanNil = true
			sealruntime.Register(c)
			sealruntime.Configure(&sealruntime.Runtime{KeyStore: stubKeyStore{}})
		}, text: "did not recognize"},
		{name: "producer_startup_refused", setup: func(c *fakeCodec) {
			c.sealerErr = sealerErr
			sealruntime.Register(c)
			sealruntime.Configure(&sealruntime.Runtime{KeyStore: stubKeyStore{}})
		}, want: sealerErr, text: "sealing producer"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sealruntime.Reset()
			t.Cleanup(sealruntime.Reset)
			codec := &fakeCodec{}
			tc.setup(codec)
			decls := newSealingDecls()
			h := DeclareTypedPublisher[sealedEvent](decls, sealedOpts())
			assert.Nil(t, h.sealer, "a refused declaration leaves the handle sealer-less")
			err := decls.Validate()
			require.Error(t, err)
			if tc.want != nil {
				assert.ErrorIs(t, err, tc.want)
			}
			if tc.text != "" {
				assert.Contains(t, err.Error(), tc.text)
			}
			assert.Contains(t, err.Error(), "payment.authorized", "the error names the event type")
		})
	}
}

func TestDeclareTypedPublisherSealedHappyPath(t *testing.T) {
	sealruntime.Reset()
	t.Cleanup(sealruntime.Reset)
	sealer := &fakeSealer{out: []byte("eyJ.sealed.bytes")}
	codec := &fakeCodec{sealer: sealer}
	sealruntime.Register(codec)
	rt := &sealruntime.Runtime{KeyStore: stubKeyStore{}, Active: map[string]string{"svc-sign": "v2"}, Tenancy: sealruntime.TenancyShared}
	sealruntime.Configure(rt)

	decls := newSealingDecls()
	h := DeclareTypedPublisher[sealedEvent](decls, sealedOpts())
	require.NoError(t, decls.Validate())
	assert.Equal(t, 1, codec.scans)
	assert.Equal(t, "payment.authorized", codec.gotEvent)
	require.NotNil(t, codec.gotRT)
	assert.Equal(t, "v2", codec.gotRT.Active["svc-sign"])
	assert.Equal(t, sealruntime.TenancyShared, codec.gotRT.Tenancy)

	client := &capturingClient{}
	require.NoError(t, h.Publish(context.Background(), client, sealedEvent{ID: "o1", Card: "4111"}))
	assert.Equal(t, 1, sealer.calls, "seal runs once per Publish, before the client")
	require.Len(t, client.data, 1)
	assert.Equal(t, "eyJ.sealed.bytes", string(client.data[0]), "the client receives the sealed bytes, never the marshaled event")
	assert.NotContains(t, string(client.data[0]), "4111")
	assert.Equal(t, "payments", client.opts[0].Exchange)
	assert.Equal(t, "payment.authorized", client.opts[0].RoutingKey)
	_, hasSealedHeader := client.opts[0].Headers["x-sealed"]
	assert.False(t, hasSealedHeader, "no unsigned sealing marker on the frame")

	bytes, err := h.Seal(context.Background(), sealedEvent{ID: "o1", Card: "4111"})
	require.NoError(t, err)
	assert.Equal(t, "eyJ.sealed.bytes", string(bytes))
	assert.Equal(t, 2, sealer.calls)
	assert.Len(t, client.data, 1, "Seal publishes nothing")
}

func TestPublishReturnsSealFailureAndPublishesNothing(t *testing.T) {
	sealruntime.Reset()
	t.Cleanup(sealruntime.Reset)
	sealErr := errors.New("seal boom")
	sealruntime.Register(&fakeCodec{sealer: &fakeSealer{err: sealErr}})
	sealruntime.Configure(&sealruntime.Runtime{KeyStore: stubKeyStore{}})
	decls := newSealingDecls()
	h := DeclareTypedPublisher[sealedEvent](decls, sealedOpts())
	require.NoError(t, decls.Validate())
	client := &capturingClient{}
	err := h.Publish(context.Background(), client, sealedEvent{ID: "o1"})
	assert.ErrorIs(t, err, sealErr)
	assert.Contains(t, err.Error(), "seal payment.authorized event")
	assert.Empty(t, client.data)
	_, err = h.Seal(context.Background(), sealedEvent{ID: "o1"})
	assert.ErrorIs(t, err, sealErr)
}

func TestValidateReportsTheFirstSealErrorBeforeOtherRules(t *testing.T) {
	sealruntime.Reset()
	t.Cleanup(sealruntime.Reset)
	decls := newSealingDecls()
	DeclareTypedPublisher[sealedEvent](decls, sealedOpts())
	second := sealedOpts()
	second.EventType = "payment.captured"
	DeclareTypedPublisher[sealedEvent](decls, second)
	err := decls.Validate()
	require.ErrorIs(t, err, ErrSealingNotLinked)
	assert.Contains(t, err.Error(), "payment.authorized", "first recorded error wins")
	assert.Len(t, decls.sealErrors, 2)
}

// capturingClient is the minimal AMQPClient double: it records what the handle publishes.
type capturingClient struct {
	AMQPClient
	opts []PublishOptions
	data [][]byte
}

func (c *capturingClient) PublishToExchange(_ context.Context, options PublishOptions, data []byte) error {
	c.opts = append(c.opts, options)
	c.data = append(c.data, data)
	return nil
}

type (
	rsaPublicKey  = rsa.PublicKey
	rsaPrivateKey = rsa.PrivateKey
)
