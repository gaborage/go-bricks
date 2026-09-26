package server

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	goerrors "errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/gaborage/go-bricks/config"
)

// appListenerCheckTimeout bounds one application-listener check (ADR-120).
const appListenerCheckTimeout = 500 * time.Millisecond

// fieldServerProbesPort names the key a Start-time refusal of the probe listener cites.
const fieldServerProbesPort = "server.probes.port"

// errAppListenerCheckUnset fails the check on a server whose Start never built it. ReadyCh
// closes only inside Start, after the check is built, so only a test reaches it.
var errAppListenerCheckUnset = goerrors.New("application listener check not configured")

// appListenerCheck is the probe listener's HEAD of the application listener's reserved
// ready route (ADR-120). A TCP connect alone would not do: the kernel completes the
// handshake into the accept backlog even when the process has stopped serving.
type appListenerCheck struct {
	client *http.Client
	scheme string
	host   string
	path   string
}

// newAppListenerCheck builds the check once, in Start, before either bind. host is the
// configured server.host, path the reserved <base><ready path>, and tlsCfg the application
// listener's TLS config, nil when TLS is off.
func newAppListenerCheck(host, path string, tlsCfg *tls.Config) (*appListenerCheck, error) {
	transport := &http.Transport{
		Proxy:               nil, // never ProxyFromEnvironment: the check targets this process
		DialContext:         (&net.Dialer{Timeout: appListenerCheckTimeout}).DialContext,
		TLSHandshakeTimeout: appListenerCheckTimeout,
		// Every check dials fresh so it exercises the application listener's accept loop;
		// a pooled connection would pass while that loop is dead.
		DisableKeepAlives: true,
	}
	check := &appListenerCheck{
		client: &http.Client{
			Transport: transport,
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
		scheme: schemeHTTP,
		host:   appListenerDialHost(host),
		path:   path,
	}
	if tlsCfg == nil {
		return check, nil
	}
	pinned, err := pinnedLeafTLSConfig(tlsCfg)
	if err != nil {
		return nil, err
	}
	transport.TLSClientConfig = pinned
	check.scheme = schemeHTTPS
	return check, nil
}

// appListenerDialHost maps server.host to the host the check dials. An unspecified host
// becomes its family's loopback, never the bound address string: BoundAddr reports [::]:P
// for 0.0.0.0, and Windows refuses a connect to an unspecified address.
func appListenerDialHost(host string) string {
	host = strings.Trim(host, "[]")
	switch host {
	case "", "0.0.0.0":
		return "127.0.0.1"
	case "::":
		return "::1"
	default:
		return host
	}
}

// pinnedLeafTLSConfig verifies the application listener against its own leaf and nothing
// else. RootCAs holds only that leaf, which Go's verifier accepts as its own root, and
// ServerName is the leaf's first DNS SAN, else its first IP SAN: a SAN covering the dialed
// loopback address cannot be assumed, so the pin names the leaf. Verification is pinned,
// never skipped (SonarCloud go:S4830). The leaf is verified against the pin once here, as
// every check's handshake will verify it, so a leaf the check could never accept refuses
// Start instead of holding /ready at 503.
func pinnedLeafTLSConfig(serverTLS *tls.Config) (*tls.Config, error) {
	leaf, err := serverLeaf(serverTLS)
	if err != nil {
		return nil, err
	}
	serverName := leafServerName(leaf)
	if serverName == "" {
		refusal := config.NewValidationError(fieldServerProbesPort,
			"the server.tls leaf certificate has no DNS or IP SAN, so the probe listener's application-listener check cannot pin it")
		refusal.Action = "issue the server.tls leaf certificate with a DNS or IP subjectAltName, or set server.probes.port to 0"
		return nil, refusal
	}
	roots := x509.NewCertPool()
	roots.AddCert(leaf)
	// DNSName takes an IP SAN's string form too: VerifyHostname matches it against the IPs.
	if _, verifyErr := leaf.Verify(x509.VerifyOptions{
		Roots:     roots,
		DNSName:   serverName,
		KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}); verifyErr != nil {
		refusal := config.NewValidationError(fieldServerProbesPort,
			"the server.tls leaf certificate fails verification against its own pin, so the probe listener's application-listener check would never pass: "+verifyErr.Error())
		refusal.Action = "issue the server.tls leaf certificate for serverAuth use and within its validity period, or set server.probes.port to 0"
		return nil, refusal
	}
	return &tls.Config{
		MinVersion: max(serverTLS.MinVersion, tls.VersionTLS12),
		RootCAs:    roots,
		ServerName: serverName,
	}, nil
}

// serverLeaf returns the parsed leaf of the application listener's certificate, sharing
// the one buildServerTLSConfig loaded rather than reading the material again.
func serverLeaf(serverTLS *tls.Config) (*x509.Certificate, error) {
	if len(serverTLS.Certificates) == 0 || len(serverTLS.Certificates[0].Certificate) == 0 {
		return nil, goerrors.New("server: tls: no certificate loaded")
	}
	cert := serverTLS.Certificates[0]
	if cert.Leaf != nil {
		return cert.Leaf, nil
	}
	leaf, err := x509.ParseCertificate(cert.Certificate[0])
	if err != nil {
		return nil, fmt.Errorf("server: tls: leaf: %w", err)
	}
	return leaf, nil
}

// leafServerName returns the leaf's first DNS SAN, else its first IP SAN, else "".
func leafServerName(leaf *x509.Certificate) string {
	if len(leaf.DNSNames) > 0 {
		return leaf.DNSNames[0]
	}
	if len(leaf.IPAddresses) > 0 {
		return leaf.IPAddresses[0].String()
	}
	return ""
}

// run HEADs the reserved ready route on the application listener bound at addr. Any
// answer below 500 passes: the reserved route answers 404.
func (c *appListenerCheck) run(ctx context.Context, addr net.Addr) error {
	tcpAddr, ok := addr.(*net.TCPAddr)
	if !ok {
		return fmt.Errorf("application listener address %v is not a TCP address", addr)
	}
	ctx, cancel := context.WithTimeout(ctx, appListenerCheckTimeout)
	defer cancel()
	url := c.scheme + "://" + hostPort(c.host, tcpAddr.Port) + c.path
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, url, http.NoBody)
	if err != nil {
		return err
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return err
	}
	_ = resp.Body.Close()
	if resp.StatusCode >= http.StatusInternalServerError {
		return fmt.Errorf("application listener answered %d", resp.StatusCode)
	}
	return nil
}

// checkApplicationListener runs the application-listener check against the application
// listener's bound port.
func (s *Server) checkApplicationListener(ctx context.Context) error {
	if s.appCheck == nil {
		return errAppListenerCheckUnset
	}
	return s.appCheck.run(ctx, s.BoundAddr())
}
