package clienttls

import (
	"crypto/tls"
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	gbtesting "github.com/gaborage/go-bricks/testing"
)

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
	caPEM, _ := gbtesting.SelfSignedCertKeyPEM(t)

	out, err := Build(testPrefix, &Material{CAValue: base64.StdEncoding.EncodeToString(caPEM)})

	require.NoError(t, err)
	assert.NotNil(t, out.RootCAs)
}

func TestBuildLoadsCAFromFile(t *testing.T) {
	caPEM, _ := gbtesting.SelfSignedCertKeyPEM(t)

	out, err := Build(testPrefix, &Material{CAFile: writeTemp(t, "ca.pem", caPEM)})

	require.NoError(t, err)
	assert.NotNil(t, out.RootCAs)
}

func TestBuildLoadsClientCertificate(t *testing.T) {
	certPEM, keyPEM := gbtesting.SelfSignedCertKeyPEM(t)

	out, err := Build(testPrefix, &Material{
		CertFile: writeTemp(t, "cert.pem", certPEM),
		KeyValue: base64.StdEncoding.EncodeToString(keyPEM),
	})

	require.NoError(t, err)
	assert.Len(t, out.Certificates, 1)
}

func TestBuildRejectsUnpairedCertAndKey(t *testing.T) {
	certPEM, keyPEM := gbtesting.SelfSignedCertKeyPEM(t)
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
	caPEM, _ := gbtesting.SelfSignedCertKeyPEM(t)

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

func TestValidateMaterial(t *testing.T) {
	tests := []struct {
		name        string
		material    Material
		enabled     bool
		wantField   string
		wantMessage string
	}{
		{
			name:     "disabled_and_empty_is_valid",
			material: Material{},
		},
		{
			name:     "enabled_without_material_is_valid",
			material: Material{},
			enabled:  true,
		},
		{
			name:      "disabled_with_ca_file",
			material:  Material{CAFile: "/etc/ssl/ca.pem"},
			wantField: "enabled",
		},
		{
			name:      "disabled_with_server_name",
			material:  Material{ServerName: "cache.internal"},
			wantField: "enabled",
		},
		{
			name:      "disabled_with_min_version",
			material:  Material{MinVersion: "1.3"},
			wantField: "enabled",
		},
		{
			name:        "ca_file_and_value_together",
			material:    Material{CAFile: "/etc/ssl/ca.pem", CAValue: "cGVt"},
			enabled:     true,
			wantField:   "cafile",
			wantMessage: "cafile and cavalue are mutually exclusive (exactly one)",
		},
		{
			name:        "cert_file_and_value_together",
			material:    Material{CertFile: "/etc/ssl/c.pem", CertValue: "cGVt", KeyFile: "/etc/ssl/k.pem"},
			enabled:     true,
			wantField:   "certfile",
			wantMessage: "certfile and certvalue are mutually exclusive (exactly one)",
		},
		{
			name:        "key_file_and_value_together",
			material:    Material{CertFile: "/etc/ssl/c.pem", KeyFile: "/etc/ssl/k.pem", KeyValue: "cGVt"},
			enabled:     true,
			wantField:   "keyfile",
			wantMessage: "keyfile and keyvalue are mutually exclusive (exactly one)",
		},
		{
			name:      "cert_without_key",
			material:  Material{CertFile: "/etc/ssl/c.pem"},
			enabled:   true,
			wantField: "keyfile",
		},
		{
			name:      "key_without_cert",
			material:  Material{KeyValue: "cGVt"},
			enabled:   true,
			wantField: "certfile",
		},
		{
			name:      "cert_and_key_pair_is_valid",
			material:  Material{CertValue: "cGVt", KeyFile: "/etc/ssl/k.pem"},
			enabled:   true,
			wantField: "",
		},
		{
			name:      "min_version_below_floor",
			material:  Material{MinVersion: "1.1"},
			enabled:   true,
			wantField: "minversion",
		},
		{
			name:     "min_version_12_is_valid",
			material: Material{MinVersion: "1.2"},
			enabled:  true,
		},
		{
			name:     "min_version_13_is_valid",
			material: Material{MinVersion: "1.3"},
			enabled:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			v := ValidateMaterial(&tt.material, tt.enabled)

			if tt.wantField == "" {
				assert.Nil(t, v)
				return
			}
			require.NotNil(t, v)
			assert.Equal(t, tt.wantField, v.Field)
			assert.NotEmpty(t, v.Message)
			if tt.wantMessage != "" {
				assert.Equal(t, tt.wantMessage, v.Message)
			}
		})
	}
}

// TestValidateMaterialMinVersionMessageElidesOverlongValue pins that an
// operator-supplied enum value is bounded before it reaches a startup log.
func TestValidateMaterialMinVersionMessageElidesOverlongValue(t *testing.T) {
	v := ValidateMaterial(&Material{MinVersion: strings.Repeat("x", 400)}, true)

	require.NotNil(t, v)
	assert.Equal(t, "minversion", v.Field)
	assert.NotContains(t, v.Message, strings.Repeat("x", 400))
}

func TestHasClientCert(t *testing.T) {
	assert.False(t, HasClientCert(&Material{}))
	assert.False(t, HasClientCert(&Material{CAFile: "/etc/ssl/ca.pem", ServerName: "x", MinVersion: "1.3"}))
	assert.True(t, HasClientCert(&Material{CertFile: "/etc/ssl/c.pem"}))
	assert.True(t, HasClientCert(&Material{CertValue: "cGVt"}))
	assert.True(t, HasClientCert(&Material{KeyFile: "/etc/ssl/k.pem"}))
	assert.True(t, HasClientCert(&Material{KeyValue: "cGVt"}))
}
