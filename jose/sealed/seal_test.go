package sealed_test

import (
	"bytes"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	gojose "github.com/go-jose/go-jose/v4"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gaborage/go-bricks/jose"
	"github.com/gaborage/go-bricks/jose/sealed"
	jositest "github.com/gaborage/go-bricks/jose/testing"
)

const (
	signKid   = "svc-payments-sign-v2"
	encKid    = "acme-core-enc-v1"
	eventType = "payment.authorized"
	testPAN   = "4111111111111111"
)

type sealKeys struct {
	signPriv *rsa.PrivateKey
	encPriv  *rsa.PrivateKey
	resolver jose.KeyResolver
}

var (
	keysOnce sync.Once
	keys     *sealKeys
)

// testKeys generates the two RSA pairs once per package run; key generation dominates test time.
func testKeys(t *testing.T) *sealKeys {
	t.Helper()
	keysOnce.Do(func() {
		signPriv, _ := jositest.GenerateTestKeyPair(t)
		encPriv, _ := jositest.GenerateTestKeyPair(t)
		keys = &sealKeys{
			signPriv: signPriv,
			encPriv:  encPriv,
			resolver: jositest.NewTestResolver(map[string]any{
				signKid: signPriv,           // producer holds the sign PRIVATE
				encKid:  &encPriv.PublicKey, // producer holds only the encrypt PUBLIC
			}),
		}
	})
	return keys
}

func testSpec(t *testing.T) *sealed.Spec {
	t.Helper()
	spec, err := sealed.ScanType(reflect.TypeOf(paymentAuthorized{}))
	require.NoError(t, err)
	return spec
}

func testOptions(t *testing.T) *sealed.Options {
	t.Helper()
	return &sealed.Options{SignKid: signKid, EncryptKid: encKid, EventType: eventType, Keys: testKeys(t).resolver}
}

func sampleEvent() paymentAuthorized {
	return paymentAuthorized{OrderID: "ord-1", Card: &cardData{PAN: testPAN, Exp: "12/29"}, Amount: 1250}
}

// decodeSegment0 base64url-decodes a compact serialization's protected header into a map —
// the wire as a partner with a stock JOSE library would read it, not the producer's structs.
func decodeSegment0(t *testing.T, compact string) (hdr map[string]any, raw []byte) {
	t.Helper()
	seg0 := strings.SplitN(compact, ".", 2)[0]
	raw, err := base64.RawURLEncoding.DecodeString(seg0)
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(raw, &hdr))
	return hdr, raw
}

func sortedKeys(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// topLevelKeys walks a JSON object's members in wire order.
func topLevelKeys(t *testing.T, doc []byte) []string {
	t.Helper()
	dec := json.NewDecoder(bytes.NewReader(doc))
	tok, err := dec.Token()
	require.NoError(t, err)
	require.Equal(t, json.Delim('{'), tok)
	var keys []string
	for dec.More() {
		tok, err = dec.Token()
		require.NoError(t, err)
		keys = append(keys, tok.(string))
		var skip json.RawMessage
		require.NoError(t, dec.Decode(&skip))
	}
	return keys
}

// openWire verifies the outer JWS with go-jose directly and returns the signed payload plus
// the parsed document; the inner JWE is returned as its compact string.
func openWire(t *testing.T, wire []byte, verify *rsa.PublicKey) (payload []byte, doc map[string]json.RawMessage, innerJWE string) {
	t.Helper()
	jws, err := gojose.ParseSigned(string(wire), []gojose.SignatureAlgorithm{gojose.PS256})
	require.NoError(t, err)
	payload, err = jws.Verify(verify)
	require.NoError(t, err, "PS256 signature must verify over the exact payload bytes")
	require.NoError(t, json.Unmarshal(payload, &doc))
	require.NoError(t, json.Unmarshal(doc["card"], &innerJWE), "subject member must be a JSON string")
	return payload, doc, innerJWE
}

func TestSealProducesTheDecidedWire(t *testing.T) {
	k := testKeys(t)
	now := time.Unix(1_800_000_000, 0)
	opts := testOptions(t)
	opts.Now = func() time.Time { return now }
	opts.TenantID = "tenant-a"

	wire, err := sealed.Seal(sampleEvent(), testSpec(t), opts)
	require.NoError(t, err)

	// Outer JWS: exactly the decided protected header set and values.
	outer, outerRaw := decodeSegment0(t, string(wire))
	assert.Equal(t, []string{"alg", "cty", "etyp", "iat", "jti", "kid", "sp", "tid", "typ"}, sortedKeys(outer))
	assert.Equal(t, "PS256", outer["alg"])
	assert.Equal(t, sealed.TypV1, outer["typ"])
	assert.Equal(t, "application/json", outer["cty"])
	assert.Equal(t, signKid, outer["kid"])
	assert.Equal(t, []any{"card"}, outer["sp"])
	assert.Equal(t, eventType, outer["etyp"])
	assert.Equal(t, "tenant-a", outer["tid"])
	assert.Equal(t, float64(now.Unix()), outer["iat"])
	jti, _ := outer["jti"].(string)
	_, err = uuid.Parse(jti)
	require.NoError(t, err, "jti must be a UUID")
	assert.Equal(t, uuid.Version(4), uuid.MustParse(jti).Version())

	payload, doc, innerJWE := openWire(t, wire, &k.signPriv.PublicKey)

	// Wire member order equals struct order; clear members are byte-identical to encoding/json's.
	assert.Equal(t, []string{"orderId", "card", "amount"}, topLevelKeys(t, payload))
	assert.Equal(t, `"ord-1"`, string(doc["orderId"]))
	assert.Equal(t, `1250`, string(doc["amount"]))

	// Inner JWE: exactly the decided header set; iss binds authorship to the outer kid.
	inner, innerRaw := decodeSegment0(t, innerJWE)
	assert.Equal(t, []string{"alg", "cty", "enc", "iss", "kid"}, sortedKeys(inner))
	assert.Equal(t, "RSA-OAEP-256", inner["alg"])
	assert.Equal(t, "A256GCM", inner["enc"])
	assert.Equal(t, "application/json", inner["cty"])
	assert.Equal(t, encKid, inner["kid"])
	assert.Equal(t, outer["kid"], inner["iss"])

	// The plaintext Subject appears in no signed byte and no header of either layer.
	for name, bs := range map[string][]byte{"signed payload": payload, "outer header": outerRaw, "inner header": innerRaw} {
		assert.NotContains(t, string(bs), testPAN, name)
		assert.NotContains(t, string(bs), "12/29", name)
	}

	// The audience decrypts with the encrypt PRIVATE and gets the Subject's own serialization.
	jwe, err := gojose.ParseEncrypted(innerJWE, []gojose.KeyAlgorithm{gojose.RSA_OAEP_256}, []gojose.ContentEncryption{gojose.A256GCM})
	require.NoError(t, err)
	plain, err := jwe.Decrypt(k.encPriv)
	require.NoError(t, err)
	assert.JSONEq(t, `{"pan":"4111111111111111","exp":"12/29"}`, string(plain))
}

func TestSealOmitsTidWithoutTenantAndDefaultsIatToNow(t *testing.T) {
	before := time.Now().Unix()
	wire, err := sealed.Seal(&paymentAuthorized{OrderID: "o", Card: &cardData{PAN: testPAN}}, testSpec(t), testOptions(t))
	require.NoError(t, err)
	outer, _ := decodeSegment0(t, string(wire))
	assert.Equal(t, []string{"alg", "cty", "etyp", "iat", "jti", "kid", "sp", "typ"}, sortedKeys(outer), "tid absent when no tenant resolves")
	iat, _ := outer["iat"].(float64)
	assert.GreaterOrEqual(t, int64(iat), before)
	assert.LessOrEqual(t, int64(iat), time.Now().Unix())
}

func TestSealNilSubjectIsJWEOfNull(t *testing.T) {
	k := testKeys(t)
	wire, err := sealed.Seal(paymentAuthorized{OrderID: "o", Card: nil, Amount: 1}, testSpec(t), testOptions(t))
	require.NoError(t, err)
	outer, _ := decodeSegment0(t, string(wire))
	assert.Equal(t, []any{"card"}, outer["sp"], "sp is constant per type, nil subject included")
	payload, _, innerJWE := openWire(t, wire, &k.signPriv.PublicKey)
	assert.Equal(t, []string{"orderId", "card", "amount"}, topLevelKeys(t, payload), "subject member present on nil")
	jwe, err := gojose.ParseEncrypted(innerJWE, []gojose.KeyAlgorithm{gojose.RSA_OAEP_256}, []gojose.ContentEncryption{gojose.A256GCM})
	require.NoError(t, err)
	plain, err := jwe.Decrypt(k.encPriv)
	require.NoError(t, err)
	assert.Equal(t, "null", string(plain))
}

func TestSealMintsAFreshJTIPerCallWithAnIdenticalHeaderSet(t *testing.T) {
	spec, opts := testSpec(t), testOptions(t)
	first, err := sealed.Seal(sampleEvent(), spec, opts)
	require.NoError(t, err)
	second, err := sealed.Seal(sampleEvent(), spec, opts)
	require.NoError(t, err)
	h1, _ := decodeSegment0(t, string(first))
	h2, _ := decodeSegment0(t, string(second))
	assert.Equal(t, sortedKeys(h1), sortedKeys(h2))
	assert.NotEqual(t, h1["jti"], h2["jti"], "seal runs once per call: a caller-side retry is a new jti")
	assert.NotEqual(t, first, second)
}

func TestSealFamilyPinRefusesForeignOrLogicalKids(t *testing.T) {
	cases := []struct {
		name    string
		sign    string
		encrypt string
		wantKid string
		role    string
	}{
		{name: "sign_other_family", sign: "svc-orders-sign-v1", encrypt: encKid, wantKid: "svc-orders-sign-v1", role: "sign"},
		{name: "sign_logical_only", sign: "svc-payments-sign", encrypt: encKid, wantKid: "svc-payments-sign", role: "sign"},
		{name: "sign_bad_generation", sign: "svc-payments-sign-v0", encrypt: encKid, wantKid: "svc-payments-sign-v0", role: "sign"},
		{name: "encrypt_other_family", sign: signKid, encrypt: "acme-core-sign-v1", wantKid: "acme-core-sign-v1", role: "encrypt"},
		{name: "encrypt_logical_only", sign: signKid, encrypt: "acme-core-enc", wantKid: "acme-core-enc", role: "encrypt"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			opts := testOptions(t)
			opts.SignKid, opts.EncryptKid = tc.sign, tc.encrypt
			wire, err := sealed.Seal(sampleEvent(), testSpec(t), opts)
			assert.Nil(t, wire)
			assert.ErrorIs(t, err, sealed.ErrKidFamilyMismatch)
			var jerr *jose.Error
			require.True(t, errors.As(err, &jerr))
			assert.Equal(t, sealed.CodeKidFamilyMismatch, jerr.Code)
			assert.Equal(t, tc.wantKid, jerr.Kid)
			assert.Contains(t, jerr.Message, tc.role+" kid")
		})
	}
}

func TestSealPropagatesResolverErrors(t *testing.T) {
	k := testKeys(t)
	cases := []struct {
		name     string
		resolver jose.KeyResolver
		wantKid  string
	}{
		{name: "sign_private_missing", resolver: jositest.NewTestResolver(map[string]any{
			signKid: &k.signPriv.PublicKey, encKid: &k.encPriv.PublicKey, // sign side public-only
		}), wantKid: signKid},
		{name: "encrypt_public_missing", resolver: jositest.NewTestResolver(map[string]any{signKid: k.signPriv}), wantKid: encKid},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			opts := testOptions(t)
			opts.Keys = tc.resolver
			_, err := sealed.Seal(sampleEvent(), testSpec(t), opts)
			assert.ErrorIs(t, err, jose.ErrKidUnknown, "resolver error propagates verbatim")
			var jerr *jose.Error
			require.True(t, errors.As(err, &jerr))
			assert.Equal(t, tc.wantKid, jerr.Kid)
		})
	}
}

func TestSealRejectsInvalidInputs(t *testing.T) {
	spec := testSpec(t)
	noKeys := testOptions(t)
	noKeys.Keys = nil
	noEvent := testOptions(t)
	noEvent.EventType = ""
	cases := []struct {
		name string
		evt  any
		spec *sealed.Spec
		opts *sealed.Options
		code string
	}{
		{name: "nil_spec", evt: sampleEvent(), spec: nil, opts: testOptions(t), code: sealed.CodeOptionsInvalid},
		{name: "spec_without_type", evt: sampleEvent(), spec: &sealed.Spec{}, opts: testOptions(t), code: sealed.CodeOptionsInvalid},
		{name: "nil_options", evt: sampleEvent(), spec: spec, opts: nil, code: sealed.CodeOptionsInvalid},
		{name: "nil_resolver", evt: sampleEvent(), spec: spec, opts: noKeys, code: sealed.CodeOptionsInvalid},
		{name: "empty_event_type", evt: sampleEvent(), spec: spec, opts: noEvent, code: sealed.CodeOptionsInvalid},
		{name: "wrong_type", evt: plainEvent{ID: "x"}, spec: spec, opts: testOptions(t), code: sealed.CodeTypeMismatch},
		{name: "nil_event", evt: nil, spec: spec, opts: testOptions(t), code: sealed.CodeTypeMismatch},
		{name: "nil_pointer_marshals_to_null", evt: (*paymentAuthorized)(nil), spec: spec, opts: testOptions(t), code: sealed.CodeDocumentInvalid},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			wire, err := sealed.Seal(tc.evt, tc.spec, tc.opts)
			assert.Nil(t, wire)
			assert.ErrorIs(t, err, sealed.ErrSealFailed)
			var jerr *jose.Error
			require.True(t, errors.As(err, &jerr))
			assert.Equal(t, tc.code, jerr.Code)
		})
	}
}

func TestOptionsValidateIsAKeyFreePreflight(t *testing.T) {
	spec := testSpec(t)
	counting := &countingResolver{inner: testKeys(t).resolver}
	opts := testOptions(t)
	opts.Keys = counting
	require.NoError(t, opts.Validate(spec))
	assert.Zero(t, counting.calls, "Validate must not touch key material")

	opts.SignKid = "svc-orders-sign-v1"
	assert.ErrorIs(t, opts.Validate(spec), sealed.ErrKidFamilyMismatch)

	var nilOpts *sealed.Options
	assert.ErrorIs(t, nilOpts.Validate(spec), sealed.ErrSealFailed)
}

type countingResolver struct {
	inner jose.KeyResolver
	calls int
}

func (c *countingResolver) PrivateKey(kid string) (*rsa.PrivateKey, error) {
	c.calls++
	return c.inner.PrivateKey(kid)
}

func (c *countingResolver) PublicKey(kid string) (*rsa.PublicKey, error) {
	c.calls++
	return c.inner.PublicKey(kid)
}

// selfMarshaling drops its subject member on the wire, which the splice must refuse rather
// than sign a document whose manifest names an absent member.
type selfMarshaling struct {
	_    struct{}  `seal:"sign=svc-payments-sign,encrypt=acme-core-enc"`
	Card *cardData `json:"card" seal:"subject"`
}

func (selfMarshaling) MarshalJSON() ([]byte, error) { return []byte(`{"other":1}`), nil }

func TestSealRefusesADocumentWithoutTheSubjectMember(t *testing.T) {
	spec, err := sealed.ScanType(reflect.TypeOf(selfMarshaling{}))
	require.NoError(t, err)
	_, err = sealed.Seal(selfMarshaling{}, spec, testOptions(t))
	var jerr *jose.Error
	require.True(t, errors.As(err, &jerr))
	assert.Equal(t, sealed.CodeDocumentInvalid, jerr.Code)
	assert.Contains(t, jerr.Message, `"card"`)
	assert.Error(t, jerr.Cause)
}

// leakyCard is a Subject whose MarshalJSON fails with the plaintext in the error text —
// the class of marshal error encoding/json wraps in *json.MarshalerError. Nothing of that
// text may reach the error chain.
type leakyCard struct{ PAN string }

func (c leakyCard) MarshalJSON() ([]byte, error) {
	return nil, fmt.Errorf("cannot encode card %s", c.PAN)
}

type leakyEvent struct {
	_    struct{}  `seal:"sign=svc-payments-sign,encrypt=acme-core-enc"`
	Card leakyCard `json:"card" seal:"subject"`
}

func TestSealMarshalFailureNeverCarriesSubjectBytes(t *testing.T) {
	spec, err := sealed.ScanType(reflect.TypeOf(leakyEvent{}))
	require.NoError(t, err)
	_, err = sealed.Seal(leakyEvent{Card: leakyCard{PAN: testPAN}}, spec, testOptions(t))
	var jerr *jose.Error
	require.True(t, errors.As(err, &jerr))
	assert.Equal(t, sealed.CodeSealFailed, jerr.Code)
	assert.NotContains(t, err.Error(), testPAN, "marshal error must be reported by type only")
	assert.NotContains(t, err.Error(), "cannot encode card")
	assert.Contains(t, err.Error(), "MarshalerError")
	assert.Contains(t, err.Error(), "leakyCard")
}

func TestOptionsValidateBoundsTheSignedSlots(t *testing.T) {
	spec := testSpec(t)
	long := testOptions(t)
	long.EventType = strings.Repeat("e", sealed.MaxEventTypeLen+1)
	assertOptionsInvalid(t, long.Validate(spec), "EventType exceeds")
	long = testOptions(t)
	long.TenantID = strings.Repeat("t", sealed.MaxTenantIDLen+1)
	assertOptionsInvalid(t, long.Validate(spec), "TenantID exceeds")
	ok := testOptions(t)
	ok.EventType = strings.Repeat("e", sealed.MaxEventTypeLen)
	ok.TenantID = strings.Repeat("t", sealed.MaxTenantIDLen)
	assert.NoError(t, ok.Validate(spec))
}

func assertOptionsInvalid(t *testing.T, err error, msg string) {
	t.Helper()
	var jerr *jose.Error
	require.True(t, errors.As(err, &jerr))
	assert.Equal(t, sealed.CodeOptionsInvalid, jerr.Code)
	assert.Contains(t, jerr.Message, msg)
}

type unmarshalable struct {
	_    struct{}     `seal:"sign=svc-payments-sign,encrypt=acme-core-enc"`
	Bad  map[bool]int `json:"bad"`
	Card *cardData    `json:"card" seal:"subject"`
}

func TestSealReportsMarshalFailure(t *testing.T) {
	spec, err := sealed.ScanType(reflect.TypeOf(unmarshalable{}))
	require.NoError(t, err)
	_, err = sealed.Seal(unmarshalable{Bad: map[bool]int{true: 1}}, spec, testOptions(t))
	var jerr *jose.Error
	require.True(t, errors.As(err, &jerr))
	assert.Equal(t, sealed.CodeSealFailed, jerr.Code)
	assert.Contains(t, jerr.Message, "marshal")
}

// brokenKeys returns a resolver whose keys are structurally RSA but cryptographically unusable
// (a 16-bit modulus), so the crypto primitive itself fails rather than key resolution.
func brokenKeys(t *testing.T, breakSign, breakEncrypt bool) jose.KeyResolver {
	t.Helper()
	k := testKeys(t)
	tiny := &rsa.PublicKey{N: big.NewInt(3233), E: 17}
	entries := map[string]any{signKid: k.signPriv, encKid: &k.encPriv.PublicKey}
	if breakEncrypt {
		entries[encKid] = tiny
	}
	if breakSign {
		entries[signKid] = &rsa.PrivateKey{PublicKey: *tiny, D: big.NewInt(2753), Primes: []*big.Int{big.NewInt(61), big.NewInt(53)}}
	}
	return jositest.NewTestResolver(entries)
}

func TestSealReportsCryptoFailures(t *testing.T) {
	cases := []struct {
		name         string
		breakSign    bool
		breakEncrypt bool
		msg          string
	}{
		{name: "encrypt_fails", breakEncrypt: true, msg: "encrypt subject"},
		{name: "sign_fails", breakSign: true, msg: "sign sealed document"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			opts := testOptions(t)
			opts.Keys = brokenKeys(t, tc.breakSign, tc.breakEncrypt)
			wire, err := sealed.Seal(sampleEvent(), testSpec(t), opts)
			assert.Nil(t, wire)
			var jerr *jose.Error
			require.True(t, errors.As(err, &jerr))
			assert.Equal(t, sealed.CodeSealFailed, jerr.Code)
			assert.Contains(t, jerr.Message, tc.msg)
			assert.Error(t, jerr.Cause)
		})
	}
}

// nestedNamesake carries a clear member whose OBJECT value has a "card" member of its own;
// only the top-level Subject may be sealed and the locator must never match by substring.
type nestedNamesake struct {
	_    struct{}       `seal:"sign=svc-payments-sign,encrypt=acme-core-enc"`
	Meta map[string]any `json:"meta"`
	Card *cardData      `json:"card" seal:"subject"`
	Note string         `json:"note"`
}

func TestSealSealsOnlyTheTopLevelSubjectMember(t *testing.T) {
	k := testKeys(t)
	spec, err := sealed.ScanType(reflect.TypeOf(nestedNamesake{}))
	require.NoError(t, err)
	evt := nestedNamesake{
		Meta: map[string]any{"card": "nested-clear-value", "z": `"card":"decoy"`},
		Card: &cardData{PAN: testPAN},
		Note: `{"card":"another decoy"}`,
	}
	wire, err := sealed.Seal(evt, spec, testOptions(t))
	require.NoError(t, err)
	payload, doc, innerJWE := openWire(t, wire, &k.signPriv.PublicKey)
	assert.Equal(t, []string{"meta", "card", "note"}, topLevelKeys(t, payload))
	assert.JSONEq(t, `{"card":"nested-clear-value","z":"\"card\":\"decoy\""}`, string(doc["meta"]), "nested namesake stays clear and untouched")
	assert.Equal(t, `"{\"card\":\"another decoy\"}"`, string(doc["note"]), "a string member that spells the subject is untouched")
	assert.NotContains(t, string(payload), testPAN)
	jwe, err := gojose.ParseEncrypted(innerJWE, []gojose.KeyAlgorithm{gojose.RSA_OAEP_256}, []gojose.ContentEncryption{gojose.A256GCM})
	require.NoError(t, err)
	plain, err := jwe.Decrypt(k.encPriv)
	require.NoError(t, err)
	assert.Contains(t, string(plain), testPAN)
}
