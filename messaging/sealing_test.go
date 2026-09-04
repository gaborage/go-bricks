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
	"github.com/gaborage/go-bricks/messaging/internal/tenantstamp"
	"github.com/gaborage/go-bricks/multitenant"
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

func (stubKeyStore) PublicKey(string) (*rsa.PublicKey, error)   { return nil, errors.New("stub") }
func (stubKeyStore) PrivateKey(string) (*rsa.PrivateKey, error) { return nil, errors.New("stub") }

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

func TestIsSealTagged(t *testing.T) {
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
		// Misplaced tags are refused by DeclareTypedPublisher, so the probe says
		// yes for them too: every lane guard fails closed on the same set.
		"tagged_embed": {reflect.TypeOf(namedEmbedEvent{}), true},
		"nested_field": {reflect.TypeOf(struct{ Inner promotedSeal }{}), true},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			// Asked twice: the second answer comes from the per-type cache and
			// must equal the first.
			assert.Equal(t, tc.want, IsSealTagged(tc.t))
			assert.Equal(t, tc.want, IsSealTagged(tc.t))
		})
	}
}

type nestedSubject struct {
	Inner struct {
		Card string `seal:"subject"`
	} `json:"inner"`
}

type deepNestedSubject struct {
	Outer struct {
		Mid struct {
			Card string `seal:"subject"`
		}
	} `json:"outer"`
}

type taggedEmbedSubject struct {
	promotedSeal `json:"inner"`
}

type sentinelWithNestedSubject struct {
	_     struct{} `seal:"sign=svc-sign,encrypt=aud-enc"`
	ID    string   `json:"id"`
	Inner struct {
		Card string `seal:"subject"`
	} `json:"inner"`
}

type selfNested struct {
	Next *selfNested
	ID   string `json:"id"`
}

// embeddedThenNamed reaches promotedSeal twice: promoted (supported) first, then
// as a named field. A cycle guard keyed by type alone would skip the second visit
// and let Inner.Card ship in plaintext.
type embeddedThenNamed struct {
	promotedSeal
	Inner promotedSeal `json:"inner"`
}

func TestMisplacedSealTag(t *testing.T) {
	cases := map[string]struct {
		t    reflect.Type
		want string
	}{
		"plain":                    {reflect.TypeOf(plainEvent{}), ""},
		"supported_top_level":      {reflect.TypeOf(sealedEvent{}), ""},
		"supported_promoted_embed": {reflect.TypeOf(promotedSealEvent{}), ""},
		"named_nested":             {reflect.TypeOf(nestedSubject{}), "Inner.Card"},
		"deep_named_nested":        {reflect.TypeOf(&deepNestedSubject{}), "Outer.Mid.Card"},
		"tagged_embed":             {reflect.TypeOf(taggedEmbedSubject{}), "promotedSeal.Card"},
		"sentinel_plus_nested":     {reflect.TypeOf(sentinelWithNestedSubject{}), "Inner.Card"},
		"recursive_type":           {reflect.TypeOf(selfNested{}), ""},
		"embedded_then_named":      {reflect.TypeOf(embeddedThenNamed{}), "Inner.Card"},
		"non_struct":               {reflect.TypeOf(1), ""},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) { assert.Equal(t, tc.want, misplacedSealTag(tc.t)) })
	}
}

func TestDeclareTypedPublisherRefusesANestedSealTag(t *testing.T) {
	sealruntime.Reset()
	t.Cleanup(sealruntime.Reset)
	codec := &fakeCodec{sealer: &fakeSealer{out: []byte("x")}}
	sealruntime.Register(codec)
	sealruntime.Configure(&sealruntime.Runtime{KeyStore: stubKeyStore{}})
	decls := newSealingDecls()
	h := DeclareTypedPublisher[nestedSubject](decls, sealedOpts())
	err := decls.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "nested member Inner.Card")
	assert.Zero(t, codec.scans, "the codec is never consulted for an unsupported shape")
	client := &capturingClient{}
	assert.Equal(t, err, h.Publish(context.Background(), client, nestedSubject{}), "fail closed: no plaintext")
	assert.Empty(t, client.data)
}

// selfEmbed embeds itself through a pointer: the cycle guard must terminate the
// walk and still answer false for a type with no seal tag anywhere on the path.
type selfEmbed struct {
	*selfEmbed
	ID string `json:"id"`
}

func TestIsSealTaggedTerminatesOnEmbeddingCycle(t *testing.T) {
	var node selfEmbed
	node.selfEmbed = &node // the cycle the walk must survive
	assert.False(t, IsSealTagged(reflect.TypeOf(node)))
}

// shapeTwinSealed and shapeTwinPlain have byte-identical field layouts; only the
// tag differs. The memo is keyed by reflect.Type, so the two must never alias.
type shapeTwinSealed struct {
	ID   string `json:"id"`
	Card string `json:"card" seal:"subject"`
}

type shapeTwinPlain struct {
	ID   string `json:"id"`
	Card string `json:"card"`
}

// TestIsSealTaggedIsTheUnionOfProbeAndMisplaced pins the guard's contract to the
// typed publish door's two refusal rules over one fixture set.
func TestIsSealTaggedIsTheUnionOfProbeAndMisplaced(t *testing.T) {
	for name, typ := range map[string]reflect.Type{
		"plain":                reflect.TypeOf(plainEvent{}),
		"sealed":               reflect.TypeOf(sealedEvent{}),
		"promoted_embed":       reflect.TypeOf(promotedSealEvent{}),
		"named_nested":         reflect.TypeOf(nestedSubject{}),
		"deep_named_nested":    reflect.TypeOf(deepNestedSubject{}),
		"tagged_embed":         reflect.TypeOf(taggedEmbedSubject{}),
		"sentinel_plus_nested": reflect.TypeOf(sentinelWithNestedSubject{}),
		"recursive":            reflect.TypeOf(selfNested{}),
	} {
		t.Run(name, func(t *testing.T) {
			want := hasSealTagIn(typ, nil) || misplacedSealTag(typ) != ""
			assert.Equal(t, want, IsSealTagged(typ))
			assert.Equal(t, name != "plain" && name != "recursive", IsSealTagged(typ), "only the untagged shapes are false")
		})
	}
}

func TestIsSealTaggedCacheDoesNotAliasIdenticalShapes(t *testing.T) {
	assert.True(t, IsSealTagged(reflect.TypeOf(shapeTwinSealed{})))
	assert.False(t, IsSealTagged(reflect.TypeOf(shapeTwinPlain{})))
	// Ask again in the other order: both answers now come from the cache.
	assert.False(t, IsSealTagged(reflect.TypeOf(&shapeTwinPlain{})))
	assert.True(t, IsSealTagged(reflect.TypeOf(&shapeTwinSealed{})))
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
			client := &capturingClient{}
			pubErr := h.Publish(context.Background(), client, sealedEvent{ID: "x", Card: "4111"})
			require.Error(t, pubErr, "a handle whose sealer failed never publishes plaintext")
			assert.Empty(t, client.data)
			_, sealErr := h.Seal(context.Background(), sealedEvent{ID: "x"})
			assert.Equal(t, pubErr, sealErr)
			err := decls.Validate()
			assert.Equal(t, err, pubErr, "Validate and the handle report the same failure")
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

// tenantSealer records the tenant the context carried when Seal ran.
type tenantSealer struct{ seen []string }

func (s *tenantSealer) Seal(ctx context.Context, _ any) ([]byte, error) {
	id, _ := multitenant.GetTenant(ctx)
	s.seen = append(s.seen, id)
	return []byte("sealed"), nil
}

type keyedClient struct {
	capturingClient
	key string
}

func (c *keyedClient) ReplayKey() string { return c.key }

func TestSealResolvesTheTenantLikeTheStampingWrapper(t *testing.T) {
	cases := []struct {
		name    string
		ctxID   string
		client  AMQPClient
		want    string
		wantErr error
	}{
		{name: "no_tenant_anywhere", client: &capturingClient{}, want: ""},
		{name: "context_only", ctxID: "t-ctx", client: &capturingClient{}, want: "t-ctx"},
		{name: "pool_key_only", client: &keyedClient{key: "t-pool"}, want: "t-pool"},
		{name: "context_and_matching_key", ctxID: "t-a", client: &keyedClient{key: "t-a"}, want: "t-a"},
		{name: "context_and_conflicting_key", ctxID: "t-a", client: &keyedClient{key: "t-b"}, wantErr: tenantstamp.ErrConflict},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sealruntime.Reset()
			t.Cleanup(sealruntime.Reset)
			sealer := &tenantSealer{}
			sealruntime.Register(&fakeCodec{sealer: &fakeSealer{}})
			sealruntime.Configure(&sealruntime.Runtime{KeyStore: stubKeyStore{}})
			decls := newSealingDecls()
			h := DeclareTypedPublisher[sealedEvent](decls, sealedOpts())
			require.NoError(t, decls.Validate())
			h.sealer = sealer // observe the context the door hands the sealer
			ctx := context.Background()
			if tc.ctxID != "" {
				ctx = multitenant.SetTenant(ctx, tc.ctxID)
			}
			err := h.Publish(ctx, tc.client, sealedEvent{ID: "x"})
			if tc.wantErr != nil {
				assert.ErrorIs(t, err, tc.wantErr)
				assert.Empty(t, sealer.seen, "a refused tenant never reaches the sealer")
				return
			}
			require.NoError(t, err)
			assert.Equal(t, []string{tc.want}, sealer.seen)
		})
	}
}

func TestSealWithoutAClientUsesTheContextOnly(t *testing.T) {
	sealruntime.Reset()
	t.Cleanup(sealruntime.Reset)
	sealer := &tenantSealer{}
	sealruntime.Register(&fakeCodec{sealer: &fakeSealer{}})
	sealruntime.Configure(&sealruntime.Runtime{KeyStore: stubKeyStore{}})
	decls := newSealingDecls()
	h := DeclareTypedPublisher[sealedEvent](decls, sealedOpts())
	require.NoError(t, decls.Validate())
	h.sealer = sealer
	_, err := h.Seal(multitenant.SetTenant(context.Background(), "t-out"), sealedEvent{ID: "x"})
	require.NoError(t, err)
	_, err = h.Seal(context.Background(), sealedEvent{ID: "x"})
	require.NoError(t, err)
	assert.Equal(t, []string{"t-out", ""}, sealer.seen)
}

func TestCloneCarriesTheSealError(t *testing.T) {
	sealruntime.Reset()
	t.Cleanup(sealruntime.Reset)
	decls := newSealingDecls()
	DeclareTypedPublisher[sealedEvent](decls, sealedOpts())
	require.ErrorIs(t, decls.Validate(), ErrSealingNotLinked)
	assert.ErrorIs(t, decls.Clone().Validate(), ErrSealingNotLinked, "a clone must not pass what its source failed")
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
	assert.NotContains(t, err.Error(), "payment.captured")
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
