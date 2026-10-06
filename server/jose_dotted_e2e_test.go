package server_test

import (
	"bytes"
	"context"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gaborage/go-bricks/app"
	"github.com/gaborage/go-bricks/config"
	"github.com/gaborage/go-bricks/httpclient"
	"github.com/gaborage/go-bricks/jose"
	jositest "github.com/gaborage/go-bricks/jose/testing"
	"github.com/gaborage/go-bricks/keystore"
	"github.com/gaborage/go-bricks/logger"
	"github.com/gaborage/go-bricks/server"
)

// dottedTokenReq and dottedTokenResp name dotted keystore entries in their jose: tags
// (ADR-144): the tag value, the keystore name and the wire kid are one string.
type dottedTokenReq struct {
	_   struct{} `jose:"decrypt=tokens.our,verify=tokens.peer"`
	PAN string   `json:"pan" validate:"required"`
}

type dottedTokenResp struct {
	_     struct{} `jose:"sign=tokens.our,encrypt=tokens.peer"`
	Token string   `json:"token"`
}

type badDottedTagReq struct {
	_   struct{} `jose:"decrypt=tokens..our,verify=tokens.peer"`
	PAN string   `json:"pan"`
}

// wireRecorder is the client's base transport: it records each request and response
// body as the wire carried them, and passes the exchange through unchanged.
type wireRecorder struct {
	mu        sync.Mutex
	requests  []string
	responses []string
}

func (w *wireRecorder) RoundTrip(req *http.Request) (*http.Response, error) {
	body, err := io.ReadAll(req.Body)
	if err != nil {
		return nil, err
	}
	req.Body = io.NopCloser(bytes.NewReader(body))
	resp, err := http.DefaultTransport.RoundTrip(req)
	if err != nil {
		return nil, err
	}
	respBody, err := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if err != nil {
		return nil, err
	}
	resp.Body = io.NopCloser(bytes.NewReader(respBody))
	w.mu.Lock()
	w.requests = append(w.requests, string(body))
	w.responses = append(w.responses, string(respBody))
	w.mu.Unlock()
	return resp, nil
}

func derB64(t *testing.T, der []byte, err error) string {
	t.Helper()
	require.NoError(t, err)
	return base64.StdEncoding.EncodeToString(der)
}

func privateB64(t *testing.T, k *rsa.PrivateKey) string {
	der, err := x509.MarshalPKCS8PrivateKey(k)
	return derB64(t, der, err)
}

func publicB64(t *testing.T, k *rsa.PublicKey) string {
	der, err := x509.MarshalPKIXPublicKey(k)
	return derB64(t, der, err)
}

// compactKid decodes the kid of a compact token's protected header.
func compactKid(t *testing.T, compact string) string {
	t.Helper()
	raw, err := base64.RawURLEncoding.DecodeString(strings.SplitN(compact, ".", 2)[0])
	require.NoError(t, err)
	var hdr struct {
		Kid string `json:"kid"`
	}
	require.NoError(t, json.Unmarshal(raw, &hdr))
	return hdr.Kid
}

// TestJOSERouteRoundTripWithDottedKids registers a jose-tagged route whose tags name
// dotted entries, on a keystore loaded from nested YAML through config.Load, and calls it
// through httpclient.WithJOSE: the wire kids are the dotted names verbatim, the response
// opens, and a request sealed to the hyphenated look-alike is an unknown kid.
func TestJOSERouteRoundTripWithDottedKids(t *testing.T) {
	ourPriv, _ := jositest.GenerateTestKeyPair(t)
	peerPriv, _ := jositest.GenerateTestKeyPair(t)

	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(`
keystore:
  keys:
    tokens:
      our:
        public: {value: `+publicB64(t, &ourPriv.PublicKey)+`}
        private: {value: `+privateB64(t, ourPriv)+`}
      peer:
        public: {value: `+publicB64(t, &peerPriv.PublicKey)+`}
        private: {value: `+privateB64(t, peerPriv)+`}
`), 0o600))
	t.Chdir(dir)
	// Other tests in this package leave APP_ENV set empty, which Load reads as app.env "".
	t.Setenv("APP_ENV", "test")
	cfg, err := config.Load()
	require.NoError(t, err)
	require.Contains(t, cfg.KeyStore.Keys, "tokens.our")
	mod := keystore.NewModule()
	require.NoError(t, mod.Init(&app.ModuleDeps{Logger: logger.New("disabled", true), Config: cfg}))
	resolver := jose.NewKeyStoreResolver(mod.KeyStore())

	srvCfg := &config.Config{App: config.AppConfig{Name: "dotted-jose", Env: "test", Version: "1.0.0"}}
	srvCfg.Server.Host = "127.0.0.1"
	srvCfg.Server.Timeout.Read = 5 * time.Second
	srvCfg.Server.Timeout.Write = 5 * time.Second
	srv := server.New(srvCfg, logger.New("disabled", true))
	hr := server.NewHandlerRegistry(srvCfg, server.WithJOSEResolver(resolver))
	server.POST(hr, srv.ModuleGroup(), "/tokens", func(req dottedTokenReq, _ server.HandlerContext) (dottedTokenResp, server.IAPIError) {
		return dottedTokenResp{Token: "tok-" + req.PAN[len(req.PAN)-4:]}, nil
	})
	errCh := make(chan error, 1)
	go func() { errCh <- srv.Start() }()
	t.Cleanup(func() {
		require.NoError(t, srv.Shutdown(context.Background()))
		<-errCh
	})
	require.Eventually(t, func() bool {
		select {
		case <-srv.ReadyCh():
			return true
		default:
			return false
		}
	}, 5*time.Second, 10*time.Millisecond)
	url := "http://" + srv.BoundAddr().String() + "/tokens"

	wire := &wireRecorder{}
	client, err := httpclient.NewBuilder(logger.New("disabled", true)).
		WithTransport(wire).
		WithJOSE(httpclient.JOSEConfig{
			Outbound: &jose.Policy{Direction: jose.DirectionOutbound, SignKid: "tokens.peer", EncryptKid: "tokens.our"},
			Inbound:  &jose.Policy{Direction: jose.DirectionInbound, DecryptKid: "tokens.peer", VerifyKid: "tokens.our"},
			Resolver: resolver,
		}).
		Build()
	require.NoError(t, err)

	resp, err := client.Post(t.Context(), &httpclient.Request{URL: url, Body: []byte(`{"pan":"4111111111111111"}`)})
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	var got struct {
		Token string `json:"token"`
	}
	require.NoError(t, json.Unmarshal(resp.Body, &got))
	assert.Equal(t, "tok-1111", got.Token)

	require.Len(t, wire.requests, 1)
	assert.Equal(t, "tokens.our", compactKid(t, wire.requests[0]), "the request JWE is encrypted to tokens.our")
	assert.Equal(t, "tokens.peer", compactKid(t, wire.responses[0]), "the response JWE is encrypted to tokens.peer")
	inner, _, _, err := jose.Open(wire.requests[0], &jose.Policy{
		Direction: jose.DirectionInbound, DecryptKid: "tokens.our", VerifyKid: "tokens.peer",
		SigAlg: jose.DefaultSigAlg, KeyAlg: jose.DefaultKeyAlg, Enc: jose.DefaultEnc, Cty: jose.DefaultCty,
	}, resolver)
	require.NoError(t, err, "the inner JWS is signed by tokens.peer")
	assert.JSONEq(t, `{"pan":"4111111111111111"}`, string(inner))

	// The same key material under the hyphenated name is another kid: the route refuses it.
	lookalike := jositest.SealForTest(t, []byte(`{"pan":"4111111111111111"}`), &jose.Policy{
		Direction: jose.DirectionOutbound, SignKid: "tokens.peer", EncryptKid: "tokens-our",
		SigAlg: jose.DefaultSigAlg, KeyAlg: jose.DefaultKeyAlg, Enc: jose.DefaultEnc, Cty: jose.DefaultCty,
	}, jositest.NewTestResolver(map[string]any{"tokens.peer": peerPriv, "tokens-our": &ourPriv.PublicKey}))
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, url, strings.NewReader(lookalike))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/jose")
	plain, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer plain.Body.Close()
	body, err := io.ReadAll(plain.Body)
	require.NoError(t, err)
	assert.GreaterOrEqual(t, plain.StatusCode, 400)
	assert.Contains(t, string(body), "JOSE_KID_UNKNOWN")

	_, err = jose.ScanType(reflect.TypeOf(badDottedTagReq{}), jose.DirectionInbound)
	var jerr *jose.Error
	require.ErrorAs(t, err, &jerr)
	assert.Equal(t, "JOSE_TAG_KID_INVALID", jerr.Code)
}
