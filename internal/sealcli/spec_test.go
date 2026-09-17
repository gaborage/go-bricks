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
			name: "sign_kid_not_a_generation", signKid: "svc-sign", encryptKid: testEncKid, subject: "card",
			wantErr: `-sign-kid "svc-sign" is not a generation: expected <logical>-v<N> with N a positive integer without leading zeros`,
		},
		{
			name: "encrypt_kid_not_a_generation", signKid: testSignKid, encryptKid: "aud-encrypt-v01", subject: "card",
			wantErr: `-encrypt-kid "aud-encrypt-v01" is not a generation: expected <logical>-v<N> with N a positive integer without leading zeros`,
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
