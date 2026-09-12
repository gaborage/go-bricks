package testing

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"math/big"
	nethttp "net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fetchJWKS performs one GET against the fake endpoint and returns the status
// and body.
func fetchJWKS(t *testing.T, srv *JWKSServer) (status int, body []byte) {
	t.Helper()
	req, err := nethttp.NewRequestWithContext(context.Background(), nethttp.MethodGet, srv.URL(), nethttp.NoBody)
	require.NoError(t, err)
	resp, err := srv.HTTPClient().Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	body, err = io.ReadAll(resp.Body)
	require.NoError(t, err)
	return resp.StatusCode, body
}

// decodeJWKS parses the served document into its entries.
func decodeJWKS(t *testing.T, body []byte) []map[string]any {
	t.Helper()
	var doc struct {
		Keys []map[string]any `json:"keys"`
	}
	require.NoError(t, json.Unmarshal(body, &doc))
	return doc.Keys
}

func newTestJWKSServer(t *testing.T) *JWKSServer {
	t.Helper()
	srv := NewJWKSServer(NewIssuer())
	t.Cleanup(srv.Close)
	return srv
}

func TestJWKSServerPublishesTheIssuersSigningKey(t *testing.T) {
	srv := newTestJWKSServer(t)

	status, body := fetchJWKS(t, srv)

	assert.Equal(t, nethttp.StatusOK, status)
	entries := decodeJWKS(t, body)
	require.Len(t, entries, 1)
	assert.Equal(t, "RSA", entries[0]["kty"])
	assert.Equal(t, DefaultKeyID, entries[0]["kid"])
	assert.Equal(t, "sig", entries[0]["use"])
}

func TestJWKSServerEncodesTheModulusAndExponentOfTheSigningKey(t *testing.T) {
	srv := newTestJWKSServer(t)

	_, body := fetchJWKS(t, srv)

	entries := decodeJWKS(t, body)
	require.Len(t, entries, 1)
	modulus, err := base64.RawURLEncoding.DecodeString(entries[0]["n"].(string))
	require.NoError(t, err)
	exponent, err := base64.RawURLEncoding.DecodeString(entries[0]["e"].(string))
	require.NoError(t, err)
	want := srv.Issuer().PublicKey(DefaultKeyID)
	assert.Equal(t, 0, new(big.Int).SetBytes(modulus).Cmp(want.N))
	assert.Equal(t, int64(want.E), new(big.Int).SetBytes(exponent).Int64())
}

func TestJWKSServerPublishesARotatedKey(t *testing.T) {
	const rotated = "rotated-kid"
	srv := newTestJWKSServer(t)

	srv.Rotate(rotated)
	_, body := fetchJWKS(t, srv)

	entries := decodeJWKS(t, body)
	require.Len(t, entries, 2)
	kids := []string{entries[0]["kid"].(string), entries[1]["kid"].(string)}
	assert.Contains(t, kids, rotated)
	assert.Contains(t, kids, DefaultKeyID)
	assert.Equal(t, rotated, srv.Issuer().ActiveKeyID())
}

func TestJWKSServerServesEachFailureMode(t *testing.T) {
	tests := []struct {
		name       string
		mode       JWKSMode
		wantStatus int
		assertBody func(t *testing.T, body []byte)
	}{
		{
			name:       "server_error",
			mode:       JWKSServerError,
			wantStatus: nethttp.StatusServiceUnavailable,
			assertBody: func(t *testing.T, body []byte) { assert.Empty(t, body) },
		},
		{
			name:       "malformed",
			mode:       JWKSMalformed,
			wantStatus: nethttp.StatusOK,
			assertBody: func(t *testing.T, body []byte) {
				var doc map[string]any
				assert.Error(t, json.Unmarshal(body, &doc))
			},
		},
		{
			name:       "oversized",
			mode:       JWKSOversized,
			wantStatus: nethttp.StatusOK,
			assertBody: func(t *testing.T, body []byte) {
				assert.GreaterOrEqual(t, len(body), DefaultOversizedBytes)
				var doc map[string]any
				assert.NoError(t, json.Unmarshal(body, &doc), "the oversized body must still be valid json")
			},
		},
		{
			name:       "non_rsa_only",
			mode:       JWKSNonRSAOnly,
			wantStatus: nethttp.StatusOK,
			assertBody: func(t *testing.T, body []byte) {
				assert.NotContains(t, string(body), `"RSA"`)
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			srv := newTestJWKSServer(t)
			srv.SetMode(tc.mode)

			status, body := fetchJWKS(t, srv)

			assert.Equal(t, tc.wantStatus, status)
			tc.assertBody(t, body)
		})
	}
}

func TestJWKSServerHonorsTheOversizedSize(t *testing.T) {
	const size = 4096
	srv := newTestJWKSServer(t)
	srv.SetMode(JWKSOversized)
	srv.SetOversizedBytes(size)

	_, body := fetchJWKS(t, srv)

	assert.GreaterOrEqual(t, len(body), size)
	assert.Less(t, len(body), DefaultOversizedBytes)
}

func TestJWKSServerPublishesAnECKeyAlongsideTheRSAKey(t *testing.T) {
	const ecKID = "ec-kid"
	srv := newTestJWKSServer(t)

	srv.AddECKey(ecKID)
	_, body := fetchJWKS(t, srv)

	entries := decodeJWKS(t, body)
	require.Len(t, entries, 2)
	var ec map[string]any
	for _, entry := range entries {
		if entry["kty"] == "EC" {
			ec = entry
		}
	}
	require.NotNil(t, ec)
	assert.Equal(t, ecKID, ec["kid"])
	assert.Equal(t, "P-256", ec["crv"])
	x, err := base64.RawURLEncoding.DecodeString(ec["x"].(string))
	require.NoError(t, err)
	assert.Len(t, x, 32, "an EC coordinate is left-padded to the curve's byte width")
}

func TestJWKSServerRecordsEveryRequest(t *testing.T) {
	srv := newTestJWKSServer(t)

	fetchJWKS(t, srv)
	fetchJWKS(t, srv)

	requests := srv.Requests()
	require.Len(t, requests, 2)
	assert.Equal(t, 2, srv.RequestCount())
	assert.Equal(t, nethttp.MethodGet, requests[0].Method)
	assert.Equal(t, JWKSPath, requests[0].Path)
	assert.False(t, requests[1].At.Before(requests[0].At), "the log is ordered oldest first")

	srv.ResetRequests()
	assert.Zero(t, srv.RequestCount())
}

func TestJWKSServerRequestsReturnsACopy(t *testing.T) {
	srv := newTestJWKSServer(t)
	fetchJWKS(t, srv)

	requests := srv.Requests()
	requests[0].Path = "mutated"

	assert.Equal(t, JWKSPath, srv.Requests()[0].Path)
}

func TestJWKSServerURLIsAnHTTPSEndpoint(t *testing.T) {
	srv := newTestJWKSServer(t)

	assert.True(t, strings.HasPrefix(srv.URL(), "https://"))
	assert.True(t, strings.HasSuffix(srv.URL(), JWKSPath))
}

func TestEncodeExponentTrimsLeadingZeroBytes(t *testing.T) {
	assert.Equal(t, "AQAB", encodeExponent(65537))
	assert.Equal(t, "Aw", encodeExponent(3))
}

func TestPadDocumentAlwaysGrowsTheDocumentPastTheRequestedSize(t *testing.T) {
	document := []byte(`{"keys":[]}`)

	padded := padDocument(document, 4)

	assert.Greater(t, len(padded), 4, "a document already past the size still gets its filler")
	var doc map[string]any
	require.NoError(t, json.Unmarshal(padded, &doc))
	assert.Equal(t, "pppp", doc["padding"])
}
