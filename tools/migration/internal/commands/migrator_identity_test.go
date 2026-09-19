package commands

import (
	"bytes"
	"context"
	"io"
	stdhttp "net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gaborage/go-bricks/migration"
)

const (
	identityTenant   = "t1"
	identityHost     = "tenant-a.db.example.com"
	identityDatabase = "tenant_a"
	identityRuntime  = "tenant_a_app"
	identityMigrator = "fleet_migrator"
)

// fakeMigratorPassword composes the migrator's synthetic password. Composed
// rather than written as a literal for the same reason as fakePassword, and long
// enough to clear redactPassword's floor.
func fakeMigratorPassword() string {
	return "not-a-real-" + identityMigrator + "-password"
}

// stubFlywayCapturingEnv writes a shell script that records its environment to
// capturePath and emits a success envelope for the given operation. Credentials
// reach Flyway by environment only, so the environment is where the overlay is
// observable. Skips on Windows.
func stubFlywayCapturingEnv(t *testing.T, operation string) (stubPath, capturePath string) {
	t.Helper()
	if runtime.GOOS == windowsOS {
		t.Skip("shell script stub not supported on windows CI")
	}
	dir := t.TempDir()
	capturePath = filepath.Join(dir, "env.txt")
	envelope := `{"operation":"` + operation + `","success":true,"targetSchemaVersion":"2","flywayVersion":"12.8.1"}`
	script := "#!/bin/sh\nenv >> \"" + capturePath + "\"\necho '" + envelope + "'\nexit 0\n"
	stubPath = filepath.Join(dir, "flyway-env-capture.sh")
	require.NoError(t, os.WriteFile(stubPath, []byte(script), 0o755))
	return stubPath, capturePath
}

// readCapturedEnv parses the KEY=VALUE dump the stub recorded. An absent file
// means the stub never ran, which is itself an assertable outcome.
func readCapturedEnv(t *testing.T, capturePath string) map[string]string {
	t.Helper()
	raw, err := os.ReadFile(capturePath)
	if os.IsNotExist(err) {
		return map[string]string{}
	}
	require.NoError(t, err)
	env := map[string]string{}
	for _, line := range strings.Split(string(raw), "\n") {
		if k, v, ok := strings.Cut(line, "="); ok {
			env[k] = v
		}
	}
	return env
}

// identityRun is what one driven command leaves behind: the environment the stub
// Flyway saw (empty when it never ran), everything the command wrote, its error,
// and how many times the control plane was asked to list tenants.
type identityRun struct {
	env      map[string]string
	output   string
	err      error
	listHits int64
}

// captureStdout redirects os.Stdout — where logger.New writes — for the duration
// of fn, so the run's log lines can be grepped alongside the command's own output.
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	orig := os.Stdout
	r, w, err := os.Pipe()
	require.NoError(t, err)
	os.Stdout = w

	drained := make(chan string, 1)
	go func() {
		var buf bytes.Buffer
		_, _ = io.Copy(&buf, r)
		drained <- buf.String()
	}()

	defer func() {
		os.Stdout = orig
		require.NoError(t, r.Close())
	}()
	fn()
	require.NoError(t, w.Close())
	return <-drained
}

// runIdentityCommand drives cmd against a one-tenant control plane and a fake
// Secrets Manager serving that tenant's runtime credentials. extraArgs are
// appended to the fixture's own flags.
func runIdentityCommand(t *testing.T, cmd *cobra.Command, operation string, extraArgs ...string) identityRun {
	t.Helper()

	var listHits atomic.Int64
	listSrv := httptest.NewServer(stdhttp.HandlerFunc(func(w stdhttp.ResponseWriter, _ *stdhttp.Request) {
		listHits.Add(1)
		writeEnvelope(w, map[string]any{
			"tenants":     []map[string]string{{"id": identityTenant}},
			"next_cursor": "",
		})
	}))
	defer listSrv.Close()

	smSrv := fakeSecretsManager(t, map[string]string{
		secretName(identityTenant): canonicalTenantSecret(identityHost, identityDatabase, identityRuntime),
	})
	defer smSrv.Close()

	t.Setenv("AWS_ACCESS_KEY_ID", "test")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "test")
	t.Setenv("AWS_REGION", "us-east-1")

	stub, capture := stubFlywayCapturingEnv(t, operation)
	cmd.SetArgs(append([]string{
		"--source-url", listSrv.URL, "--allow-insecure-scheme",
		"--aws-endpoint", smSrv.URL, "--aws-region", "us-east-1",
		"--flyway-path", stub, "--flyway-config", flywayConfPath(t),
		"--migrations-dir", makeTempDir(t),
	}, extraArgs...))
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetContext(t.Context())

	var err error
	logged := captureStdout(t, func() { err = cmd.Execute() })

	return identityRun{
		env:      readCapturedEnv(t, capture),
		output:   out.String() + logged,
		err:      err,
		listHits: listHits.Load(),
	}
}

func TestMigrateCommandAppliesMigratorIdentityToFlywayEnv(t *testing.T) {
	unsetMigratorEnv(t)
	t.Setenv(envMigratorUser, identityMigrator)
	t.Setenv(envMigratorPassword, fakeMigratorPassword())

	run := runIdentityCommand(t, NewMigrateCommand(), "migrate")
	require.NoError(t, run.err)

	assert.Equal(t, identityMigrator, run.env["DB_USER"], "Flyway must connect as the migrator, not the tenant's runtime role")
	assert.Equal(t, fakeMigratorPassword(), run.env["DB_PASSWORD"])
	assert.Equal(t, identityHost, run.env["DB_HOST"], "host targeting stays the tenant's")
	assert.Equal(t, identityDatabase, run.env["DB_NAME"], "database targeting stays the tenant's")
}

func TestMigrateCommandWithoutMigratorIdentityUsesTenantCredentials(t *testing.T) {
	unsetMigratorEnv(t)

	run := runIdentityCommand(t, NewMigrateCommand(), "migrate")
	require.NoError(t, run.err)

	assert.Equal(t, identityRuntime, run.env["DB_USER"], "with no overlay Flyway keeps the secret's own username")
	assert.Equal(t, fakePassword(identityRuntime), run.env["DB_PASSWORD"])
	assert.NotContains(t, run.output, "Migrator identity overlay active")
}

func TestMigratorIdentityAppliesToEveryAction(t *testing.T) {
	tests := []struct {
		name      string
		operation string
		newCmd    func() *cobra.Command
	}{
		{name: "migrate", operation: "migrate", newCmd: NewMigrateCommand},
		{name: "validate", operation: "validate", newCmd: NewValidateCommand},
		{name: "info", operation: "info", newCmd: NewInfoCommand},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			unsetMigratorEnv(t)
			t.Setenv(envMigratorUser, identityMigrator)
			t.Setenv(envMigratorPassword, fakeMigratorPassword())

			run := runIdentityCommand(t, tt.newCmd(), tt.operation)
			require.NoError(t, run.err)

			assert.Equal(t, identityMigrator, run.env["DB_USER"])
			assert.Equal(t, fakeMigratorPassword(), run.env["DB_PASSWORD"])
		})
	}
}

func TestMigrateCommandNeverPrintsMigratorPassword(t *testing.T) {
	for _, asJSON := range []bool{false, true} {
		name := "text_summary"
		if asJSON {
			name = "json_records"
		}
		t.Run(name, func(t *testing.T) {
			unsetMigratorEnv(t)
			t.Setenv(envMigratorUser, identityMigrator)
			t.Setenv(envMigratorPassword, fakeMigratorPassword())

			var extra []string
			if asJSON {
				extra = append(extra, "--json")
			}
			run := runIdentityCommand(t, NewMigrateCommand(), "migrate", extra...)
			require.NoError(t, run.err)

			if asJSON {
				require.Contains(t, run.output, `"event":"tenant_complete"`, "--json must have produced a record to grep")
			}

			// Positive control: the overlay ran and its username was logged, so an
			// absent password is silence about the password, not silence about the run.
			assert.Contains(t, run.output, identityMigrator)
			assert.NotContains(t, run.output, fakeMigratorPassword())
		})
	}
}

func TestMigrateCommandRejectsPartialMigratorIdentityBeforeListing(t *testing.T) {
	tests := []struct {
		name    string
		envVar  string
		value   string
		missing string
	}{
		{name: "only_user_set", envVar: envMigratorUser, value: identityMigrator, missing: envMigratorPassword},
		{name: "only_password_set", envVar: envMigratorPassword, value: fakeMigratorPassword(), missing: envMigratorUser},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			unsetMigratorEnv(t)
			t.Setenv(tt.envVar, tt.value)

			run := runIdentityCommand(t, NewMigrateCommand(), "migrate")

			require.Error(t, run.err)
			assert.Contains(t, run.err.Error(), tt.missing)
			assert.Zero(t, run.listHits, "the run must fail before the control plane is asked for tenants")
			assert.Empty(t, run.env, "Flyway must never be invoked")
			assert.NotContains(t, run.output, fakeMigratorPassword())
		})
	}
}

func TestMigrateCommandRejectsInvalidMigratorIdentityBeforeListing(t *testing.T) {
	unsetMigratorEnv(t)
	t.Setenv(envMigratorUser, identityMigrator)
	t.Setenv(envMigratorPassword, "short")

	run := runIdentityCommand(t, NewMigrateCommand(), "migrate")

	require.ErrorIs(t, run.err, migration.ErrInvalidMigratorIdentity)
	assert.Zero(t, run.listHits, "an invalid identity must fail before the control plane is asked for tenants")
	assert.Empty(t, run.env, "Flyway must never be invoked")
}

// TestQuiesceRejectsPartialMigratorIdentity pins the blast radius of the
// both-or-neither check. It lives in resolveFlags, which `quiesce` also calls, so
// a half-set pair stops quiesce too — before the control plane is opened. The
// `list` subcommand deliberately bypasses resolveFlags and is unaffected.
func TestQuiesceRejectsPartialMigratorIdentity(t *testing.T) {
	opened := false
	orig := controllerOpener
	controllerOpener = func(context.Context, *CommonFlags, string) (migration.QuiesceController, func(), error) {
		opened = true
		return migration.NewMemoryQuiesceController(), func() {}, nil
	}
	t.Cleanup(func() { controllerOpener = orig })

	unsetMigratorEnv(t)
	t.Setenv(envMigratorUser, identityMigrator)

	cmd := NewQuiesceCommand()
	cmd.SetArgs([]string{"status", "--tenant", "cp"})
	cmd.SetContext(t.Context())
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)

	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), envMigratorPassword)
	assert.False(t, opened, "the control plane must not be opened on a half-set identity")
}
