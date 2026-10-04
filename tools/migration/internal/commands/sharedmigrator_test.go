package commands

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	stdhttp "net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gaborage/go-bricks/config"
	"github.com/gaborage/go-bricks/migration"
)

const (
	sharedSchema      = "tenant_schema"
	sharedArmedFlag   = "--shared-migrator"
	sharedUnparseable = "maybe"
	refusedHost       = "h2.db.example.com"
	typelessTenant    = "t-typeless"
)

// unsetSharedMigratorEnv makes GOBRICKS_MIGRATE_SHARED_MIGRATOR genuinely absent
// for the duration of the test.
func unsetSharedMigratorEnv(t *testing.T) {
	t.Helper()
	t.Setenv(envSharedMigrator, "")
	require.NoError(t, os.Unsetenv(envSharedMigrator))
}

// tenantSecret renders a canonical secret whose username, and so password, is
// derived from host: a leak of any credential field then shows up as the host.
func tenantSecret(t *testing.T, dbType, host, schema string) string {
	t.Helper()
	username := "user-" + host
	doc := map[string]any{
		"type": dbType, "host": host, "port": 5432, "database": "d",
		"username": username, "password": fakePassword(username),
	}
	if dbType == config.Oracle {
		doc["port"] = 1521
	}
	if schema != "" {
		doc["postgresql"] = map[string]string{"schema": schema}
	}
	raw, err := json.Marshal(doc)
	require.NoError(t, err)
	return string(raw)
}

// fleetRun is what one driven command leaves behind: the environment the stub
// Flyway saw as parsed (last value wins) and as recorded across every run, how
// often the stub ran, everything the command wrote, its error, and how many times
// the control plane was asked to list tenants.
type fleetRun struct {
	env         map[string]string
	envDump     string
	output      string
	err         error
	listHits    int64
	invocations int
}

// tenantResult is one tenant_complete record of a --json run.
type tenantResult struct {
	status string
	err    string
}

// stubInvocations counts how often the capturing stub ran: each run records its
// argv one argument per line, and the operation verb appears exactly once in it.
func stubInvocations(argvDump, operation string) int {
	n := 0
	for _, line := range strings.Split(argvDump, "\n") {
		if line == operation {
			n++
		}
	}
	return n
}

// runFleet drives cmd against a control plane listing the secrets' tenant ids in
// sorted order, each served its secret by a fake Secrets Manager.
func runFleet(t *testing.T, cmd *cobra.Command, secrets map[string]string, extraArgs ...string) fleetRun {
	t.Helper()

	ids := make([]string, 0, len(secrets))
	payloads := make(map[string]string, len(secrets))
	for id, payload := range secrets {
		ids = append(ids, id)
		payloads[secretName(id)] = payload
	}
	slices.Sort(ids)
	tenants := make([]map[string]string, 0, len(ids))
	for _, id := range ids {
		tenants = append(tenants, map[string]string{"id": id})
	}

	var listHits atomic.Int64
	listSrv := httptest.NewServer(stdhttp.HandlerFunc(func(w stdhttp.ResponseWriter, _ *stdhttp.Request) {
		listHits.Add(1)
		writeEnvelope(w, map[string]any{"tenants": tenants, "next_cursor": ""})
	}))
	defer listSrv.Close()
	smSrv := fakeSecretsManager(t, payloads)
	defer smSrv.Close()
	setFakeAWSEnv(t)

	run := executeFleet(t, cmd, append([]string{
		"--source-url", listSrv.URL, "--allow-insecure-scheme",
		"--aws-endpoint", smSrv.URL, "--aws-region", "us-east-1",
	}, extraArgs...))
	run.listHits = listHits.Load()
	return run
}

// runSharedFileFleet drives migrate with both the listing and the credentials
// read from a tenants.yaml, the only source that lets a type-less tenant through.
func runSharedFileFleet(t *testing.T, tenantsYAML string, extraArgs ...string) fleetRun {
	t.Helper()
	return executeFleet(t, NewMigrateCommand(), append([]string{
		"--source-config", writeTenantStoreYAMLContent(t, tenantsYAML),
		"--credentials-from", credsSourceFile,
	}, extraArgs...))
}

// executeFleet runs cmd with args against a capturing Flyway stub for cmd's own
// operation and collects what the run left behind.
func executeFleet(t *testing.T, cmd *cobra.Command, args []string) fleetRun {
	t.Helper()
	stub, argvPath, envPath := stubFlywayCapturing(t, cmd.Name())
	cmd.SetArgs(append(args,
		"--flyway-path", stub, "--flyway-config", flywayConfPath(t),
		"--migrations-dir", makeTempDir(t),
	))
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetContext(t.Context())

	var err error
	logged := captureStdout(t, func() { err = cmd.Execute() })

	envDump := readCaptured(t, envPath)
	return fleetRun{
		env:         parseEnvDump(envDump),
		envDump:     envDump,
		output:      out.String() + logged,
		err:         err,
		invocations: stubInvocations(readCaptured(t, argvPath), cmd.Name()),
	}
}

// tenantResults maps each tenant_complete record of a --json run to its result.
func tenantResults(t *testing.T, out string) map[string]tenantResult {
	t.Helper()
	got := map[string]tenantResult{}
	for _, line := range strings.Split(out, "\n") {
		if !strings.HasPrefix(line, `{"`) || !strings.Contains(line, `"tenant_complete"`) {
			continue
		}
		var rec struct {
			TenantID string `json:"tenant_id"`
			Status   string `json:"status"`
			Error    string `json:"error"`
		}
		require.NoError(t, json.Unmarshal([]byte(line), &rec))
		got[rec.TenantID] = tenantResult{status: rec.Status, err: rec.Error}
	}
	return got
}

func requireFleetSplit(t *testing.T, err error) {
	t.Helper()
	require.ErrorIs(t, err, migration.ErrFleetSplit)
	assert.Equal(t, ExitFleetSplit, ExitCode(err))
}

func TestSharedMigratorRefusesEmptySchemaOnEveryAction(t *testing.T) {
	tests := []struct {
		name   string
		newCmd func() *cobra.Command
	}{
		{name: "migrate", newCmd: NewMigrateCommand},
		{name: "validate", newCmd: NewValidateCommand},
		{name: "info", newCmd: NewInfoCommand},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			unsetSharedMigratorEnv(t)
			run := runFleet(t, tt.newCmd(), map[string]string{
				"t1": tenantSecret(t, config.PostgreSQL, "h1.db.example.com", ""),
			}, sharedArmedFlag)

			require.ErrorIs(t, run.err, migration.ErrSharedMigratorSchemaRequired)
			requireFleetSplit(t, run.err)
			assert.Zero(t, run.invocations, "Flyway must never run for a tenant with no schema target")
		})
	}
}

func TestSharedMigratorRefusesUnsupportedTypeBeforeFlyway(t *testing.T) {
	tests := []struct {
		name   string
		tenant string
		dbType string
	}{
		{name: "lowercase_alias", tenant: "t-alias", dbType: "postgres"},
		{name: "mixed_case", tenant: "t-mixed-case", dbType: "PostgreSQL"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			unsetSharedMigratorEnv(t)
			run := runFleet(t, NewMigrateCommand(), map[string]string{
				tt.tenant: tenantSecret(t, tt.dbType, refusedHost, sharedSchema),
			}, sharedArmedFlag)

			requireFleetSplit(t, run.err)
			assert.Zero(t, run.invocations, "Flyway must never run for an unsupported type")
			assertTypeRefusal(t, &run, tt.tenant, tt.dbType)
		})
	}

	t.Run("empty_type_from_config_file", func(t *testing.T) {
		unsetSharedMigratorEnv(t)
		run := runSharedFileFleet(t, typelessTenantYAML(refusedHost), sharedArmedFlag)

		requireFleetSplit(t, run.err)
		assert.Zero(t, run.invocations, "Flyway must never run for a tenant with no type")
		assertTypeRefusal(t, &run, typelessTenant, "")
	})
}

// typelessTenantYAML renders a tenants.yaml with one tenant carrying no type and
// no connectionstring to infer one from.
func typelessTenantYAML(host string) string {
	return `
multitenant:
  enabled: true
  tenants:
    ` + typelessTenant + `:
      database:
        host: ` + host + `
        port: 5432
        database: d
        username: user-` + host + `
        password: ` + fakePassword("user-"+host) + `
        postgresql:
          schema: ` + sharedSchema + `
`
}

// assertTypeRefusal pins that the refusal names the tenant and the type and
// echoes no credential field. refusedHost is disjoint from the tenant id and the
// type, and the username and password both embed it.
func assertTypeRefusal(t *testing.T, run *fleetRun, tenant, dbType string) {
	t.Helper()
	msg := run.err.Error()
	assert.Contains(t, msg, fmt.Sprintf("tenant %q", tenant))
	assert.Contains(t, msg, fmt.Sprintf("unsupported database type: %q", dbType))
	for _, leak := range []string{refusedHost, fakePassword("user-" + refusedHost)} {
		assert.NotContains(t, msg, leak)
		assert.NotContains(t, run.output, leak)
	}
}

func TestSharedMigratorAcceptsTypeInferredFromConnectionString(t *testing.T) {
	unsetSharedMigratorEnv(t)
	run := runSharedFileFleet(t, `
multitenant:
  enabled: true
  tenants:
    t1:
      database:
        connectionstring: postgres://u@h3.db.example.com:5432/d
        postgresql:
          schema: `+sharedSchema+`
`, sharedArmedFlag)

	require.NoError(t, run.err, run.output)
	assert.Equal(t, 1, run.invocations)
}

func TestSharedMigratorRunsOracleAndSchemaTargetedTenants(t *testing.T) {
	unsetSharedMigratorEnv(t)
	run := runFleet(t, NewMigrateCommand(), map[string]string{
		"t-oracle":   tenantSecret(t, config.Oracle, "h4.db.example.com", ""),
		"t-postgres": tenantSecret(t, config.PostgreSQL, "h5.db.example.com", sharedSchema),
	}, sharedArmedFlag)

	require.NoError(t, run.err, run.output)
	assert.Equal(t, 2, run.invocations)
	assert.Contains(t, run.envDump, "DB_HOST=h5.db.example.com")
	assert.Contains(t, run.output, sharedMigratorArmedLogMsg)
}

func TestSharedMigratorFailFastStopsAtFirstRefusal(t *testing.T) {
	unsetSharedMigratorEnv(t)
	run := runFleet(t, NewMigrateCommand(), map[string]string{
		"a-empty-schema": tenantSecret(t, config.PostgreSQL, "h14.db.example.com", ""),
		"b-wrong-type":   tenantSecret(t, "postgres", "h15.db.example.com", sharedSchema),
		"c-postgres":     tenantSecret(t, config.PostgreSQL, "h16.db.example.com", sharedSchema),
	}, sharedArmedFlag, "--json")

	require.ErrorIs(t, run.err, migration.ErrSharedMigratorSchemaRequired)
	requireFleetSplit(t, run.err)
	assert.Zero(t, run.invocations, "no tenant after the first refusal reaches Flyway")

	results := tenantResults(t, run.output)
	require.Len(t, results, 1, run.output)
	assert.Equal(t, "fail", results["a-empty-schema"].status)
	assert.NotContains(t, run.output, `"b-wrong-type"`, "the second refusable tenant is never dispatched")
	assert.NotContains(t, run.output, `unsupported database type: "postgres"`)

	rec := requireSummary(t, run.output)
	assert.Equal(t, 1, rec.Attempted)
	assert.Equal(t, 1, rec.Failed)
	assert.Equal(t, 2, rec.NotAttempted)
}

func TestSharedMigratorContinueOnErrorReportsEveryRefusal(t *testing.T) {
	unsetSharedMigratorEnv(t)
	run := runFleet(t, NewMigrateCommand(), map[string]string{
		"a-oracle":       tenantSecret(t, config.Oracle, "h6.db.example.com", ""),
		"b-empty-schema": tenantSecret(t, config.PostgreSQL, "h7.db.example.com", ""),
		"c-wrong-type":   tenantSecret(t, "postgres", "h8.db.example.com", sharedSchema),
		"d-postgres":     tenantSecret(t, config.PostgreSQL, "h9.db.example.com", sharedSchema),
	}, sharedArmedFlag, "--continue-on-error", "--json")

	requireFleetSplit(t, run.err)
	assert.Equal(t, 2, run.invocations, "the valid tenants still run")
	assert.NotContains(t, run.envDump, "h7.db.example.com")
	assert.NotContains(t, run.envDump, "h8.db.example.com")

	results := tenantResults(t, run.output)
	assert.Equal(t, "ok", results["a-oracle"].status)
	assert.Equal(t, "ok", results["d-postgres"].status)
	assert.Equal(t, "fail", results["b-empty-schema"].status)
	assert.Contains(t, results["b-empty-schema"].err, migration.ErrSharedMigratorSchemaRequired.Error())
	assert.Equal(t, "fail", results["c-wrong-type"].status)
	assert.Contains(t, results["c-wrong-type"].err, `unsupported database type: "postgres"`)

	rec := requireSummary(t, run.output)
	assert.Equal(t, 4, rec.Attempted)
	assert.Equal(t, 2, rec.Failed)
}

func TestSharedMigratorEnvFallback(t *testing.T) {
	emptySchema := func(t *testing.T) map[string]string {
		return map[string]string{"t1": tenantSecret(t, config.PostgreSQL, "h10.db.example.com", "")}
	}

	t.Run("env_true_arms_the_guard", func(t *testing.T) {
		t.Setenv(envSharedMigrator, "true")
		run := runFleet(t, NewMigrateCommand(), emptySchema(t))

		require.ErrorIs(t, run.err, migration.ErrSharedMigratorSchemaRequired)
		requireFleetSplit(t, run.err)
		assert.Zero(t, run.invocations)
	})

	t.Run("explicit_false_overrides_env_true", func(t *testing.T) {
		t.Setenv(envSharedMigrator, "true")
		run := runFleet(t, NewMigrateCommand(), emptySchema(t), sharedArmedFlag+"=false")

		require.NoError(t, run.err, run.output)
		assert.Equal(t, 1, run.invocations)
		assert.NotContains(t, run.output, sharedMigratorArmedLogMsg)
	})

	t.Run("unparseable_env_is_nothing_attempted", func(t *testing.T) {
		t.Setenv(envSharedMigrator, sharedUnparseable)
		run := runFleet(t, NewMigrateCommand(), emptySchema(t))

		require.ErrorIs(t, run.err, migration.ErrNothingAttempted)
		assert.Equal(t, ExitNothingAttempted, ExitCode(run.err))
		assert.Contains(t, run.err.Error(), envSharedMigrator)
		assert.Zero(t, run.listHits, "the run must fail before the control plane is asked for tenants")
		assert.Zero(t, run.invocations)
	})
}

// With neither the flag nor the env var, neither refusal exists: both tenants
// reach Flyway exactly as they did before the guard was added.
func TestSharedMigratorUnarmedKeepsTodaysBehavior(t *testing.T) {
	unsetSharedMigratorEnv(t)
	run := runFleet(t, NewMigrateCommand(), map[string]string{
		"t-empty-schema": tenantSecret(t, config.PostgreSQL, "h11.db.example.com", ""),
		"t-wrong-type":   tenantSecret(t, "postgres", "h12.db.example.com", sharedSchema),
	}, "--continue-on-error")

	require.NoError(t, run.err, run.output)
	assert.Equal(t, 2, run.invocations)
	assert.NotContains(t, run.output, migration.ErrSharedMigratorSchemaRequired.Error())
	assert.NotContains(t, run.output, "unsupported database type")
	assert.NotContains(t, run.output, sharedMigratorArmedLogMsg)

	fileRun := runSharedFileFleet(t, typelessTenantYAML("h13.db.example.com"))
	require.NoError(t, fileRun.err, fileRun.output)
	assert.Equal(t, 1, fileRun.invocations, "a type-less tenant still reaches Flyway unarmed")
	assert.NotContains(t, fileRun.output, "unsupported database type")
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
		{name: "flag_never_reads_unparseable_env", env: new(sharedUnparseable), args: []string{sharedArmedFlag}, want: true},
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
