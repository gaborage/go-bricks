package commands

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	stdhttp "net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gaborage/go-bricks/migration"
)

const windowsOS = "windows"

// secretName composes the Secrets Manager key the CLI looks up for a tenant.
// Derived from the same constant that backs the --secrets-prefix default, so a
// change to the prefix moves the fixtures with the command instead of silently
// desynchronising them.
func secretName(tenantID string) string {
	return migration.DefaultSecretsPrefix + tenantID
}

// fakePassword composes a synthetic tenant password. Composing it, rather than
// writing a literal next to a "password" key, keeps a credential-shaped string
// out of this source for secret scanners while clearing the 8-character floor
// under which redactPassword suppresses Flyway output wholesale.
func fakePassword(tenant string) string {
	return "not-a-real-" + tenant + "-password"
}

// canonicalTenantSecret renders the canonical go-bricks DatabaseConfig secret
// payload the fake Secrets Manager serves for one tenant.
func canonicalTenantSecret(host, database, username string) string {
	return fmt.Sprintf(`{"type":"postgresql","host":%q,"port":5432,"database":%q,"username":%q,"password":%q}`,
		host, database, username, fakePassword(username))
}

// rdsTenantSecret renders the AWS-managed RDS rotation payload shape — the
// fallback SecretsProvider parses when the canonical keys are absent.
func rdsTenantSecret(host, dbname, username string) string {
	return fmt.Sprintf(`{"engine":"postgres","host":%q,"port":5432,"dbname":%q,"username":%q,"password":%q}`,
		host, dbname, username, fakePassword(username))
}

// writeEnvelope writes a go-bricks-style APIResponse envelope (status 200) to w.
func writeEnvelope(w stdhttp.ResponseWriter, data map[string]any) {
	body := map[string]any{"meta": map[string]any{}}
	if data != nil {
		body["data"] = data
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(body)
}

// stubFlyway writes a shell script that always exits 0 to a temp file and
// returns its path. Skips on Windows.
func stubFlyway(t *testing.T) string {
	t.Helper()
	if runtime.GOOS == windowsOS {
		t.Skip("shell script stub not supported on windows CI")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "flyway-stub.sh")
	// Emit a parseable migrate success envelope: the migration engine now treats
	// empty/unparseable output from a zero-exit run as a failure (#673), so a
	// bare `exit 0` no longer represents a successful migration.
	const migrateJSON = `{"operation":"migrate","success":true,"targetSchemaVersion":"2","flywayVersion":"12.8.1"}`
	require.NoError(t, os.WriteFile(path, []byte("#!/bin/sh\necho '"+migrateJSON+"'\nexit 0\n"), 0o755))
	return path
}

// stubFlywayFailing writes a script that exits 1 (always failing).
func stubFlywayFailing(t *testing.T) string {
	t.Helper()
	if runtime.GOOS == windowsOS {
		t.Skip("shell script stub not supported on windows CI")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "flyway-fail.sh")
	require.NoError(t, os.WriteFile(path, []byte("#!/bin/sh\necho stub-failure 1>&2\nexit 1\n"), 0o755))
	return path
}

// fakeSecretsManager spins up a fake AWS SM endpoint that returns the supplied
// secret-name → JSON-payload map. It serves the /secretsmanager.GetSecretValue
// JSON-1.1 protocol that the AWS SDK uses.
func fakeSecretsManager(t *testing.T, secrets map[string]string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(stdhttp.HandlerFunc(func(w stdhttp.ResponseWriter, r *stdhttp.Request) {
		var req struct {
			SecretID string `json:"SecretId"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			stdhttp.Error(w, err.Error(), stdhttp.StatusBadRequest)
			return
		}
		payload, ok := secrets[req.SecretID]
		if !ok {
			w.Header().Set("Content-Type", "application/x-amz-json-1.1")
			w.WriteHeader(stdhttp.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(map[string]string{
				"__type":  "ResourceNotFoundException",
				"message": "secret not found: " + req.SecretID,
			})
			return
		}
		w.Header().Set("Content-Type", "application/x-amz-json-1.1")
		w.WriteHeader(stdhttp.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"ARN":          "arn:aws:secretsmanager:us-east-1:000:secret:" + req.SecretID,
			"Name":         req.SecretID,
			"SecretString": payload,
			"VersionId":    "v1",
		})
	}))
	return srv
}

func TestMigrateCommandSuccessAcrossPagesAndShapes(t *testing.T) {
	flyway := stubFlyway(t)

	listSrv := httptest.NewServer(stdhttp.HandlerFunc(func(w stdhttp.ResponseWriter, r *stdhttp.Request) {
		switch r.URL.Query().Get("cursor") {
		case "":
			writeEnvelope(w, map[string]any{
				"tenants":     []map[string]string{{"id": "t1"}, {"id": "t2"}},
				"next_cursor": "p2",
			})
		default:
			writeEnvelope(w, map[string]any{
				"tenants":     []map[string]string{{"id": "t3"}},
				"next_cursor": "",
			})
		}
	}))
	defer listSrv.Close()

	smSrv := fakeSecretsManager(t, map[string]string{
		// Canonical shape
		secretName("t1"): canonicalTenantSecret("h1", "d1", "u1"),
		secretName("t2"): canonicalTenantSecret("h2", "d2", "u2"),
		// RDS rotation fallback
		secretName("t3"): rdsTenantSecret("h3", "d3", "u3"),
	})
	defer smSrv.Close()

	setFakeAWSEnv(t)

	cmd := NewMigrateCommand()
	cmd.SetArgs([]string{
		"--source-url", listSrv.URL,
		"--allow-insecure-scheme",
		"--aws-endpoint", smSrv.URL,
		"--aws-region", "us-east-1",
		"--flyway-path", flyway,
		"--flyway-config", flywayConfPath(t),
		"--migrations-dir", makeTempDir(t),
	})
	var stdout bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetContext(context.Background())

	require.NoError(t, cmd.Execute())
	out := stdout.String()
	assert.Contains(t, out, "t1")
	assert.Contains(t, out, "t2")
	assert.Contains(t, out, "t3")
	assert.Contains(t, out, "ok")
	assert.Contains(t, out, "summary")
}

func TestMigrateCommandFailFastStopsAfterFirstFailure(t *testing.T) {
	flyway := stubFlywayFailing(t)

	listSrv := httptest.NewServer(stdhttp.HandlerFunc(func(w stdhttp.ResponseWriter, _ *stdhttp.Request) {
		writeEnvelope(w, map[string]any{
			"tenants":     []map[string]string{{"id": "t1"}, {"id": "t2"}, {"id": "t3"}},
			"next_cursor": "",
		})
	}))
	defer listSrv.Close()

	smSrv := fakeSecretsManager(t, map[string]string{
		secretName("t1"): canonicalTenantSecret("h", "d", "u"),
		secretName("t2"): canonicalTenantSecret("h", "d", "u"),
		secretName("t3"): canonicalTenantSecret("h", "d", "u"),
	})
	defer smSrv.Close()

	setFakeAWSEnv(t)

	cmd := NewMigrateCommand()
	cmd.SetArgs([]string{
		"--source-url", listSrv.URL,
		"--allow-insecure-scheme",
		"--aws-endpoint", smSrv.URL,
		"--aws-region", "us-east-1",
		"--flyway-path", flyway,
		"--flyway-config", flywayConfPath(t),
		"--migrations-dir", makeTempDir(t),
	})
	var stdout bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetContext(context.Background())

	err := cmd.Execute()
	require.Error(t, err)

	// Default fail-fast should stop after t1 — t2/t3 should NOT appear in output.
	out := stdout.String()
	assert.Contains(t, out, "t1")
	assert.Contains(t, out, "FAIL")
	assert.NotContains(t, out, "t2")
	assert.NotContains(t, out, "t3")
}

func TestMigrateCommandContinueOnErrorListsAllFailures(t *testing.T) {
	flyway := stubFlywayFailing(t)

	listSrv := httptest.NewServer(stdhttp.HandlerFunc(func(w stdhttp.ResponseWriter, _ *stdhttp.Request) {
		writeEnvelope(w, map[string]any{
			"tenants":     []map[string]string{{"id": "t1"}, {"id": "t2"}},
			"next_cursor": "",
		})
	}))
	defer listSrv.Close()

	smSrv := fakeSecretsManager(t, map[string]string{
		secretName("t1"): canonicalTenantSecret("h", "d", "u"),
		secretName("t2"): canonicalTenantSecret("h", "d", "u"),
	})
	defer smSrv.Close()

	setFakeAWSEnv(t)

	cmd := NewMigrateCommand()
	cmd.SetArgs([]string{
		"--source-url", listSrv.URL,
		"--allow-insecure-scheme",
		"--aws-endpoint", smSrv.URL,
		"--aws-region", "us-east-1",
		"--flyway-path", flyway,
		"--flyway-config", flywayConfPath(t),
		"--migrations-dir", makeTempDir(t),
		"--continue-on-error",
	})
	var stdout bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetContext(context.Background())

	err := cmd.Execute()
	require.Error(t, err) // exit non-zero because there were failures

	out := stdout.String()
	assert.Contains(t, out, "t1")
	assert.Contains(t, out, "t2")
	assert.Equal(t, 2, strings.Count(out, "FAIL"))
}

func TestMigrateCommandMalformedSecretMentionsTenant(t *testing.T) {
	flyway := stubFlyway(t)

	listSrv := httptest.NewServer(stdhttp.HandlerFunc(func(w stdhttp.ResponseWriter, _ *stdhttp.Request) {
		writeEnvelope(w, map[string]any{
			"tenants":     []map[string]string{{"id": "broken"}},
			"next_cursor": "",
		})
	}))
	defer listSrv.Close()

	smSrv := fakeSecretsManager(t, map[string]string{
		secretName("broken"): `not a json`,
	})
	defer smSrv.Close()

	setFakeAWSEnv(t)

	cmd := NewMigrateCommand()
	cmd.SetArgs([]string{
		"--source-url", listSrv.URL,
		"--allow-insecure-scheme",
		"--aws-endpoint", smSrv.URL,
		"--aws-region", "us-east-1",
		"--flyway-path", flyway,
		"--flyway-config", flywayConfPath(t),
		"--migrations-dir", makeTempDir(t),
	})
	var stdout bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetContext(context.Background())

	err := cmd.Execute()
	require.Error(t, err)
	out := stdout.String()
	assert.Contains(t, out, "broken")
	assert.Contains(t, out, secretName("broken"))
}

// flywayConfPath creates an empty flyway.conf in a temp directory and returns its path.
func flywayConfPath(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "flyway.conf")
	require.NoError(t, os.WriteFile(path, []byte(""), 0o644))
	return path
}

// makeTempDir creates an empty temp directory and returns its path.
func makeTempDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(dir, 0o755))
	return dir
}

// setFakeAWSEnv points the AWS SDK at throwaway credentials so the fake
// Secrets Manager endpoint is reached without a real credential chain.
func setFakeAWSEnv(t *testing.T) {
	t.Helper()
	t.Setenv("AWS_ACCESS_KEY_ID", "test")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "test")
	t.Setenv("AWS_REGION", "us-east-1")
}

// tenantListServer serves one page of the control-plane listing envelope.
func tenantListServer(ids ...string) *httptest.Server {
	tenants := make([]map[string]string, 0, len(ids))
	for _, id := range ids {
		tenants = append(tenants, map[string]string{"id": id})
	}
	return httptest.NewServer(stdhttp.HandlerFunc(func(w stdhttp.ResponseWriter, _ *stdhttp.Request) {
		writeEnvelope(w, map[string]any{"tenants": tenants, "next_cursor": ""})
	}))
}

// summaryRecords returns the NDJSON summary records found in a --json run's
// output. A pipeline must see exactly one per invocation.
func summaryRecords(t *testing.T, out string) []summaryRecord {
	t.Helper()
	var recs []summaryRecord
	for _, line := range strings.Split(out, "\n") {
		// A subcommand exercised outside the root prints cobra's usage block on
		// error; only the NDJSON records are of interest here.
		if !strings.HasPrefix(line, "{") {
			continue
		}
		var rec summaryRecord
		require.NoError(t, json.Unmarshal([]byte(line), &rec))
		if rec.Event == "summary" {
			recs = append(recs, rec)
		}
	}
	return recs
}

// fakeFleet starts the control-plane listing and Secrets Manager endpoints for
// the named tenants and points the AWS SDK at them, returning both URLs.
func fakeFleet(t *testing.T, ids ...string) (listURL, smURL string) {
	t.Helper()
	secrets := make(map[string]string, len(ids))
	for _, id := range ids {
		secrets[secretName(id)] = canonicalTenantSecret("h", "d", "u")
	}
	listSrv := tenantListServer(ids...)
	smSrv := fakeSecretsManager(t, secrets)
	t.Cleanup(listSrv.Close)
	t.Cleanup(smSrv.Close)
	setFakeAWSEnv(t)
	return listSrv.URL, smSrv.URL
}

// fleetArgs is the flag set every fleet run in these tests shares.
func fleetArgs(t *testing.T, listURL, smURL, flywayPath string) []string {
	t.Helper()
	return []string{
		"--source-url", listURL,
		"--allow-insecure-scheme",
		"--aws-endpoint", smURL,
		"--aws-region", "us-east-1",
		"--flyway-path", flywayPath,
		"--flyway-config", flywayConfPath(t),
		"--migrations-dir", makeTempDir(t),
	}
}

// runMigrateJSON runs the migrate subcommand in --json mode and returns its
// stdout with the command's error.
func runMigrateJSON(t *testing.T, args ...string) (stdout string, err error) {
	t.Helper()
	cmd := NewMigrateCommand()
	cmd.SetArgs(append(args, "--json"))
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetContext(context.Background())
	err = cmd.Execute()
	return out.String(), err
}

func TestMigrateCommandEmptyTenantListIsNothingAttempted(t *testing.T) {
	listURL, smURL := fakeFleet(t)

	stdout, err := runMigrateJSON(t, fleetArgs(t, listURL, smURL, stubFlyway(t))...)
	require.Error(t, err)
	require.ErrorIs(t, err, migration.ErrNothingAttempted)
	assert.Equal(t, ExitNothingAttempted, ExitCode(err))

	rec := requireSummary(t, stdout)
	assert.Equal(t, verdictNothingAttempted, rec.Verdict)
	assert.Zero(t, rec.Listed)
}

func TestMigrateCommandListTenantsFailureStillEmitsSummary(t *testing.T) {
	listSrv := httptest.NewServer(stdhttp.HandlerFunc(func(w stdhttp.ResponseWriter, _ *stdhttp.Request) {
		stdhttp.Error(w, "control plane down", stdhttp.StatusInternalServerError)
	}))
	defer listSrv.Close()

	// The listing fails, so no secret is ever fetched; this endpoint only has to exist.
	smSrv := fakeSecretsManager(t, nil)
	defer smSrv.Close()
	setFakeAWSEnv(t)

	stdout, err := runMigrateJSON(t, fleetArgs(t, listSrv.URL, smSrv.URL, stubFlyway(t))...)
	require.Error(t, err)
	require.ErrorIs(t, err, migration.ErrNothingAttempted)
	assert.Equal(t, ExitNothingAttempted, ExitCode(err))
	assert.Equal(t, verdictNothingAttempted, requireSummary(t, stdout).Verdict)
}

func TestMigrateCommandUnreadableTenantStoreIsNothingAttempted(t *testing.T) {
	stdout, err := runMigrateJSON(t,
		"--tenant", "t1",
		"--credentials-from", "config-file",
		"--source-config", filepath.Join(t.TempDir(), "absent.yaml"),
	)
	require.Error(t, err)
	require.ErrorIs(t, err, migration.ErrNothingAttempted)
	assert.Equal(t, ExitNothingAttempted, ExitCode(err))

	rec := requireSummary(t, stdout)
	assert.Equal(t, verdictNothingAttempted, rec.Verdict)
	assert.Equal(t, "migrate", rec.Action)
}

// A misuse dispatched no tenant, so it exits 2 like any other pre-dispatch
// failure — exit 1 stays reserved for a split fleet, which a pipeline must be
// able to trust (ADR-115).
func TestMigrateCommandInvalidCredentialSourceIsNothingAttempted(t *testing.T) {
	stdout, err := runMigrateJSON(t,
		"--tenant", "t1",
		"--credentials-from", "not-a-credential-source",
	)
	require.Error(t, err)
	require.ErrorIs(t, err, migration.ErrNothingAttempted)
	assert.Equal(t, ExitNothingAttempted, ExitCode(err))
	assert.Equal(t, verdictNothingAttempted, requireSummary(t, stdout).Verdict)
}

// A half-set GOBRICKS_MIGRATE_MIGRATOR_USER/_PASSWORD pair (#1766) is rejected
// before the first dispatch, so it costs what every other misuse costs: exit 2,
// with exactly one summary record.
func TestMigrateCommandHalfSetMigratorPairIsNothingAttempted(t *testing.T) {
	t.Setenv(envMigratorUser, "migrator")
	os.Unsetenv(envMigratorPassword)

	stdout, err := runMigrateJSON(t, "--tenant", "t1")
	require.Error(t, err)
	require.ErrorContains(t, err, envMigratorPassword)
	require.ErrorIs(t, err, migration.ErrNothingAttempted)
	assert.Equal(t, ExitNothingAttempted, ExitCode(err))

	rec := requireSummary(t, stdout)
	assert.Equal(t, verdictNothingAttempted, rec.Verdict)
	assert.Zero(t, rec.Listed)
}

// Misuse is marked wherever it is detected, so exit 1 keeps meaning a split
// fleet and nothing else. These go through the real root with every subcommand
// attached, because several of the paths (an unknown command, a non-runnable
// parent, list/quiesce flag resolution) never reach an action at all.
func TestRootMarksEveryMisuseAsNothingAttempted(t *testing.T) {
	tests := []struct {
		name string
		args []string
	}{
		{name: "unknown_command_at_the_root", args: []string{"not-a-command"}},
		{name: "unknown_flag", args: []string{"migrate", "--not-a-flag"}},
		{name: "stray_positional_on_an_action", args: []string{"migrate", "stray"}},
		{name: "stray_positional_on_a_parent", args: []string{"quiesce", "stray"}},
		{name: "stray_positional_on_version", args: []string{"version", "stray"}},
		{name: "list_without_a_source_selector", args: []string{"list"}},
		{name: "list_with_two_source_selectors", args: []string{"list", "--source-url", "http://x", "--tenant", "y"}},
		{name: "quiesce_without_a_source_selector", args: []string{"quiesce", "status"}},
		// A flag VALUE cobra cannot parse, as distinct from an unknown flag.
		{name: "unparseable_flag_value", args: []string{"migrate", "--parallel", "notanumber", "--tenant", "t1"}},
		// The same config misuse must cost the same on every subcommand: a
		// plaintext source URL without the opt-in, and a credential provider
		// that cannot be built.
		{name: "list_rejects_a_plaintext_source_url", args: []string{"list", "--source-url", "http://x"}},
		{name: "migrate_rejects_a_plaintext_source_url", args: []string{"migrate", "--source-url", "http://x"}},
		{name: "list_with_an_absent_tenant_store", args: []string{"list", "--source-config", "/absent.yaml"}},
		// These two die in resolveFlags, before any provider is built.
		{
			name: "migrate_with_config_file_credentials_and_no_store",
			args: []string{"migrate", "--tenant", "t1", "--credentials-from", "config-file"},
		},
		{
			name: "quiesce_with_config_file_credentials_and_no_store",
			args: []string{"quiesce", "status", "--tenant", "t1", "--credentials-from", "config-file"},
		},
		// A prefix without its trailing slash passes flag resolution and fails
		// inside buildConfigProvider, which is the arm these reach.
		{
			name: "migrate_cannot_build_the_credential_provider",
			args: []string{"migrate", "--tenant", "t1", "--secrets-prefix", "no-slash"},
		},
		{
			name: "quiesce_cannot_build_the_credential_provider",
			args: []string{"quiesce", "status", "--tenant", "t1", "--secrets-prefix", "no-slash"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			root := newTestRoot()
			root.SetArgs(tc.args)

			err := root.Execute()
			require.Error(t, err)
			require.ErrorIs(t, err, migration.ErrNothingAttempted)
			assert.Equal(t, ExitNothingAttempted, ExitCode(err))
		})
	}
}

// list's own work failing is not "nothing attempted": the command ran, the
// control plane answered badly, and exit 2's promise does not apply. Only its
// setup can reach 2.
func TestListOutageExitsOneNotTwo(t *testing.T) {
	srv := httptest.NewServer(stdhttp.HandlerFunc(func(w stdhttp.ResponseWriter, _ *stdhttp.Request) {
		stdhttp.Error(w, "control plane down", stdhttp.StatusInternalServerError)
	}))
	defer srv.Close()

	root := newTestRoot()
	root.SetArgs([]string{"list", "--source-url", srv.URL, "--allow-insecure-scheme"})

	err := root.Execute()
	require.Error(t, err)
	require.NotErrorIs(t, err, migration.ErrNothingAttempted)
	assert.Equal(t, ExitFleetSplit, ExitCode(err))
}

// A bare invocation is not a misuse: it answers with help and exits 0. Neither
// is a subcommand that does its job without dispatching anything.
func TestRootExitsCleanWhenNothingIsMisused(t *testing.T) {
	for _, args := range [][]string{{}, {"quiesce"}, {"version"}} {
		root := newTestRoot()
		root.SetArgs(args)
		err := root.Execute()
		require.NoError(t, err)
		assert.Equal(t, ExitClean, ExitCode(err))
	}
}

// newTestRoot builds the command tree main wires up, with output captured.
func newTestRoot() *cobra.Command {
	root := NewRootCommand()
	root.AddCommand(
		NewMigrateCommand(), NewValidateCommand(), NewInfoCommand(),
		NewListCommand(), NewQuiesceCommand(), NewVersionCommand("test"),
	)
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})
	return root
}

func TestMigrateCommandFailFastIsFleetSplitWithNeverDispatched(t *testing.T) {
	listURL, smURL := fakeFleet(t, "t1", "t2", "t3")

	stdout, err := runMigrateJSON(t, fleetArgs(t, listURL, smURL, stubFlywayFailing(t))...)
	require.Error(t, err)
	require.ErrorIs(t, err, migration.ErrFleetSplit)
	assert.Equal(t, ExitFleetSplit, ExitCode(err))

	// t1 was dispatched and failed; fail-fast left t2 and t3 unreached.
	rec := requireSummary(t, stdout)
	assert.Equal(t, verdictFleetSplit, rec.Verdict)
	assert.Equal(t, 3, rec.Listed)
	assert.Equal(t, 1, rec.Attempted)
	assert.Equal(t, 1, rec.Failed)
	assert.Equal(t, 2, rec.NotAttempted)
}

func TestMigrateCommandCleanRunIsExitZero(t *testing.T) {
	listURL, smURL := fakeFleet(t, "t1")

	stdout, err := runMigrateJSON(t, fleetArgs(t, listURL, smURL, stubFlyway(t))...)
	require.NoError(t, err)
	assert.Equal(t, ExitClean, ExitCode(err))

	rec := requireSummary(t, stdout)
	assert.Equal(t, verdictClean, rec.Verdict)
	assert.Equal(t, 1, rec.Listed)
	assert.Equal(t, 1, rec.Attempted)
	assert.Zero(t, rec.NotAttempted)
}
