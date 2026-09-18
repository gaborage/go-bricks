//go:build integration

package containers

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestResolveAnnounceIPAcceptsOnlyIPv4 pins the IPv4-only contract the cluster
// announce pair rests on: cluster-announce-ip is the address a host-side client
// dials back through the fixture's v4 port mapping, so an IPv6 literal is refused
// at this seam rather than published into a slot map whose redirect would be
// unreachable. An IPv4-mapped v6 literal is announced in its dotted form, the only
// spelling Redis takes. No Docker involved: the function is host-side string work
// plus a resolver lookup, and localhost answers from the hosts file.
func TestResolveAnnounceIPAcceptsOnlyIPv4(t *testing.T) {
	tests := []struct {
		name    string
		host    string
		want    string
		wantErr string
	}{
		{name: "ipv4_literal_passes_through", host: "192.0.2.10", want: "192.0.2.10"},
		{name: "ipv4_mapped_v6_literal_is_dotted", host: "::ffff:192.0.2.10", want: "192.0.2.10"},
		{name: "ipv6_literal_is_refused", host: "::1", wantErr: "IPv6 literal"},
		{name: "hostname_resolves_to_ipv4", host: "localhost", want: "127.0.0.1"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := resolveAnnounceIP(t.Context(), tt.host)
			if tt.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErr)
				assert.Empty(t, got)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

// TestRedisCLICommandBuildsVerifyingArgv compares the WHOLE argv redisCLICommand
// emits, which is what catches this seam's entire class of quiet regressions.
// The builder is a pure function with no Docker behind it, and every bootstrap
// and readiness exec goes through it, so a change here is invisible to the
// container-backed tests: dropping `-e` puts error replies back on exit 0 and a
// refused CONFIG SET passes silently, while adding `--insecure` stops the admin
// path verifying the fixture CA — and both fixtures keep going green either way.
// Comparing the whole slice rather than probing for one flag is what makes an
// ADDITION as visible as a deletion.
func TestRedisCLICommandBuildsVerifyingArgv(t *testing.T) {
	tests := []struct {
		name string
		cfg  *RedisContainerConfig
		args []string
		want []string
	}{
		{
			name: "plaintext_carries_only_the_error_exit_flag",
			cfg:  &RedisContainerConfig{},
			args: []string{"cluster", "info"},
			want: []string{"redis-cli", "-e", "cluster", "info"},
		},
		{
			name: "tls_adds_the_ca_pinned_transport_flags",
			cfg:  &RedisContainerConfig{TLS: &RedisTLSMaterial{}},
			args: []string{"config", "set", "cluster-announce-ip", "127.0.0.1"},
			want: []string{
				"redis-cli", "-e", "--tls", "--cacert", "/tls/ca.crt",
				"config", "set", "cluster-announce-ip", "127.0.0.1",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := redisCLICommand(tt.cfg, tt.args...)

			assert.Equal(t, tt.want, got)
			// Redundant against the exact comparison above, and kept because it
			// names the class: no verification-weakening flag may appear on
			// either arm, whatever else the argv grows.
			for _, weakening := range []string{"--insecure", "--tls-auth-clients"} {
				assert.NotContains(t, got, weakening,
					"the in-container admin path must keep verifying against the fixture CA")
			}
		})
	}
}

// TestClusterReadyTimeoutFallsBackToTheDefault pins the zero-value arm, which no
// fixture reaches: both cluster fixtures start from DefaultRedisConfig, so
// deleting the fallback would redden nothing else. See clusterReadyTimeout for
// why a zero is worse than a default here — WithStartupTimeout honors it, and
// the wait context then expires before the first poll.
func TestClusterReadyTimeoutFallsBackToTheDefault(t *testing.T) {
	tests := []struct {
		name string
		cfg  *RedisContainerConfig
		want time.Duration
	}{
		{
			name: "zero_falls_back_to_the_package_default",
			cfg:  &RedisContainerConfig{},
			want: 60 * time.Second,
		},
		{
			name: "explicit_timeout_is_honored",
			cfg:  &RedisContainerConfig{StartupTimeout: 5 * time.Second},
			want: 5 * time.Second,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, clusterReadyTimeout(tt.cfg))
		})
	}
}
