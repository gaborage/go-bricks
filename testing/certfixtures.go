package testing

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"testing"
	"time"
)

// caCommonName labels the throwaway root the CA fixtures mint, so a failing
// handshake in a test log names the fixture rather than an anonymous issuer.
const caCommonName = "go-bricks test CA"

// defaultCertHosts are the SANs a server certificate gets when the caller names
// none: the three addresses a container mapped onto the loopback interface can
// present itself as.
var defaultCertHosts = []string{"localhost", "127.0.0.1", "::1"}

// DefaultCertHosts returns the SANs a server certificate gets when the caller
// names none. A caller that has to decide in advance whether an address is
// covered (a container host resolved only after start, say) reads them from
// here instead of repeating the list.
func DefaultCertHosts() []string {
	return append([]string(nil), defaultCertHosts...)
}

// issueCert creates and parses one certificate: tmpl signed by parent (pass
// tmpl itself for a self-signed one) over pub, using signer's key.
func issueCert(tmpl, parent *x509.Certificate, pub crypto.PublicKey, signer crypto.Signer) (*x509.Certificate, error) {
	der, err := x509.CreateCertificate(rand.Reader, tmpl, parent, pub, signer)
	if err != nil {
		return nil, fmt.Errorf("create certificate: %w", err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, fmt.Errorf("parse certificate: %w", err)
	}
	return cert, nil
}

// encodeCertPEM wraps a certificate's DER in its PEM block.
func encodeCertPEM(cert *x509.Certificate) []byte {
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cert.Raw})
}

// newECKeyPEM mints a fresh P-256 key and returns it with its PEM encoding.
func newECKeyPEM() (key *ecdsa.PrivateKey, keyPEM []byte, err error) {
	key, err = ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, fmt.Errorf("generate key: %w", err)
	}
	der, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return nil, nil, fmt.Errorf("marshal key: %w", err)
	}
	return key, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: der}), nil
}

// SelfSignedCert issues a minimal self-signed leaf certificate for key, valid
// for one hour. A fixture only: tests that need a certificate to pair with a
// key (PKCS#12 bundles, TLS) build one here instead of hand-rolling x509
// templates.
func SelfSignedCert(t testing.TB, key crypto.Signer) *x509.Certificate {
	t.Helper()
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(1), NotAfter: time.Now().Add(time.Hour)}
	cert, err := issueCert(tmpl, tmpl, key.Public(), key)
	if err != nil {
		t.Fatalf("SelfSignedCert: %v", err)
	}
	return cert
}

// SelfSignedCertKeyPEM issues a fresh P-256 self-signed certificate and returns
// it with its private key, both PEM-encoded ("CERTIFICATE" and "EC PRIVATE KEY"
// blocks) and ready for tls.X509KeyPair. Every call mints a new key, so two
// calls yield two distinct certificates.
func SelfSignedCertKeyPEM(t testing.TB) (certPEM, keyPEM []byte) {
	t.Helper()
	key, keyPEM, err := newECKeyPEM()
	if err != nil {
		t.Fatalf("SelfSignedCertKeyPEM: %v", err)
	}
	cert := SelfSignedCert(t, key)
	return encodeCertPEM(cert), keyPEM
}

// CAAndServerCertPEM mints a throwaway P-256 CA and a server certificate signed
// by it, all three PEM-encoded ("CERTIFICATE", "CERTIFICATE" and "EC PRIVATE
// KEY") and valid for one hour. The leaf carries ExtKeyUsageServerAuth and one
// SAN per host — a DNS name for anything that is not an IP literal, an IP SAN
// for anything that parses as one. With no hosts it covers DefaultCertHosts.
//
// It is the fixture for a real TLS handshake: caPEM is what the client trusts,
// serverCertPEM/serverKeyPEM are what the server presents.
func CAAndServerCertPEM(t testing.TB, hosts ...string) (caPEM, serverCertPEM, serverKeyPEM []byte) {
	t.Helper()
	caPEM, serverCertPEM, serverKeyPEM, err := NewCAAndServerCertPEM(hosts...)
	if err != nil {
		t.Fatalf("CAAndServerCertPEM: %v", err)
	}
	return caPEM, serverCertPEM, serverKeyPEM
}

// NewCAAndServerCertPEM is CAAndServerCertPEM for callers that have no
// testing.TB to fail: a TestMain-style container starter, say. It returns the
// same three PEM blocks and any minting error instead of aborting the test.
// A fixture only: the throwaway CA and key it mints are for tests, and must
// never be used to produce production key material.
func NewCAAndServerCertPEM(hosts ...string) (caPEM, serverCertPEM, serverKeyPEM []byte, err error) {
	if len(hosts) == 0 {
		hosts = defaultCertHosts
	}

	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("generate CA key: %w", err)
	}

	caTmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: caCommonName},
		NotBefore:             time.Now().Add(-time.Minute),
		NotAfter:              time.Now().Add(time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
	}
	caCert, err := issueCert(caTmpl, caTmpl, caKey.Public(), caKey)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("CA: %w", err)
	}

	leafKey, leafKeyPEM, err := newECKeyPEM()
	if err != nil {
		return nil, nil, nil, fmt.Errorf("server key: %w", err)
	}

	leafTmpl := &x509.Certificate{
		SerialNumber: big.NewInt(2),
		Subject:      pkix.Name{CommonName: hosts[0]},
		NotBefore:    time.Now().Add(-time.Minute),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	for _, h := range hosts {
		if ip := net.ParseIP(h); ip != nil {
			leafTmpl.IPAddresses = append(leafTmpl.IPAddresses, ip)
			continue
		}
		leafTmpl.DNSNames = append(leafTmpl.DNSNames, h)
	}

	leafCert, err := issueCert(leafTmpl, caCert, leafKey.Public(), caKey)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("server cert: %w", err)
	}

	return encodeCertPEM(caCert), encodeCertPEM(leafCert), leafKeyPEM, nil
}
