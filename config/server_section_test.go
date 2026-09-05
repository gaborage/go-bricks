package config

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestValidateServerSuccess(t *testing.T) {
	tests := []struct {
		name string
		cfg  ServerConfig
	}{
		{
			name: "standard_config",
			cfg: ServerConfig{
				Port: 8080,
				Timeout: TimeoutConfig{
					Read:       15 * time.Second,
					Write:      30 * time.Second,
					Middleware: 5 * time.Second,
					Shutdown:   10 * time.Second,
				},
			},
		},
		{
			name: "minimum_port",
			cfg: ServerConfig{
				Port: 1,
				Timeout: TimeoutConfig{
					Read:       1 * time.Second,
					Write:      2 * time.Second,
					Middleware: 1 * time.Second,
					Shutdown:   1 * time.Second,
				},
			},
		},
		{
			name: "maximum_port",
			cfg: ServerConfig{
				Port: 65535,
				Timeout: TimeoutConfig{
					Read:       1 * time.Hour,
					Write:      2 * time.Hour,
					Middleware: 30 * time.Second,
					Shutdown:   1 * time.Minute,
				},
			},
		},
		{
			name: "common_ports",
			cfg: ServerConfig{
				Port: 3000,
				Timeout: TimeoutConfig{
					Read:       10 * time.Second,
					Write:      20 * time.Second,
					Middleware: 5 * time.Second,
					Shutdown:   10 * time.Second,
				},
			},
		},
		{
			name: "tls_disabled_ignores_material",
			cfg: ServerConfig{
				Port:    8080,
				Timeout: standardServerTimeout(),
				TLS: ServerTLSConfig{
					Enabled:    false,
					CertFile:   "/staged/cert.pem",
					CertValue:  "staged-cert-value",
					MinVersion: "bogus",
				},
			},
		},
		{
			name: "tls_enabled_valid",
			cfg: ServerConfig{
				Port:    8080,
				Timeout: standardServerTimeout(),
				TLS: ServerTLSConfig{
					Enabled:    true,
					CertFile:   "/etc/tls/cert.pem",
					KeyFile:    "/etc/tls/key.pem",
					MinVersion: "1.3",
				},
			},
		},
		{
			name: "forwardedcert_enabled_valid",
			cfg: ServerConfig{
				Port:    8080,
				Timeout: standardServerTimeout(),
				ForwardedClientCert: ForwardedClientCertConfig{
					Enabled: true,
					Require: true,
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := checkServer(&tt.cfg)
			assert.NoError(t, err)
		})
	}
}

func standardServerTimeout() TimeoutConfig {
	return TimeoutConfig{
		Read:       15 * time.Second,
		Write:      30 * time.Second,
		Middleware: 5 * time.Second,
		Shutdown:   10 * time.Second,
	}
}

func TestValidateServerFailures(t *testing.T) {
	tests := []struct {
		name          string
		cfg           ServerConfig
		expectedError string
	}{
		{
			name: "zero_port",
			cfg: ServerConfig{
				Port: 0,
				Timeout: TimeoutConfig{
					Read:       15 * time.Second,
					Write:      30 * time.Second,
					Middleware: 5 * time.Second,
					Shutdown:   10 * time.Second,
				},
			},
			expectedError: serverPort,
		},
		{
			name: "negative_port",
			cfg: ServerConfig{
				Port: -1,
				Timeout: TimeoutConfig{
					Read:       15 * time.Second,
					Write:      30 * time.Second,
					Middleware: 5 * time.Second,
					Shutdown:   10 * time.Second,
				},
			},
			expectedError: serverPort,
		},
		{
			name: "port_too_high",
			cfg: ServerConfig{
				Port: 65536,
				Timeout: TimeoutConfig{
					Read:       15 * time.Second,
					Write:      30 * time.Second,
					Middleware: 5 * time.Second,
					Shutdown:   10 * time.Second,
				},
			},
			expectedError: serverPort,
		},
		{
			name: "zero_read_timeout",
			cfg: ServerConfig{
				Port: 8080,
				Timeout: TimeoutConfig{
					Read:  0,
					Write: 30 * time.Second,
				},
			},
			expectedError: "server.timeout.read must be positive",
		},
		{
			name: "negative_read_timeout",
			cfg: ServerConfig{
				Port: 8080,
				Timeout: TimeoutConfig{
					Read:  -1 * time.Second,
					Write: 30 * time.Second,
				},
			},
			expectedError: "server.timeout.read must be positive",
		},
		{
			name: "zero_write_timeout",
			cfg: ServerConfig{
				Port: 8080,
				Timeout: TimeoutConfig{
					Read:  15 * time.Second,
					Write: 0,
				},
			},
			expectedError: "server.timeout.write must be positive",
		},
		{
			name: "negative_write_timeout",
			cfg: ServerConfig{
				Port: 8080,
				Timeout: TimeoutConfig{
					Read:  15 * time.Second,
					Write: -1 * time.Second,
				},
			},
			expectedError: "server.timeout.write must be positive",
		},
		{
			// The bound is <=, not <: zero is a delivered value, not "unset".
			name: "middleware_timeout_zero_rejected",
			cfg: ServerConfig{
				Port: 8080,
				Timeout: TimeoutConfig{
					Read:       15 * time.Second,
					Write:      30 * time.Second,
					Middleware: 0,
					Shutdown:   10 * time.Second,
				},
			},
			expectedError: "server.timeout.middleware must be positive",
		},
		{
			// Same bound on the shutdown budget, reached only once the
			// middleware/write pair above it is valid.
			name: "shutdown_timeout_zero_rejected",
			cfg: ServerConfig{
				Port: 8080,
				Timeout: TimeoutConfig{
					Read:       15 * time.Second,
					Write:      30 * time.Second,
					Middleware: 5 * time.Second,
					Shutdown:   0,
				},
			},
			expectedError: "server.timeout.shutdown must be positive",
		},
		{
			name: "middleware_timeout_equal_to_write_timeout",
			cfg: ServerConfig{
				Port: 8080,
				Timeout: TimeoutConfig{
					Read:       15 * time.Second,
					Write:      10 * time.Second,
					Middleware: 10 * time.Second,
					Shutdown:   5 * time.Second,
				},
			},
			expectedError: "server.timeout.middleware must be less than server.timeout.write",
		},
		{
			name: "middleware_timeout_greater_than_write_timeout",
			cfg: ServerConfig{
				Port: 8080,
				Timeout: TimeoutConfig{
					Read:       15 * time.Second,
					Write:      5 * time.Second,
					Middleware: 10 * time.Second,
					Shutdown:   5 * time.Second,
				},
			},
			expectedError: "server.timeout.middleware must be less than server.timeout.write",
		},
		{
			name: "negative_gzip_minlength",
			cfg: ServerConfig{
				Port: 8080,
				Timeout: TimeoutConfig{
					Read:       15 * time.Second,
					Write:      30 * time.Second,
					Middleware: 5 * time.Second,
					Shutdown:   10 * time.Second,
				},
				Gzip: GzipConfig{MinLength: -1},
			},
			expectedError: "server.gzip.minlength",
		},
		{
			name: "negative_bodylimit",
			cfg: ServerConfig{
				Port: 8080,
				Timeout: TimeoutConfig{
					Read:       15 * time.Second,
					Write:      30 * time.Second,
					Middleware: 5 * time.Second,
					Shutdown:   10 * time.Second,
				},
				BodyLimit: -1,
			},
			expectedError: "server.bodylimit",
		},
		{
			name: "tls_enabled_missing_key",
			cfg: ServerConfig{
				Port:    8080,
				Timeout: standardServerTimeout(),
				TLS: ServerTLSConfig{
					Enabled:  true,
					CertFile: "/etc/tls/cert.pem",
				},
			},
			expectedError: "server.tls.key",
		},
		{
			name: "tls_cert_file_and_value_both_set",
			cfg: ServerConfig{
				Port:    8080,
				Timeout: standardServerTimeout(),
				TLS: ServerTLSConfig{
					Enabled:   true,
					CertFile:  "/etc/tls/cert.pem",
					CertValue: "aGVsbG8=",
					KeyFile:   "/etc/tls/key.pem",
				},
			},
			expectedError: "server.tls.cert",
		},
		{
			name: "tls_bad_minversion",
			cfg: ServerConfig{
				Port:    8080,
				Timeout: standardServerTimeout(),
				TLS: ServerTLSConfig{
					Enabled:    true,
					CertFile:   "/etc/tls/cert.pem",
					KeyFile:    "/etc/tls/key.pem",
					MinVersion: "1.1",
				},
			},
			expectedError: "server.tls.minversion",
		},
		{
			name: "forwardedcert_require_without_enabled",
			cfg: ServerConfig{
				Port:    8080,
				Timeout: standardServerTimeout(),
				ForwardedClientCert: ForwardedClientCertConfig{
					Enabled: false,
					Require: true,
				},
			},
			expectedError: "server.forwardedclientcert.require",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := checkServer(&tt.cfg)
			require.ErrorContains(t, err, tt.expectedError)
		})
	}
}

// trustedProxyServerConfig returns a ServerConfig that satisfies every other
// checkServer check, so any error can only come from TrustedProxies.
func trustedProxyServerConfig(entries ...string) ServerConfig {
	cfg := createValidServerConfig()
	cfg.TrustedProxies = entries
	return cfg
}

func TestTrustedProxiesRejectsInvalidCIDR(t *testing.T) {
	tests := []struct {
		name  string
		entry string
	}{
		{name: "not_a_cidr", entry: "not-a-cidr"},
		// net.ParseCIDR rejects a bare address, so an operator writing a single
		// host gets an error rather than a silently dropped entry.
		{name: "bare_ip_without_prefix", entry: "10.0.0.5"},
		// net.ParseCIDR accepts these and masks them to 10.0.0.0/8, widening
		// the trusted set past what was written.
		{name: "host_bits_set", entry: "10.1.2.3/8"},
		// A default route trusts every proxy, which walks the XFF chain to its
		// caller-authored left-most entry.
		{name: "ipv4_default_route", entry: "0.0.0.0/0"},
		{name: "ipv6_default_route", entry: "::/0"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := trustedProxyServerConfig(tt.entry)
			err := checkServer(&cfg)
			require.Error(t, err)
			assert.Contains(t, err.Error(), "server.trustedproxies")
		})
	}
}

func TestTrustedProxiesAcceptsValidCIDRs(t *testing.T) {
	// The whitespace-padded entry pins the TrimSpace inside ParseTrustedProxyCIDR:
	// validateCIDRList and server.ParseCIDRs both forgive incidental YAML spacing,
	// and a disagreement here would let validation accept an entry the extractor
	// then silently drops.
	cfg := trustedProxyServerConfig("10.0.0.0/8", "2001:db8::/32", "  172.16.0.0/12  ")
	assert.NoError(t, checkServer(&cfg))
}

// TestParseTrustedProxyCIDRRejectsMappedDefaultRoute pins the exported per-entry parser
// directly. The set-level coverage check also catches this shape, so reverting the parser's
// own normalization breaks no other test — but ParseTrustedProxyCIDR is exported and
// server.trustedProxyOptions calls it per entry, so a consumer reaching it without the set
// check must still be told that "::ffff:0.0.0.0/96" is a default route.
//
// Mask.Size() reads it as 96 of 128 bits; Contains re-derives a 4-byte mask and matches
// every IPv4 address. NormalizeIPNet measures the one Contains will use.
func TestParseTrustedProxyCIDRRejectsMappedDefaultRoute(t *testing.T) {
	for _, entry := range []string{"::ffff:0.0.0.0/96", "::ffff:0:0/96", "0:0:0:0:0:ffff:0:0/96"} {
		t.Run(safeSubtestName(entry), func(t *testing.T) {
			_, err := ParseTrustedProxyCIDR(entry)
			require.Error(t, err, "%s matches every IPv4 address", entry)
			assert.ErrorIs(t, err, errTrustedProxyDefaultRoute)
		})
	}

	// A genuine /96 that is NOT v4-mapped stays legal — the rule is about what Contains
	// will match, not about the number 96.
	_, err := ParseTrustedProxyCIDR("2001:db8::/96")
	assert.NoError(t, err)
}

// TestNormalizeServerBodyLimit pins server.bodylimit on both configuration doors.
// Every literal-door case runs through Validate rather than checkServer alone:
// normalization and rejection are one contract, and only the whole path shows that
// a zero is filled while a negative is refused instead of quietly defaulted.
func TestNormalizeServerBodyLimit(t *testing.T) {
	tests := []struct {
		name          string
		bodyLimit     int64
		expected      int64
		expectedError string
	}{
		// Zero means "unset": koanf's default and a struct literal look identical
		// here, so this is the case the ownership move exists for.
		{name: "literal_door_zero_fills_default", bodyLimit: 0, expected: DefaultBodyLimitBytes},
		// An operator who wrote a number keeps it, including one below the default.
		{name: "literal_door_explicit_value_survives", bodyLimit: 1024, expected: 1024},
		{name: "literal_door_value_above_default_survives", bodyLimit: DefaultBodyLimitBytes * 2, expected: DefaultBodyLimitBytes * 2},
		// The case that separates "fill a zero" from "fill anything non-positive":
		// a negative must reach validation as an operator error, never be laundered
		// into the default.
		{name: "literal_door_negative_is_rejected", bodyLimit: -1, expectedError: "server.bodylimit"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := createValidFullConfig()
			cfg.Server.BodyLimit = tt.bodyLimit

			err := Validate(cfg)

			if tt.expectedError != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.expectedError)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.expected, cfg.Server.BodyLimit)
		})
	}

	// The other door: an absent key must render the same 10 MB, so the koanf default
	// and the normalize fill cannot drift apart unnoticed.
	t.Run("koanf_door_absent_defaults", func(t *testing.T) {
		cfg, err := loadDefaultConfig(t)
		require.NoError(t, err)
		assert.Equal(t, DefaultBodyLimitBytes, cfg.Server.BodyLimit)
	})
}
