package testing_test

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
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
			require.NoError(t, cert.CheckSignature(cert.SignatureAlgorithm, cert.RawTBSCertificate, cert.Signature))
			assert.True(t, time.Now().Before(cert.NotAfter))
		})
	}
}

// TestSelfSignedCertKeyPEMParsesAsAKeyPair pins the fixture's contract: the two
// PEM blocks it returns load together as one tls.Certificate, and successive
// calls mint distinct certificates.
func TestSelfSignedCertKeyPEMParsesAsAKeyPair(t *testing.T) {
	certPEM, keyPEM := testconsts.SelfSignedCertKeyPEM(t)

	pair, err := tls.X509KeyPair(certPEM, keyPEM)
	require.NoError(t, err)
	require.Len(t, pair.Certificate, 1)

	otherCertPEM, _ := testconsts.SelfSignedCertKeyPEM(t)
	assert.NotEqual(t, certPEM, otherCertPEM)
}

// TestCAAndServerCertPEMLeafVerifiesAgainstItsCA pins the fixture's contract:
// the leaf chains to the returned CA, carries both the DNS and the IP default
// SANs, and loads with its key as one tls.Certificate.
func TestCAAndServerCertPEMLeafVerifiesAgainstItsCA(t *testing.T) {
	caPEM, certPEM, keyPEM := testconsts.CAAndServerCertPEM(t)

	pool := x509.NewCertPool()
	require.True(t, pool.AppendCertsFromPEM(caPEM), "CA PEM should parse into a pool")

	block, _ := pem.Decode(certPEM)
	require.NotNil(t, block)
	leaf, err := x509.ParseCertificate(block.Bytes)
	require.NoError(t, err)

	for _, host := range []string{"localhost", "127.0.0.1", "::1"} {
		t.Run(host, func(t *testing.T) {
			_, verifyErr := leaf.Verify(x509.VerifyOptions{
				Roots:     pool,
				DNSName:   host,
				KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
			})
			require.NoError(t, verifyErr)
		})
	}

	pair, err := tls.X509KeyPair(certPEM, keyPEM)
	require.NoError(t, err)
	assert.Len(t, pair.Certificate, 1)
}

// TestCAAndServerCertPEMHonorsExplicitHosts checks that explicit hosts replace
// the defaults and split into DNS versus IP SANs by parseability.
func TestCAAndServerCertPEMHonorsExplicitHosts(t *testing.T) {
	caPEM, certPEM, _ := testconsts.CAAndServerCertPEM(t, "redis.test", "10.1.2.3")

	pool := x509.NewCertPool()
	require.True(t, pool.AppendCertsFromPEM(caPEM))

	block, _ := pem.Decode(certPEM)
	require.NotNil(t, block)
	leaf, err := x509.ParseCertificate(block.Bytes)
	require.NoError(t, err)

	assert.Equal(t, []string{"redis.test"}, leaf.DNSNames)
	require.Len(t, leaf.IPAddresses, 1)
	assert.Equal(t, "10.1.2.3", leaf.IPAddresses[0].String())

	_, err = leaf.Verify(x509.VerifyOptions{Roots: pool, DNSName: "redis.test"})
	require.NoError(t, err)

	_, err = leaf.Verify(x509.VerifyOptions{Roots: pool, DNSName: "localhost"})
	require.Error(t, err, "a host outside the requested set must not verify")
}
