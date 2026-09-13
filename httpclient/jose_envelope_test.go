package httpclient_test

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	jose "github.com/go-jose/go-jose/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gaborage/go-bricks/httpclient"
	bricksjose "github.com/gaborage/go-bricks/jose"
	jositest "github.com/gaborage/go-bricks/jose/testing"
	"github.com/gaborage/go-bricks/logger"
)

const (
	visaKid       = "visa-key"
	visaClientKid = "client-key"
)

// visaKeys generates the two 2048-bit RSA keys this file's fixtures use, once. The keys
// are read-only and every test wants the same pair of roles, so paying for generation per
// test buys nothing.
var visaKeys = sync.OnceValues(func() (keys *visaKeyPair, err error) {
	visaPriv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return nil, err
	}
	clientPriv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return nil, err
	}
	return &visaKeyPair{visa: visaPriv, client: clientPriv}, nil
})

// visaKeyPair holds Visa's key and the client's key.
type visaKeyPair struct {
	visa   *rsa.PrivateKey
	client *rsa.PrivateKey
}

// visaFixture is the key material and the bare-JWE policies a Visa Message Level
// Encryption integration uses: the client encrypts to Visa's key and decrypts responses
// with its own; peerOutbound is the Visa side of that second leg. Nothing is signed —
// bare mode has no inner JWS.
type visaFixture struct {
	visaPrivate  *rsa.PrivateKey
	resolver     bricksjose.KeyResolver
	outbound     *bricksjose.Policy
	inbound      *bricksjose.Policy
	peerOutbound *bricksjose.Policy
}

// newVisaFixture builds that material with A128GCM content encryption — the algorithm
// Visa MLE specifies, and deliberately NOT the jose package default (A256GCM), so any
// default-filling that overwrote it would show up as a decrypt failure.
func newVisaFixture(t *testing.T) *visaFixture {
	t.Helper()
	keys, err := visaKeys()
	require.NoError(t, err)
	visaPriv, clientPriv := keys.visa, keys.client
	return &visaFixture{
		visaPrivate: visaPriv,
		resolver: jositest.NewTestResolver(map[string]any{
			visaKid:       visaPriv,
			visaClientKid: clientPriv,
		}),
		outbound: &bricksjose.Policy{
			Direction:  bricksjose.DirectionOutbound,
			Mode:       bricksjose.SealModeBareJWE,
			EncryptKid: visaKid,
			Enc:        jose.A128GCM,
			Typ:        "JOSE",
			IATMillis:  true,
		},
		inbound: &bricksjose.Policy{
			Direction:  bricksjose.DirectionInbound,
			Mode:       bricksjose.SealModeBareJWE,
			DecryptKid: visaClientKid,
			Enc:        jose.A128GCM,
		},
		// Sealed by hand rather than by a Builder, so the algorithm fields the builder
		// would default must be spelled out.
		peerOutbound: &bricksjose.Policy{
			Direction:  bricksjose.DirectionOutbound,
			Mode:       bricksjose.SealModeBareJWE,
			EncryptKid: visaClientKid,
			KeyAlg:     bricksjose.DefaultKeyAlg,
			Enc:        jose.A128GCM,
			Cty:        bricksjose.DefaultCty,
		},
	}
}

// fakeVisaEndpoint decrypts the MLE envelope with raw go-jose — an independent path from
// the code under test — publishes what it saw on calls, and replies with respond.
//
// The handler runs on httptest's goroutine: t.Errorf only, never require/t.Fatal.
func fakeVisaEndpoint(t *testing.T, f *visaFixture, calls chan<- visaCall, respond func(w http.ResponseWriter)) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("visa endpoint: read request body: %v", err)
			http.Error(w, `{"errorCode":"read"}`, http.StatusBadRequest)
			return
		}
		var envelope struct {
			EncData string `json:"encData"`
		}
		if unmarshalErr := json.Unmarshal(body, &envelope); unmarshalErr != nil {
			t.Errorf("visa endpoint: request body is not an MLE envelope: %v (body %q)", unmarshalErr, string(body))
			http.Error(w, `{"errorCode":"envelope"}`, http.StatusBadRequest)
			return
		}
		// Restricting the accepted algorithms here is the assertion that the transport
		// used RSA-OAEP-256 + A128GCM: anything else fails to parse.
		obj, err := jose.ParseEncrypted(envelope.EncData,
			[]jose.KeyAlgorithm{jose.RSA_OAEP_256},
			[]jose.ContentEncryption{jose.A128GCM})
		if err != nil {
			t.Errorf("visa endpoint: parse encData: %v", err)
			http.Error(w, `{"errorCode":"parse"}`, http.StatusBadRequest)
			return
		}
		plaintext, err := obj.Decrypt(f.visaPrivate)
		if err != nil {
			t.Errorf("visa endpoint: decrypt encData: %v", err)
			http.Error(w, `{"errorCode":"decrypt"}`, http.StatusBadRequest)
			return
		}
		calls <- visaCall{
			contentType: r.Header.Get("Content-Type"),
			encData:     envelope.EncData,
			plaintext:   plaintext,
			header:      obj.Header,
		}
		respond(w)
	}))
}

// visaClient builds a client wired for MLE: bare-JWE policies plus the body envelope.
func visaClient(t *testing.T, f *visaFixture, opts ...func(*httpclient.Builder) *httpclient.Builder) httpclient.Client {
	t.Helper()
	b := httpclient.NewBuilder(logger.New("info", false)).
		WithJOSE(httpclient.JOSEConfig{
			Outbound: f.outbound,
			Inbound:  f.inbound,
			Resolver: f.resolver,
			Envelope: httpclient.VisaMLEEnvelope(),
		})
	for _, opt := range opts {
		b = opt(b)
	}
	client, err := b.Build()
	require.NoError(t, err)
	return client
}

func TestJOSETransportEnvelopeWrapSendsTheVisaEnvelope(t *testing.T) {
	f := newVisaFixture(t)
	calls := make(chan visaCall, 1)
	// A sealed reply, because a plaintext 2xx is now refused outright and the request half
	// under test would never be reached.
	server := fakeVisaEndpoint(t, f, calls, respondVisaEnvelope(t, f, `{"ok":true}`))
	defer server.Close()

	before := time.Now().UnixMilli()
	_, err := visaClient(t, f).Post(context.Background(), &httpclient.Request{
		URL:  server.URL,
		Body: []byte(`{"pan":"card-fixture-0000"}`),
	})
	require.NoError(t, err)
	after := time.Now().UnixMilli()

	call := <-calls
	assert.Equal(t, "application/json", call.contentType)
	assert.JSONEq(t, `{"pan":"card-fixture-0000"}`, string(call.plaintext))
	assert.Equal(t, visaKid, call.header.KeyID)
	assert.Equal(t, "JOSE", call.header.ExtraHeaders[jose.HeaderKey("typ")])
	// The explicit A128GCM survived Build's default-filling (which would otherwise have
	// written A256GCM); the fake endpoint parses nothing else, so it never got this far.
	assert.Equal(t, string(jose.A128GCM), call.header.ExtraHeaders[jose.HeaderKey("enc")])

	iat, isFloat := call.header.ExtraHeaders[jose.HeaderKey("iat")].(float64)
	require.True(t, isFloat, "iat header missing or not a JSON number")
	assert.GreaterOrEqual(t, int64(iat), before)
	assert.LessOrEqual(t, int64(iat), after)
}

// respondVisaEnvelope seals payload to the client's key as a bare JWE and returns a
// handler that writes it back inside an MLE envelope labeled application/json — the shape
// Visa returns, which carries no application/jose Content-Type to key off.
//
// The sealing happens here, on the test goroutine, not in the returned handler: it is
// fixed per test, and SealForTest fails with t.Fatalf, which must not run in a handler.
func respondVisaEnvelope(t *testing.T, f *visaFixture, payload string) func(http.ResponseWriter) {
	t.Helper()
	compact := jositest.SealForTest(t, []byte(payload), f.peerOutbound, f.resolver)
	body, err := json.Marshal(map[string]string{"encData": compact})
	require.NoError(t, err)
	return func(w http.ResponseWriter) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(body)
	}
}

func TestJOSETransportEnvelopeUnwrapDecryptsTheVisaEnvelope(t *testing.T) {
	f := newVisaFixture(t)
	calls := make(chan visaCall, 1)
	server := fakeVisaEndpoint(t, f, calls, respondVisaEnvelope(t, f, `{"token":"tok-42"}`))
	defer server.Close()

	resp, err := visaClient(t, f).Post(context.Background(), &httpclient.Request{
		URL:  server.URL,
		Body: []byte(`{"pan":"card-fixture-0000"}`),
	})
	require.NoError(t, err)

	<-calls
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, "application/json", resp.Headers.Get("Content-Type"))
	assert.JSONEq(t, `{"token":"tok-42"}`, string(resp.Body))
}

func TestJOSETransportEnvelopeUnwrapPassesThroughNonEnvelope(t *testing.T) {
	const errorEnvelope = `{"errorCode":"9001","message":"denied","details":[{"field":"pan"}]}`
	f := newVisaFixture(t)
	calls := make(chan visaCall, 1)
	// 400, not 200: Unwrap declining a body is a pass-through only on a failure status —
	// on a success it is ErrJOSEPlaintextResponse.
	server := fakeVisaEndpoint(t, f, calls, func(w http.ResponseWriter) {
		w.Header().Set("Content-Type", "application/json;charset=UTF-8")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(errorEnvelope))
	})
	defer server.Close()

	resp, err := visaClient(t, f).Post(context.Background(), &httpclient.Request{
		URL:  server.URL,
		Body: []byte(`{"pan":"card-fixture-0000"}`),
	})
	// The 400 is the peer's own verdict, surfaced as an HTTPError; the declined body still
	// reaches the caller untouched, which is the property under test.
	require.Error(t, err)
	require.NotErrorIs(t, err, httpclient.ErrJOSEPlaintextResponse)
	require.NotNil(t, resp)

	<-calls
	//nolint:testifylint // byte-identity is the property under test; JSONEq would pass on a re-encoded body
	assert.Equal(t, errorEnvelope, string(resp.Body))
	assert.Equal(t, "application/json;charset=UTF-8", resp.Headers.Get("Content-Type"))
}

func TestJOSETransportSealsEveryRetryAttemptFreshly(t *testing.T) {
	f := newVisaFixture(t)
	calls := make(chan visaCall, 2)
	var attempts atomic.Int32
	sealedOK := respondVisaEnvelope(t, f, `{"ok":true}`)
	server := fakeVisaEndpoint(t, f, calls, func(w http.ResponseWriter) {
		if attempts.Add(1) == 1 {
			http.Error(w, `{"errorCode":"busy"}`, http.StatusServiceUnavailable)
			return
		}
		sealedOK(w)
	})
	defer server.Close()

	client := visaClient(t, f, func(b *httpclient.Builder) *httpclient.Builder {
		return b.WithRetries(1, time.Millisecond)
	})

	resp, err := client.Post(context.Background(), &httpclient.Request{
		URL:  server.URL,
		Body: []byte(`{"pan":"card-fixture-0000"}`),
	})
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, resp.StatusCode)

	first, second := <-calls, <-calls
	assert.NotEqual(t, first.encData, second.encData, "each attempt must carry a freshly sealed JWE")
	// Same-millisecond attempts are legal, so the claim is monotonicity, not strict growth.
	firstIAT, ok := first.header.ExtraHeaders[jose.HeaderKey("iat")].(float64)
	require.True(t, ok, "first attempt has no numeric iat header")
	secondIAT, ok := second.header.ExtraHeaders[jose.HeaderKey("iat")].(float64)
	require.True(t, ok, "second attempt has no numeric iat header")
	assert.GreaterOrEqual(t, int64(secondIAT), int64(firstIAT))
}

// visaCall is one request as the fake Visa endpoint saw it.
type visaCall struct {
	contentType string
	encData     string
	plaintext   []byte
	header      jose.Header
}

func TestVisaMLEEnvelopeWrapProducesEncDataObject(t *testing.T) {
	envelope := httpclient.VisaMLEEnvelope()

	body, contentType, err := envelope.Wrap("eyJhbGciOiJSU0EtT0FFUC0yNTYifQ.aaa.bbb.ccc.ddd")

	require.NoError(t, err)
	assert.Equal(t, "application/json", contentType)

	var decoded map[string]any
	require.NoError(t, json.Unmarshal(body, &decoded))
	assert.Equal(t, map[string]any{"encData": "eyJhbGciOiJSU0EtT0FFUC0yNTYifQ.aaa.bbb.ccc.ddd"}, decoded)
}

func TestVisaMLEEnvelopeUnwrapRecognizesEncDataByShape(t *testing.T) {
	envelope := httpclient.VisaMLEEnvelope()

	tests := []struct {
		name        string
		body        string
		wantOK      bool
		wantCompact string
	}{
		{name: "encdata_only", body: `{"encData":"header.key.iv.ct.tag"}`, wantOK: true, wantCompact: "header.key.iv.ct.tag"},
		{name: "encdata_with_sibling_members", body: `{"responseId":"r-1","encData":"a.b.c.d.e","status":{"code":0}}`, wantOK: true, wantCompact: "a.b.c.d.e"},
		{name: "not_json", body: `<html><body>gateway error</body></html>`},
		{name: "json_non_object", body: `["a.b.c.d.e"]`},
		{name: "object_without_encdata", body: `{"errorCode":"9001","message":"denied"}`},
		{name: "encdata_not_a_string", body: `{"encData":{"jwe":"a.b.c.d.e"}}`},
		{name: "encdata_null", body: `{"encData":null}`},
		{name: "encdata_empty_string", body: `{"encData":""}`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			compact, ok := envelope.Unwrap("application/json", []byte(tt.body))

			assert.Equal(t, tt.wantOK, ok)
			assert.Equal(t, tt.wantCompact, compact)
		})
	}
}
