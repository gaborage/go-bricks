package testing

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"math/big"
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
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cert.Raw}),
		pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: der})
}
