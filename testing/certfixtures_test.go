package testing_test

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	testconsts "github.com/gaborage/go-bricks/testing"
)

func TestSelfSignedCertPairsWithTheKeyItWasIssuedFor(t *testing.T) {
	rsaKey, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	ecKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)

	for name, key := range map[string]crypto.Signer{"rsa": rsaKey, "ecdsa": ecKey} {
		t.Run(name, func(t *testing.T) {
			cert := testconsts.SelfSignedCert(t, key)

			pub, ok := key.Public().(interface{ Equal(crypto.PublicKey) bool })
			require.True(t, ok)
			assert.True(t, pub.Equal(cert.PublicKey))
			assert.NoError(t, cert.CheckSignature(cert.SignatureAlgorithm, cert.RawTBSCertificate, cert.Signature))
			assert.True(t, time.Now().Before(cert.NotAfter))
		})
	}
}
