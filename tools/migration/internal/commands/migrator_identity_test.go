package commands

import (
	"bytes"
	"context"
	"io"
	stdhttp "net/http"
	"net/http/httptest"
	"os"
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

// unsetMigratorEnv clears both migrator-identity variables for the duration of
// the test. t.Setenv registers the restore; os.Unsetenv then makes the variable
// genuinely absent, which t.Setenv alone cannot express.
func unsetMigratorEnv(t *testing.T) {
	t.Helper()
	for _, k := range []string{envMigratorUser, envMigratorPassword} {
		t.Setenv(k, "")
		require.NoError(t, os.Unsetenv(k))
	}
}

// setMigratorEnv exports a full migrator identity.
func setMigratorEnv(t *testing.T) {
	t.Helper()
	unsetMigratorEnv(t)
	t.Setenv(envMigratorUser, identityMigrator)
	t.Setenv(envMigratorPassword, fakePassword(identityMigrator))
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
		b, _ := io.ReadAll(r)
		drained <- string(b)
	}()

	defer func() {
		os.Stdout = orig
		require.NoError(t, r.Close())
	}()
	fn()
	require.NoError(t, w.Close())
	return <-drained
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

	stub, _, envPath := stubFlywayCapturing(t, operation)
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
		env:      readCapturedEnv(t, envPath),
		output:   out.String() + logged,
		err:      err,
		listHits: listHits.Load(),
	}
}

func TestResolveMigratorIdentityEnvPairs(t *testing.T) {
	password := fakePassword(identityMigrator)

	tests := []struct {
		name string
		// env is the environment as presence, not as values: a key absent from the
		// map is an unset variable, mirroring the os.LookupEnv semantics under test.
		env          map[string]string
		wantIdentity *migration.MigratorIdentity
		wantErr      string
	}{
		{
			name:         "both_unset",
			wantIdentity: nil,
		},
		{
			name:         "both_set",
			env:          map[string]string{envMigratorUser: identityMigrator, envMigratorPassword: password},
			wantIdentity: &migration.MigratorIdentity{Username: identityMigrator, Password: password},
		},
		{
			name: "only_user_set",
			env:  map[string]string{envMigratorUser: identityMigrator},
			wantErr: envMigratorPassword + " is required when " + envMigratorUser +
				" is set; set both or neither",
		},
		{
			name: "only_password_set",
			env:  map[string]string{envMigratorPassword: password},
			wantErr: envMigratorUser + " is required when " + envMigratorPassword +
				" is set; set both or neither",
		},
		{
			// Presence, not emptiness, pairs the two: a set-but-empty value must
			// reach MigrateAll so it fails with ErrInvalidMigratorIdentity rather
			// than silently running as the tenant's runtime role.
			name:         "user_set_empty_password_set",
			env:          map[string]string{envMigratorUser: "", envMigratorPassword: password},
			wantIdentity: &migration.MigratorIdentity{Username: "", Password: password},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			unsetMigratorEnv(t)
			for k, v := range tt.env {
				t.Setenv(k, v)
			}

			flags := addCommonFlags(&cobra.Command{})
			err := resolveMigratorIdentity(flags)

			if tt.wantErr != "" {
				// Exact, not Contains: both names appear in either message, so only
				// the whole string tells the two branches apart.
				require.EqualError(t, err, tt.wantErr)
				assert.Nil(t, flags.migratorIdentity)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.wantIdentity, flags.migratorIdentity)
		})
	}
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
			setMigratorEnv(t)

			run := runIdentityCommand(t, tt.newCmd(), tt.operation)
			require.NoError(t, run.err)

			assert.Equal(t, identityMigrator, run.env["DB_USER"], "Flyway must connect as the migrator, not the tenant's runtime role")
			assert.Equal(t, fakePassword(identityMigrator), run.env["DB_PASSWORD"])
			assert.Equal(t, identityHost, run.env["DB_HOST"], "host targeting stays the tenant's")
			assert.Equal(t, identityDatabase, run.env["DB_NAME"], "database targeting stays the tenant's")
		})
	}
}

func TestMigrateCommandWithoutMigratorIdentityUsesTenantCredentials(t *testing.T) {
	unsetMigratorEnv(t)

	run := runIdentityCommand(t, NewMigrateCommand(), "migrate")
	require.NoError(t, run.err)

	assert.Equal(t, identityRuntime, run.env["DB_USER"], "with no overlay Flyway keeps the secret's own username")
	assert.Equal(t, fakePassword(identityRuntime), run.env["DB_PASSWORD"])
	assert.NotContains(t, run.output, "Migrator identity overlay active")
}

func TestMigrateCommandNeverPrintsMigratorPassword(t *testing.T) {
	for _, asJSON := range []bool{false, true} {
		name := "text_summary"
		if asJSON {
			name = "json_records"
		}
		t.Run(name, func(t *testing.T) {
			setMigratorEnv(t)

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
			assert.NotContains(t, run.output, fakePassword(identityMigrator))
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
		{name: "only_password_set", envVar: envMigratorPassword, value: fakePassword(identityMigrator), missing: envMigratorUser},
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
			if tt.envVar == envMigratorPassword {
				// Only meaningful in this arm: the other never put a password in the
				// environment, so its absence from the output would prove nothing.
				assert.NotContains(t, run.output, tt.value)
			}
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

// TestQuiesceIgnoresMigratorIdentity pins the scope boundary. quiesce shares
// resolveFlags with the action subcommands but opens its control plane with the
// tenant secret's own credentials, so it neither applies the overlay nor refuses
// a half-set pair. Whether quiesce should connect as the migrator is open
// (its CreateTable is DDL); until that is decided it must not fail on a
// credential it never reads.
func TestQuiesceIgnoresMigratorIdentity(t *testing.T) {
	mem := migration.NewMemoryQuiesceController()
	orig := controllerOpener
	controllerOpener = func(context.Context, *CommonFlags, string) (migration.QuiesceController, func(), error) {
		return mem, func() {}, nil
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

	require.NoError(t, cmd.Execute(), "a half-set identity must not stop a path that never uses it")
}
