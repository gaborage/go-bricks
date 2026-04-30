package jose

import (
	"errors"
	"testing"

	"github.com/gaborage/go-bricks/jose/internal/cryptoadapter"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Direct unit tests for mapDecryptError / mapVerifyError. The end-to-end roundtrip
// covers ErrParseEncrypted, ErrDecryptFailed, ErrParseSigned, and ErrVerifyFailed
// indirectly; the table tests below cover the remaining sentinels (ErrKidMissing,
// ErrKidMismatch, ErrTypRejected, default) plus the full mapVerifyError surface.

func TestMapDecryptErrorAllArms(t *testing.T) {
	hdr := cryptoadapter.Header{Kid: "k1", Alg: "RSA-OAEP-256", Enc: "A256GCM"}
	tests := []struct {
		name     string
		in       error
		wantCode string
		wantStat int
	}{
		{name: "parse_failed", in: cryptoadapter.ErrParseEncrypted, wantCode: "JOSE_MALFORMED", wantStat: 400},
		{name: "kid_missing", in: cryptoadapter.ErrKidMissing, wantCode: "JOSE_KID_MISSING", wantStat: 401},
		{name: "kid_mismatch", in: cryptoadapter.ErrKidMismatch, wantCode: "JOSE_KID_UNKNOWN", wantStat: 401},
		{name: "typ_rejected", in: cryptoadapter.ErrTypRejected, wantCode: "JOSE_TYP_REJECTED", wantStat: 400},
		{name: "decrypt_failed", in: cryptoadapter.ErrDecryptFailed, wantCode: "JOSE_DECRYPT_FAILED", wantStat: 401},
		{name: "default_unknown", in: errors.New("something else"), wantCode: "JOSE_DECRYPT_FAILED", wantStat: 401},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := mapDecryptError(tt.in, nil, &hdr)
			assert.Equal(t, tt.wantCode, got.Code)
			assert.Equal(t, tt.wantStat, got.Status)
		})
	}
}

func TestMapVerifyErrorAllArms(t *testing.T) {
	hdr := cryptoadapter.Header{Kid: "k1", Alg: "RS256"}
	tests := []struct {
		name     string
		in       error
		wantCode string
		wantStat int
	}{
		{name: "parse_failed", in: cryptoadapter.ErrParseSigned, wantCode: "JOSE_INNER_NOT_JWS", wantStat: 400},
		{name: "kid_missing", in: cryptoadapter.ErrKidMissing, wantCode: "JOSE_KID_MISSING", wantStat: 401},
		{name: "kid_mismatch", in: cryptoadapter.ErrKidMismatch, wantCode: "JOSE_KID_UNKNOWN", wantStat: 401},
		{name: "verify_failed", in: cryptoadapter.ErrVerifyFailed, wantCode: "JOSE_SIGNATURE_INVALID", wantStat: 401},
		{name: "default_unknown", in: errors.New("something else"), wantCode: "JOSE_SIGNATURE_INVALID", wantStat: 401},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := mapVerifyError(tt.in, nil, &hdr)
			assert.Equal(t, tt.wantCode, got.Code)
			assert.Equal(t, tt.wantStat, got.Status)
		})
	}
}

func TestParseClaimsNonObjectPayload(t *testing.T) {
	c := parseClaims([]byte("not-json-at-all"))
	require.NotNil(t, c)
	assert.Empty(t, c.Raw)
	assert.Empty(t, c.Issuer)
}

func TestParseClaimsAudAsString(t *testing.T) {
	c := parseClaims([]byte(`{"aud":"single-audience"}`))
	assert.Equal(t, []string{"single-audience"}, c.Audience)
}
