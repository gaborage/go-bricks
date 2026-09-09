package httpclient_test

import (
	"context"
	"crypto/rsa"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
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

// visaFixture is the key material and the two bare-JWE policies a Visa Message Level
// Encryption integration uses: the client encrypts to Visa's key and decrypts responses
// with its own. Nothing is signed — bare mode has no inner JWS.
type visaFixture struct {
	visaPrivate   *rsa.PrivateKey
	clientPrivate *rsa.PrivateKey
	resolver      bricksjose.KeyResolver
	outbound      *bricksjose.Policy
	inbound       *bricksjose.Policy
}

// newVisaFixture builds that material with A128GCM content encryption — the algorithm
// Visa MLE specifies, and deliberately NOT the jose package default (A256GCM), so any
// default-filling that overwrote it would show up as a decrypt failure.
func newVisaFixture(t *testing.T) *visaFixture {
	t.Helper()
	visaPriv, _ := jositest.GenerateTestKeyPair(t)
	clientPriv, _ := jositest.GenerateTestKeyPair(t)
	return &visaFixture{
		visaPrivate:   visaPriv,
		clientPrivate: clientPriv,
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
	}
}

// fakeVisaEndpoint decrypts the MLE envelope with raw go-jose — an independent path from
// the code under test — publishes what it saw on calls, and replies with respond.
//
// The handler runs on httptest's goroutine: t.Errorf only, never require/t.Fatal.
func fakeVisaEndpoint(t *testing.T, f *visaFixture, calls chan<- visaCall, respond func(w http.ResponseWriter, call visaCall)) *httptest.Server {
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
		if err := json.Unmarshal(body, &envelope); err != nil {
			t.Errorf("visa endpoint: request body is not an MLE envelope: %v (body %q)", err, string(body))
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
		call := visaCall{
			contentType: r.Header.Get("Content-Type"),
			encData:     envelope.EncData,
			plaintext:   plaintext,
			header:      obj.Header,
		}
		calls <- call
		respond(w, call)
	}))
}

// visaClient builds a client wired for MLE: bare-JWE policies plus the envelope hooks.
func visaClient(t *testing.T, f *visaFixture, opts ...func(*httpclient.Builder) *httpclient.Builder) httpclient.Client {
	t.Helper()
	wrap, unwrap := httpclient.VisaMLEEnvelope()
	b := httpclient.NewBuilder(logger.New("info", false)).
		WithJOSE(httpclient.JOSEConfig{
			Outbound:   f.outbound,
			Inbound:    f.inbound,
			Resolver:   f.resolver,
			WrapBody:   wrap,
			UnwrapBody: unwrap,
		})
	for _, opt := range opts {
		b = opt(b)
	}
	client, err := b.Build()
	require.NoError(t, err)
	return client
}

func TestBuilderWithJOSEKeepsABarePolicyAlgorithms(t *testing.T) {
	f := newVisaFixture(t)
	calls := make(chan visaCall, 1)
	server := fakeVisaEndpoint(t, f, calls, func(w http.ResponseWriter, _ visaCall) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	})
	defer server.Close()

	client := visaClient(t, f)

	resp, err := client.Post(context.Background(), &httpclient.Request{
		URL:  server.URL,
		Body: []byte(`{"pan":"4111111111111111"}`),
	})
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, resp.StatusCode)

	call := <-calls
	// The fake endpoint only parses A128GCM, so reaching here proves the explicit
	// content encryption survived Build; a defaulted SigAlg would have failed Build.
	assert.Equal(t, string(jose.A128GCM), call.header.ExtraHeaders[jose.HeaderKey("enc")])
}

func TestJOSETransportWrapBodySendsTheVisaEnvelope(t *testing.T) {
	f := newVisaFixture(t)
	calls := make(chan visaCall, 1)
	server := fakeVisaEndpoint(t, f, calls, func(w http.ResponseWriter, _ visaCall) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	})
	defer server.Close()

	before := time.Now().UnixMilli()
	_, err := visaClient(t, f).Post(context.Background(), &httpclient.Request{
		URL:  server.URL,
		Body: []byte(`{"pan":"4111111111111111"}`),
	})
	require.NoError(t, err)
	after := time.Now().UnixMilli()

	call := <-calls
	assert.Equal(t, "application/json", call.contentType)
	assert.JSONEq(t, `{"pan":"4111111111111111"}`, string(call.plaintext))
	assert.Equal(t, visaKid, call.header.KeyID)
	assert.Equal(t, "JOSE", call.header.ExtraHeaders[jose.HeaderKey("typ")])

	iat, isFloat := call.header.ExtraHeaders[jose.HeaderKey("iat")].(float64)
	require.True(t, isFloat, "iat header missing or not a JSON number")
	assert.GreaterOrEqual(t, int64(iat), before)
	assert.LessOrEqual(t, int64(iat), after)
}

// respondVisaEnvelope seals payload to the client's key as a bare JWE with raw go-jose
// and writes it back inside an MLE envelope labelled application/json — the shape Visa
// returns, which carries no application/jose Content-Type to key off.
func respondVisaEnvelope(t *testing.T, f *visaFixture, payload string) func(http.ResponseWriter, visaCall) {
	t.Helper()
	return func(w http.ResponseWriter, _ visaCall) {
		encrypter, err := jose.NewEncrypter(jose.A128GCM,
			jose.Recipient{Algorithm: jose.RSA_OAEP_256, Key: &f.clientPrivate.PublicKey, KeyID: visaClientKid},
			(&jose.EncrypterOptions{}).WithContentType("application/json"))
		if err != nil {
			t.Errorf("visa endpoint: build encrypter: %v", err)
			http.Error(w, `{"errorCode":"encrypter"}`, http.StatusInternalServerError)
			return
		}
		obj, err := encrypter.Encrypt([]byte(payload))
		if err != nil {
			t.Errorf("visa endpoint: encrypt response: %v", err)
			http.Error(w, `{"errorCode":"encrypt"}`, http.StatusInternalServerError)
			return
		}
		compact, err := obj.CompactSerialize()
		if err != nil {
			t.Errorf("visa endpoint: serialize response: %v", err)
			http.Error(w, `{"errorCode":"serialize"}`, http.StatusInternalServerError)
			return
		}
		body, err := json.Marshal(map[string]string{"encData": compact})
		if err != nil {
			t.Errorf("visa endpoint: marshal response envelope: %v", err)
			http.Error(w, `{"errorCode":"marshal"}`, http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(body)
	}
}

func TestJOSETransportUnwrapBodyDecryptsTheVisaEnvelope(t *testing.T) {
	f := newVisaFixture(t)
	calls := make(chan visaCall, 1)
	server := fakeVisaEndpoint(t, f, calls, respondVisaEnvelope(t, f, `{"token":"tok-42"}`))
	defer server.Close()

	resp, err := visaClient(t, f).Post(context.Background(), &httpclient.Request{
		URL:  server.URL,
		Body: []byte(`{"pan":"4111111111111111"}`),
	})
	require.NoError(t, err)

	<-calls
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.JSONEq(t, `{"token":"tok-42"}`, string(resp.Body))
}

// visaCall is one request as the fake Visa endpoint saw it.
type visaCall struct {
	contentType string
	encData     string
	plaintext   []byte
	header      jose.Header
}


func TestVisaMLEEnvelopeWrapProducesEncDataObject(t *testing.T) {
	wrap, _ := httpclient.VisaMLEEnvelope()

	body, contentType, err := wrap("eyJhbGciOiJSU0EtT0FFUC0yNTYifQ.aaa.bbb.ccc.ddd")

	require.NoError(t, err)
	assert.Equal(t, "application/json", contentType)

	var decoded map[string]any
	require.NoError(t, json.Unmarshal(body, &decoded))
	assert.Equal(t, map[string]any{"encData": "eyJhbGciOiJSU0EtT0FFUC0yNTYifQ.aaa.bbb.ccc.ddd"}, decoded)
}

func TestVisaMLEEnvelopeUnwrapExtractsEncData(t *testing.T) {
	_, unwrap := httpclient.VisaMLEEnvelope()

	compact, ok := unwrap("application/json", []byte(`{"encData":"header.key.iv.ct.tag"}`))

	assert.True(t, ok)
	assert.Equal(t, "header.key.iv.ct.tag", compact)
}

func TestVisaMLEEnvelopeUnwrapIgnoresSiblingMembers(t *testing.T) {
	_, unwrap := httpclient.VisaMLEEnvelope()

	compact, ok := unwrap("application/json", []byte(`{"responseId":"r-1","encData":"a.b.c.d.e","status":{"code":0}}`))

	assert.True(t, ok)
	assert.Equal(t, "a.b.c.d.e", compact)
}

func TestVisaMLEEnvelopeUnwrapRejectsNonEnvelopeBodies(t *testing.T) {
	_, unwrap := httpclient.VisaMLEEnvelope()

	tests := []struct {
		name string
		body string
	}{
		{name: "not_json", body: `<html><body>gateway error</body></html>`},
		{name: "json_non_object", body: `["a.b.c.d.e"]`},
		{name: "object_without_encdata", body: `{"errorCode":"9001","message":"denied"}`},
		{name: "encdata_not_a_string", body: `{"encData":{"jwe":"a.b.c.d.e"}}`},
		{name: "encdata_empty_string", body: `{"encData":""}`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			compact, ok := unwrap("application/json", []byte(tt.body))

			assert.False(t, ok)
			assert.Empty(t, compact)
		})
	}
}
