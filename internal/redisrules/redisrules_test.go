package redisrules

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/gaborage/go-bricks/internal/clienttls"
)

func validEndpoint() Endpoint {
	return Endpoint{
		Host:         "localhost",
		Mode:         "standalone",
		Port:         6379,
		PoolSize:     10,
		DialTimeout:  5 * time.Second,
		ReadTimeout:  3 * time.Second,
		WriteTimeout: 3 * time.Second,
	}
}

func TestCheck(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(e *Endpoint)
		want   *Violation
	}{
		{
			name:   "valid",
			mutate: func(*Endpoint) {},
		},
		{
			name:   "empty_mode_is_standalone",
			mutate: func(e *Endpoint) { e.Mode = "" },
		},
		{
			name:   "read_and_write_timeouts_accept_minus_one",
			mutate: func(e *Endpoint) { e.ReadTimeout, e.WriteTimeout = -1, -1 },
		},
		{
			name:   "password_alone_selects_default_user",
			mutate: func(e *Endpoint) { e.Password = "secret" },
		},
		{
			name:   "host_missing",
			mutate: func(e *Endpoint) { e.Host = "" },
			want:   &Violation{Field: "host", Message: "required", Missing: true},
		},
		{
			name:   "port_zero",
			mutate: func(e *Endpoint) { e.Port = 0 },
			want:   &Violation{Field: "port", Message: "invalid value: 0", Allowed: []string{"1-65535"}},
		},
		{
			name:   "port_above_range",
			mutate: func(e *Endpoint) { e.Port = 65536 },
			want:   &Violation{Field: "port", Message: "invalid value: 65536", Allowed: []string{"1-65535"}},
		},
		{
			name:   "mode_unknown",
			mutate: func(e *Endpoint) { e.Mode = "sentinel" },
			want: &Violation{
				Field: "mode", Message: "'sentinel' is not supported",
				Allowed: []string{"standalone", "cluster"},
			},
		},
		{
			name:   "cluster_needs_database_zero",
			mutate: func(e *Endpoint) { e.Mode, e.Database = "cluster", 3 },
			want: &Violation{
				Field:   "database",
				Message: "must be 0 when mode is cluster: the cluster client has no database selection",
			},
		},
		{
			name:   "username_whitespace_only",
			mutate: func(e *Endpoint) { e.Username, e.Password = "  ", "secret" },
			want:   &Violation{Field: "username", Message: "must not be whitespace-only"},
		},
		{
			name:   "username_needs_password",
			mutate: func(e *Endpoint) { e.Username = "app" },
			want: &Violation{Field: "username", Message: "requires password: the client sends no AUTH without one, " +
				"so the connection would silently run as the default user"},
		},
		{
			name:   "database_negative",
			mutate: func(e *Endpoint) { e.Database = -1 },
			want:   &Violation{Field: "database", Message: "must be between 0 and 15"},
		},
		{
			name:   "database_above_range",
			mutate: func(e *Endpoint) { e.Database = 16 },
			want:   &Violation{Field: "database", Message: "must be between 0 and 15"},
		},
		{
			name:   "poolsize_zero",
			mutate: func(e *Endpoint) { e.PoolSize = 0 },
			want:   &Violation{Field: "poolsize", Message: "must be positive"},
		},
		{
			name:   "dialtimeout_negative",
			mutate: func(e *Endpoint) { e.DialTimeout = -1 },
			want:   &Violation{Field: "dialtimeout", Message: "must be non-negative"},
		},
		{
			name:   "readtimeout_below_minus_one",
			mutate: func(e *Endpoint) { e.ReadTimeout = -2 },
			want:   &Violation{Field: "readtimeout", Message: "must be >= -1"},
		},
		{
			name:   "writetimeout_below_minus_one",
			mutate: func(e *Endpoint) { e.WriteTimeout = -2 },
			want:   &Violation{Field: "writetimeout", Message: "must be >= -1"},
		},
		{
			name:   "tls_material_under_disabled_block",
			mutate: func(e *Endpoint) { e.TLS = clienttls.Material{CAFile: "/ca.pem"} },
			want:   &Violation{Field: "tls.enabled", Message: "must be true when any tls.* field is set"},
		},
		{
			name: "tls_half_client_pair",
			mutate: func(e *Endpoint) {
				e.TLSEnabled = true
				e.TLS = clienttls.Material{CertFile: "/cert.pem"}
			},
			want: &Violation{Field: "tls.keyfile", Message: "a client certificate requires keyfile or keyvalue"},
		},
		{
			name:   "two_faults_host_before_port",
			mutate: func(e *Endpoint) { e.Host, e.Port = "", 0 },
			want:   &Violation{Field: "host", Message: "required", Missing: true},
		},
		{
			name:   "two_faults_port_before_mode",
			mutate: func(e *Endpoint) { e.Port, e.Mode = 0, "sentinel" },
			want:   &Violation{Field: "port", Message: "invalid value: 0", Allowed: []string{"1-65535"}},
		},
		{
			name:   "two_faults_mode_before_username",
			mutate: func(e *Endpoint) { e.Mode, e.Username = "sentinel", "app" },
			want: &Violation{
				Field: "mode", Message: "'sentinel' is not supported",
				Allowed: []string{"standalone", "cluster"},
			},
		},
		{
			name:   "two_faults_cluster_database_before_username",
			mutate: func(e *Endpoint) { e.Mode, e.Database, e.Username = "cluster", 3, "app" },
			want: &Violation{
				Field:   "database",
				Message: "must be 0 when mode is cluster: the cluster client has no database selection",
			},
		},
		{
			name:   "two_faults_cluster_database_before_range",
			mutate: func(e *Endpoint) { e.Mode, e.Database = "cluster", 16 },
			want: &Violation{
				Field:   "database",
				Message: "must be 0 when mode is cluster: the cluster client has no database selection",
			},
		},
		{
			name:   "two_faults_username_whitespace_before_needs_password",
			mutate: func(e *Endpoint) { e.Username = " " },
			want:   &Violation{Field: "username", Message: "must not be whitespace-only"},
		},
		{
			name:   "two_faults_username_before_database",
			mutate: func(e *Endpoint) { e.Username, e.Database = "app", 16 },
			want: &Violation{Field: "username", Message: "requires password: the client sends no AUTH without one, " +
				"so the connection would silently run as the default user"},
		},
		{
			name:   "two_faults_database_before_poolsize",
			mutate: func(e *Endpoint) { e.Database, e.PoolSize = 16, 0 },
			want:   &Violation{Field: "database", Message: "must be between 0 and 15"},
		},
		{
			name:   "two_faults_poolsize_before_dialtimeout",
			mutate: func(e *Endpoint) { e.PoolSize, e.DialTimeout = 0, -1 },
			want:   &Violation{Field: "poolsize", Message: "must be positive"},
		},
		{
			name:   "two_faults_dialtimeout_before_readtimeout",
			mutate: func(e *Endpoint) { e.DialTimeout, e.ReadTimeout = -1, -2 },
			want:   &Violation{Field: "dialtimeout", Message: "must be non-negative"},
		},
		{
			name:   "two_faults_readtimeout_before_writetimeout",
			mutate: func(e *Endpoint) { e.ReadTimeout, e.WriteTimeout = -2, -2 },
			want:   &Violation{Field: "readtimeout", Message: "must be >= -1"},
		},
		{
			name: "two_faults_writetimeout_before_tls",
			mutate: func(e *Endpoint) {
				e.WriteTimeout = -2
				e.TLS = clienttls.Material{CAFile: "/ca.pem"}
			},
			want: &Violation{Field: "writetimeout", Message: "must be >= -1"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := validEndpoint()
			tt.mutate(&e)
			assert.Equal(t, tt.want, Check(&e))
		})
	}
}
