package commands

import (
	"context"
	"errors"
	"os"
	"strconv"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gaborage/go-bricks/config"
)

const (
	sharedArmedFlag   = "--shared-migrator"
	sharedUnparseable = "maybe"
)

// unsetSharedMigratorEnv makes GOBRICKS_MIGRATE_SHARED_MIGRATOR genuinely absent
// for the duration of the test.
func unsetSharedMigratorEnv(t *testing.T) {
	t.Helper()
	t.Setenv(envSharedMigrator, "")
	require.NoError(t, os.Unsetenv(envSharedMigrator))
}

func TestResolveSharedMigrator(t *testing.T) {
	tests := []struct {
		name    string
		env     *string
		args    []string
		want    bool
		wantErr bool
	}{
		{name: "unset", want: false},
		{name: "empty_is_unset", env: new(""), want: false},
		{name: "env_true", env: new("true"), want: true},
		{name: "env_one", env: new("1"), want: true},
		{name: "env_false", env: new("false"), want: false},
		{name: "env_unparseable", env: new(sharedUnparseable), wantErr: true},
		{name: "flag_wins_over_env", env: new("true"), args: []string{sharedArmedFlag + "=false"}, want: false},
		{name: "flag_without_env", args: []string{sharedArmedFlag}, want: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			unsetSharedMigratorEnv(t)
			if tt.env != nil {
				t.Setenv(envSharedMigrator, *tt.env)
			}
			cmd := &cobra.Command{}
			flags := &CommonFlags{}
			cmd.Flags().BoolVar(&flags.SharedMigrator, flagSharedMigrator, false, "")
			require.NoError(t, cmd.Flags().Parse(tt.args))

			err := resolveSharedMigrator(cmd, flags)
			if tt.wantErr {
				require.Error(t, err)
				assert.Contains(t, err.Error(), envSharedMigrator)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, flags.SharedMigrator)
		})
	}
}

// refusedTypeConfig is a fully populated config whose type the guard refuses.
func refusedTypeConfig(dbType string) *config.DatabaseConfig {
	return &config.DatabaseConfig{Type: dbType, Host: "db.internal", Username: "app", Password: fakePassword("app")}
}

func TestSharedMigratorProviderJudgesType(t *testing.T) {
	errInner := errors.New("inner failed")
	tests := []struct {
		name    string
		inner   *stubProvider
		wantErr string
		wantNil bool
	}{
		{name: "postgresql_passes", inner: &stubProvider{cfg: pgConfig()}},
		{name: "oracle_passes", inner: &stubProvider{cfg: oracleConfig()}},
		{name: "inner_error_passes_through", inner: &stubProvider{err: errInner}, wantErr: errInner.Error()},
		{name: "nil_config_passes_through", inner: &stubProvider{}, wantNil: true},
		{name: "lowercase_alias_refused", inner: &stubProvider{cfg: refusedTypeConfig("postgres")}, wantErr: `tenant "tenant-a"`},
		{name: "mixed_case_refused", inner: &stubProvider{cfg: refusedTypeConfig("PostgreSQL")}, wantErr: `tenant "tenant-a"`},
		{name: "empty_type_refused", inner: &stubProvider{cfg: refusedTypeConfig("")}, wantErr: `tenant "tenant-a"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := &sharedMigratorProvider{inner: tt.inner}
			cfg, err := p.DBConfig(context.Background(), testTenantKey)

			assert.Equal(t, testTenantKey, tt.inner.lastKey)
			if tt.wantErr != "" {
				require.ErrorContains(t, err, tt.wantErr)
				assert.Nil(t, cfg)
				if tt.inner.cfg != nil {
					require.ErrorContains(t, err, strconv.Quote(tt.inner.cfg.Type), "the refusal names the type")
					assert.NotContains(t, err.Error(), tt.inner.cfg.Host)
					assert.NotContains(t, err.Error(), tt.inner.cfg.Password)
				}
				return
			}
			require.NoError(t, err)
			if tt.wantNil {
				assert.Nil(t, cfg)
				return
			}
			assert.Same(t, tt.inner.cfg, cfg)
		})
	}
}
