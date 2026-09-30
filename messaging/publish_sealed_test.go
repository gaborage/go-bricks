package messaging

import (
	"bytes"
	"context"
	"errors"
	"maps"
	"sync"
	"testing"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gaborage/go-bricks/internal/publishdoor"
	"github.com/gaborage/go-bricks/messaging/internal/sealruntime"
	"github.com/gaborage/go-bricks/multitenant"
	gobrickstrace "github.com/gaborage/go-bricks/trace"
)

// fakeVerifier returns env or err and records every body it was handed; when overwrite is set it
// scribbles over that slice mid-call, as a caller mutating its own buffer would.
type fakeVerifier struct {
	mu        sync.Mutex
	env       SealEnvelope
	err       error
	seen      [][]byte
	overwrite []byte
}

func (v *fakeVerifier) Verify(_ context.Context, body []byte) (SealEnvelope, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.seen = append(v.seen, bytes.Clone(body))
	if v.overwrite != nil {
		copy(v.overwrite, bytes.Repeat([]byte("X"), len(v.overwrite)))
	}
	return v.env, v.err
}

func (v *fakeVerifier) calls() int {
	v.mu.Lock()
	defer v.mu.Unlock()
	return len(v.seen)
}

// verifyingCodec is fakeCodec plus the OPTIONAL producer verification.
type verifyingCodec struct {
	*fakeCodec
	verifier    sealruntime.Verifier
	verifierErr error
}

func (c *verifyingCodec) NewVerifier(sealruntime.Spec, string, *sealruntime.Runtime) (sealruntime.Verifier, error) {
	if c.verifierErr != nil {
		return nil, c.verifierErr
	}
	return c.verifier, nil
}

func declareWithCodec(t *testing.T, codec sealruntime.Codec, opts *PublisherOptions) *Publisher[sealedEvent] {
	t.Helper()
	sealruntime.Reset()
	t.Cleanup(sealruntime.Reset)
	sealruntime.Register(codec)
	sealruntime.Configure(&sealruntime.Runtime{KeyStore: stubKeyStore{}})
	decls := newSealingDecls()
	h := DeclareTypedPublisher[sealedEvent](decls, opts)
	require.NoError(t, decls.Validate())
	return h
}

func declareVerifying(t *testing.T, opts *PublisherOptions, verifier sealruntime.Verifier) *Publisher[sealedEvent] {
	t.Helper()
	return declareWithCodec(t, &verifyingCodec{
		fakeCodec: &fakeCodec{sealer: &fakeSealer{out: []byte("eyJ.sealed.bytes"), jti: "jti-1"}},
		verifier:  verifier,
	}, opts)
}

func TestPublishSealedRefusesAPlainHandle(t *testing.T) {
	sealruntime.Reset()
	t.Cleanup(sealruntime.Reset)
	h := DeclareTypedPublisher[plainEvent](newSealingDecls(), sealedOpts())
	client := &capturingClient{}
	err := h.PublishSealed(context.Background(), client, []byte("eyJ.sealed.bytes"))
	require.ErrorIs(t, err, ErrNotSealTagged)
	assert.Contains(t, err.Error(), "payment.authorized")
	assert.NotContains(t, err.Error(), "outbox", "the text fits Seal and PublishSealed alike")
	assert.Empty(t, client.data)
}

func TestPublishSealedReturnsTheSealStartupError(t *testing.T) {
	sealruntime.Reset()
	t.Cleanup(sealruntime.Reset)
	decls := newSealingDecls()
	h := DeclareTypedPublisher[sealedEvent](decls, sealedOpts())
	require.ErrorIs(t, decls.Validate(), ErrSealingNotLinked)
	client := &capturingClient{}
	require.ErrorIs(t, h.PublishSealed(context.Background(), client, []byte("eyJ.sealed.bytes")), ErrSealingNotLinked)
	assert.Empty(t, client.data)
}

func TestPublishSealedNeedsACodecThatVerifies(t *testing.T) {
	boom := errors.New("verifier boom")
	cases := []struct {
		name  string
		codec sealruntime.Codec
		want  error
	}{
		{name: "codec_without_verification", codec: &fakeCodec{sealer: &fakeSealer{out: []byte("eyJ.sealed.bytes")}}, want: ErrSealingNotLinked},
		{name: "verifier_startup_error", codec: &verifyingCodec{fakeCodec: &fakeCodec{sealer: &fakeSealer{out: []byte("eyJ.sealed.bytes")}}, verifierErr: boom}, want: boom},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := declareWithCodec(t, tc.codec, sealedOpts()) // Validate passes: only PublishSealed needs the verifier
			client := &capturingClient{}
			require.NoError(t, h.Publish(context.Background(), client, sealedEvent{ID: "o1"}))
			require.ErrorIs(t, h.PublishSealed(context.Background(), client, []byte("eyJ.sealed.bytes")), tc.want)
			assert.Len(t, client.data, 1, "PublishSealed published nothing")
		})
	}
}

func TestPublishSealedWrapsAVerificationRefusal(t *testing.T) {
	cause := errors.New("jose open error")
	h := declareVerifying(t, sealedOpts(), &fakeVerifier{err: &SealOpenRefusedError{Code: "SEAL_SIGNATURE_INVALID", Cause: cause}})
	client := &capturingClient{}
	err := h.PublishSealed(context.Background(), client, []byte("eyJ.forged.bytes"))
	require.ErrorIs(t, err, ErrSealedBytesRejected)
	var refused *SealOpenRefusedError
	require.ErrorAs(t, err, &refused)
	assert.Equal(t, "SEAL_SIGNATURE_INVALID", refused.Code)
	require.ErrorIs(t, err, cause)
	assert.Contains(t, err.Error(), "payment.authorized")
	assert.NotContains(t, err.Error(), "eyJ.forged.bytes")
	assert.Empty(t, client.data)
}

func TestPublishSealedTenantRule(t *testing.T) {
	cases := []struct {
		name      string
		signedTid string
		ctxTenant string
		poolKey   string
		want      error
	}{
		{name: "no_tenant_anywhere"},
		{name: "context_tenant_matches", signedTid: "t-a", ctxTenant: "t-a"},
		{name: "pool_key_matches", signedTid: "t-a", poolKey: "t-a"},
		{name: "present_and_different", signedTid: "t-a", ctxTenant: "t-b", want: ErrSealedTenantMismatch},
		{name: "absent_while_context_has_a_tenant", ctxTenant: "t-a", want: ErrSealedTenantMismatch},
		{name: "absent_through_a_per_tenant_client", poolKey: "t-a", want: ErrSealedTenantMismatch},
		{name: "present_while_no_tenant_resolves", signedTid: "t-a", want: ErrSealedTenantMismatch},
		{name: "context_conflicts_with_pool_key", signedTid: "t-a", ctxTenant: "t-a", poolKey: "t-b", want: ErrTenantStampConflict},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			v := &fakeVerifier{env: SealEnvelope{TenantID: tc.signedTid}}
			h := declareVerifying(t, sealedOpts(), v)
			rec := &keyedClient{key: tc.poolKey}
			var client AMQPClient = rec
			if tc.poolKey == "" {
				client = &rec.capturingClient
			}
			ctx := context.Background()
			if tc.ctxTenant != "" {
				ctx = multitenant.SetTenant(ctx, tc.ctxTenant)
			}
			err := h.PublishSealed(ctx, client, []byte("eyJ.stored.bytes"))
			if tc.want == nil {
				require.NoError(t, err)
				assert.Len(t, rec.data, 1)
				return
			}
			require.ErrorIs(t, err, tc.want)
			assert.Empty(t, rec.data, "refused before any broker I/O")
			if errors.Is(tc.want, ErrTenantStampConflict) {
				assert.Zero(t, v.calls(), "the tenant resolves before the bytes are verified")
			}
		})
	}
}

func TestPublishSealedHandsTheDoorTheSameOptionsAsPublish(t *testing.T) {
	opts := sealedOpts()
	opts.Headers = map[string]any{"x-app": "a"}
	opts.Mandatory = true
	h := declareVerifying(t, opts, &fakeVerifier{})
	client := &capturingClient{}
	ctx := context.Background()
	require.NoError(t, h.Publish(ctx, client, sealedEvent{ID: "o1"}))
	require.NoError(t, h.PublishSealed(ctx, client, []byte("eyJ.stored.bytes")))

	require.Len(t, client.opts, 2)
	viaPublish, viaDoor := client.opts[0], client.opts[1]
	require.NotNil(t, viaPublish.props)
	require.NotNil(t, viaDoor.props)
	assert.Equal(t, *viaPublish.props, *viaDoor.props)
	assert.Empty(t, viaDoor.props.MessageID, "the client mints message_id per call")
	assert.Equal(t, publishdoor.ContentTypeJOSE, viaDoor.props.ContentType)
	assert.Equal(t, "payment.authorized", viaDoor.props.EventType)
	viaPublish.props, viaDoor.props = nil, nil
	assert.Equal(t, viaPublish, viaDoor)
	assert.Equal(t, map[string]any{"x-app": "a"}, viaDoor.Headers)
	assert.True(t, viaDoor.Mandatory)
}

func TestPublishSealedPublishesTheBytesItVerified(t *testing.T) {
	caller := []byte("eyJ.original.bytes")
	v := &fakeVerifier{overwrite: caller}
	h := declareVerifying(t, sealedOpts(), v)
	client := &capturingClient{}
	require.NoError(t, h.PublishSealed(context.Background(), client, caller))
	require.Len(t, client.data, 1)
	assert.Equal(t, "eyJ.original.bytes", string(client.data[0]))
	assert.Equal(t, v.seen[0], client.data[0], "the wire carries exactly the bytes verified")
	assert.NotEqual(t, string(caller), string(client.data[0]), "the caller's buffer changed mid-call")
}

func TestPublishSealedStampsLikePublishWithFreshMessageIDs(t *testing.T) {
	h := declareVerifying(t, sealedOpts(), &fakeVerifier{env: SealEnvelope{TenantID: "tenant-a"}})
	ch := &fakeChannel{}
	c := returnTestClient(t, ch)
	ackRetries(ch)
	pooled := newStampingPublisher(c, "tenant-a")
	ctx := context.Background()
	data := []byte("eyJ.stored.bytes")

	require.NoError(t, h.Publish(ctx, pooled, sealedEvent{ID: "o1"}))
	require.NoError(t, h.PublishSealed(ctx, pooled, data))
	require.NoError(t, h.PublishSealed(ctx, pooled, data))

	msgs := ch.publishedMessages()
	require.Len(t, msgs, 3)
	viaPublish, first, second := msgs[0], msgs[1], msgs[2]
	assert.Equal(t, data, first.Body)
	assert.Equal(t, first.Body, second.Body)
	for _, m := range []amqp.Publishing{first, second} {
		assert.Equal(t, "application/jose", m.ContentType)
		assert.Equal(t, "payment.authorized", m.Type)
		assert.Equal(t, "tenant-a", m.Headers[TenantStampHeader])
		assert.Equal(t, viaPublish.Headers[TenantStampHeader], m.Headers[TenantStampHeader])
		assert.NotEmpty(t, m.MessageId)
	}
	assert.NotEqual(t, first.MessageId, second.MessageId)

	normalize := func(m amqp.Publishing) amqp.Publishing {
		m.Body, m.MessageId, m.CorrelationId, m.Timestamp = nil, "", "", time.Time{}
		headers := amqp.Table{}
		maps.Copy(headers, m.Headers)
		delete(headers, gobrickstrace.HeaderXRequestID)
		delete(headers, gobrickstrace.HeaderTraceParent)
		delete(headers, gobrickstrace.HeaderTraceState)
		m.Headers = headers
		return m
	}
	assert.Equal(t, normalize(viaPublish), normalize(first), "on the wire they differ only in the body and the per-call fields")
}

func TestPublishSealedReturnsTheClientsErrorUnwrapped(t *testing.T) {
	h := declareVerifying(t, sealedOpts(), &fakeVerifier{})
	doorless := &struct{ AMQPClient }{}
	err := h.PublishSealed(context.Background(), doorless, []byte("eyJ.stored.bytes"))
	require.ErrorIs(t, err, ErrPublishDoorUnavailable)
	assert.NotErrorIs(t, err, ErrSealedBytesRejected)
}

func TestPublishSealedIsSafeForConcurrentUse(t *testing.T) {
	h := declareVerifying(t, sealedOpts(), &fakeVerifier{})
	ch := &fakeChannel{}
	c := returnTestClient(t, ch)
	ackRetries(ch)
	data := []byte("eyJ.stored.bytes")
	const n = 8
	errs := make(chan error, n)
	var wg sync.WaitGroup
	for range n {
		wg.Go(func() { errs <- h.PublishSealed(context.Background(), c, data) })
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}
	ids := map[string]bool{}
	for _, m := range ch.publishedMessages() {
		assert.Equal(t, data, m.Body)
		ids[m.MessageId] = true
	}
	assert.Len(t, ids, n, "every concurrent republish of the same bytes carries its own message_id")
}
