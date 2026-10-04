package sealed_test

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	josesealed "github.com/gaborage/go-bricks/jose/sealed"
	kstest "github.com/gaborage/go-bricks/keystore/testing"
	"github.com/gaborage/go-bricks/messaging"
	"github.com/gaborage/go-bricks/messaging/sealed"
	"github.com/gaborage/go-bricks/multitenant"
)

// otherFamilyEvent is paymentAuthorized under a different sign family.
type otherFamilyEvent struct {
	_       struct{}  `seal:"sign=other-payments-sign,encrypt=acme-core-enc"`
	OrderID string    `json:"orderId"`
	Card    *cardData `json:"card" seal:"subject"`
}

// otherSubjectEvent shares the families and EventType but seals a different member.
type otherSubjectEvent struct {
	_       struct{}  `seal:"sign=svc-payments-sign,encrypt=acme-core-enc"`
	OrderID *cardData `json:"orderId" seal:"subject"`
	Card    string    `json:"card"`
}

// doorCall is one PublishSealed call a refusal row arranges; rec records what reached the client.
type doorCall struct {
	h      *messaging.Publisher[paymentAuthorized]
	client messaging.AMQPClient
	rec    *capturingClient
	tenant string
	data   []byte
}

func callOn(h *messaging.Publisher[paymentAuthorized], tenant string, data []byte) doorCall {
	rec := &capturingClient{}
	return doorCall{h: h, client: rec, rec: rec, tenant: tenant, data: data}
}

// callPooled publishes through a per-tenant client pooled under key.
func callPooled(h *messaging.Publisher[paymentAuthorized], key, tenant string, data []byte) doorCall {
	pc := &pooledClient{key: key}
	return doorCall{h: h, client: pc, rec: &pc.capturingClient, tenant: tenant, data: data}
}

func tenantCtx(tenant string) context.Context {
	if tenant == "" {
		return context.Background()
	}
	return multitenant.SetTenant(context.Background(), tenant)
}

// pairHandle configures the canonical producer store and declares the handle under test on it.
func pairHandle(t *testing.T) *messaging.Publisher[paymentAuthorized] {
	t.Helper()
	configureStore(t, pairStore(t), nil)
	return declare(t)
}

// rotatingSignStore holds sign v1 and v2 (both pairs) and the encrypt v1 public key.
func rotatingSignStore(t *testing.T) *kstest.MockKeyStore {
	t.Helper()
	keys(t)
	return withPair(pairStore(t), signFamily, "v2", sign2)
}

func sealFor[T any](t *testing.T, h *messaging.Publisher[T], tenant string, evt T) []byte {
	t.Helper()
	data, _, err := h.Seal(tenantCtx(tenant), evt)
	require.NoError(t, err)
	return data
}

// sealThenSwap seals under store a with active, then declares the handle under test on store b with bActive.
func sealThenSwap(t *testing.T, a *kstest.MockKeyStore, active map[string]string, b *kstest.MockKeyStore, bActive map[string]string) doorCall {
	t.Helper()
	configureStore(t, a, active)
	data := sealFor(t, declare(t), "", doorEvent())
	configureStore(t, b, bActive)
	return callOn(declare(t), "", data)
}

// flipSignatureBit toggles a middle character of the signature segment between 'A' and 'B'; never
// the last one, whose trailing bits may decode identically.
func flipSignatureBit(data []byte) []byte {
	out := bytes.Clone(data)
	start := bytes.LastIndexByte(out, '.') + 1
	i := start + (len(out)-start)/2
	if out[i] == 'A' {
		out[i] = 'B'
	} else {
		out[i] = 'A'
	}
	return out
}

func publishedVector(t *testing.T, name string) []byte {
	t.Helper()
	for _, v := range loadVectors(t).Vectors {
		if v.Name == name {
			return []byte(v.Body)
		}
	}
	require.Failf(t, "vector not found", "%s", name)
	return nil
}

func openErrorOf(t *testing.T, err error) *josesealed.OpenError {
	t.Helper()
	var oe *josesealed.OpenError
	require.ErrorAs(t, err, &oe, "the jose/sealed error stays in the chain")
	return oe
}

func requireRejected(t *testing.T, err error, code string) {
	t.Helper()
	require.ErrorIs(t, err, messaging.ErrSealedBytesRejected)
	var refused *messaging.SealOpenRefusedError
	require.ErrorAs(t, err, &refused)
	assert.Equal(t, code, refused.Code)
	assert.Equal(t, code == josesealed.CodeKidUnknownGeneration, refused.Recoverable)
	assert.Equal(t, code, openErrorOf(t, err).Err.Code)
}

// assertNoSecretInError checks that no jti (a UUID), no tenant, no foreign etyp and no body
// segment reaches err's text (#1307).
func assertNoSecretInError(t *testing.T, err error, data []byte) {
	t.Helper()
	text := err.Error()
	assert.NotRegexp(t, `[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}`, text, "no jti")
	for _, secret := range []string{"tenant-a", "tenant-b", "tenant-pool", "payment.captured"} {
		assert.NotContains(t, text, secret)
	}
	for seg := range bytes.SplitSeq(data, []byte(".")) {
		if len(seg) >= 8 {
			assert.NotContains(t, text, string(seg))
		}
	}
}

func doorEvent() paymentAuthorized {
	return paymentAuthorized{OrderID: "o9", Card: &cardData{PAN: testPAN}}
}

func TestPublishSealedRepublishesSealedBytesByteIdentical(t *testing.T) {
	h := pairHandle(t)
	data, jti, err := h.Seal(context.Background(), doorEvent())
	require.NoError(t, err)
	rec := &capturingClient{}
	require.NoError(t, h.PublishSealed(context.Background(), rec, data))
	require.NoError(t, h.PublishSealed(context.Background(), rec, data))
	require.Len(t, rec.data, 2)
	assert.Equal(t, data, rec.data[0])
	assert.Equal(t, rec.data[0], rec.data[1])
	assert.Equal(t, "application/jose", rec.opts[0].Props.ContentType)
	env, _ := openWire(t, rec.data[0], josesealed.TenantExpectation{})
	assert.Equal(t, jti, env.JTI, "the consumer sees the jti Seal returned")
}

func TestPublishSealedRefusesBeforeAnyPublish(t *testing.T) {
	// Rows read signPriv, encPriv and sign2 as plain operands, which Go may evaluate before the calls beside them, so the keys must exist first.
	keys(t)
	cases := []struct {
		name    string
		arrange func(t *testing.T) doorCall
		code    string
		want    error
		check   func(t *testing.T, err error)
	}{
		{name: "other_event_type", code: josesealed.CodeEventTypeMismatch, arrange: func(t *testing.T) doorCall {
			h := pairHandle(t)
			return callOn(h, "", sealFor(t, declareAs[paymentAuthorized](t, "payment.captured"), "", doorEvent()))
		}},
		{name: "other_sign_family", code: josesealed.CodeKidFamilyMismatch, arrange: func(t *testing.T) doorCall {
			configureStore(t, withPair(pairStore(t), "other-payments-sign", "v1", sign2), nil)
			h := declare(t)
			evt := otherFamilyEvent{OrderID: "o", Card: &cardData{PAN: testPAN}}
			return callOn(h, "", sealFor(t, declareAs[otherFamilyEvent](t, eventType), "", evt))
		}},
		{name: "other_subject_member", code: josesealed.CodeManifestMismatch, arrange: func(t *testing.T) doorCall {
			h := pairHandle(t)
			evt := otherSubjectEvent{OrderID: &cardData{PAN: testPAN}, Card: "c"}
			return callOn(h, "", sealFor(t, declareAs[otherSubjectEvent](t, eventType), "", evt))
		}},
		{name: "flipped_signature_bit", code: josesealed.CodeSignatureInvalid, arrange: func(t *testing.T) doorCall {
			h := pairHandle(t)
			return callOn(h, "", flipSignatureBit(sealFor(t, h, "", doorEvent())))
		}},
		{name: "truncated_segment", code: josesealed.CodeNotSealed, arrange: func(t *testing.T) doorCall {
			h := pairHandle(t)
			data := sealFor(t, h, "", doorEvent())
			return callOn(h, "", data[:bytes.LastIndexByte(data, '.')])
		}, check: func(t *testing.T, err error) {
			require.ErrorIs(t, err, josesealed.ErrNotSealed)
		}},
		{name: "plaintext_json", code: josesealed.CodeNotSealed, arrange: func(t *testing.T) doorCall {
			h := pairHandle(t)
			raw, err := json.Marshal(doorEvent())
			require.NoError(t, err)
			return callOn(h, "", raw)
		}, check: func(t *testing.T, err error) {
			assert.NotContains(t, err.Error(), testPAN, "the plaintext Subject never reaches the error")
		}},
		{name: "empty_body", code: josesealed.CodeNotSealed, arrange: func(t *testing.T) doorCall {
			return callOn(pairHandle(t), "", []byte{})
		}},
		{name: "subject_not_a_jwe", code: josesealed.CodePayloadUndecodable, arrange: func(t *testing.T) doorCall {
			store := withPair(kstest.NewMockKeyStore(), signFamily, "v2", vectorKey(t, vecSignKid))
			configureStore(t, withPublic(store, encFamily, "v1", &vectorKey(t, vecEncKid).PublicKey), nil)
			return callOn(declare(t), vecTenant, publishedVector(t, "subject_not_a_jwe"))
		}, check: func(t *testing.T, err error) {
			assert.Equal(t, 10, openErrorOf(t, err).Rule)
		}},
		{name: "subject_case_fold_twin", code: josesealed.CodePayloadUndecodable, arrange: func(t *testing.T) doorCall {
			store := withPair(kstest.NewMockKeyStore(), signFamily, "v2", vectorKey(t, vecSignKid))
			configureStore(t, withPublic(store, encFamily, "v1", &vectorKey(t, vecEncKid).PublicKey), nil)
			return callOn(declare(t), vecTenant, publishedVector(t, "subject_case_fold_twin"))
		}, check: func(t *testing.T, err error) {
			assert.Equal(t, 10, openErrorOf(t, err).Rule)
			assert.NotContains(t, err.Error(), "5555555555554444", "the clear twin never reaches the error")
		}},
		{name: "sign_generation_removed", code: josesealed.CodeKidUnknownGeneration, arrange: func(t *testing.T) doorCall {
			b := withPublic(withPair(kstest.NewMockKeyStore(), signFamily, "v2", sign2), encFamily, "v1", &encPriv.PublicKey)
			return sealThenSwap(t, rotatingSignStore(t), map[string]string{signFamily: "v1"}, b, nil)
		}, check: func(t *testing.T, err error) {
			require.ErrorIs(t, err, josesealed.ErrKidUnknownGeneration)
			assert.Equal(t, absentSignGenerationMessage, openErrorOf(t, err).Err.Message)
		}},
		{name: "sign_generation_held_public_only", code: josesealed.CodeKidUnknownGeneration, arrange: func(t *testing.T) doorCall {
			b := withPublic(kstest.NewMockKeyStore(), signFamily, "v1", &signPriv.PublicKey)
			b = withPublic(withPair(b, signFamily, "v2", sign2), encFamily, "v1", &encPriv.PublicKey)
			return sealThenSwap(t, rotatingSignStore(t), map[string]string{signFamily: "v1"}, b, map[string]string{signFamily: "v2"})
		}, check: func(t *testing.T, err error) {
			requireNotSignable(t, err, signFamily+"-v1")
			assert.Contains(t, err.Error(), "sealed open refused: "+josesealed.CodeKidUnknownGeneration)
			assert.Contains(t, err.Error(), eventType)
			assert.NotContains(t, err.Error(), openErrorOf(t, err).Err.Message, "the codec's message reaches only an errors.As caller")
		}},
		{name: "encrypt_generation_removed", code: josesealed.CodeKidUnknownGeneration, arrange: func(t *testing.T) doorCall {
			a := withPublic(pairStore(t), encFamily, "v2", &sign2.PublicKey)
			b := withPublic(withPair(kstest.NewMockKeyStore(), signFamily, "v1", signPriv), encFamily, "v2", &sign2.PublicKey)
			return sealThenSwap(t, a, map[string]string{encFamily: "v1"}, b, nil)
		}, check: func(t *testing.T, err error) {
			assert.Equal(t, "jwe", openErrorOf(t, err).Details[josesealed.DetailLayer])
		}},
		{name: "tid_present_and_different", want: messaging.ErrSealedTenantMismatch, arrange: func(t *testing.T) doorCall {
			h := pairHandle(t)
			return callOn(h, "tenant-b", sealFor(t, h, "tenant-a", doorEvent()))
		}},
		{name: "tid_absent_while_resolved_non_empty", want: messaging.ErrSealedTenantMismatch, arrange: func(t *testing.T) doorCall {
			h := pairHandle(t)
			return callPooled(h, "tenant-pool", "", sealFor(t, h, "", doorEvent()))
		}},
		{name: "tid_present_while_resolved_empty", want: messaging.ErrSealedTenantMismatch, arrange: func(t *testing.T) doorCall {
			h := pairHandle(t)
			return callOn(h, "", sealFor(t, h, "tenant-a", doorEvent()))
		}},
		{name: "tenant_conflicts_with_pool_key", want: messaging.ErrTenantStampConflict, arrange: func(t *testing.T) doorCall {
			h := pairHandle(t)
			return callPooled(h, "tenant-b", "tenant-a", sealFor(t, h, "tenant-a", doorEvent()))
		}},
		{name: "seal_startup_error", want: sealed.ErrRoleMismatch, arrange: func(t *testing.T) doorCall {
			store := withPublic(kstest.NewMockKeyStore(), signFamily, "v1", &signPriv.PublicKey) // the sign generation holds no private key
			configureStore(t, withPublic(store, encFamily, "v1", &encPriv.PublicKey), nil)
			decls, h := declareIn[paymentAuthorized](eventType)
			require.ErrorIs(t, decls.Validate(), sealed.ErrRoleMismatch)
			return callOn(h, "", []byte("eyJ.stored.bytes"))
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			call := tc.arrange(t)
			err := call.h.PublishSealed(tenantCtx(call.tenant), call.client, call.data)
			if tc.code != "" {
				requireRejected(t, err, tc.code)
			} else {
				require.ErrorIs(t, err, tc.want)
			}
			if tc.check != nil {
				tc.check(t, err)
			}
			assertNoSecretInError(t, err, call.data)
			assert.Empty(t, call.rec.data, "refused before any broker I/O")
		})
	}
}

func TestPublishSealedAcceptsBytesSealedBeforeAnActivationFlip(t *testing.T) {
	store := rotatingSignStore(t)
	configureStore(t, store, map[string]string{signFamily: "v1"})
	data := sealFor(t, declare(t), "", doorEvent())
	v := newVerifier(t, store)
	before, err := v.Verify(context.Background(), data)
	require.NoError(t, err)
	require.Equal(t, signFamily+"-v1", before.SignKid, "the stored bytes were sealed under v1")

	configureStore(t, store, map[string]string{signFamily: "v2"})
	after := declare(t)
	rec := &capturingClient{}
	require.NoError(t, after.PublishSealed(context.Background(), rec, data), "v1 is still provisioned: it verifies")
	require.Len(t, rec.data, 1)
	assert.Equal(t, data, rec.data[0])

	fresh := sealFor(t, after, "", doorEvent())
	env, err := v.Verify(context.Background(), fresh)
	require.NoError(t, err)
	assert.Equal(t, signFamily+"-v2", env.SignKid, "the flip took effect for new seals")
}
