package sealcli

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDocumentSpec(t *testing.T) {
	cases := []struct {
		name       string
		signKid    string
		encryptKid string
		subject    string
		wantErr    string
	}{
		{name: "valid_generation_pair", signKid: testSignKid, encryptKid: testEncKid, subject: "card"},
		{
			name: "sign_kid_is_a_family", signKid: "svc-sign", encryptKid: testEncKid, subject: "card",
			wantErr: `-sign-kid "svc-sign" is a family, not a generation: pass svc-sign-v<N>`,
		},
		{
			name: "sign_kid_is_a_dotted_family", signKid: "payments.sign", encryptKid: testEncKid, subject: "card",
			wantErr: `-sign-kid "payments.sign" is a family, not a generation: pass payments.sign.v<N>`,
		},
		{
			name: "sign_kid_dotted_family_hyphen_marker", signKid: "payments.sign-v1", encryptKid: testEncKid, subject: "card",
			wantErr: `-sign-kid "payments.sign-v1" is not a generation: family "payments.sign" takes the marker of payments.sign.v1`,
		},
		{
			name: "encrypt_kid_undotted_family_segment_marker", signKid: testSignKid, encryptKid: "audit.v1", subject: "card",
			wantErr: `-encrypt-kid "audit.v1" is not a generation: family "audit" takes the marker of audit-v1`,
		},
		{
			name: "encrypt_kid_not_a_generation", signKid: testSignKid, encryptKid: "aud-encrypt-v01", subject: "card",
			wantErr: `-encrypt-kid "aud-encrypt-v01" is not a generation: expected <family>.v<N> (family with '.') or <family>-v<N> (family without '.'), with N a positive integer without leading zeros`,
		},
		{
			name: "encrypt_kid_bad_alphabet", signKid: testSignKid, encryptKid: "aud encrypt-v1", subject: "card",
			wantErr: `-encrypt-kid "aud encrypt-v1" is not a generation: expected <family>.v<N>`,
		},
		{name: "empty_subject", signKid: testSignKid, encryptKid: testEncKid, subject: "", wantErr: "non-empty subject path"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			spec, err := DocumentSpec(tc.signKid, tc.encryptKid, tc.subject)
			if tc.wantErr != "" {
				require.ErrorContains(t, err, tc.wantErr)
				assert.Nil(t, spec)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, "svc-sign", spec.SignLogical)
			assert.Equal(t, "aud-encrypt", spec.EncryptLogical)
			assert.Equal(t, "card", spec.SubjectPath)
			assert.Nil(t, spec.Type, "a document spec is type-free")
		})
	}
}

// TestDocumentSpecDottedGenerations: a dotted generation splits into its dotted
// family, the family the Spec carries and the opener pins (ADR-144).
func TestDocumentSpecDottedGenerations(t *testing.T) {
	spec, err := DocumentSpec("payments.sign.v1", "payments.encrypt.v12", "card")
	require.NoError(t, err)
	assert.Equal(t, "payments.sign", spec.SignLogical)
	assert.Equal(t, "payments.encrypt", spec.EncryptLogical)
}
