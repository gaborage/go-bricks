package migration

import (
	"encoding/base64"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestScramSHA256VerifierMatchesRFC7677 is the RFC 7677 §3 known answer: password "pencil",
// the RFC's salt, 4096 iterations. It is a public test vector, so ADR-102's rule on asserting
// key material does not apply.
func TestScramSHA256VerifierMatchesRFC7677(t *testing.T) {
	salt, err := base64.StdEncoding.DecodeString("W22ZaJ0SNY7soEsUEjb6gQ==")
	require.NoError(t, err)

	got, err := scramSHA256Verifier("pencil", salt, 4096)
	require.NoError(t, err)
	assert.Equal(t,
		"SCRAM-SHA-256$4096:W22ZaJ0SNY7soEsUEjb6gQ==$WG5d8oPm3OtcPnkdi4Uo7BkeZkBFzpcXkuLmtbsT4qY=:wfPLwcE6nTWhTAmQ7tl2KeoiWGPlZqQxSrmfPwDl2dU=",
		got)
}

// TestScramSHA256VerifierUsesStandardBase64 pins the padded standard alphabet: a URL-safe or
// unpadded encoding would render this salt as -_-_… and PostgreSQL would refuse the verifier.
func TestScramSHA256VerifierUsesStandardBase64(t *testing.T) {
	salt := []byte{
		0xfb, 0xff, 0xbf, 0xfb, 0xff, 0xbf, 0xfb, 0xff, 0xbf, 0xfb, 0xff, 0xbf, 0xfb, 0xff, 0xbf, 0xfb,
	}
	got, err := scramSHA256Verifier("correct horse battery staple", salt, 4096)
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(got, scramSHA256Prefix+"4096:+/+/+/+/+/+/+/+/+/+/+w==$"),
		"verifier length %d", len(got))
}

func TestScramSHA256VerifierIterationBound(t *testing.T) {
	_, err := scramSHA256Verifier("pencil", []byte("0123456789abcdef"), 0)
	require.ErrorIs(t, err, errSCRAMIterations, "a non-positive iteration count cannot derive a key")

	got, err := scramSHA256Verifier("pencil", []byte("0123456789abcdef"), 1)
	require.NoError(t, err, "one iteration is the smallest usable count")
	assert.True(t, strings.HasPrefix(got, scramSHA256Prefix+"1:"), "verifier length %d", len(got))
}

func TestIsSCRAMSafePassword(t *testing.T) {
	cases := []struct {
		name     string
		password string
		want     bool
	}{
		{name: "printable_ascii", password: "Abc 123 ~!@#$%^&*()_+-={}[]|:;'<>,.?/`\"\\", want: true},
		{name: "space_is_printable", password: " ", want: true},
		{name: "tilde_upper_bound", password: "~", want: true},
		{name: "empty", password: "", want: true},
		{name: "tab", password: "a\tb", want: false},
		{name: "unit_separator_below_space", password: "a\x1fb", want: false},
		{name: "del", password: "a\x7fb", want: false},
		{name: "latin_e_acute", password: "café", want: false},
		{name: "invalid_utf8_byte", password: "a\xffb", want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, isSCRAMSafePassword(tc.password))
		})
	}
}
