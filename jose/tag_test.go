package jose

import (
	"errors"
	"testing"

	jose "github.com/go-jose/go-jose/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseTagInboundValid(t *testing.T) {
	p, err := ParseTag("decrypt=our-signing,verify=visa-verify", DirectionInbound)
	require.NoError(t, err)
	assert.Equal(t, DirectionInbound, p.Direction)
	assert.Equal(t, "our-signing", p.DecryptKid)
	assert.Equal(t, "visa-verify", p.VerifyKid)
	assert.Equal(t, DefaultSigAlg, p.SigAlg)
	assert.Equal(t, DefaultKeyAlg, p.KeyAlg)
	assert.Equal(t, DefaultEnc, p.Enc)
}

func TestParseTagOutboundValid(t *testing.T) {
	p, err := ParseTag("sign=our-signing,encrypt=visa-encrypt", DirectionOutbound)
	require.NoError(t, err)
	assert.Equal(t, DirectionOutbound, p.Direction)
	assert.Equal(t, "our-signing", p.SignKid)
	assert.Equal(t, "visa-encrypt", p.EncryptKid)
}

func TestParseTagAlgorithmOverrides(t *testing.T) {
	p, err := ParseTag("decrypt=ours,verify=peer,sig_alg=PS256", DirectionInbound)
	require.NoError(t, err)
	assert.Equal(t, jose.PS256, p.SigAlg)
}

func TestParseTagInvalid(t *testing.T) {
	tests := []struct {
		name     string
		tag      string
		dir      Direction
		wantCode string
	}{
		{name: "empty_tag", tag: "", dir: DirectionInbound, wantCode: "JOSE_TAG_EMPTY"},
		{name: "no_equals", tag: "decrypt", dir: DirectionInbound, wantCode: "JOSE_TAG_MALFORMED"},
		{name: "unknown_key", tag: "decrypt=ours,frobnicate=true", dir: DirectionInbound, wantCode: "JOSE_TAG_UNKNOWN_KEY"},
		{name: "empty_value", tag: "decrypt=ours,verify=", dir: DirectionInbound, wantCode: "JOSE_TAG_EMPTY_VALUE"},
		{name: "kid_special_chars", tag: "decrypt=ours,verify=peer kid", dir: DirectionInbound, wantCode: "JOSE_TAG_KID_INVALID"},
		{name: "kid_with_dot", tag: "decrypt=ours,verify=peer.kid", dir: DirectionInbound, wantCode: "JOSE_TAG_KID_INVALID"},
		{name: "disallowed_sig_alg", tag: "decrypt=ours,verify=peer,sig_alg=HS256", dir: DirectionInbound, wantCode: "JOSE_ALGORITHM_DISALLOWED"},
		{name: "disallowed_key_alg", tag: "decrypt=ours,verify=peer,key_alg=RSA1_5", dir: DirectionInbound, wantCode: "JOSE_ALGORITHM_DISALLOWED"},
		{name: "inbound_missing_decrypt", tag: "verify=peer", dir: DirectionInbound, wantCode: "JOSE_POLICY_INCOMPLETE"},
		{name: "inbound_missing_verify", tag: "decrypt=ours", dir: DirectionInbound, wantCode: "JOSE_POLICY_INCOMPLETE"},
		{name: "outbound_missing_sign", tag: "encrypt=peer", dir: DirectionOutbound, wantCode: "JOSE_POLICY_INCOMPLETE"},
		{name: "inbound_with_outbound_keys", tag: "decrypt=ours,verify=peer,sign=oops", dir: DirectionInbound, wantCode: "JOSE_POLICY_DIRECTION_MISMATCH"},
		{name: "outbound_with_inbound_keys", tag: "sign=ours,encrypt=peer,decrypt=oops", dir: DirectionOutbound, wantCode: "JOSE_POLICY_DIRECTION_MISMATCH"},
		{name: "duplicate_verify_key", tag: "decrypt=ours,verify=a,verify=b", dir: DirectionInbound, wantCode: "JOSE_TAG_DUPLICATE_KEY"},
		{name: "duplicate_decrypt_key", tag: "decrypt=ours,decrypt=other,verify=peer", dir: DirectionInbound, wantCode: "JOSE_TAG_DUPLICATE_KEY"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := ParseTag(tt.tag, tt.dir)
			require.Error(t, err)
			var jerr *Error
			require.True(t, errors.As(err, &jerr))
			assert.Equal(t, tt.wantCode, jerr.Code, "expected code %q, got %q", tt.wantCode, jerr.Code)
		})
	}
}
