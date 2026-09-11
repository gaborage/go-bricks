// Package clienttls turns declarative client-side TLS material — PEM from a
// file path or a base64-encoded value — into a *tls.Config. It is the shared
// loader behind the httpclient TLS options and the Redis cache client, and it
// never disables certificate verification: no InsecureSkipVerify escape hatch
// exists here.
package clienttls

import (
	"crypto/tls"
	"fmt"

	"github.com/gaborage/go-bricks/internal/secretfile"
)

// Material is the client-side TLS material: three PEM pieces, each from a file
// path or a base64-encoded value, plus the SNI override and the version floor.
// Every field is a comparable type so the structs embedding it stay comparable.
type Material struct {
	CertFile   string
	CertValue  string
	KeyFile    string
	KeyValue   string
	CAFile     string
	CAValue    string
	ServerName string
	MinVersion string
}

// Build loads m into a *tls.Config. prefix names the consumer's error
// namespace, as in secretfile.LoadPEM. Material-free input is legal and yields
// a verifying config over the system roots.
func Build(prefix string, m *Material) (*tls.Config, error) {
	certPEM, err := secretfile.LoadPEM(prefix, m.CertFile, m.CertValue, "cert")
	if err != nil {
		return nil, err
	}
	keyPEM, err := secretfile.LoadPEM(prefix, m.KeyFile, m.KeyValue, "key")
	if err != nil {
		return nil, err
	}
	switch {
	case certPEM != nil && keyPEM == nil:
		return nil, fmt.Errorf("%s cert: set without a matching key", prefix)
	case certPEM == nil && keyPEM != nil:
		return nil, fmt.Errorf("%s key: set without a matching cert", prefix)
	}
	caPEM, err := secretfile.LoadPEM(prefix, m.CAFile, m.CAValue, "ca")
	if err != nil {
		return nil, err
	}
	minVersion, err := secretfile.ParseTLSMinVersion(prefix, m.MinVersion)
	if err != nil {
		return nil, err
	}
	out := &tls.Config{
		MinVersion: minVersion,
		ServerName: m.ServerName,
	}
	if certPEM != nil {
		pair, err := tls.X509KeyPair(certPEM, keyPEM)
		if err != nil {
			return nil, fmt.Errorf("%s cert/key: %w", prefix, err)
		}
		out.Certificates = []tls.Certificate{pair}
	}
	if caPEM != nil {
		pool, err := secretfile.CertPool(prefix, caPEM)
		if err != nil {
			return nil, err
		}
		out.RootCAs = pool
	}
	return out, nil
}

// HasAnyMaterial reports whether m names any PEM piece. ServerName and
// MinVersion are deliberately excluded: they tune a connection, they do not
// stage material for one.
func HasAnyMaterial(m *Material) bool {
	return m.CertFile != "" || m.CertValue != "" ||
		m.KeyFile != "" || m.KeyValue != "" ||
		m.CAFile != "" || m.CAValue != ""
}

// Relative config keys for the Material fields. A consumer prefixes them with
// its own namespace ("cache.redis.tls." + v.Field) when reporting a Violation.
const (
	fieldEnabled    = "enabled"
	fieldCAFile     = "cafile"
	fieldCAValue    = "cavalue"
	fieldCertFile   = "certfile"
	fieldCertValue  = "certvalue"
	fieldKeyFile    = "keyfile"
	fieldKeyValue   = "keyvalue"
	fieldMinVersion = "minversion"
)

// Violation names the Material field (relative key such as "cafile") that
// breaks a structural rule, with the message to report. Field is relative on
// purpose: each consumer owns its own key namespace and prefixes it.
type Violation struct {
	Field   string
	Message string
}

// ValidateMaterial applies the structural rules that need no filesystem:
// material staged under a disabled block, a piece configured from two sources,
// a half client-certificate pair, and the min-version enum. It returns nil when
// the shape is valid — enabled with no material at all is valid, and yields a
// config verifying against the system roots.
//
// Reading and parsing the PEM happens later, in Build.
func ValidateMaterial(m *Material, enabled bool) *Violation {
	if !enabled {
		if *m != (Material{}) {
			return &Violation{fieldEnabled, "must be true when any tls.* field is set"}
		}
		return nil
	}

	sources := []struct{ fileField, valueField, file, value string }{
		{fieldCAFile, fieldCAValue, m.CAFile, m.CAValue},
		{fieldCertFile, fieldCertValue, m.CertFile, m.CertValue},
		{fieldKeyFile, fieldKeyValue, m.KeyFile, m.KeyValue},
	}
	for _, s := range sources {
		if s.file != "" && s.value != "" {
			return &Violation{s.fileField, s.fileField + " and " + s.valueField + " are mutually exclusive (exactly one)"}
		}
	}

	hasCert := m.CertFile != "" || m.CertValue != ""
	hasKey := m.KeyFile != "" || m.KeyValue != ""
	switch {
	case hasCert && !hasKey:
		return &Violation{fieldKeyFile, "a client certificate requires " + fieldKeyFile + " or " + fieldKeyValue}
	case hasKey && !hasCert:
		return &Violation{fieldCertFile, "a client key requires " + fieldCertFile + " or " + fieldCertValue}
	}

	// Delegated so the accepted set lives in exactly one place; the parsed
	// version is Build's business, not the shape check's.
	if _, err := secretfile.ParseTLSMinVersion("", m.MinVersion); err != nil {
		return &Violation{
			fieldMinVersion,
			"invalid value: " + secretfile.SafeRef(m.MinVersion) + ` (accepted values are "1.2" and "1.3")`,
		}
	}
	return nil
}

// HasClientCert reports whether any cert or key field is set. A CA-only
// Material is server authentication alone: it presents no client certificate.
func HasClientCert(m *Material) bool {
	return m.CertFile != "" || m.CertValue != "" ||
		m.KeyFile != "" || m.KeyValue != ""
}
