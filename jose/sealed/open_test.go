package sealed_test

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	jose "github.com/go-jose/go-jose/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	bricksjose "github.com/gaborage/go-bricks/jose"
	"github.com/gaborage/go-bricks/jose/sealed"
	jositest "github.com/gaborage/go-bricks/jose/testing"
)

// update regenerates testdata/keys.json (only when absent) and testdata/vectors.json.
var update = flag.Bool("update", false, "regenerate the published sealed-message vectors")

const (
	vecSignKid   = "svc-payments-sign-v2"
	vecSignKidV1 = "svc-payments-sign-v1" // a second provisioned Generation, for strip-and-re-sign
	vecEncKid    = "acme-core-enc-v1"
	vecRogueKid  = "rogue"
	vecTenant    = "tenant-a"
	vecJTI       = "0f4b7c1e-3d2a-4e8b-9c6d-1a2b3c4d5e6f"
	vecIAT       = int64(1_800_000_000)
	positiveDoc  = `{"orderId":"ord-1","card":%s,"amount":1250}`
	keysFile     = "testdata/keys.json"
	vectorsFile  = "testdata/vectors.json"
)

// fixtureNote travels inside both fixture files so their provenance is readable in place.
const fixtureNote = "Generated test material for the jose/sealed opener vectors (go test ./jose/sealed -update): " +
	"disposable RSA keys and the tokens signed/encrypted under them. Never provisioned anywhere; nothing to rotate."

// keysFile is testdata/keys.json: a provenance note and the fixed test keys, kid -> base64 PKCS#1 DER.
type keysFileShape struct {
	Note string            `json:"note"`
	Keys map[string]string `json:"keys"`
}

// vectorKeys are the fixed test keys the published vectors are bound to. They exist so the
// vectors are byte-stable files a partner can replay; they are test material only.
type vectorKeys struct {
	priv     map[string]*rsa.PrivateKey
	consumer bricksjose.KeyResolver // sign PUBLIC (v1 and v2) + encrypt PRIVATE — the opener's view
	inner    string                 // the positive vector's Subject JWE, shared by every vector that leaves it alone
}

// loadVectorKeys reads testdata/keys.json, generating it first under -update when absent.
func loadVectorKeys(t *testing.T) *vectorKeys {
	t.Helper()
	file := keysFileShape{Note: fixtureNote, Keys: map[string]string{}}
	raw, err := os.ReadFile(keysFile)
	if errors.Is(err, os.ErrNotExist) && *update {
		for _, kid := range []string{vecSignKid, vecSignKidV1, vecEncKid, vecRogueKid} {
			priv, _ := jositest.GenerateTestKeyPair(t)
			file.Keys[kid] = base64.StdEncoding.EncodeToString(x509.MarshalPKCS1PrivateKey(priv))
		}
		raw, err = json.MarshalIndent(file, "", "  ")
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(keysFile, append(raw, '\n'), 0o600))
	}
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(raw, &file))

	k := &vectorKeys{priv: map[string]*rsa.PrivateKey{}}
	for kid, b64 := range file.Keys {
		der, err := base64.StdEncoding.DecodeString(b64)
		require.NoError(t, err)
		k.priv[kid], err = x509.ParsePKCS1PrivateKey(der)
		require.NoError(t, err)
	}
	k.consumer = jositest.NewTestResolver(map[string]any{
		vecSignKid:   &k.priv[vecSignKid].PublicKey,
		vecSignKidV1: &k.priv[vecSignKidV1].PublicKey,
		vecEncKid:    k.priv[vecEncKid],
	})
	return k
}

// vectorFile is the published set: one positive body and the negatives derived from it.
type vectorFile struct {
	Note     string   `json:"note"`
	Positive string   `json:"positive"`
	Vectors  []vector `json:"vectors"`
}

type vector struct {
	Name   string      `json:"name"`
	Code   string      `json:"code"`
	Rule   int         `json:"rule"`
	Layer  string      `json:"layer,omitempty"`
	Slot   string      `json:"slot,omitempty"`
	Diffs  int         `json:"diffs"` // fields differing from the positive; -1 when the body is not even parseable
	Tenant *tenantRule `json:"tenant,omitempty"`
	Body   string      `json:"body"`
}

// tenantRule is TenantExpectation as the vector file spells it.
type tenantRule struct {
	Required bool   `json:"required"`
	Expected string `json:"expected"`
}

// ---- builders: the wire as an attacker (or a stock JOSE library) would produce it ----

type innerOpts struct {
	alg       jose.KeyAlgorithm
	cenc      jose.ContentEncryption
	cty       string
	kid, iss  string
	crit      bool
	pub       *rsa.PublicKey
	plaintext []byte
}

// innerDefaults is the positive vector's Subject JWE as the sealer would produce it.
func (k *vectorKeys) innerDefaults() innerOpts {
	return innerOpts{
		alg: jose.RSA_OAEP_256, cenc: jose.A256GCM, cty: sealed.ContentTypeJSON, kid: vecEncKid, iss: vecSignKid,
		pub: &k.priv[vecEncKid].PublicKey, plaintext: []byte(`{"pan":"4111111111111111","exp":"12/29"}`),
	}
}

// encryptSubject builds a compact JWE with go-jose directly, so headers the sealer never writes can be produced.
func encryptSubject(t *testing.T, o *innerOpts) string {
	t.Helper()
	extra := map[jose.HeaderKey]any{}
	if o.iss != "" {
		extra[jose.HeaderKey(sealed.HeaderIssuer)] = o.iss
	}
	if o.crit {
		extra[jose.HeaderKey("crit")] = []string{"exp"}
	}
	opts := (&jose.EncrypterOptions{ExtraHeaders: extra})
	if o.cty != "" {
		opts = opts.WithContentType(jose.ContentType(o.cty))
	}
	encrypter, err := jose.NewEncrypter(o.cenc, jose.Recipient{Algorithm: o.alg, Key: o.pub, KeyID: o.kid}, opts)
	require.NoError(t, err)
	obj, err := encrypter.Encrypt(o.plaintext)
	require.NoError(t, err)
	compact, err := obj.CompactSerialize()
	require.NoError(t, err)
	return compact
}

// outerDefaults is the positive vector's outer protected header.
func outerDefaults() map[string]any {
	return map[string]any{
		"alg": "PS256", "kid": vecSignKid, "typ": sealed.TypV1, "cty": sealed.ContentTypeJSON,
		"jti": vecJTI, "iat": vecIAT, "etyp": eventType, "sp": []string{"card"}, "tid": vecTenant,
	}
}

// signCompact builds a compact JWS with full header control. signOver defaults to payload;
// a different value yields a stale signature (the tamper vectors). A nil key leaves a
// garbage signature segment for headers the opener must refuse before verifying.
func signCompact(t *testing.T, hdr map[string]any, payload []byte, key *rsa.PrivateKey, signOver []byte) string {
	t.Helper()
	hdrJSON, err := json.Marshal(hdr)
	require.NoError(t, err)
	seg := func(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }
	if signOver == nil {
		signOver = payload
	}
	sig := []byte("not-a-signature")
	if key != nil {
		h := sha256.Sum256([]byte(seg(hdrJSON) + "." + seg(signOver)))
		if hdr["alg"] == "RS256" {
			sig, err = rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, h[:]) // NOSONAR: test vector only; PKCS#1 v1.5 signing is an allowed v1 alg
		} else {
			sig, err = rsa.SignPSS(rand.Reader, key, crypto.SHA256, h[:], &rsa.PSSOptions{SaltLength: rsa.PSSSaltLengthEqualsHash})
		}
		require.NoError(t, err)
	}
	return seg(hdrJSON) + "." + seg(payload) + "." + seg(sig)
}

// mutation describes one negative vector as a delta from the positive.
type mutation struct {
	outer     func(h map[string]any)
	inner     func(o *innerOpts)
	doc       func(doc string) string
	signKey   string // kid of the signing key; "" = vecSignKid; "none" = garbage signature
	staleSign bool   // sign over the positive document, then swap the tampered one in
}

// build renders the positive vector with one mutation applied.
func (k *vectorKeys) build(t *testing.T, m mutation) string {
	t.Helper()
	inner := k.inner
	if m.inner != nil {
		io := k.innerDefaults()
		m.inner(&io)
		inner = encryptSubject(t, &io)
	} else if inner == "" {
		defaults := k.innerDefaults()
		k.inner = encryptSubject(t, &defaults)
		inner = k.inner
	}
	quoted, err := json.Marshal(inner)
	require.NoError(t, err)
	doc := strings.Replace(positiveDoc, "%s", string(quoted), 1)
	signOver := []byte(nil)
	if m.doc != nil {
		if m.staleSign {
			signOver = []byte(doc)
		}
		doc = m.doc(doc)
	}
	hdr := outerDefaults()
	if m.outer != nil {
		m.outer(hdr)
	}
	var key *rsa.PrivateKey
	switch m.signKey {
	case "none":
	case "":
		key = k.priv[vecSignKid]
	default:
		key = k.priv[m.signKey]
	}
	return signCompact(t, hdr, []byte(doc), key, signOver)
}

// set is the outer-header mutation that writes one param.
func set(key string, val any) func(map[string]any) { return func(h map[string]any) { h[key] = val } }

// tamperAmount is the clear-field mutation: a changed amount under a stale signature.
func tamperAmount(d string) string { return strings.Replace(d, `"amount":1250`, `"amount":1`, 1) }

// del is the outer-header mutation that drops one param.
func del(key string) func(map[string]any) { return func(h map[string]any) { delete(h, key) } }

// negativeVectors is the published set, in rule order. Every entry differs from the
// positive in exactly one header field or one payload member unless Diffs says otherwise.
func (k *vectorKeys) negativeVectors(t *testing.T) []vector {
	t.Helper()
	v := func(name, code string, rule, diffs int, m mutation) vector {
		return vector{Name: name, Code: code, Rule: rule, Diffs: diffs, Body: k.build(t, m)}
	}
	slot := func(name, slot string, m mutation) vector {
		out := v(name, sealed.CodeHeaderSlotInvalid, 6, 1, m)
		out.Slot = slot
		return out
	}
	jwe := func(name, code string, m mutation) vector {
		out := v(name, code, 10, 1, m)
		out.Layer = "jwe"
		return out
	}
	badCrit := func(h map[string]any) { h["crit"] = []string{"exp"} }
	longJTI := strings.Repeat("a", 129)
	rogue := &k.priv[vecRogueKid].PublicKey
	absent := v("tid_absent_but_required", sealed.CodeTenantMismatch, 8, 1, mutation{outer: del("tid")})
	absent.Tenant = &tenantRule{Required: true, Expected: vecTenant}
	return []vector{
		// Rule 1.
		{Name: "not_a_jws_two_segments", Code: sealed.CodeNotSealed, Rule: 1, Diffs: -1, Body: "eyJhbGciOiJQUzI1NiJ9.e30"},
		{Name: "header_not_json", Code: sealed.CodeNotSealed, Rule: 1, Diffs: -1, Body: "bm90LWpzb24.e30.c2ln"},
		v("wrong_typ", sealed.CodeNotSealed, 1, 1, mutation{outer: set("typ", "JWT")}),
		v("typ_absent", sealed.CodeNotSealed, 1, 1, mutation{outer: del("typ")}),
		// Rule 2.
		v("outer_alg_hs256", sealed.CodeAlgNotAllowed, 2, 1, mutation{outer: set("alg", "HS256"), signKey: "none"}),
		v("outer_alg_none", sealed.CodeAlgNotAllowed, 2, 1, mutation{outer: set("alg", "none"), signKey: "none"}),
		v("outer_cty_wrong", sealed.CodeCtyInvalid, 2, 1, mutation{outer: set("cty", "text/plain")}),
		v("outer_cty_absent", sealed.CodeCtyInvalid, 2, 1, mutation{outer: del("cty")}),
		v("outer_crit_present", sealed.CodeCritPresent, 2, 1, mutation{outer: badCrit}),
		// Rules 3–4.
		v("cross_family_kid", sealed.CodeKidFamilyMismatch, 3, 1, mutation{outer: set("kid", "other-svc-sign-v1"), signKey: vecRogueKid}),
		v("logical_kid_on_wire", sealed.CodeKidFamilyMismatch, 3, 1, mutation{outer: set("kid", "svc-payments-sign")}),
		v("unprovisioned_generation", sealed.CodeKidUnknownGeneration, 4, 1, mutation{outer: set("kid", "svc-payments-sign-v9")}),
		// Rule 5.
		v("tampered_clear_field", sealed.CodeSignatureInvalid, 5, 1, mutation{staleSign: true, doc: tamperAmount}),
		v("signed_by_other_key_same_kid", sealed.CodeSignatureInvalid, 5, 0, mutation{signKey: vecRogueKid}),
		// Rule 6 — slots.
		slot("iat_absent", "iat", mutation{outer: del("iat")}),
		slot("iat_string", "iat", mutation{outer: set("iat", "1800000000")}),
		slot("iat_negative", "iat", mutation{outer: set("iat", -1)}),
		slot("iat_fractional", "iat", mutation{outer: set("iat", 1800000000.5)}),
		slot("jti_absent", "jti", mutation{outer: del("jti")}),
		slot("jti_has_colon", "jti", mutation{outer: set("jti", "has:colon")}),
		slot("jti_129_chars", "jti", mutation{outer: set("jti", longJTI)}),
		slot("etyp_absent", "etyp", mutation{outer: del("etyp")}),
		slot("etyp_empty", "etyp", mutation{outer: set("etyp", "")}),
		slot("sp_absent", "sp", mutation{outer: del("sp")}),
		slot("sp_empty", "sp", mutation{outer: set("sp", []string{})}),
		slot("tid_not_a_string", "tid", mutation{outer: set("tid", 7)}),
		// Rules 7–9.
		v("etyp_mismatch", sealed.CodeEventTypeMismatch, 7, 1, mutation{outer: set("etyp", "payment.voided")}),
		v("tid_mismatch", sealed.CodeTenantMismatch, 8, 1, mutation{outer: set("tid", "tenant-b")}),
		v("sp_wrong_member", sealed.CodeManifestMismatch, 9, 1, mutation{outer: set("sp", []string{"orderId"})}),
		// Rule 10 — the payload document and the inner JWE.
		v("subject_not_a_string", sealed.CodePayloadUndecodable, 10, 1, mutation{doc: func(string) string { return `{"orderId":"ord-1","card":{"pan":"x"},"amount":1250}` }}),
		v("subject_not_a_jwe", sealed.CodePayloadUndecodable, 10, 1, mutation{doc: func(string) string { return `{"orderId":"ord-1","card":"a.b.c","amount":1250}` }}),
		jwe("inner_alg_rsa1_5", sealed.CodeAlgNotAllowed, mutation{inner: func(o *innerOpts) { o.alg = jose.RSA1_5 }}),
		jwe("inner_enc_a128gcm", sealed.CodeAlgNotAllowed, mutation{inner: func(o *innerOpts) { o.cenc = jose.A128GCM }}),
		jwe("inner_cty_wrong", sealed.CodeCtyInvalid, mutation{inner: func(o *innerOpts) { o.cty = "text/plain" }}),
		jwe("inner_cty_absent", sealed.CodeCtyInvalid, mutation{inner: func(o *innerOpts) { o.cty = "" }}),
		jwe("inner_crit_present", sealed.CodeCritPresent, mutation{inner: func(o *innerOpts) { o.crit = true }}),
		jwe("strip_and_resign_iss_differs", sealed.CodeAuthorshipMismatch, mutation{outer: set("kid", vecSignKidV1), signKey: vecSignKidV1}),
		jwe("inner_iss_absent", sealed.CodeAuthorshipMismatch, mutation{inner: func(o *innerOpts) { o.iss = "" }}),
		jwe("inner_cross_family_kid", sealed.CodeKidFamilyMismatch, mutation{inner: func(o *innerOpts) { o.kid = "other-enc-v1" }}),
		jwe("inner_unprovisioned_generation", sealed.CodeKidUnknownGeneration, mutation{inner: func(o *innerOpts) { o.kid = "acme-core-enc-v9" }}),
		jwe("wrong_key_same_name", sealed.CodeDecryptFailed, mutation{inner: func(o *innerOpts) { o.pub = rogue }}),
		// Rule 11.
		v("opened_document_wrong_shape", sealed.CodePayloadUndecodable, 11, 1, mutation{inner: func(o *innerOpts) { o.plaintext = []byte(`"not-an-object"`) }}),
		// Order: two rules broken, the earlier one fires.
		v("order_bad_alg_and_bad_kid", sealed.CodeAlgNotAllowed, 2, 2, mutation{outer: func(h map[string]any) { h["alg"] = "HS256"; h["kid"] = "other-svc-sign-v1" }, signKey: "none"}),
		v("order_unknown_generation_and_tampered", sealed.CodeKidUnknownGeneration, 4, 2,
			mutation{outer: set("kid", "svc-payments-sign-v9"), staleSign: true, doc: tamperAmount}),
		{
			Name: "order_bad_slot_and_wrong_etyp", Code: sealed.CodeHeaderSlotInvalid, Rule: 6, Slot: "jti", Diffs: 2,
			Body: k.build(t, mutation{outer: func(h map[string]any) { delete(h, "jti"); h["etyp"] = "payment.voided" }}),
		},
		v("order_wrong_etyp_and_wrong_sp", sealed.CodeEventTypeMismatch, 7, 2, mutation{outer: func(h map[string]any) { h["etyp"] = "payment.voided"; h["sp"] = []string{"orderId"} }}),
		absent,
	}
}

// loadVectors reads testdata/vectors.json, regenerating it first under -update.
func loadVectors(t *testing.T, k *vectorKeys) *vectorFile {
	t.Helper()
	if *update {
		vf := &vectorFile{Note: fixtureNote, Positive: k.build(t, mutation{}), Vectors: k.negativeVectors(t)}
		raw, err := json.MarshalIndent(vf, "", "  ")
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(vectorsFile, append(raw, '\n'), 0o600))
	}
	raw, err := os.ReadFile(vectorsFile)
	require.NoError(t, err)
	var vf vectorFile
	require.NoError(t, json.Unmarshal(raw, &vf))
	return &vf
}

// vectorOptions is the consumer's view for opening a vector: declared EventType, tenant rule, keys.
func vectorOptions(k *vectorKeys, tenant *tenantRule) *sealed.OpenOptions {
	want := sealed.TenantExpectation{Expected: vecTenant}
	if tenant != nil {
		want = sealed.TenantExpectation{Required: tenant.Required, Expected: tenant.Expected}
	}
	return &sealed.OpenOptions{EventType: eventType, Tenant: want, Keys: k.consumer}
}

// ---- tests ----

// TestOpenPositiveVector opens the published positive vector and checks the Envelope and DedupKey properties.
func TestOpenPositiveVector(t *testing.T) {
	k := loadVectorKeys(t)
	vf := loadVectors(t, k)

	var evt paymentAuthorized
	env, err := sealed.Open([]byte(vf.Positive), testSpec(t), vectorOptions(k, nil), &evt)
	require.NoError(t, err)

	assert.Equal(t, paymentAuthorized{OrderID: "ord-1", Card: &cardData{PAN: "4111111111111111", Exp: "12/29"}, Amount: 1250}, evt)
	assert.Equal(t, &sealed.Envelope{
		JTI: vecJTI, IssuedAt: time.Unix(vecIAT, 0).UTC(), EventType: eventType, TenantID: vecTenant,
		SignKid: vecSignKid, SignFamily: "svc-payments-sign", EncKid: vecEncKid,
	}, env)

	key := env.DedupKey()
	assert.Equal(t, 1, strings.Count(key, ":"), "exactly one separator")
	assert.Equal(t, "svc-payments-sign", strings.SplitN(key, ":", 2)[0], "left side is the Logical kid, not the generation")
	assert.NotContains(t, strings.SplitN(key, ":", 2)[0], "-v2")

	// Redelivery: the same bytes yield the same key.
	var again paymentAuthorized
	env2, err := sealed.Open([]byte(vf.Positive), testSpec(t), vectorOptions(k, nil), &again)
	require.NoError(t, err)
	assert.Equal(t, key, env2.DedupKey())
}

// TestOpenNegativeVectors asserts the exact code, rule, details and sentinel of every published negative vector.
func TestOpenNegativeVectors(t *testing.T) {
	k := loadVectorKeys(t)
	vf := loadVectors(t, k)
	require.NotEmpty(t, vf.Vectors)

	for _, tc := range vf.Vectors {
		t.Run(tc.Name, func(t *testing.T) {
			var evt paymentAuthorized
			env, err := sealed.Open([]byte(tc.Body), testSpec(t), vectorOptions(k, tc.Tenant), &evt)
			require.Error(t, err)
			assert.Nil(t, env)
			assert.Zero(t, evt, "nothing decodes on a refused message")

			var oe *sealed.OpenError
			require.ErrorAs(t, err, &oe)
			assert.Equal(t, tc.Code, oe.Err.Code)
			assert.Equal(t, tc.Rule, oe.Rule)
			assert.Equal(t, tc.Layer, oe.Details[sealed.DetailLayer])
			assert.Equal(t, tc.Slot, oe.Details[sealed.DetailSlot])

			var je *bricksjose.Error
			require.ErrorAs(t, err, &je, "*bricksjose.Error-compatible")
			assert.Equal(t, tc.Code, je.Code)
			// Never a slot value in the error text (#1307): presence and lengths only.
			// This runs BEFORE the sentinel switch: the switch is require, and a
			// sentinel regression must not abort before the leak check, which is an
			// independent property of the same error.
			for _, secret := range []string{vecJTI, eventType, vecTenant, "payment.voided", "tenant-b", "has:colon"} {
				assert.NotContains(t, err.Error(), secret)
			}
			switch tc.Code {
			case sealed.CodeNotSealed:
				require.ErrorIs(t, err, sealed.ErrNotSealed)
			case sealed.CodeKidUnknownGeneration:
				require.ErrorIs(t, err, sealed.ErrKidUnknownGeneration)
			case sealed.CodeKidFamilyMismatch:
				require.ErrorIs(t, err, sealed.ErrKidFamilyMismatch)
			default:
				require.ErrorIs(t, err, sealed.ErrOpenFailed)
			}
		})
	}
}

// TestEveryCodeNamesItsRules pins the identity contract: callers match on the code, so the
// rule number behind each (code, layer) pair is a fixed table and cannot drift silently.
// SEAL_PAYLOAD_UNDECODABLE is the one code the #1356 spec itself assigns to two steps (the
// document shape in rule 10, the decode into T in rule 11) — {10, 11} is the contract, not
// a drift to fix.
func TestEveryCodeNamesItsRules(t *testing.T) {
	k := loadVectorKeys(t)
	vf := loadVectors(t, k)
	want := map[string][]int{
		sealed.CodeNotSealed + "/":               {1},
		sealed.CodeAlgNotAllowed + "/":           {2},
		sealed.CodeCtyInvalid + "/":              {2},
		sealed.CodeCritPresent + "/":             {2},
		sealed.CodeKidFamilyMismatch + "/":       {3},
		sealed.CodeKidUnknownGeneration + "/":    {4},
		sealed.CodeSignatureInvalid + "/":        {5},
		sealed.CodeHeaderSlotInvalid + "/":       {6},
		sealed.CodeEventTypeMismatch + "/":       {7},
		sealed.CodeTenantMismatch + "/":          {8},
		sealed.CodeManifestMismatch + "/":        {9},
		sealed.CodePayloadUndecodable + "/":      {10, 11},
		sealed.CodeAlgNotAllowed + "/jwe":        {10},
		sealed.CodeCtyInvalid + "/jwe":           {10},
		sealed.CodeCritPresent + "/jwe":          {10},
		sealed.CodeAuthorshipMismatch + "/jwe":   {10},
		sealed.CodeKidFamilyMismatch + "/jwe":    {10},
		sealed.CodeKidUnknownGeneration + "/jwe": {10},
		sealed.CodeDecryptFailed + "/jwe":        {10},
	}
	got := map[string]map[int]struct{}{}
	for _, tc := range vf.Vectors {
		key := tc.Code + "/" + tc.Layer
		if got[key] == nil {
			got[key] = map[int]struct{}{}
		}
		got[key][tc.Rule] = struct{}{}
	}
	require.Len(t, got, len(want), "every (code, layer) pair has a vector")
	for key, rules := range want {
		seen := make([]int, 0, len(got[key]))
		for r := range got[key] {
			seen = append(seen, r)
		}
		assert.ElementsMatch(t, rules, seen, "rules firing %s", key)
	}
}

// TestNegativeVectorsDifferInExactlyOneField pins the "one field per vector" property of
// the published set: outer header fields plus top-level payload members, where a changed
// Subject counts by its inner header fields (or one, for a ciphertext-only change).
func TestNegativeVectorsDifferInExactlyOneField(t *testing.T) {
	k := loadVectorKeys(t)
	vf := loadVectors(t, k)
	pos := parseVector(t, vf.Positive)

	for _, tc := range vf.Vectors {
		if tc.Diffs < 0 {
			continue
		}
		t.Run(tc.Name, func(t *testing.T) {
			assert.Equal(t, tc.Diffs, pos.diff(parseVector(t, tc.Body)), "fields differing from the positive vector")
		})
	}
}

type parsedVector struct {
	outer map[string]json.RawMessage
	doc   map[string]json.RawMessage
	inner map[string]json.RawMessage
	jwe   string
}

// parseVector decodes a vector into its outer header, payload members and inner header for the diff count.
func parseVector(t *testing.T, body string) *parsedVector {
	t.Helper()
	segs := strings.Split(body, ".")
	require.Len(t, segs, 3)
	p := &parsedVector{}
	require.NoError(t, json.Unmarshal(decodeSeg(t, segs[0]), &p.outer))
	require.NoError(t, json.Unmarshal(decodeSeg(t, segs[1]), &p.doc))
	if err := json.Unmarshal(p.doc["card"], &p.jwe); err == nil && strings.Count(p.jwe, ".") == 4 {
		require.NoError(t, json.Unmarshal(decodeSeg(t, strings.SplitN(p.jwe, ".", 2)[0]), &p.inner))
	}
	return p
}

// decodeSeg base64url-decodes one compact segment.
func decodeSeg(t *testing.T, seg string) []byte {
	t.Helper()
	raw, err := base64.RawURLEncoding.DecodeString(seg)
	require.NoError(t, err)
	return raw
}

// diff counts the fields o differs from p in (see TestNegativeVectorsDifferInExactlyOneField).
func (p *parsedVector) diff(o *parsedVector) int {
	n := countDiffs(p.outer, o.outer)
	for key := range union(p.doc, o.doc) {
		if string(p.doc[key]) == string(o.doc[key]) {
			continue
		}
		if key != "card" || p.inner == nil || o.inner == nil {
			n++
			continue
		}
		if d := countDiffs(p.inner, o.inner); d > 0 {
			n += d
		} else {
			n++ // same header, different ciphertext: a different key or plaintext
		}
	}
	return n
}

// countDiffs counts the keys whose raw values differ between two objects.
func countDiffs(a, b map[string]json.RawMessage) int {
	n := 0
	for key := range union(a, b) {
		if string(a[key]) != string(b[key]) {
			n++
		}
	}
	return n
}

// union is the key set of both objects.
func union(a, b map[string]json.RawMessage) map[string]struct{} {
	keys := map[string]struct{}{}
	for k := range a {
		keys[k] = struct{}{}
	}
	for k := range b {
		keys[k] = struct{}{}
	}
	return keys
}

// TestOpenRejectsWiringMistakes covers the pre-flight: every wiring mistake is an *OpenError with Rule 0.
func TestOpenRejectsWiringMistakes(t *testing.T) {
	k := loadVectorKeys(t)
	vf := loadVectors(t, k)
	body := []byte(vf.Positive)
	spec := testSpec(t)
	opts := vectorOptions(k, nil)
	var evt paymentAuthorized

	cases := []struct {
		name string
		code string
		call func() error
	}{
		{"nil_spec", sealed.CodeOptionsInvalid, func() error { _, err := sealed.Open(body, nil, opts, &evt); return err }},
		{"nil_opts", sealed.CodeOptionsInvalid, func() error { _, err := sealed.Open(body, spec, nil, &evt); return err }},
		{"nil_keys", sealed.CodeOptionsInvalid, func() error {
			_, err := sealed.Open(body, spec, &sealed.OpenOptions{EventType: eventType}, &evt)
			return err
		}},
		{"empty_event_type", sealed.CodeOptionsInvalid, func() error {
			_, err := sealed.Open(body, spec, &sealed.OpenOptions{Keys: k.consumer}, &evt)
			return err
		}},
		{"out_not_a_pointer", sealed.CodeTypeMismatch, func() error { _, err := sealed.Open(body, spec, opts, evt); return err }},
		{"out_wrong_type", sealed.CodeTypeMismatch, func() error { _, err := sealed.Open(body, spec, opts, new(cardData)); return err }},
		{"out_nil", sealed.CodeTypeMismatch, func() error { _, err := sealed.Open(body, spec, opts, nil); return err }},
		{"out_typed_nil", sealed.CodeTypeMismatch, func() error {
			_, err := sealed.Open(body, spec, opts, (*paymentAuthorized)(nil))
			return err
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.call()
			var oe *sealed.OpenError
			require.ErrorAs(t, err, &oe, "every Open failure is an *OpenError")
			assert.Zero(t, oe.Rule, "pre-flight, no rule fired")
			var je *bricksjose.Error
			require.ErrorAs(t, err, &je)
			assert.Equal(t, tc.code, je.Code)
			assert.ErrorIs(t, err, sealed.ErrSealFailed)
		})
	}
}

// TestOpenOpensWhatSealProduced is the Seal → Open round trip a producer and consumer share.
func TestOpenOpensWhatSealProduced(t *testing.T) {
	// Seal (random keys, fresh jti) → Open, the round trip a producer and consumer share;
	// the consumer resolver holds the mirror roles of the producer's.
	keys := testKeys(t)
	consumer := jositest.NewTestResolver(map[string]any{signKid: &keys.signPriv.PublicKey, encKid: keys.encPriv})
	opts := testOptions(t)
	opts.TenantID = "tenant-a"
	wire, err := sealed.Seal(sampleEvent(), testSpec(t), opts)
	require.NoError(t, err)

	var evt paymentAuthorized
	env, err := sealed.Open(wire, testSpec(t), &sealed.OpenOptions{EventType: eventType, Keys: consumer, Tenant: sealed.TenantExpectation{Required: true, Expected: "tenant-a"}}, &evt)
	require.NoError(t, err)
	assert.Equal(t, sampleEvent(), evt)
	assert.Equal(t, signKid, env.SignKid)
	assert.Equal(t, encKid, env.EncKid)
	assert.Regexp(t, `^svc-payments-sign:[0-9a-f-]{36}$`, env.DedupKey())

	// tid surfaced, not judged, when the caller expects nothing in particular.
	env, err = sealed.Open(wire, testSpec(t), &sealed.OpenOptions{EventType: eventType, Keys: consumer}, &evt)
	require.NoError(t, err)
	assert.Equal(t, "tenant-a", env.TenantID)
}

// TestOpenAcceptsIssuedAtZero pins the slot rule's lower bound: iat 0 is a valid NumericDate
// (the epoch), only a negative one is refused.
func TestOpenAcceptsIssuedAtZero(t *testing.T) {
	k := loadVectorKeys(t)
	loadVectors(t, k) // primes the shared positive Subject JWE
	body := k.build(t, mutation{outer: set("iat", 0)})

	var evt paymentAuthorized
	env, err := sealed.Open([]byte(body), testSpec(t), vectorOptions(k, nil), &evt)
	require.NoError(t, err)
	assert.Equal(t, time.Unix(0, 0).UTC(), env.IssuedAt)
}

// TestOpenErrorRendersDetailsSorted pins OpenError's text form and nil safety.
func TestOpenErrorRendersDetailsSorted(t *testing.T) {
	err := &sealed.OpenError{Err: &bricksjose.Error{Code: "X", Message: "m"}, Rule: 6, Details: map[string]string{"slot": "jti", "len": "0", "present": "false"}}
	assert.Equal(t, "X: m [len=0 present=false slot=jti]", err.Error())
	assert.Equal(t, "<nil>", (*sealed.OpenError)(nil).Error())
	assert.NoError(t, (*sealed.OpenError)(nil).Unwrap())
}

// TestOpenPathReadsNoClock is the grep test: iat is informational, so the open path never
// consults time.Now or time.Since.
func TestOpenPathReadsNoClock(t *testing.T) {
	src, err := os.ReadFile(filepath.Join(".", "open.go"))
	require.NoError(t, err)
	assert.NotRegexp(t, `time\.(Now|Since)`, string(src))
}

// TestOpenRefusesReflectMismatchBeforeAnyKey proves a wrong out type is refused before the resolver is consulted.
func TestOpenRefusesReflectMismatchBeforeAnyKey(t *testing.T) {
	// A wrong out type is refused before the resolver is consulted.
	k := loadVectorKeys(t)
	vf := loadVectors(t, k)
	r := &countingResolver{inner: k.consumer}
	_, err := sealed.Open([]byte(vf.Positive), testSpec(t), &sealed.OpenOptions{EventType: eventType, Keys: r}, new(cardData))
	require.Error(t, err)
	assert.Zero(t, r.calls)
}
