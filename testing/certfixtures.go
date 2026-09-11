package testing

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"testing"
	"time"
)

// SelfSignedCert issues a minimal self-signed leaf certificate for key, valid
// for one hour. A fixture only: tests that need a certificate to pair with a
// key (PKCS#12 bundles, TLS) build one here instead of hand-rolling x509
// templates.
func SelfSignedCert(t testing.TB, key crypto.Signer) *x509.Certificate {
	t.Helper()
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(1), NotAfter: time.Now().Add(time.Hour)}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, key.Public(), key)
	if err != nil {
		t.Fatalf("SelfSignedCert: create: %v", err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatalf("SelfSignedCert: parse: %v", err)
	}
	return cert
}

// SelfSignedCertKeyPEM issues a fresh P-256 self-signed certificate and returns
// it with its private key, both PEM-encoded ("CERTIFICATE" and "EC PRIVATE KEY"
// blocks) and ready for tls.X509KeyPair. Every call mints a new key, so two
// calls yield two distinct certificates.
func SelfSignedCertKeyPEM(t testing.TB) (certPEM, keyPEM []byte) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("SelfSignedCertKeyPEM: generate key: %v", err)
	}
	cert := SelfSignedCert(t, key)
	der, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatalf("SelfSignedCertKeyPEM: marshal key: %v", err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: pemTypeCertificate, Bytes: cert.Raw}),
		pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: der})
}

// pemTypeCertificate is the PEM block type every certificate in this file is
// wrapped in.
const pemTypeCertificate = "CERTIFICATE"

// caCommonName labels the throwaway root CAAndServerCertPEM mints, so a failing
// handshake in a test log names the fixture rather than an anonymous issuer.
const caCommonName = "go-bricks test CA"

// defaultCertHosts are the SANs a server certificate gets when the caller names
// none: the three addresses a container mapped onto the loopback interface can
// present itself as.
var defaultCertHosts = []string{"localhost", "127.0.0.1", "::1"}

// CAAndServerCertPEM mints a throwaway P-256 CA and a server certificate signed
// by it, all three PEM-encoded ("CERTIFICATE", "CERTIFICATE" and "EC PRIVATE
// KEY") and valid for one hour. The leaf carries ExtKeyUsageServerAuth and one
// SAN per host — a DNS name for anything that is not an IP literal, an IP SAN
// for anything that parses as one. With no hosts it covers localhost, 127.0.0.1
// and ::1.
//
// It is the fixture for a real TLS handshake: caPEM is what the client trusts,
// serverCertPEM/serverKeyPEM are what the server presents.
func CAAndServerCertPEM(t testing.TB, hosts ...string) (caPEM, serverCertPEM, serverKeyPEM []byte) {
	t.Helper()

	if len(hosts) == 0 {
		hosts = defaultCertHosts
	}

	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("CAAndServerCertPEM: generate CA key: %v", err)
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
	caDER, err := x509.CreateCertificate(rand.Reader, caTmpl, caTmpl, caKey.Public(), caKey)
	if err != nil {
		t.Fatalf("CAAndServerCertPEM: create CA: %v", err)
	}
	caCert, err := x509.ParseCertificate(caDER)
	if err != nil {
		t.Fatalf("CAAndServerCertPEM: parse CA: %v", err)
	}

	leafKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("CAAndServerCertPEM: generate server key: %v", err)
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

	leafDER, err := x509.CreateCertificate(rand.Reader, leafTmpl, caCert, leafKey.Public(), caKey)
	if err != nil {
		t.Fatalf("CAAndServerCertPEM: create server cert: %v", err)
	}

	leafKeyDER, err := x509.MarshalECPrivateKey(leafKey)
	if err != nil {
		t.Fatalf("CAAndServerCertPEM: marshal server key: %v", err)
	}

	return pem.EncodeToMemory(&pem.Block{Type: pemTypeCertificate, Bytes: caDER}),
		pem.EncodeToMemory(&pem.Block{Type: pemTypeCertificate, Bytes: leafDER}),
		pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: leafKeyDER})
}
