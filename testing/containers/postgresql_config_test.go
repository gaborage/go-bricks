package containers

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPostgreSQLStartLogText(t *testing.T) {
	tests := []struct {
		name    string
		cfg     *PostgreSQLContainerConfig
		want    string
		secrets []string
	}{
		{
			name: "password_holding_at_sign",
			cfg: &PostgreSQLContainerConfig{
				Username: "appuser",
				Password: "FAKEp@FAKEtail",
				Database: "appdb",
			},
			want:    "PostgreSQL container started successfully at localhost:54321 (database: appdb, user: appuser)",
			secrets: []string{"FAKEp", "FAKEtail"},
		},
		{
			name:    "default_config",
			cfg:     DefaultPostgreSQLConfig(),
			want:    "PostgreSQL container started successfully at localhost:54321 (database: testdb, user: testuser)",
			secrets: []string{"testpass"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := postgreSQLStartLogText(tt.cfg, "localhost", 54321)
			for _, secret := range tt.secrets {
				assert.NotContains(t, got, secret)
			}
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestPostgreSQLSettingArgs(t *testing.T) {
	tests := []struct {
		name     string
		settings map[string]string
		want     []string
	}{
		{name: "nil_settings", settings: nil, want: nil},
		{name: "empty_settings", settings: map[string]string{}, want: nil},
		{
			name: "sorted_by_key",
			settings: map[string]string{
				"max_connections":          "200",
				"fsync":                    "off",
				"log_min_duration_ms":      "0",
				"shared_preload_libraries": "pg_stat_statements,auto_explain",
			},
			want: []string{
				"-c", "fsync=off",
				"-c", "log_min_duration_ms=0",
				"-c", "max_connections=200",
				"-c", "shared_preload_libraries=pg_stat_statements,auto_explain",
			},
		},
		{
			name:     "value_keeps_equals_and_spaces",
			settings: map[string]string{"search_path": "a, b=c"},
			want:     []string{"-c", "search_path=a, b=c"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := postgreSQLSettingArgs(tt.settings)
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestPostgreSQLSettingArgsRejectsInvalidKey(t *testing.T) {
	tests := []struct {
		name string
		key  string
	}{
		{name: "empty_key", key: ""},
		{name: "key_with_space", key: "max connections"},
		{name: "key_with_tab", key: "max\tconnections"},
		{name: "key_with_newline", key: "fsync\nwork_mem"},
		{name: "key_with_equals", key: "fsync=off"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := postgreSQLSettingArgs(map[string]string{
				"fsync": "off",
				tt.key:  "1",
			})
			require.Error(t, err)
			assert.Nil(t, got)
			assert.Contains(t, err.Error(), fmt.Sprintf("%q", tt.key))
		})
	}
}

func TestPostgreSQLDataTmpfs(t *testing.T) {
	tests := []struct {
		name string
		size string
		want map[string]string
	}{
		{name: "empty_size", size: "", want: nil},
		{name: "sized", size: "512m", want: map[string]string{"/var/lib/postgresql": "rw,size=512m"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, postgreSQLDataTmpfs(tt.size))
		})
	}
}
