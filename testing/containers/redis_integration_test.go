//go:build integration

package containers

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	gbtesting "github.com/gaborage/go-bricks/testing"
)

// ACL password rules the arg-vector tests expect, spelled as literals rather than
// derived from the code under test — an expectation computed by the same hash call
// redisACLUserArgs makes would hold for whatever that call emitted. Each was taken
// from an independent route, `printf '%s' <password> | shasum -a 256`.
const (
	aclAdminPasswordRule = "#16175223c8ddce5ace0493c948569c211b03c4c6bb3d3e484434999448cffe01" // admin-secret
	aclAppPasswordRule   = "#6c904c5190e8b45c2f0af062eefdb2f5b41ce3809b0e6b5bc50aafdd60b290d8" // app-secret
	aclExtraPasswordRule = "#1c58d900e5c88aba0e65a4b6301d3235e5cc5947f0680d769765d6f609765b08" // extra-secret
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
//
// The last row is the one no single-capability fixture reaches: cluster, TLS and
// ACL together. One builder serves it because the transport flags and the admin
// credential are arms of the same function — two builders would each have emitted
// half an argv, which is exactly the regression this row would catch.
func TestRedisCLICommandBuildsVerifyingArgv(t *testing.T) {
	gatedACL := &RedisACL{
		Admin: RedisACLUser{Username: "admin-id", Password: "admin-secret"},
		App:   RedisACLUser{Username: "app-id", Password: "app-secret"},
	}
	leaf := mintedTLS(t)

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
			// No bootstrap runs on a standalone node, so its leaf is never read
			// for a name; the exec still speaks TLS against the TLS-only listener.
			name: "standalone_tls_adds_the_ca_pinned_transport_flags_without_a_name",
			cfg:  &RedisContainerConfig{TLS: leaf},
			args: []string{"cluster", "info"},
			want: []string{"redis-cli", "-e", "--tls", "--cacert", "/tls/ca.crt", "cluster", "info"},
		},
		{
			name: "tls_adds_the_ca_pinned_name_verifying_transport_flags",
			cfg:  &RedisContainerConfig{Cluster: true, TLS: leaf},
			args: []string{"config", "set", "cluster-announce-ip", "127.0.0.1"},
			want: []string{
				"redis-cli", "-e", "--tls", "--cacert", "/tls/ca.crt", "--sni", "localhost",
				"config", "set", "cluster-announce-ip", "127.0.0.1",
			},
		},
		{
			// The bootstrap and readiness execs run as Admin: with `default` off,
			// an unauthenticated CONFIG SET is refused and the only symptom is a
			// cluster that never reaches cluster_state:ok.
			name: "acl_adds_the_admin_credential",
			cfg:  &RedisContainerConfig{ACL: gatedACL},
			args: []string{"cluster", "info"},
			want: []string{
				"redis-cli", "-e",
				"--user", "admin-id", "--pass", "admin-secret", "--no-auth-warning",
				"cluster", "info",
			},
		},
		{
			name: "tls_and_acl_arrive_on_the_same_invocation",
			cfg:  &RedisContainerConfig{Cluster: true, TLS: leaf, ACL: gatedACL},
			args: []string{"cluster", "addslotsrange", "0", "16383"},
			want: []string{
				"redis-cli", "-e", "--tls", "--cacert", "/tls/ca.crt", "--sni", "localhost",
				"--user", "admin-id", "--pass", "admin-secret", "--no-auth-warning",
				"cluster", "addslotsrange", "0", "16383",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			served, err := newRedisServed(tt.cfg)
			require.NoError(t, err)
			got := redisCLICommand(tt.cfg, served, tt.args...)

			assert.Equal(t, tt.want, got)
			// Redundant against the exact comparison above, and kept because it
			// names the class: no verification-weakening flag may appear on
			// either arm, whatever else the argv grows.
			for _, weakening := range []string{"--insecure", "--no-verify-hostname", "--tls-auth-clients"} {
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

// TestRedisOptionsDisableDefaultAndInstallBothUsers pins what cfg.ACL adds to a
// container command, as a delta: the same config is rendered with and without it, so
// the assertion states what ACL contributes and that it joins the other arms' flags
// rather than replacing them, without restating what cluster or TLS emit.
//
// `--user default off` is the token that matters. Left on, `default` is nopass ~* +@all,
// so every credential test the fixture supports would pass against an unauthenticated
// connection and prove nothing.
//
// The password rules are the second thing pinned: a boot argument is log-visible, so
// it must carry the SHA-256 digest and never the clear-text password the client AUTHs
// with.
func TestRedisOptionsDisableDefaultAndInstallBothUsers(t *testing.T) {
	tests := []struct {
		name     string
		appRules []string
		wantApp  []string
	}{
		{
			name:     "app_rules_are_installed_as_given",
			appRules: []string{"~app:*", "+get"},
			wantApp:  []string{"~app:*", "+get"},
		},
		{
			// An App naming no rules must land on the narrow default rather than on
			// nothing: the lazy path is the one a careless caller takes.
			name:    "absent_app_rules_fall_back_to_the_narrow_default",
			wantApp: []string{"~*", "+ping", "+info", "+get", "+set", "+del", "+eval", "+command", "+cluster|slots"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := DefaultRedisConfig()
			cfg.Cluster = true
			withoutACL := redisCmdAsStarted(t, cfg)

			cfg.ACL = &RedisACL{
				Admin: RedisACLUser{Username: "admin-id", Password: "admin-secret"},
				App:   RedisACLUser{Username: "app-id", Password: "app-secret", Rules: tt.appRules},
			}
			withACL := redisCmdAsStarted(t, cfg)

			want := slices.Concat(withoutACL, []string{
				"--user", "default", "off",
				"--user", "admin-id", "~*", "&*", "+@all", "resetpass", "on", aclAdminPasswordRule,
				"--user", "app-id",
			}, tt.wantApp, []string{"resetpass", "on", aclAppPasswordRule})
			assert.Equal(t, want, withACL)
		})
	}
}

// TestRedisACLArgsInstallAdditionalIdentities pins that AdditionalUsers are installed
// after App with their own rules, which is what lets one container serve a second
// credential whose grants differ.
//
// It also pins the two tokens that make an installed identity require exactly its
// declared password: `on` and the digest trail the caller's rules, so a flag-like
// rule such as the extra user's `nopass` is overridden by position, and `resetpass`
// sits between the two, so a caller's own password rule cannot leave a second
// credential behind. The server-side halves of that argument are cache/redis's
// TestRealRedisACLNopassRuleCannotDisableAuthentication and
// TestRealRedisACLCallerPasswordRuleCannotAddACredential.
func TestRedisACLArgsInstallAdditionalIdentities(t *testing.T) {
	acl := &RedisACL{
		Admin:           RedisACLUser{Username: "admin-id", Password: "admin-secret", Rules: []string{"+@all"}},
		App:             RedisACLUser{Username: "app-id", Password: "app-secret", Rules: []string{"~*", "+get"}},
		AdditionalUsers: []RedisACLUser{{Username: "extra-id", Password: "extra-secret", Rules: []string{"~*", "nopass", "+ping"}}},
	}

	assert.Equal(t, []string{
		"--user", "default", "off",
		"--user", "admin-id", "+@all", "resetpass", "on", aclAdminPasswordRule,
		"--user", "app-id", "~*", "+get", "resetpass", "on", aclAppPasswordRule,
		"--user", "extra-id", "~*", "nopass", "+ping", "resetpass", "on", aclExtraPasswordRule,
	}, redisACLArgs(acl))
}

// TestRedisOptionsAreACLFreeWithoutACL keeps the knob opt-in: every existing caller
// passes no ACL and must still get the stock open server. The ACL arm shares one
// argument list with cluster and TLS now, so the guard is that a nil ACL contributes
// no token to it — on the empty command a plain fixture gets, and on a command the
// other arms have already filled.
func TestRedisOptionsAreACLFreeWithoutACL(t *testing.T) {
	assert.Empty(t, redisCmdAsStarted(t, DefaultRedisConfig()))

	loaded := DefaultRedisConfig()
	loaded.Cluster = true
	loaded.TLS = mintedTLS(t)
	cmd := redisCmdAsStarted(t, loaded)
	assert.NotEmpty(t, cmd, "the other arms still emit their own flags")
	assert.NotContains(t, cmd, redisACLUserFlag, "a nil ACL installs no identity")
}

// TestWithoutRedisCLICredentialStripsTheAdminPassword pins that an exec failure
// cannot carry the credential out: execRedisCLI quotes text it did not author —
// the daemon's error and redis-cli's own output — and the argv it was handed
// carries `--pass <password>`.
func TestWithoutRedisCLICredentialStripsTheAdminPassword(t *testing.T) {
	gated := DefaultRedisConfig()
	gated.ACL = &RedisACL{Admin: RedisACLUser{Username: "admin-id", Password: "admin-secret"}}
	failure := "redis container: " + strings.Join(redisCLICommand(gated, redisServed{}, "cluster", "info"), " ") + " exited 1"

	got := withoutRedisCLICredential(gated, failure)

	assert.NotContains(t, got, "admin-secret")
	assert.Contains(t, got, "admin-id", "only the password is stripped; the identity stays readable")
	assert.Equal(t, failure, withoutRedisCLICredential(DefaultRedisConfig(), failure),
		"an open server has no credential to strip")
}

// TestValidateRedisACLUsernamesRefusesWhatRedisWouldEcho pins the check that keeps a
// `--user` directive out of a CI log in the first place: a username Redis rejects makes
// it print the whole offending directive to its log and exit, and testcontainers then
// dumps that log to stderr when the readiness wait fails. The directive's password rule
// is a digest, so this is the trigger half of the defense rather than the only half. No
// Docker involved: the check is host-side string work over the config.
func TestValidateRedisACLUsernamesRefusesWhatRedisWouldEcho(t *testing.T) {
	// Passwords are derived from the username but never equal to one. The two
	// assertions below would otherwise contradict each other: the refusal has to
	// name the offending username to be diagnostic, so a user whose password WAS
	// its username would trip the leak check on the diagnostic itself.
	named := func(name string) RedisACLUser {
		password := "secret-" + name
		require.NotEqual(t, name, password, "a password equal to its username would make the leak check unfalsifiable")
		return RedisACLUser{Username: name, Password: password}
	}

	tests := []struct {
		name    string
		acl     *RedisACL
		wantErr string
	}{
		{
			name:    "empty_admin_username_is_rejected",
			acl:     &RedisACL{Admin: RedisACLUser{Password: "admin-secret"}, App: named("app-id")},
			wantErr: "every ACL user needs a username",
		},
		{
			// Redis quotes each argv element, so a whitespace-only name parses
			// rather than failing at boot — which is why the fixture has to be the
			// one to refuse it.
			name:    "whitespace_only_username_is_rejected",
			acl:     &RedisACL{Admin: RedisACLUser{Username: "  ", Password: "admin-secret"}, App: named("app-id")},
			wantErr: "every ACL user needs a username",
		},
		{
			// The whitespace-only case would pass this one through: TrimSpace sees
			// a non-empty name. Redis' own ACL-file grammar splits on whitespace,
			// so an interior space is a name that means one thing as an argv
			// element and another read back from a file.
			name:    "interior_space_in_username_is_rejected",
			acl:     &RedisACL{Admin: RedisACLUser{Username: "admin id", Password: "admin-secret"}, App: named("app-id")},
			wantErr: "every ACL user needs a username",
		},
		{
			// NUL terminates the C string the server compares against, so the name
			// it installs is not the name the caller wrote.
			name:    "nul_in_username_is_rejected",
			acl:     &RedisACL{Admin: RedisACLUser{Username: "admin\x00id", Password: "admin-secret"}, App: named("app-id")},
			wantErr: "every ACL user needs a username",
		},
		{
			// sha256("") is a valid rule, so an empty password installs an
			// identity that authenticates with the empty string — the server
			// boots and every ACL assertion still passes against it.
			name:    "empty_admin_password_is_rejected",
			acl:     &RedisACL{Admin: RedisACLUser{Username: "admin-id"}, App: named("app-id")},
			wantErr: "needs a password",
		},
		{
			name:    "empty_additional_user_password_is_rejected",
			acl:     &RedisACL{Admin: named("admin-id"), App: named("app-id"), AdditionalUsers: []RedisACLUser{{Username: "extra-id"}}},
			wantErr: "needs a password",
		},
		{
			name:    "empty_app_username_is_rejected",
			acl:     &RedisACL{Admin: named("admin-id"), App: RedisACLUser{Password: "app-secret"}},
			wantErr: "every ACL user needs a username",
		},
		{
			name: "empty_additional_username_is_rejected",
			acl: &RedisACL{
				Admin:           named("admin-id"),
				App:             named("app-id"),
				AdditionalUsers: []RedisACLUser{{Password: "extra-secret"}},
			},
			wantErr: "every ACL user needs a username",
		},
		{
			name:    "admin_and_app_sharing_a_username_is_rejected",
			acl:     &RedisACL{Admin: named("same-id"), App: named("same-id")},
			wantErr: `"same-id" is declared twice`,
		},
		{
			name: "an_additional_user_repeating_app_is_rejected",
			acl: &RedisACL{
				Admin:           named("admin-id"),
				App:             named("app-id"),
				AdditionalUsers: []RedisACLUser{named("app-id")},
			},
			wantErr: `"app-id" is declared twice`,
		},
		{
			name: "two_additional_users_sharing_a_username_are_rejected",
			acl: &RedisACL{
				Admin:           named("admin-id"),
				App:             named("app-id"),
				AdditionalUsers: []RedisACLUser{named("extra-id"), named("extra-id")},
			},
			wantErr: `"extra-id" is declared twice`,
		},
		{
			// `--user default off` is already in the argument list, so naming an
			// identity `default` is the duplicate declaration Redis echoes.
			name:    "admin_named_default_is_rejected",
			acl:     &RedisACL{Admin: named(redisACLDefaultUsername), App: named("app-id")},
			wantErr: "is the implicit user the fixture disables",
		},
		{
			name:    "app_named_default_is_rejected",
			acl:     &RedisACL{Admin: named("admin-id"), App: named(redisACLDefaultUsername)},
			wantErr: "is the implicit user the fixture disables",
		},
		{
			name: "an_additional_user_named_default_is_rejected",
			acl: &RedisACL{
				Admin:           named("admin-id"),
				App:             named("app-id"),
				AdditionalUsers: []RedisACLUser{named(redisACLDefaultUsername)},
			},
			wantErr: "is the implicit user the fixture disables",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateRedisACLUsernames(tt.acl)

			require.Error(t, err)
			// The leak check runs before the wording check: a refusal that
			// carried a password would still be a leak with the wrong wording,
			// and require would abort before we ever looked.
			for _, u := range slices.Concat([]RedisACLUser{tt.acl.Admin, tt.acl.App}, tt.acl.AdditionalUsers) {
				if u.Password == "" {
					// Every string contains "", so NotContains cannot express
					// this case — and an absent password is not a leak anyway.
					continue
				}
				assert.NotContains(t, err.Error(), u.Password,
					"the refusal exists to keep passwords out of the log, so it must not carry one itself")
			}
			require.ErrorContains(t, err, tt.wantErr)
		})
	}

	assert.NoError(t, validateRedisACLUsernames(&RedisACL{
		Admin:           named("admin-id"),
		App:             named("app-id"),
		AdditionalUsers: []RedisACLUser{named("extra-id")},
	}), "distinct non-default usernames are what the fixture is for")
}

// TestStartRedisContainerInternalRefusesABadACLBeforeStarting proves the check is wired
// where it can still be enforced rather than only that it exists. The context is already
// canceled, so a container start would fail on its own — the ACL message coming back
// instead is what places the refusal ahead of it.
func TestStartRedisContainerInternalRefusesABadACLBeforeStarting(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	cfg := DefaultRedisConfig()
	cfg.ACL = &RedisACL{
		Admin: RedisACLUser{Username: "shared-id", Password: "admin-secret"},
		App:   RedisACLUser{Username: "shared-id", Password: "app-secret"},
	}

	c, err := startRedisContainerInternal(ctx, cfg)

	assert.Nil(t, c)
	require.ErrorContains(t, err, `"shared-id" is declared twice`)
	assert.NotContains(t, err.Error(), "admin-secret")
	assert.NotContains(t, err.Error(), "app-secret")
}

// TestRedisOptionsRunTheClusterBusOverTLS pins the flags that keep a cluster node
// bootable on Redis 8.12, which refuses an unauthenticated bus: every cluster
// arm carries `--tls-cluster yes` with the certificate flags, and only the TLS
// arm moves the client listener. The plain cluster arm must leave the client
// port alone, or every plaintext cluster test would lose its listener.
func TestRedisOptionsRunTheClusterBusOverTLS(t *testing.T) {
	busOverTLS := []string{
		"--cluster-enabled", "yes", "--tls-cluster", "yes",
		"--tls-cert-file", "/tls/server.crt",
		"--tls-key-file", "/tls/server.key",
		"--tls-ca-cert-file", "/tls/ca.crt",
	}
	tests := []struct {
		name string
		tls  bool
		want []string
	}{
		{
			name: "plain_cluster_keeps_the_plaintext_listener",
			want: busOverTLS,
		},
		{
			name: "tls_cluster_moves_the_listener_to_tls",
			tls:  true,
			want: slices.Concat(busOverTLS, []string{"--tls-port", "6379", "--port", "0", "--tls-auth-clients", "no"}),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := DefaultRedisConfig()
			cfg.Cluster = true
			if tt.tls {
				cfg.TLS = mintedTLS(t)
			}
			assert.Equal(t, tt.want, redisCmdAsStarted(t, cfg))
		})
	}
}

// TestNewRedisServedDerivesWhatTheNodeServes pins the one value the server
// flags and the bootstrap redis-cli both read: a caller's TLS is served as
// given, only a plaintext cluster gets a fresh bus pair, and only a TLS cluster
// node, the one a bootstrap dials over TLS, carries a name to verify.
func TestNewRedisServedDerivesWhatTheNodeServes(t *testing.T) {
	served, err := newRedisServed(&RedisContainerConfig{})
	require.NoError(t, err)
	assert.Equal(t, redisServed{}, served)

	standaloneTLS := &RedisTLSMaterial{CA: []byte("ca")}
	served, err = newRedisServed(&RedisContainerConfig{TLS: standaloneTLS})
	require.NoError(t, err)
	assert.Same(t, standaloneTLS, served.material)
	assert.True(t, served.tlsClients)
	assert.Empty(t, served.cliServerName, "no bootstrap dials a standalone node, so its leaf is not read")

	clusterTLS := mintedTLS(t)
	served, err = newRedisServed(&RedisContainerConfig{Cluster: true, TLS: clusterTLS})
	require.NoError(t, err)
	assert.Same(t, clusterTLS, served.material)
	assert.True(t, served.tlsClients)
	assert.Equal(t, "localhost", served.cliServerName)

	served, err = newRedisServed(&RedisContainerConfig{Cluster: true})
	require.NoError(t, err)
	require.NotNil(t, served.material)
	assert.NotEmpty(t, served.material.CA)
	assert.NotEmpty(t, served.material.Cert)
	assert.NotEmpty(t, served.material.Key)
	assert.False(t, served.tlsClients, "the minted pair serves the bus, never a client")
	assert.Empty(t, served.cliServerName)
}

// TestRedisTLSServerNameComesFromTheLeafSAN pins the identity the cluster bootstrap's
// --sni carries: redis-cli 8.12 verifies it against the leaf, so it must be a
// name the leaf actually holds. A leaf holding none yields no name rather than
// an error, because redis-cli before 8.12 bootstraps it; only material no
// image can serve is refused.
func TestRedisTLSServerNameComesFromTheLeafSAN(t *testing.T) {
	tests := []struct {
		name    string
		cert    func(t *testing.T) []byte
		want    string
		wantErr string
	}{
		{
			name: "first_dns_san_wins_over_ip_sans",
			cert: leafFor("127.0.0.1", "redis.test", "localhost"),
			want: "redis.test",
		},
		{
			name: "ip_san_when_no_dns_san",
			cert: leafFor("10.1.2.3"),
			want: "10.1.2.3",
		},
		{
			name: "ipv6_san_is_used_as_given",
			cert: leafFor("::1"),
			want: "::1",
		},
		{
			name: "wildcard_dns_san_is_skipped_for_a_later_concrete_one",
			cert: leafFor("*.cache.test", "localhost"),
			want: "localhost",
		},
		{
			name: "ip_san_wins_over_a_wildcard_only_dns_san",
			cert: leafFor("*.cache.test", "127.0.0.1"),
			want: "127.0.0.1",
		},
		{
			name: "wildcard_only_leaf_yields_a_name_the_wildcard_covers",
			cert: leafFor("*.cache.test"),
			want: "node.cache.test",
		},
		{
			name: "wildcard_over_a_single_label_suffix_yields_no_name",
			cert: leafFor("*.test"),
		},
		{
			name: "wildcard_over_an_empty_suffix_yields_no_name",
			cert: leafFor("*."),
		},
		{
			name: "wildcard_outside_the_leftmost_label_yields_no_name",
			cert: leafFor("node.*.test"),
		},
		{
			name: "ip_literal_dns_san_yields_no_name",
			cert: dnsSANLeaf("127.0.0.1"),
		},
		{
			name: "ip_literal_dns_san_is_skipped_for_a_later_concrete_one",
			cert: dnsSANLeaf("127.0.0.1", "localhost"),
			want: "localhost",
		},
		{
			name: "certificate_after_a_leading_key_block_is_found",
			cert: func(t *testing.T) []byte {
				return append(junkPEM("PRIVATE KEY")(t), leafFor("redis.test")(t)...)
			},
			want: "redis.test",
		},
		{
			name:    "non_pem_is_refused",
			cert:    func(*testing.T) []byte { return []byte("not a certificate") },
			wantErr: "no PEM CERTIFICATE block",
		},
		{
			name:    "non_certificate_pem_block_is_refused",
			cert:    junkPEM("PRIVATE KEY"),
			wantErr: "no PEM CERTIFICATE block",
		},
		{
			name:    "unparseable_certificate_block_is_refused",
			cert:    junkPEM("CERTIFICATE"),
			wantErr: "parsing TLS.Cert",
		},
		{
			name: "leaf_without_a_san_yields_no_name",
			cert: sanlessLeaf,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := redisTLSServerName(&RedisTLSMaterial{Cert: tt.cert(t)})
			if tt.wantErr != "" {
				require.ErrorContains(t, err, tt.wantErr)
				assert.Empty(t, got)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

// TestUnnamedClusterLeafBootstrapsWithoutSNI pins what a cluster TLS leaf
// offering no usable name is given: the CA-pinned transport flags with no
// --sni, the argv every redis-cli bootstrapped such a leaf with before 8.12
// began verifying a name.
func TestUnnamedClusterLeafBootstrapsWithoutSNI(t *testing.T) {
	cfg := DefaultRedisConfig()
	cfg.Cluster = true
	cfg.TLS = &RedisTLSMaterial{Cert: sanlessLeaf(t)}

	served, err := newRedisServed(cfg)

	require.NoError(t, err)
	assert.True(t, served.tlsClients)
	assert.Empty(t, served.cliServerName)
	assert.Equal(t,
		[]string{"redis-cli", "-e", "--tls", "--cacert", "/tls/ca.crt", "cluster", "info"},
		redisCLICommand(cfg, served, "cluster", "info"))
	assert.Contains(t, redisCLIUnnamedLeafHint(served), "TLS.Cert's SANs",
		"a failed exec must say which identity redis-cli checked")
}

// TestRedisCLIUnnamedLeafHintOnlyFollowsAnUnnamedTLSExec keeps the hint off every
// exec it would misdescribe: a plaintext one, and one carrying --sni.
func TestRedisCLIUnnamedLeafHintOnlyFollowsAnUnnamedTLSExec(t *testing.T) {
	assert.Empty(t, redisCLIUnnamedLeafHint(redisServed{}))
	assert.Empty(t, redisCLIUnnamedLeafHint(redisServed{material: &RedisTLSMaterial{}}))
	assert.Empty(t, redisCLIUnnamedLeafHint(redisServed{tlsClients: true, cliServerName: "localhost"}))
	assert.NotEmpty(t, redisCLIUnnamedLeafHint(redisServed{tlsClients: true}))
}

// TestStartRedisContainerInternalRefusesAnUnparseableClusterLeafBeforeStarting
// places the certificate check ahead of the container start, as the ACL refusal
// above is: the context is canceled, so only a check that runs first can answer.
func TestStartRedisContainerInternalRefusesAnUnparseableClusterLeafBeforeStarting(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	cfg := DefaultRedisConfig()
	cfg.Cluster = true
	cfg.TLS = &RedisTLSMaterial{Cert: junkPEM("CERTIFICATE")(t)}

	c, err := startRedisContainerInternal(ctx, cfg)

	assert.Nil(t, c)
	require.ErrorContains(t, err, "redis container: parsing TLS.Cert")
}

// redisCmdAsStarted is the redis-server argv the start path would build for
// cfg, served material derived the same way, so a test pins a command line a
// real start can produce.
func redisCmdAsStarted(t *testing.T, cfg *RedisContainerConfig) []string {
	t.Helper()
	served, err := newRedisServed(cfg)
	require.NoError(t, err)
	return requestFromOptions(t, redisOptions(cfg, served)).Cmd
}

// mintedTLS is caller TLS material the start path accepts for a cluster: a
// CA-signed leaf covering the default hosts.
func mintedTLS(t *testing.T) *RedisTLSMaterial {
	t.Helper()
	ca, cert, key := gbtesting.CAAndServerCertPEM(t)
	return &RedisTLSMaterial{CA: ca, Cert: cert, Key: key}
}

// leafFor mints a CA-signed leaf carrying exactly hosts as its SANs.
func leafFor(hosts ...string) func(t *testing.T) []byte {
	return func(t *testing.T) []byte {
		t.Helper()
		_, leaf, _ := gbtesting.CAAndServerCertPEM(t, hosts...)
		return leaf
	}
}

// junkPEM is a PEM block of blockType whose body is not DER.
func junkPEM(blockType string) func(*testing.T) []byte {
	return func(*testing.T) []byte {
		return pem.EncodeToMemory(&pem.Block{Type: blockType, Bytes: []byte("junk")})
	}
}

// dnsSANLeaf is a self-signed leaf carrying exactly names as DNS-type SANs,
// unparsed, so an IP literal lands there instead of among the IP SANs as
// CAAndServerCertPEM would sort it. Only parsing reads it, so no CA signs it.
func dnsSANLeaf(names ...string) func(t *testing.T) []byte {
	return func(t *testing.T) []byte {
		t.Helper()
		key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		require.NoError(t, err)
		tmpl := &x509.Certificate{
			SerialNumber: big.NewInt(1),
			Subject:      pkix.Name{CommonName: "dns-san-leaf"},
			NotBefore:    time.Now().Add(-time.Minute),
			NotAfter:     time.Now().Add(time.Hour),
			DNSNames:     names,
		}
		der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
		require.NoError(t, err)
		return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	}
}

// sanlessLeaf is a parseable leaf with no DNS or IP SAN at all — the shape
// CAAndServerCertPEM never mints, since it falls back to default hosts.
func sanlessLeaf(t *testing.T) []byte {
	t.Helper()
	cert, _ := gbtesting.SelfSignedCertKeyPEM(t)
	return cert
}
