package commands

import (
	"bytes"
	"context"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gaborage/go-bricks/config"
	"github.com/gaborage/go-bricks/migration"
)

func TestRunQuiesceSetActivatesAndRenders(t *testing.T) {
	ctrl := migration.NewMemoryQuiesceController()
	var out bytes.Buffer
	flags := &quiesceFlags{common: &CommonFlags{AppliedBy: "op@ci"}, reason: "deploy-7", ttl: time.Hour}

	require.NoError(t, runQuiesceSet(context.Background(), &out, ctrl, flags))
	assert.Contains(t, out.String(), "Quiesce flag set")
	assert.Contains(t, out.String(), "ACTIVE")
	assert.Contains(t, out.String(), "op@ci")

	set, _ := ctrl.IsSet(context.Background())
	assert.True(t, set)
}

func TestRunQuiesceSetRejectsNegativeTTL(t *testing.T) {
	ctrl := migration.NewMemoryQuiesceController()
	var out bytes.Buffer
	flags := &quiesceFlags{common: &CommonFlags{AppliedBy: "op"}, ttl: -time.Second}

	err := runQuiesceSet(context.Background(), &out, ctrl, flags)
	require.Error(t, err)
	assert.ErrorIs(t, err, migration.ErrInvalidQuiesceTTL)
}

func TestRunQuiesceClearWhenActive(t *testing.T) {
	ctrl := migration.NewMemoryQuiesceController()
	_, err := ctrl.Set(context.Background(), migration.QuiesceSetOptions{By: "op", TTL: time.Hour})
	require.NoError(t, err)

	var out bytes.Buffer
	require.NoError(t, runQuiesceClear(context.Background(), &out, ctrl, "ops", false))
	assert.Contains(t, out.String(), "cleared")

	set, _ := ctrl.IsSet(context.Background())
	assert.False(t, set)
}

func TestRunQuiesceClearWhenInactivePrintsCleanMessage(t *testing.T) {
	ctrl := migration.NewMemoryQuiesceController()
	var out bytes.Buffer

	require.NoError(t, runQuiesceClear(context.Background(), &out, ctrl, "ops", false),
		"clearing nothing is not an error at the CLI layer")
	assert.Contains(t, out.String(), "No active quiesce flag")
}

func TestRunQuiesceStatusJSON(t *testing.T) {
	ctrl := migration.NewMemoryQuiesceController()
	_, err := ctrl.Set(context.Background(), migration.QuiesceSetOptions{By: "op@ci", Reason: "r", TTL: time.Hour})
	require.NoError(t, err)

	var out bytes.Buffer
	require.NoError(t, runQuiesceStatus(context.Background(), &out, ctrl, true))
	assert.Contains(t, out.String(), `"active": true`)
	assert.Contains(t, out.String(), `"setBy": "op@ci"`)
}

func TestRunQuiesceStatusTextNotQuiesced(t *testing.T) {
	ctrl := migration.NewMemoryQuiesceController()
	var out bytes.Buffer
	require.NoError(t, runQuiesceStatus(context.Background(), &out, ctrl, false))
	assert.Contains(t, out.String(), "not quiesced")
}

func TestNewQuiesceCommandWiresSubcommands(t *testing.T) {
	cmd := NewQuiesceCommand()
	have := map[string]bool{}
	for _, c := range cmd.Commands() {
		have[c.Name()] = true
	}
	for _, want := range []string{"set", "clear", "status"} {
		assert.True(t, have[want], "quiesce must have a %q subcommand", want)
	}
	// --table is on each child.
	for _, c := range cmd.Commands() {
		assert.NotNil(t, c.Flags().Lookup("table"), "%s must have --table", c.Name())
	}
}

func TestControlPlaneDSN(t *testing.T) {
	base := &config.DatabaseConfig{
		Host: "db.internal", Port: 5432, Database: "ctl",
		Username: "migrator", Password: "p@ss:w/rd?", // special chars must be URL-encoded
	}

	// No TLS.Mode → sslmode omitted (never a forced plaintext downgrade); creds encoded.
	dsn := controlPlaneDSN(base)
	assert.Contains(t, dsn, "postgres://")
	assert.Contains(t, dsn, "@db.internal:5432/ctl")
	assert.NotContains(t, dsn, "sslmode", "sslmode must be omitted when TLS.Mode is unset")
	assert.NotContains(t, dsn, "p@ss:w/rd?", "raw special-char password must be percent-encoded")
	assert.Contains(t, dsn, "migrator:")

	// TLS.Mode is honored, matching the framework connector.
	withTLS := *base
	withTLS.TLS.Mode = "require"
	assert.Contains(t, controlPlaneDSN(&withTLS), "sslmode=require")

	// Full TLS material (CA + client cert/key) must be wired so the control-plane
	// connection is authenticated/mTLS — not just sslmode (ADR-027). url.Values encodes
	// the paths, so parse and assert decoded values.
	fullTLS := *base
	fullTLS.TLS.Mode = "verify-full"
	fullTLS.TLS.CAFile = "/etc/ssl/ca.pem"
	fullTLS.TLS.CertFile = "/etc/ssl/client.crt"
	fullTLS.TLS.KeyFile = "/etc/ssl/client.key"
	u, err := url.Parse(controlPlaneDSN(&fullTLS))
	require.NoError(t, err)
	q := u.Query()
	assert.Equal(t, "verify-full", q.Get("sslmode"))
	assert.Equal(t, "/etc/ssl/ca.pem", q.Get("sslrootcert"))
	assert.Equal(t, "/etc/ssl/client.crt", q.Get("sslcert"))
	assert.Equal(t, "/etc/ssl/client.key", q.Get("sslkey"))

	// An explicit ConnectionString wins verbatim.
	assert.Equal(t, "postgres://verbatim/x",
		controlPlaneDSN(&config.DatabaseConfig{ConnectionString: "postgres://verbatim/x"}))
}

func TestOpenControlPlaneDBRequiresTenant(t *testing.T) {
	_, _, err := openControlPlaneDB(context.Background(), &CommonFlags{}) // no Tenant
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--tenant")
}

func TestQuiesceSetWithoutSourceFails(t *testing.T) {
	cmd := NewQuiesceCommand()
	cmd.SetArgs([]string{"set", "--applied-by", "op"}) // no --tenant/--source-*
	cmd.SetContext(context.Background())
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	require.Error(t, cmd.Execute(), "missing tenant/source must fail before touching the DB")
}

func TestQuiesceRejectsNonPostgresControlPlane(t *testing.T) {
	dir := t.TempDir()
	yaml := "multitenant:\n  enabled: true\n  tenants:\n    ora:\n      database:\n" +
		"        type: oracle\n        host: \"h\"\n        port: 1521\n" +
		"        username: \"u\"\n        password: \"p\"\n        database: \"d\"\n"
	path := filepath.Join(dir, "t.yaml")
	require.NoError(t, os.WriteFile(path, []byte(yaml), 0o600))

	cmd := NewQuiesceCommand()
	cmd.SetArgs([]string{
		"set", "--tenant", "ora", "--source-config", path,
		"--credentials-from", "config-file", "--applied-by", "op", "--ttl", "1h",
	})
	cmd.SetContext(context.Background())
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)

	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "PostgreSQL-only",
		"a non-PostgreSQL control-plane target must be rejected before any DB connection")
}

func TestQuiesceCommandPathWithInjectedController(t *testing.T) {
	mem := migration.NewMemoryQuiesceController()
	orig := controllerOpener
	controllerOpener = func(context.Context, *CommonFlags, string) (migration.QuiesceController, func(), error) {
		return mem, func() {}, nil
	}
	t.Cleanup(func() { controllerOpener = orig })

	run := func(args ...string) (string, error) {
		cmd := NewQuiesceCommand()
		cmd.SetArgs(append([]string{"--tenant", "cp"}, args...))
		cmd.SetContext(context.Background())
		var out bytes.Buffer
		cmd.SetOut(&out)
		cmd.SetErr(&out)
		err := cmd.Execute()
		return out.String(), err
	}

	out, err := run("set", "--applied-by", "op", "--ttl", "1h")
	require.NoError(t, err, out)
	assert.Contains(t, out, "Quiesce flag set")

	out, err = run("status", "--json")
	require.NoError(t, err, out)
	assert.Contains(t, out, `"active": true`)

	out, err = run("clear", "--applied-by", "ops")
	require.NoError(t, err, out)
	assert.Contains(t, out, "cleared")
}

// erroringController fails every read/write to exercise the CLI's error wrapping.
type erroringController struct{ err error }

func (e erroringController) IsSet(context.Context) (bool, error) { return false, e.err }
func (e erroringController) Query(context.Context) (*migration.QuiesceStatus, error) {
	return nil, e.err
}
func (e erroringController) Set(context.Context, migration.QuiesceSetOptions) (*migration.QuiesceStatus, error) {
	return nil, e.err
}
func (e erroringController) Clear(context.Context, string) (*migration.QuiesceStatus, error) {
	return nil, e.err
}
func (e erroringController) CreateTable(context.Context) error { return nil }

func TestRunQuiesceStatusReportsClearedState(t *testing.T) {
	ctrl := migration.NewMemoryQuiesceController()
	_, err := ctrl.Set(context.Background(), migration.QuiesceSetOptions{By: "op", TTL: time.Hour})
	require.NoError(t, err)
	_, err = ctrl.Clear(context.Background(), "ops")
	require.NoError(t, err)

	var out bytes.Buffer
	require.NoError(t, runQuiesceStatus(context.Background(), &out, ctrl, false))
	assert.Contains(t, out.String(), "cleared", "a cleared (non-active) flag renders as cleared")
}

func TestRunQuiesceWrapsControllerErrors(t *testing.T) {
	boom := erroringController{err: errAssert}
	var out bytes.Buffer

	require.Error(t, runQuiesceStatus(context.Background(), &out, boom, false))
	require.Error(t, runQuiesceClear(context.Background(), &out, boom, "op", false))
	flags := &quiesceFlags{common: &CommonFlags{AppliedBy: "op"}, ttl: time.Hour}
	require.Error(t, runQuiesceSet(context.Background(), &out, boom, flags))
}

var errAssert = stringError("control-plane unavailable")

type stringError string

func (e stringError) Error() string { return string(e) }

func TestRunQuiesceStatusReportsExpiredState(t *testing.T) {
	now := time.Now().UTC()
	ctrl := migration.NewMemoryQuiesceController().WithClock(func() time.Time { return now })
	_, err := ctrl.Set(context.Background(), migration.QuiesceSetOptions{By: "op", TTL: time.Minute})
	require.NoError(t, err)
	ctrl.WithClock(func() time.Time { return now.Add(2 * time.Minute) }) // past TTL

	var out bytes.Buffer
	require.NoError(t, runQuiesceStatus(context.Background(), &out, ctrl, false))
	assert.Contains(t, out.String(), "expired")
}

func TestRunQuiesceStatusClearedJSON(t *testing.T) {
	ctrl := migration.NewMemoryQuiesceController()
	_, err := ctrl.Set(context.Background(), migration.QuiesceSetOptions{By: "op", TTL: time.Hour})
	require.NoError(t, err)
	_, err = ctrl.Clear(context.Background(), "ops")
	require.NoError(t, err)

	var out bytes.Buffer
	require.NoError(t, runQuiesceStatus(context.Background(), &out, ctrl, true))
	assert.Contains(t, out.String(), `"cleared": true`)
	assert.Contains(t, out.String(), `"clearedAt"`)
}

// createTableErrController embeds a working memory controller but fails the
// table-provisioning step, exercising withControlPlaneController's DDL guard.
type createTableErrController struct {
	*migration.MemoryQuiesceController
}

func (createTableErrController) CreateTable(context.Context) error { return stringError("ddl failed") }

func TestWithControlPlaneControllerCreateTableError(t *testing.T) {
	orig := controllerOpener
	controllerOpener = func(context.Context, *CommonFlags, string) (migration.QuiesceController, func(), error) {
		return createTableErrController{migration.NewMemoryQuiesceController()}, func() {}, nil
	}
	t.Cleanup(func() { controllerOpener = orig })

	cmd := NewQuiesceCommand()
	cmd.SetArgs([]string{"status", "--tenant", "cp"})
	cmd.SetContext(context.Background())
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	require.Error(t, cmd.Execute(), "a CreateTable failure must abort the command")
}

func TestRunQuiesceClearNoOpEmitsJSON(t *testing.T) {
	ctrl := migration.NewMemoryQuiesceController() // nothing set
	var out bytes.Buffer
	require.NoError(t, runQuiesceClear(context.Background(), &out, ctrl, "ops", true))
	assert.Contains(t, out.String(), `"active": false`, "clear --json must emit machine-readable output on the no-op path")
}
