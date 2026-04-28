package testing_test

import (
	"errors"
	"testing"

	"github.com/gaborage/go-bricks/jose"
	jositest "github.com/gaborage/go-bricks/jose/testing"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSealOpenRoundtripViaHelpers(t *testing.T) {
	ourPriv, _ := jositest.GenerateTestKeyPair(t)
	peerPriv, _ := jositest.GenerateTestKeyPair(t)

	resolver := jositest.NewTestResolver(map[string]any{
		"our-key":  ourPriv,
		"peer-key": peerPriv,
	})

	outbound := &jose.Policy{
		Direction:  jose.DirectionOutbound,
		SignKid:    "peer-key",
		EncryptKid: "our-key",
		SigAlg:     jose.DefaultSigAlg,
		KeyAlg:     jose.DefaultKeyAlg,
		Enc:        jose.DefaultEnc,
		Cty:        jose.DefaultCty,
	}
	inbound := &jose.Policy{
		Direction:  jose.DirectionInbound,
		DecryptKid: "our-key",
		VerifyKid:  "peer-key",
		SigAlg:     jose.DefaultSigAlg,
		KeyAlg:     jose.DefaultKeyAlg,
		Enc:        jose.DefaultEnc,
		Cty:        jose.DefaultCty,
	}

	payload := []byte(`{"merchant":"acme","amount":1234}`)
	compact := jositest.SealForTest(t, payload, outbound, resolver)
	assert.NotEmpty(t, compact)

	plaintext, claims := jositest.OpenForTest(t, compact, inbound, resolver)
	assert.Equal(t, payload, plaintext)
	require.NotNil(t, claims)
}

func TestNewTestResolverAcceptsPublicOnly(t *testing.T) {
	_, ourPub := jositest.GenerateTestKeyPair(t)
	resolver := jositest.NewTestResolver(map[string]any{"peer-pub": ourPub})

	gotPub, err := resolver.PublicKey("peer-pub")
	require.NoError(t, err)
	assert.Equal(t, ourPub, gotPub)

	// Public-only entries cannot satisfy a private-key lookup.
	_, err = resolver.PrivateKey("peer-pub")
	require.Error(t, err)
	var jerr *jose.Error
	require.True(t, errors.As(err, &jerr))
	assert.Equal(t, "JOSE_KID_UNKNOWN", jerr.Code)
}

func TestNewTestResolverPanicsOnUnsupportedType(t *testing.T) {
	assert.PanicsWithValue(t,
		`jositest: unsupported key type for kid "weird": string`,
		func() {
			jositest.NewTestResolver(map[string]any{"weird": "not-a-key"})
		},
	)
}
