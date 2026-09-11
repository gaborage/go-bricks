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
