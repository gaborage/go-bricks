package testing

import (
	"crypto"
	"crypto/rand"
	"crypto/x509"
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
