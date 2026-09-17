package config

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gaborage/go-bricks/internal/testutil"
)

func TestScanPostgresDSN(t *testing.T) {
	tests := []struct {
		name      string
		dsn       string
		want      pgDSNScan
		claimsTLS bool
	}{
		{
			name: "keyword_host",
			dsn:  "host=db.example.com user=u",
			want: pgDSNScan{hostSet: true, host: "db.example.com"},
		},
		{name: "keyword_without_host", dsn: "user=u dbname=d", want: pgDSNScan{}},
		// pgx skips whitespace after '=', so the next pair becomes the host value.
		{name: "keyword_empty_host_swallows_next_pair", dsn: "host= user=u dbname=d", want: pgDSNScan{hostSet: true, host: "user=u"}},
		{name: "keyword_empty_host_at_end", dsn: "user=u host=", want: pgDSNScan{hostSet: true}},
		{name: "keyword_empty_entry", dsn: "host=a,,b user=u", want: pgDSNScan{hostSet: true, host: "a,,b"}},
		{
			name: "keyword_multi_host_socket", dsn: "host=db.internal,/var/run/postgresql sslmode=verify-full",
			want: pgDSNScan{hostSet: true, host: "db.internal,/var/run/postgresql"}, claimsTLS: true,
		},
		{name: "keyword_whitespace_around_tokens", dsn: " \thost \n=\r db\v\f", want: pgDSNScan{hostSet: true, host: "db"}},
		{
			name: "keyword_socket_verify_full", dsn: "host=/var/run/postgresql sslmode=verify-full",
			want: pgDSNScan{hostSet: true, host: "/var/run/postgresql"}, claimsTLS: true,
		},
		{name: "keyword_socket_disable", dsn: "host=/var/run/postgresql sslmode=disable", want: pgDSNScan{hostSet: true, host: "/var/run/postgresql"}},
		{name: "keyword_socket_prefer", dsn: "host=/var/run/postgresql sslmode=prefer", want: pgDSNScan{hostSet: true, host: "/var/run/postgresql"}},
		{name: "keyword_socket_allow", dsn: "host=/var/run/postgresql sslmode=allow", want: pgDSNScan{hostSet: true, host: "/var/run/postgresql"}},
		{name: "keyword_socket_no_claim", dsn: "host=/var/run/postgresql", want: pgDSNScan{hostSet: true, host: "/var/run/postgresql"}},
		// pgx drops an unquoted backslash and keeps the next byte, so this Windows spelling names host "C:pg".
		{
			name: "keyword_unquoted_backslash_escapes_next_byte", dsn: `host=C:\pg sslmode=require`,
			want: pgDSNScan{hostSet: true, host: "C:pg"}, claimsTLS: true,
		},
		{
			name: "keyword_escaped_backslash_windows_socket", dsn: `host=C:\\pg sslmode=require`,
			want: pgDSNScan{hostSet: true, host: `C:\pg`}, claimsTLS: true,
		},
		{name: "keyword_quoted_windows_socket", dsn: `host='C:\\pg'`, want: pgDSNScan{hostSet: true, host: `C:\pg`}},
		{
			name: "keyword_escaped_space_stays_in_value", dsn: `host=/a\ b sslmode=require`,
			want: pgDSNScan{hostSet: true, host: "/a b"}, claimsTLS: true,
		},
		{name: "keyword_trailing_backslash_ends_value", dsn: `host=a\`, want: pgDSNScan{hostSet: true, host: "a"}},
		{
			name: "keyword_quoted_value", dsn: `host='/var/run/my socket' sslmode='verify-ca'`,
			want: pgDSNScan{hostSet: true, host: "/var/run/my socket"}, claimsTLS: true,
		},
		{name: "keyword_quoted_escapes", dsn: `host='C:\\pg\'s' user=u`, want: pgDSNScan{hostSet: true, host: `C:\pg's`}},
		{name: "keyword_quoted_empty", dsn: `host='' user=u`, want: pgDSNScan{hostSet: true}},
		{name: "keyword_sslrootcert", dsn: "host=/s sslrootcert=/x", want: pgDSNScan{hostSet: true, host: "/s"}, claimsTLS: true},
		{name: "keyword_sslcert", dsn: "host=/s sslcert=/x", want: pgDSNScan{hostSet: true, host: "/s"}, claimsTLS: true},
		{name: "keyword_sslkey", dsn: "host=/s sslkey=/x", want: pgDSNScan{hostSet: true, host: "/s"}, claimsTLS: true},
		// configTLS ignores an empty material value, so it claims nothing.
		{name: "keyword_empty_material", dsn: "host=/s sslcert='' sslkey='' sslrootcert=", want: pgDSNScan{hostSet: true, host: "/s"}},
		{name: "keyword_ssl_true_is_not_an_alias", dsn: "host=/s ssl=true", want: pgDSNScan{hostSet: true, host: "/s"}},
		// pgx v5.11.0 pgconn/config.go:903-907 upgrades prefer (and unset) to require under direct negotiation.
		{name: "keyword_sslnegotiation_direct_claims_tls", dsn: "host=/s sslnegotiation=direct", want: pgDSNScan{hostSet: true, host: "/s"}, claimsTLS: true},
		// pgx accepts direct with disable and connects in plaintext; the string still names TLS negotiation, so it claims.
		{
			name: "keyword_sslnegotiation_direct_with_disable_claims_tls", dsn: "host=/s sslnegotiation=direct sslmode=disable",
			want: pgDSNScan{hostSet: true, host: "/s"}, claimsTLS: true,
		},
		{
			name: "keyword_sslnegotiation_direct_with_allow_claims_tls", dsn: "host=/s sslnegotiation=direct sslmode=allow",
			want: pgDSNScan{hostSet: true, host: "/s"}, claimsTLS: true,
		},
		{
			name: "keyword_sslnegotiation_postgres_prefer_no_claim", dsn: "host=/s sslnegotiation=postgres sslmode=prefer",
			want: pgDSNScan{hostSet: true, host: "/s"},
		},
		{
			name: "keyword_last_occurrence_wins", dsn: "host=a sslmode=require host=/s sslmode=disable",
			want: pgDSNScan{hostSet: true, host: "/s"},
		},

		{name: "uri_authority_host", dsn: "postgres://db.example.com/db", want: pgDSNScan{hostSet: true, host: "db.example.com"}},
		{name: "uri_postgresql_scheme", dsn: "postgresql://db.example.com:5432/db", want: pgDSNScan{hostSet: true, host: "db.example.com"}},
		{name: "uri_authority_only", dsn: "postgres://db.example.com", want: pgDSNScan{hostSet: true, host: "db.example.com"}},
		{name: "uri_no_host", dsn: "postgres:///db", want: pgDSNScan{}},
		{name: "uri_no_host_verify_full", dsn: "postgres:///db?sslmode=verify-full", want: pgDSNScan{}, claimsTLS: true},
		{name: "uri_space_host_decodes_empty", dsn: "postgres:// /db", want: pgDSNScan{hostSet: true}},
		{name: "uri_empty_entry", dsn: "postgres://a,,b/db", want: pgDSNScan{hostSet: true, host: "a,,b"}},
		{name: "uri_multi_host_ports", dsn: "postgres://a:5432,b:5433/db", want: pgDSNScan{hostSet: true, host: "a,b"}},
		{name: "uri_port_only", dsn: "postgres://:5432/db", want: pgDSNScan{}},
		{name: "uri_port_only_list", dsn: "postgres://:5432,:5433/db", want: pgDSNScan{hostSet: true, host: ","}},
		{name: "uri_ipv6", dsn: "postgres://[::1]:5432,[fe80::1]/db", want: pgDSNScan{hostSet: true, host: "::1,fe80::1"}},
		{name: "uri_ipv6_before_query", dsn: "postgres://[::1]?sslmode=require", want: pgDSNScan{hostSet: true, host: "::1"}, claimsTLS: true},
		{name: "uri_ipv6_at_end", dsn: "postgres://[::1]", want: pgDSNScan{hostSet: true, host: "::1"}},
		{name: "uri_userinfo", dsn: "postgres://u:p%40ss:w@db.example.com/db", want: pgDSNScan{hostSet: true, host: "db.example.com"}},
		{name: "uri_empty_userinfo", dsn: "postgres://@h/db", want: pgDSNScan{hostSet: true, host: "h"}},
		{name: "uri_userinfo_first_at_wins", dsn: "postgres://u:p@ss@h/db", want: pgDSNScan{hostSet: true, host: "ss@h"}},
		{name: "uri_slash_stops_userinfo_search", dsn: "postgres://h/db@x", want: pgDSNScan{hostSet: true, host: "h"}},
		{
			name: "uri_percent_encoded_socket", dsn: "postgres://%2Fvar%2Frun%2Fpostgresql/db?sslmode=require",
			want: pgDSNScan{hostSet: true, host: "/var/run/postgresql"}, claimsTLS: true,
		},
		{name: "uri_percent_encoded_comma_splits", dsn: "postgres://a%2C/db", want: pgDSNScan{hostSet: true, host: "a,"}},
		{name: "uri_query_host", dsn: "postgres:///db?host=db.example.com", want: pgDSNScan{hostSet: true, host: "db.example.com"}},
		{name: "uri_query_host_overrides_authority", dsn: "postgres://a/db?host=c", want: pgDSNScan{hostSet: true, host: "c"}},
		{name: "uri_query_empty_host", dsn: "postgres://h/db?host=", want: pgDSNScan{hostSet: true}},
		{name: "uri_query_decoded", dsn: "postgres:///db?%68ost=%2Fs&sslmode=verify%2Dca", want: pgDSNScan{hostSet: true, host: "/s"}, claimsTLS: true},
		{
			name: "uri_query_socket_sslrootcert", dsn: "postgres:///db?host=/var/run/postgresql&sslrootcert=/x",
			want: pgDSNScan{hostSet: true, host: "/var/run/postgresql"}, claimsTLS: true,
		},
		{
			name: "uri_query_sslnegotiation_direct_claims_tls", dsn: "postgres:///db?host=/s&sslnegotiation=direct",
			want: pgDSNScan{hostSet: true, host: "/s"}, claimsTLS: true,
		},
		{name: "uri_ssl_true_after_sslmode", dsn: "postgres://h?sslmode=disable&ssl=true", want: pgDSNScan{hostSet: true, host: "h"}, claimsTLS: true},
		{name: "uri_sslmode_after_ssl_true", dsn: "postgres://h?ssl=true&sslmode=disable", want: pgDSNScan{hostSet: true, host: "h"}},
		{name: "uri_ssl_true_superseded", dsn: "postgres://h?ssl=true&ssl=false", want: pgDSNScan{hostSet: true, host: "h"}},
		// Only the exact lowercase prefix selects the URI parser; anything else is keyword form.
		{name: "keyword_form_with_uri_looking_value", dsn: "host=postgres://h", want: pgDSNScan{hostSet: true, host: "postgres://h"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := scanPostgresDSN(tt.dsn)
			require.True(t, ok)
			assert.Equal(t, tt.want.hostSet, got.hostSet)
			assert.Equal(t, tt.want.host, got.host)
			assert.Equal(t, tt.claimsTLS, got.dsnClaimsTLS())
		})
	}
}

func TestScanPostgresDSNRejectsUntokenizable(t *testing.T) {
	for _, dsn := range testutil.UntokenizablePostgresDSNs {
		_, ok := scanPostgresDSN(dsn)
		assert.False(t, ok, "%q", dsn)
	}
}

// TestScanPostgresDSNMatchesSharedHostFixtures shares testutil.PostgresDSNHostCases with
// database/postgresql's pgconn.ParseConfig oracle test, so the two host mirrors cannot drift
// apart from each other without a failing test on at least one side.
func TestScanPostgresDSNMatchesSharedHostFixtures(t *testing.T) {
	for _, c := range testutil.PostgresDSNHostCases {
		t.Run(c.Name, func(t *testing.T) {
			got, ok := scanPostgresDSN(c.DSN)
			require.True(t, ok)
			assert.Equal(t, c.HostSet, got.hostSet)
			assert.Equal(t, c.Host, got.host)
		})
	}
}

func TestPGSSLEnvKeys(t *testing.T) {
	assert.Equal(t, []pgSSLEnvKey{
		{env: "PGSSLMODE", dsn: pgDSNSSLMode},
		{env: "PGSSLROOTCERT", dsn: pgDSNSSLRootCert},
		{env: "PGSSLCERT", dsn: pgDSNSSLCert},
		{env: "PGSSLKEY", dsn: pgDSNSSLKey},
		{env: "PGSSLNEGOTIATION", dsn: pgDSNSSLNegotiation},
	}, pgSSLEnvKeys)
}

func TestScanPostgresDSNRecordsTLSKeyPresence(t *testing.T) {
	tests := []struct {
		name string
		dsn  string
		key  string
		want pgDSNSetting
	}{
		{name: "sslmode_present", dsn: "host=/s sslmode=verify-full", key: pgDSNSSLMode, want: pgDSNSetting{set: true, value: "verify-full"}},
		{name: "sslmode_empty_still_present", dsn: "host=/s sslmode=", key: pgDSNSSLMode, want: pgDSNSetting{set: true}},
		{name: "sslmode_absent", dsn: "host=/s", key: pgDSNSSLMode, want: pgDSNSetting{}},
		{name: "sslrootcert_present", dsn: "host=/s sslrootcert=/x", key: pgDSNSSLRootCert, want: pgDSNSetting{set: true, value: "/x"}},
		{name: "sslcert_empty_quoted", dsn: "host=/s sslcert=''", key: pgDSNSSLCert, want: pgDSNSetting{set: true}},
		{name: "sslkey_present", dsn: "host=/s sslkey=/k", key: pgDSNSSLKey, want: pgDSNSetting{set: true, value: "/k"}},
		{name: "sslnegotiation_direct", dsn: "host=/s sslnegotiation=direct", key: pgDSNSSLNegotiation, want: pgDSNSetting{set: true, value: "direct"}},
		{name: "uri_sslmode_from_ssl_true_alias", dsn: "postgres://h?ssl=true", key: pgDSNSSLMode, want: pgDSNSetting{set: true, value: sslModeRequire}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := scanPostgresDSN(tt.dsn)
			require.True(t, ok)
			assert.Equal(t, tt.want, got.tls.setting(tt.key))
		})
	}
}

func TestPgDSNSettingOver(t *testing.T) {
	tests := []struct {
		name        string
		setting     pgDSNSetting
		env         string
		wantValue   string
		wantFromEnv bool
	}{
		{name: "present_key_wins", setting: pgDSNSetting{set: true, value: "dsn"}, env: "env", wantValue: "dsn"},
		{name: "present_empty_key_shadows_env", setting: pgDSNSetting{set: true}, env: "env"},
		{name: "absent_key_inherits_env", env: "env", wantValue: "env", wantFromEnv: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			value, fromEnv := tt.setting.over(tt.env)
			assert.Equal(t, tt.wantValue, value)
			assert.Equal(t, tt.wantFromEnv, fromEnv)
		})
	}
}

func TestScanPostgresDSNRecordsServicePresence(t *testing.T) {
	tests := []struct {
		name string
		dsn  string
		want pgDSNSetting
	}{
		{name: "keyword_service", dsn: "host=h service=svc", want: pgDSNSetting{set: true, value: "svc"}},
		{name: "keyword_empty_service_still_present", dsn: "host=h service=''", want: pgDSNSetting{set: true}},
		{name: "uri_query_service", dsn: "postgres:///db?service=svc", want: pgDSNSetting{set: true, value: "svc"}},
		{name: "absent", dsn: "host=h", want: pgDSNSetting{}},
		{name: "servicefile_is_not_service", dsn: "host=h servicefile=/x", want: pgDSNSetting{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := scanPostgresDSN(tt.dsn)
			require.True(t, ok)
			assert.Equal(t, tt.want, got.service)
		})
	}
}
