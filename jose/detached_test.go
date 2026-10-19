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

// signedSerializations signs payload RS512 with go-jose directly and returns three
// serializations of the one signature object: detached, attached and JSON.
func signedSerializations(t *testing.T, payload string) (detached, attached, full string) {
	t.Helper()
	signPriv, _ := signingKeys()
	signer, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.RS512, Key: signPriv}, nil)
	require.NoError(t, err)
	obj, err := signer.Sign([]byte(payload))
	require.NoError(t, err)
	detached, err = obj.DetachedCompactSerialize()
	require.NoError(t, err)
	attached, err = obj.CompactSerialize()
	require.NoError(t, err)
	return detached, attached, obj.FullSerialize()
}

// detachedJWE is a real kid-less inner JWE, the string the profile's peer signs.
func detachedJWE(t *testing.T) string {
	t.Helper()
	_, encPub := bareKeys()
	return mintMinimalInner(t, encPub, jose.RSA_OAEP_256, jose.A256GCM, nil)
}

func middleSegment(t *testing.T, compact string) string {
	t.Helper()
	segments := strings.Split(compact, ".")
	require.Len(t, segments, 3)
	return segments[1]
}

// The three payload lengths cover every residue mod 3, so a padded encoding cannot pass.
func TestAttachDetachedJWSMatchesGoJoseCompactSerialize(t *testing.T) {
	jwe := detachedJWE(t)
	for _, payload := range []string{jwe, jwe + "x", jwe + "xx"} {
		detached, attached, _ := signedSerializations(t, payload)
		got, err := AttachDetachedJWS(detached, payload)
		require.NoError(t, err)
		assert.Equal(t, attached, got)
	}
}

func TestAttachDetachedJWSEncodesJWEVerbatim(t *testing.T) {
	jwe := detachedJWE(t)
	detached, _, _ := signedSerializations(t, jwe)
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
	detached, attached, full := signedSerializations(t, jwe)
	header, _, _ := strings.Cut(detached, ".")
	signature := detached[strings.LastIndexByte(detached, '.')+1:]

	tests := []struct {
		name     string
		detached string
		jwe      string
	}{
		{"embedded_payload", attached, jwe},
		{"two_segments", header + "." + signature, jwe},
		{"five_segment_jwe", jwe, jwe},
		{"json_serialization", full, jwe},
		{"jws_scheme_prefix", "JWS " + detached, jwe},
		{"interior_whitespace", header[:8] + " " + header[8:] + ".." + signature, jwe},
		{"non_base64url_character", header + ".." + signature[:10] + "+" + signature[11:], jwe},
		{"empty_header_segment", ".." + signature, jwe},
		{"empty_signature_segment", header + "..", jwe},
		// Not 1 mod 4, so only the size bound can refuse it.
		{"oversize_header", strings.Repeat("A", 16*1024+4) + ".." + signature, jwe},
		{"empty_jwe", detached, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := AttachDetachedJWS(tt.detached, tt.jwe)
			assert.Empty(t, got)
			requireJOSEErrorStatus(t, err, codeMalformed, 400)
			require.ErrorIs(t, err, ErrMalformed)
			var jerr *Error
			require.ErrorAs(t, err, &jerr)
			assert.Equal(t, "Malformed detached JWS", jerr.Message)
		})
	}
}

// Cause carries the adapter's refusal and nothing for a rule this package applies itself.
func TestAttachDetachedJWSRefusalCause(t *testing.T) {
	jwe := detachedJWE(t)
	detached, attached, _ := signedSerializations(t, jwe)
	signature := detached[strings.LastIndexByte(detached, '.')+1:]
	cause := func(detached, jwe string) error {
		t.Helper()
		_, err := AttachDetachedJWS(detached, jwe)
		var jerr *Error
		require.ErrorAs(t, err, &jerr)
		return jerr.Cause
	}

	require.ErrorIs(t, cause(strings.Repeat("A", 16*1024+4)+".."+signature, jwe), cryptoadapter.ErrHeaderTooLarge)
	assert.NoError(t, cause(attached, jwe), "embedded payload")
	assert.NoError(t, cause(detached, ""), "empty jwe")
}
