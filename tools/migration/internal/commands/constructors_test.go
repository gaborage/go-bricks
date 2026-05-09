package commands

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gaborage/go-bricks/migration"
)

func TestNewRootCommandWiresSubcommands(t *testing.T) {
	root := NewRootCommand()
	require.NotNil(t, root)
	assert.Equal(t, "go-bricks-migrate", root.Use)
	assert.True(t, root.SilenceUsage)
	assert.True(t, root.SilenceErrors)
	assert.Contains(t, root.Long, "Run Flyway migrations")
}

func TestNewMigrateCommand(t *testing.T) {
	cmd := NewMigrateCommand()
	require.NotNil(t, cmd)
	assert.Equal(t, "migrate", cmd.Use)
	assert.NotEmpty(t, cmd.Short)
	assert.NotNil(t, cmd.RunE)
	assert.NotNil(t, cmd.Flags().Lookup("source-url"))
	assert.NotNil(t, cmd.Flags().Lookup("secrets-prefix"))
}

func TestNewValidateCommand(t *testing.T) {
	cmd := NewValidateCommand()
	require.NotNil(t, cmd)
	assert.Equal(t, "validate", cmd.Use)
	assert.NotNil(t, cmd.RunE)
	assert.NotNil(t, cmd.Flags().Lookup("source-url"))
}

func TestNewInfoCommand(t *testing.T) {
	cmd := NewInfoCommand()
	require.NotNil(t, cmd)
	assert.Equal(t, "info", cmd.Use)
	assert.NotNil(t, cmd.RunE)
	assert.NotNil(t, cmd.Flags().Lookup("source-url"))
}

func TestNewListCommandFlags(t *testing.T) {
	cmd := NewListCommand()
	require.NotNil(t, cmd)
	assert.Equal(t, "list", cmd.Use)
	assert.NotNil(t, cmd.RunE)
	assert.NotNil(t, cmd.Flags().Lookup("source-url"))
}

func TestSubcommandsRejectExtraPositionalArgs(t *testing.T) {
	ctors := []func() *cobra.Command{
		NewListCommand,
		NewInfoCommand,
		NewMigrateCommand,
		NewValidateCommand,
		func() *cobra.Command { return NewVersionCommand("dev") },
	}
	for _, ctor := range ctors {
		cmd := ctor()
		t.Run(cmd.Use, func(t *testing.T) {
			cmd.SetArgs([]string{"unexpected-positional-arg"})
			cmd.SetOut(&bytes.Buffer{})
			cmd.SetErr(&bytes.Buffer{})
			cmd.SetContext(context.Background())
			err := cmd.Execute()
			require.Error(t, err)
			assert.Contains(t, strings.ToLower(err.Error()), "arg")
		})
	}
}

func TestNewVersionCommand(t *testing.T) {
	cmd := NewVersionCommand("v1.2.3")
	require.NotNil(t, cmd)
	assert.Equal(t, "version", cmd.Use)
	require.NotNil(t, cmd.Run)

	var buf strings.Builder
	cmd.SetOut(&buf)
	cmd.Run(cmd, nil)
	assert.Contains(t, buf.String(), "v1.2.3")
}

func TestTitleASCII(t *testing.T) {
	cases := map[string]string{
		"":         "",
		"migrate":  "Migrate",
		"info":     "Info",
		"Validate": "Validate",
		"42":       "42",
	}
	for in, want := range cases {
		assert.Equal(t, want, titleASCII(in), "input %q", in)
	}
}

func TestVendorOrUnknown(t *testing.T) {
	assert.Equal(t, "unknown", vendorOrUnknown(""))
	assert.Equal(t, "postgresql", vendorOrUnknown("postgresql"))
}

func TestMaybeLoadFileStoreSkipsWhenNotNeeded(t *testing.T) {
	// SourceURL set + AWS creds → no file store needed
	store, err := maybeLoadFileStore(&CommonFlags{
		SourceURL:       "https://example.com",
		CredentialsFrom: credsSourceAWS,
	})
	require.NoError(t, err)
	assert.Nil(t, store)

	// Tenant set + AWS creds → no file store needed
	store, err = maybeLoadFileStore(&CommonFlags{
		Tenant:          "single",
		CredentialsFrom: credsSourceAWS,
	})
	require.NoError(t, err)
	assert.Nil(t, store)

	// SourceConfig empty → no file store needed (and no error)
	store, err = maybeLoadFileStore(&CommonFlags{})
	require.NoError(t, err)
	assert.Nil(t, store)
}

func TestMaybeLoadFileStorePropagatesError(t *testing.T) {
	// non-existent path → loadTenantStoreFromFile returns an error
	_, err := maybeLoadFileStore(&CommonFlags{
		SourceConfig:    "/definitely/does/not/exist.yaml",
		CredentialsFrom: credsSourceFile,
	})
	require.Error(t, err)
	assert.True(t, strings.Contains(err.Error(), "/definitely/does/not/exist.yaml"))
}

func TestValidateConfigPathRejectsUnsafe(t *testing.T) {
	cases := []string{
		"",
		"/tmp/../etc/passwd",
		"/tmp/foo;rm",
		"/tmp/foo&bg",
		"/tmp/foo|cat",
		"/tmp/foo$VAR",
		"/tmp/foo`whoami`",
	}
	for _, c := range cases {
		err := validateConfigPath(c)
		assert.Error(t, err, "expected error for %q", c)
	}
}

func TestMakeHookJSONSuccessAndFailure(t *testing.T) {
	var buf bytes.Buffer
	hook := makeHook(&buf, true)
	hook(migration.TenantResult{TenantID: "t1", Vendor: "postgresql", Duration: 10 * time.Millisecond})
	hook(migration.TenantResult{TenantID: "t2", Vendor: "oracle", Err: errors.New("boom")})
	out := buf.String()
	assert.Contains(t, out, `"tenant_id":"t1"`)
	assert.Contains(t, out, `"status":"ok"`)
	assert.Contains(t, out, `"tenant_id":"t2"`)
	assert.Contains(t, out, `"status":"fail"`)
	assert.Contains(t, out, `"error":"boom"`)
}

func TestMakeHookPlainText(t *testing.T) {
	var buf bytes.Buffer
	hook := makeHook(&buf, false)
	hook(migration.TenantResult{TenantID: "t1", Vendor: "postgresql", Duration: 10 * time.Millisecond})
	hook(migration.TenantResult{TenantID: "t2", Err: errors.New("boom")})
	out := buf.String()
	assert.Contains(t, out, "t1 (postgresql)")
	assert.Contains(t, out, "ok")
	assert.Contains(t, out, "t2 (unknown)")
	assert.Contains(t, out, "FAIL")
}

func TestWriteSummaryJSON(t *testing.T) {
	var buf bytes.Buffer
	result := &migration.MigrateAllResult{
		Action: migration.ActionMigrate,
		Results: []migration.TenantResult{
			{TenantID: "t1"},
			{TenantID: "t2", Err: errors.New("x")},
		},
	}
	writeSummary(&buf, result, true)
	out := buf.String()
	assert.Contains(t, out, `"event":"summary"`)
	assert.Contains(t, out, `"action":"migrate"`)
	assert.Contains(t, out, `"failed":1`)
}

func TestWriteSummaryPlainText(t *testing.T) {
	var buf bytes.Buffer
	result := &migration.MigrateAllResult{
		Action: migration.ActionValidate,
		Results: []migration.TenantResult{
			{TenantID: "t1"},
			{TenantID: "t2", Err: errors.New("kaboom")},
		},
	}
	writeSummary(&buf, result, false)
	out := buf.String()
	assert.Contains(t, out, "Validate summary")
	assert.Contains(t, out, "1 failed")
	assert.Contains(t, out, "kaboom")
}

func TestWriteSummaryNilNoOp(t *testing.T) {
	var buf bytes.Buffer
	writeSummary(&buf, nil, false)
	assert.Empty(t, buf.String())
}

func TestBuildBaseConfig(t *testing.T) {
	cfg := buildBaseConfig(&CommonFlags{
		FlywayPath:    "flyway",
		FlywayConfig:  "x.conf",
		MigrationsDir: "m",
	})
	assert.Equal(t, "flyway", cfg.FlywayPath)
	assert.Equal(t, "x.conf", cfg.ConfigPath)
	assert.Equal(t, "m", cfg.MigrationPath)
	// Timeout left zero so per-vendor default wins inside MigrateAll.
	assert.Zero(t, cfg.Timeout)
}
