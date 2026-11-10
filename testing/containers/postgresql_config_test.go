package containers

import (
	"testing"

	"github.com/stretchr/testify/assert"
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
