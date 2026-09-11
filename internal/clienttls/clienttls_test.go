package clienttls

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	gbtesting "github.com/gaborage/go-bricks/testing"
)

// certKeyPEM issues a self-signed certificate and returns it with its key, both
// PEM-encoded.
func certKeyPEM(t *testing.T) (certPEM, keyPEM []byte) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	cert := gbtesting.SelfSignedCert(t, key)
	der, err := x509.MarshalPKCS8PrivateKey(key)
	require.NoError(t, err)
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cert.Raw}),
		pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})
}

// writeTemp writes data to a file in t.TempDir and returns its path.
func writeTemp(t *testing.T, name string, data []byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	require.NoError(t, os.WriteFile(path, data, 0o600))
	return path
}

const testPrefix = "cache: redis: tls:"

func TestBuildWithoutMaterial(t *testing.T) {
	out, err := Build(testPrefix, &Material{})

	require.NoError(t, err)
	require.NotNil(t, out)
	assert.Equal(t, uint16(tls.VersionTLS12), out.MinVersion)
	assert.Empty(t, out.ServerName)
	assert.Nil(t, out.RootCAs)
	assert.Empty(t, out.Certificates)
	assert.False(t, out.InsecureSkipVerify)
}

func TestBuildCarriesServerNameAndMinVersion(t *testing.T) {
	out, err := Build(testPrefix, &Material{ServerName: "cache.internal", MinVersion: "1.3"})

	require.NoError(t, err)
	assert.Equal(t, "cache.internal", out.ServerName)
	assert.Equal(t, uint16(tls.VersionTLS13), out.MinVersion)
}

func TestBuildLoadsCAFromValue(t *testing.T) {
	caPEM, _ := certKeyPEM(t)

	out, err := Build(testPrefix, &Material{CAValue: base64.StdEncoding.EncodeToString(caPEM)})

	require.NoError(t, err)
	assert.NotNil(t, out.RootCAs)
}

func TestBuildLoadsCAFromFile(t *testing.T) {
	caPEM, _ := certKeyPEM(t)

	out, err := Build(testPrefix, &Material{CAFile: writeTemp(t, "ca.pem", caPEM)})

	require.NoError(t, err)
	assert.NotNil(t, out.RootCAs)
}

func TestBuildLoadsClientCertificate(t *testing.T) {
	certPEM, keyPEM := certKeyPEM(t)

	out, err := Build(testPrefix, &Material{
		CertFile: writeTemp(t, "cert.pem", certPEM),
		KeyValue: base64.StdEncoding.EncodeToString(keyPEM),
	})

	require.NoError(t, err)
	assert.Len(t, out.Certificates, 1)
}

func TestBuildRejectsUnpairedCertAndKey(t *testing.T) {
	certPEM, keyPEM := certKeyPEM(t)
	tests := []struct {
		name     string
		material Material
		want     string
	}{
		{
			name:     "cert_without_key",
			material: Material{CertValue: base64.StdEncoding.EncodeToString(certPEM)},
			want:     "cache: redis: tls: cert: set without a matching key",
		},
		{
			name:     "key_without_cert",
			material: Material{KeyValue: base64.StdEncoding.EncodeToString(keyPEM)},
			want:     "cache: redis: tls: key: set without a matching cert",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out, err := Build(testPrefix, &tt.material)

			require.Error(t, err)
			assert.Nil(t, out)
			assert.Equal(t, tt.want, err.Error())
		})
	}
}

func TestBuildRejectsBothCASources(t *testing.T) {
	caPEM, _ := certKeyPEM(t)

	out, err := Build(testPrefix, &Material{
		CAFile:  writeTemp(t, "ca.pem", caPEM),
		CAValue: base64.StdEncoding.EncodeToString(caPEM),
	})

	require.Error(t, err)
	assert.Nil(t, out)
	assert.Equal(t, "cache: redis: tls: ca: set file or value, not both", err.Error())
}

func TestBuildRejectsUnknownMinVersion(t *testing.T) {
	out, err := Build(testPrefix, &Material{MinVersion: "1.1"})

	require.Error(t, err)
	assert.Nil(t, out)
	assert.Equal(t, `cache: redis: tls: minversion "1.1": accepted values are "1.2" and "1.3"`, err.Error())
}

func TestHasAnyMaterial(t *testing.T) {
	tests := []struct {
		name     string
		material Material
		want     bool
	}{
		{name: "empty", material: Material{}, want: false},
		{name: "sni_and_floor_only", material: Material{ServerName: "cache.internal", MinVersion: "1.3"}, want: false},
		{name: "ca_file_set", material: Material{CAFile: "/run/secrets/ca.pem"}, want: true},
		{name: "key_value_set", material: Material{KeyValue: "cGVt"}, want: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, HasAnyMaterial(&tt.material))
		})
	}
}
