package jose

import (
	"encoding/base64"
	"strings"
	"testing"

	jose "github.com/go-jose/go-jose/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gaborage/go-bricks/jose/internal/cryptoadapter"
)

// detachedJWE is a real kid-less inner JWE, the string the profile's peer signs.
func detachedJWE(t *testing.T) string {
	t.Helper()
	_, encPub := bareKeys()
	return mintMinimalInner(t, encPub, jose.RSA_OAEP_256, jose.A256GCM, nil)
}

// detachedSignature signs payload RS512 and returns the detached form. mintDetachedOuter also
// asserts that AttachDetachedJWS rebuilds go-jose's own attached form byte for byte.
func detachedSignature(t *testing.T, payload string) string {
	t.Helper()
	signPriv, _ := signingKeys()
	return mintDetachedOuter(t, signPriv, jose.RS512, payload, nil)
}

func middleSegment(t *testing.T, compact string) string {
	t.Helper()
	segments := strings.Split(compact, ".")
	require.Len(t, segments, 3)
	return segments[1]
}

// The three payload lengths cover every residue mod 3, so a padded encoding cannot pass the
// byte-for-byte comparison detachedSignature makes.
func TestAttachDetachedJWSMatchesGoJoseCompactSerialize(t *testing.T) {
	jwe := detachedJWE(t)
	for _, payload := range []string{jwe, jwe + "x", jwe + "xx"} {
		detachedSignature(t, payload)
	}
}

func TestAttachDetachedJWSEncodesJWEVerbatim(t *testing.T) {
	jwe := detachedJWE(t)
	detached := detachedSignature(t, jwe)
	plain, err := AttachDetachedJWS(detached, jwe)
	require.NoError(t, err)

	spaced, err := AttachDetachedJWS(detached, jwe+" ")
	require.NoError(t, err)
	assert.NotEqual(t, middleSegment(t, plain), middleSegment(t, spaced))
	decoded, err := base64.RawURLEncoding.DecodeString(middleSegment(t, spaced))
	require.NoError(t, err)
	assert.Equal(t, jwe+" ", string(decoded))

	padded, err := AttachDetachedJWS(" \t"+detached+"\n", jwe)
	require.NoError(t, err)
	assert.Equal(t, plain, padded)
}

func TestAttachDetachedJWSRefusals(t *testing.T) {
	jwe := detachedJWE(t)
	detached := detachedSignature(t, jwe)
	header, signature, _ := strings.Cut(detached, "..")
	jsonSerialization := `{"protected":"` + header + `","payload":"","signature":"` + signature + `"}`

	tests := []struct {
		name     string
		detached string
		jwe      string
		cause    error // the adapter's refusal; nil when a rule of this package refused
	}{
		{"embedded_payload", spliceDetached(t, detached, jwe), jwe, nil},
		{"two_segments", header + "." + signature, jwe, cryptoadapter.ErrNotCompact},
		{"five_segment_jwe", jwe, jwe, nil},
		{"json_serialization", jsonSerialization, jwe, cryptoadapter.ErrNotCompact},
		{"jws_scheme_prefix", "JWS " + detached, jwe, cryptoadapter.ErrNotCompact},
		{"interior_whitespace", header[:8] + " " + header[8:] + ".." + signature, jwe, cryptoadapter.ErrNotCompact},
		{"non_base64url_character", header + ".." + signature[:10] + "+" + signature[11:], jwe, cryptoadapter.ErrNotCompact},
		{"empty_header_segment", ".." + signature, jwe, nil},
		{"empty_signature_segment", header + "..", jwe, nil},
		// 16 KiB + 4: over the adapter's bound and not 1 mod 4, so only the size bound refuses it.
		{"oversize_header", strings.Repeat("A", 16*1024+4) + ".." + signature, jwe, cryptoadapter.ErrHeaderTooLarge},
		{"empty_jwe", detached, "", nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := AttachDetachedJWS(tt.detached, tt.jwe)
			assert.Empty(t, got)
			require.ErrorIs(t, err, ErrMalformed)
			var jerr *Error
			require.ErrorAs(t, err, &jerr)
			assert.Equal(t, codeMalformed, jerr.Code)
			assert.Equal(t, 400, jerr.Status)
			assert.Equal(t, "Malformed detached JWS", jerr.Message)
			if tt.cause == nil {
				assert.NoError(t, jerr.Cause)
				return
			}
			assert.ErrorIs(t, jerr.Cause, tt.cause)
		})
	}
}
