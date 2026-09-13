package config

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gaborage/go-bricks/internal/testutil"
)

func TestScanPostgresDSN(t *testing.T) {
	tests := []struct {
		name string
		dsn  string
		want pgDSNScan
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
			want: pgDSNScan{hostSet: true, host: "db.internal,/var/run/postgresql", claimsTLS: true},
		},
		{name: "keyword_whitespace_around_tokens", dsn: " \thost \n=\r db\v\f", want: pgDSNScan{hostSet: true, host: "db"}},
		{
			name: "keyword_socket_verify_full", dsn: "host=/var/run/postgresql sslmode=verify-full",
			want: pgDSNScan{hostSet: true, host: "/var/run/postgresql", claimsTLS: true},
		},
		{name: "keyword_socket_disable", dsn: "host=/var/run/postgresql sslmode=disable", want: pgDSNScan{hostSet: true, host: "/var/run/postgresql"}},
		{name: "keyword_socket_prefer", dsn: "host=/var/run/postgresql sslmode=prefer", want: pgDSNScan{hostSet: true, host: "/var/run/postgresql"}},
		{name: "keyword_socket_allow", dsn: "host=/var/run/postgresql sslmode=allow", want: pgDSNScan{hostSet: true, host: "/var/run/postgresql"}},
		{name: "keyword_socket_no_claim", dsn: "host=/var/run/postgresql", want: pgDSNScan{hostSet: true, host: "/var/run/postgresql"}},
		// pgx drops an unquoted backslash and keeps the next byte, so this Windows spelling names host "C:pg".
		{
			name: "keyword_unquoted_backslash_escapes_next_byte", dsn: `host=C:\pg sslmode=require`,
			want: pgDSNScan{hostSet: true, host: "C:pg", claimsTLS: true},
		},
		{
			name: "keyword_escaped_backslash_windows_socket", dsn: `host=C:\\pg sslmode=require`,
			want: pgDSNScan{hostSet: true, host: `C:\pg`, claimsTLS: true},
		},
		{name: "keyword_quoted_windows_socket", dsn: `host='C:\\pg'`, want: pgDSNScan{hostSet: true, host: `C:\pg`}},
		{
			name: "keyword_escaped_space_stays_in_value", dsn: `host=/a\ b sslmode=require`,
			want: pgDSNScan{hostSet: true, host: "/a b", claimsTLS: true},
		},
		{name: "keyword_trailing_backslash_ends_value", dsn: `host=a\`, want: pgDSNScan{hostSet: true, host: "a"}},
		{
			name: "keyword_quoted_value", dsn: `host='/var/run/my socket' sslmode='verify-ca'`,
			want: pgDSNScan{hostSet: true, host: "/var/run/my socket", claimsTLS: true},
		},
		{name: "keyword_quoted_escapes", dsn: `host='C:\\pg\'s' user=u`, want: pgDSNScan{hostSet: true, host: `C:\pg's`}},
		{name: "keyword_quoted_empty", dsn: `host='' user=u`, want: pgDSNScan{hostSet: true}},
		{name: "keyword_sslrootcert", dsn: "host=/s sslrootcert=/x", want: pgDSNScan{hostSet: true, host: "/s", claimsTLS: true}},
		{name: "keyword_sslcert", dsn: "host=/s sslcert=/x", want: pgDSNScan{hostSet: true, host: "/s", claimsTLS: true}},
		{name: "keyword_sslkey", dsn: "host=/s sslkey=/x", want: pgDSNScan{hostSet: true, host: "/s", claimsTLS: true}},
		// configTLS ignores an empty material value, so it claims nothing.
		{name: "keyword_empty_material", dsn: "host=/s sslcert='' sslkey='' sslrootcert=", want: pgDSNScan{hostSet: true, host: "/s"}},
		{name: "keyword_ssl_true_is_not_an_alias", dsn: "host=/s ssl=true", want: pgDSNScan{hostSet: true, host: "/s"}},
		// pgx v5.11.0 pgconn/config.go:903-907 upgrades prefer (and unset) to require under direct negotiation.
		{name: "keyword_sslnegotiation_direct_claims_tls", dsn: "host=/s sslnegotiation=direct", want: pgDSNScan{hostSet: true, host: "/s", claimsTLS: true}},
		// pgx accepts direct with disable and connects in plaintext; the string still names TLS negotiation, so it claims.
		{
			name: "keyword_sslnegotiation_direct_with_disable_claims_tls", dsn: "host=/s sslnegotiation=direct sslmode=disable",
			want: pgDSNScan{hostSet: true, host: "/s", claimsTLS: true},
		},
		{
			name: "keyword_sslnegotiation_postgres_prefer_no_claim", dsn: "host=/s sslnegotiation=postgres sslmode=prefer",
			want: pgDSNScan{hostSet: true, host: "/s"},
		},

		{name: "uri_authority_host", dsn: "postgres://db.example.com/db", want: pgDSNScan{hostSet: true, host: "db.example.com"}},
		{name: "uri_postgresql_scheme", dsn: "postgresql://db.example.com:5432/db", want: pgDSNScan{hostSet: true, host: "db.example.com"}},
		{name: "uri_authority_only", dsn: "postgres://db.example.com", want: pgDSNScan{hostSet: true, host: "db.example.com"}},
		{name: "uri_no_host", dsn: "postgres:///db", want: pgDSNScan{}},
		{name: "uri_no_host_verify_full", dsn: "postgres:///db?sslmode=verify-full", want: pgDSNScan{claimsTLS: true}},
		{name: "uri_space_host_decodes_empty", dsn: "postgres:// /db", want: pgDSNScan{hostSet: true}},
		{name: "uri_empty_entry", dsn: "postgres://a,,b/db", want: pgDSNScan{hostSet: true, host: "a,,b"}},
		{name: "uri_multi_host_ports", dsn: "postgres://a:5432,b:5433/db", want: pgDSNScan{hostSet: true, host: "a,b"}},
		{name: "uri_port_only", dsn: "postgres://:5432/db", want: pgDSNScan{}},
		{name: "uri_port_only_list", dsn: "postgres://:5432,:5433/db", want: pgDSNScan{hostSet: true, host: ","}},
		{name: "uri_ipv6", dsn: "postgres://[::1]:5432,[fe80::1]/db", want: pgDSNScan{hostSet: true, host: "::1,fe80::1"}},
		{name: "uri_ipv6_before_query", dsn: "postgres://[::1]?sslmode=require", want: pgDSNScan{hostSet: true, host: "::1", claimsTLS: true}},
		{name: "uri_ipv6_at_end", dsn: "postgres://[::1]", want: pgDSNScan{hostSet: true, host: "::1"}},
		{name: "uri_userinfo", dsn: "postgres://u:p%40ss:w@db.example.com/db", want: pgDSNScan{hostSet: true, host: "db.example.com"}},
		{name: "uri_empty_userinfo", dsn: "postgres://@h/db", want: pgDSNScan{hostSet: true, host: "h"}},
		{name: "uri_userinfo_first_at_wins", dsn: "postgres://u:p@ss@h/db", want: pgDSNScan{hostSet: true, host: "ss@h"}},
		{name: "uri_slash_stops_userinfo_search", dsn: "postgres://h/db@x", want: pgDSNScan{hostSet: true, host: "h"}},
		{
			name: "uri_percent_encoded_socket", dsn: "postgres://%2Fvar%2Frun%2Fpostgresql/db?sslmode=require",
			want: pgDSNScan{hostSet: true, host: "/var/run/postgresql", claimsTLS: true},
		},
		{name: "uri_percent_encoded_comma_splits", dsn: "postgres://a%2C/db", want: pgDSNScan{hostSet: true, host: "a,"}},
		{name: "uri_query_host", dsn: "postgres:///db?host=db.example.com", want: pgDSNScan{hostSet: true, host: "db.example.com"}},
		{name: "uri_query_host_overrides_authority", dsn: "postgres://a,,b/db?host=c", want: pgDSNScan{hostSet: true, host: "c"}},
		{name: "uri_query_empty_host", dsn: "postgres://h/db?host=", want: pgDSNScan{hostSet: true}},
		{name: "uri_query_decoded", dsn: "postgres:///db?%68ost=%2Fs&sslmode=verify%2Dca", want: pgDSNScan{hostSet: true, host: "/s", claimsTLS: true}},
		{
			name: "uri_query_socket_sslrootcert", dsn: "postgres:///db?host=/var/run/postgresql&sslrootcert=/x",
			want: pgDSNScan{hostSet: true, host: "/var/run/postgresql", claimsTLS: true},
		},
		// pgx v5.11.0 pgconn/config.go:903-907 upgrades prefer (and unset) to require under direct negotiation.
		{
			name: "uri_query_sslnegotiation_direct_claims_tls", dsn: "postgres:///db?host=/s&sslnegotiation=direct",
			want: pgDSNScan{hostSet: true, host: "/s", claimsTLS: true},
		},
		{name: "uri_ssl_true_after_sslmode", dsn: "postgres://h?sslmode=disable&ssl=true", want: pgDSNScan{hostSet: true, host: "h", claimsTLS: true}},
		{name: "uri_sslmode_after_ssl_true", dsn: "postgres://h?ssl=true&sslmode=disable", want: pgDSNScan{hostSet: true, host: "h"}},
		{name: "uri_ssl_true_superseded", dsn: "postgres://h?ssl=true&ssl=false", want: pgDSNScan{hostSet: true, host: "h"}},
		// Only the exact lowercase prefix selects the URI parser; anything else is keyword form.
		{name: "keyword_form_with_uri_looking_value", dsn: "host=postgres://h", want: pgDSNScan{hostSet: true, host: "postgres://h"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := scanPostgresDSN(tt.dsn)
			require.True(t, ok)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestScanPostgresDSNRejectsUntokenizable(t *testing.T) {
	for _, dsn := range testutil.UntokenizablePostgresDSNs {
		_, ok := scanPostgresDSN(dsn)
		assert.False(t, ok, "%q", dsn)
	}
}
