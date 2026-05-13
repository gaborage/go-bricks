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
			// cobra.NoArgs surfaces as either an "unknown command" error
			// (when the subcommand has no further children) or an "accepts
			// 0 arg(s)" error from the validator. Match both, not the
			// over-broad substring "arg".
			msg := strings.ToLower(err.Error())
			matched := strings.Contains(msg, "unknown command") ||
				strings.Contains(msg, "accepts 0 arg")
			assert.Truef(t, matched, "expected NoArgs rejection, got: %s", err.Error())
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

func TestMakeHookJSONIncludesStructuredResultFields(t *testing.T) {
	// The CLI's JSON-mode hook is the contract consumers (CI pipelines) rely
	// on. Adding new fields is non-breaking, but they must show up exactly
	// when the underlying Result was populated — never on Err-only entries.
	var buf bytes.Buffer
	hook := makeHook(&buf, true)
	hook(migration.TenantResult{
		TenantID: "tenant_acme",
		Vendor:   "postgresql",
		Duration: 50 * time.Millisecond,
		Result: migration.Result{
			Success:         true,
			AppliedVersions: []string{"1", "2"},
			StartingVersion: "",
			EndingVersion:   "2",
			DurationMillis:  42,
			FlywayVersion:   "10.22.0",
		},
	})
	out := buf.String()
	assert.Contains(t, out, `"applied_versions":["1","2"]`)
	assert.Contains(t, out, `"ending_version":"2"`)
	assert.NotContains(t, out, `"starting_version"`, "empty StartingVersion should be omitted, not emitted as \"\"")
	assert.Contains(t, out, `"duration_millis":42`)
	assert.Contains(t, out, `"flyway_version":"10.22.0"`)
}

func TestMakeHookPlainTextRendersSchemaSummary(t *testing.T) {
	var buf bytes.Buffer
	hook := makeHook(&buf, false)
	// Fresh-apply: ∅ → v2 with 2 migrations.
	hook(migration.TenantResult{
		TenantID: "fresh",
		Vendor:   "postgresql",
		Duration: 10 * time.Millisecond,
		Result: migration.Result{
			AppliedVersions: []string{"1", "2"},
			EndingVersion:   "2",
		},
	})
	// No-op rerun: starting and ending both at v2, zero applied.
	hook(migration.TenantResult{
		TenantID: "noop",
		Vendor:   "postgresql",
		Duration: 1 * time.Millisecond,
		Result: migration.Result{
			StartingVersion: "2",
			EndingVersion:   "2",
		},
	})
	out := buf.String()
	assert.Contains(t, out, "schema=v∅→v2 (2 applied)")
	assert.Contains(t, out, "schema=v2 (no-op)")
}

func TestFormatSchemaSummaryFallsBackToLastAppliedWhenEndingMissing(t *testing.T) {
	// Defensive against a Flyway-output shape we didn't fully parse: when
	// AppliedVersions is populated but EndingVersion came back empty (e.g.
	// parser tolerated a missing targetSchemaVersion field), the summary
	// must still render a usable terminus rather than "v→v".
	got := formatSchemaSummary(&migration.Result{
		AppliedVersions: []string{"3", "4"},
		StartingVersion: "2",
	})
	assert.Equal(t, "schema=v2→v4 (2 applied)", got)
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
