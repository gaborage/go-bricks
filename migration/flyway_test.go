package migration

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gaborage/go-bricks/config"
	"github.com/gaborage/go-bricks/logger"
)

// helper to create an executable script that verifies required env vars and exits 0
func createFlywayStub(t *testing.T, vendor string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "flyway-stub.sh")
	var content string
	switch vendor {
	case "postgresql":
		content = "#!/bin/sh\n: \"${DB_HOST:?}\"\n: \"${DB_PORT:?}\"\n: \"${DB_USER:?}\"\n: \"${DB_PASSWORD:?}\"\n: \"${DB_NAME:?}\"\nexit 0\n"
	case "oracle":
		content = "#!/bin/sh\n: \"${ORACLE_HOST:?}\"\n: \"${ORACLE_PORT:?}\"\n: \"${ORACLE_USER:?}\"\n: \"${ORACLE_PASSWORD:?}\"\n: \"${ORACLE_PDB:?}\"\nexit 0\n"
	default:
		content = "#!/bin/sh\nexit 0\n"
	}
	require.NoError(t, os.WriteFile(path, []byte(content), 0o755))
	return path
}

func TestValidateFlywayPath(t *testing.T) {
	cfg := &config.Config{Database: config.DatabaseConfig{Type: "postgresql"}, App: config.AppConfig{Env: "test"}}
	fm := NewFlywayMigrator(cfg, logger.New("disabled", true))

	// empty path
	err := fm.validateFlywayPath("")
	require.Error(t, err)

	// dangerous path
	err = fm.validateFlywayPath("../bin/flyway")
	require.Error(t, err)

	// non-existent
	err = fm.validateFlywayPath("/definitely/not/exist/flyway")
	require.Error(t, err)

	// ok path
	stub := createFlywayStub(t, "postgresql")
	require.NoError(t, fm.validateFlywayPath(stub))
}

func TestBuildEnvironmentVariables(t *testing.T) {
	// Postgres
	cfg := &config.Config{Database: config.DatabaseConfig{
		Type:     "postgresql",
		Host:     "h",
		Port:     5432,
		Username: "u",
		Password: "p",
		Database: "d",
	}}
	fm := NewFlywayMigrator(cfg, logger.New("disabled", true))
	env := fm.buildEnvironmentVariables()
	joined := "" + (func() string {
		s := ""
		for _, e := range env {
			s += e + "\n"
		}
		return s
	})()
	assert.Contains(t, joined, "DB_HOST=h")
	assert.Contains(t, joined, "DB_PORT=5432")
	assert.Contains(t, joined, "DB_USER=u")
	assert.Contains(t, joined, "DB_PASSWORD=p")
	assert.Contains(t, joined, "DB_NAME=d")

	// Oracle
	cfg = &config.Config{Database: config.DatabaseConfig{
		Type:     "oracle",
		Host:     "oh",
		Port:     1521,
		Username: "ou",
		Password: "op",
		Database: "pdb1",
	}}
	fm = NewFlywayMigrator(cfg, logger.New("disabled", true))
	env = fm.buildEnvironmentVariables()
	joined = "" + (func() string {
		s := ""
		for _, e := range env {
			s += e + "\n"
		}
		return s
	})()
	assert.Contains(t, joined, "ORACLE_HOST=oh")
	assert.Contains(t, joined, "ORACLE_PORT=1521")
	assert.Contains(t, joined, "ORACLE_USER=ou")
	assert.Contains(t, joined, "ORACLE_PASSWORD=op")
	assert.Contains(t, joined, "ORACLE_PDB=pdb1")
}

func TestRunFlywayCommand_Success_WithEnv(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script stub not supported on windows CI")
	}

	cfg := &config.Config{Database: config.DatabaseConfig{
		Type:     "postgresql",
		Host:     "h",
		Port:     15432,
		Username: "user",
		Password: "pass",
		Database: "db",
	}, App: config.AppConfig{Env: "test"}}

	fm := NewFlywayMigrator(cfg, logger.New("disabled", true))

	stub := createFlywayStub(t, "postgresql")
	mcfg := &Config{
		FlywayPath:    stub,
		ConfigPath:    filepath.Join(t.TempDir(), "flyway.conf"),
		MigrationPath: filepath.Join(t.TempDir(), "migrations"),
		Timeout:       10_000_000_000, // 10s
		Environment:   cfg.App.Env,
	}

	// Ensure config/migration paths exist to avoid oddities
	require.NoError(t, os.WriteFile(mcfg.ConfigPath, []byte(""), 0o644))
	require.NoError(t, os.MkdirAll(mcfg.MigrationPath, 0o755))

	ctx := context.Background()
	// Should succeed and validate env variables presence
	require.NoError(t, fm.Migrate(ctx, mcfg))
}
