package migration

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gaborage/go-bricks/config"
	"github.com/gaborage/go-bricks/logger"
)

const (
	windowsOS = "windows"
)

// helper to create an executable script that verifies required env vars and exits 0
func createFlywayStub(t *testing.T, vendor string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "flyway-stub.sh")
	// Emit a parseable success envelope so migrate callers get a valid Result;
	// info/validate callers take the !isMigrate early return and discard stdout.
	emit := "echo '" + minimalMigrateSuccessJSON + "'\n"
	var content string
	switch vendor {
	case "postgresql":
		content = "#!/bin/sh\n: \"${DB_HOST:?}\"\n: \"${DB_PORT:?}\"\n: \"${DB_USER:?}\"\n: \"${DB_PASSWORD:?}\"\n: \"${DB_NAME:?}\"\n" + emit + "exit 0\n"
	case "oracle":
		content = "#!/bin/sh\n: \"${ORACLE_HOST:?}\"\n: \"${ORACLE_PORT:?}\"\n: \"${ORACLE_USER:?}\"\n: \"${ORACLE_PASSWORD:?}\"\n: \"${ORACLE_PDB:?}\"\n" + emit + "exit 0\n"
	default:
		content = "#!/bin/sh\n" + emit + "exit 0\n"
	}
	require.NoError(t, os.WriteFile(path, []byte(content), 0o755))
	return path
}

// helper to create a simple stub that just exits successfully (for commands that don't need env vars)
func createSimpleFlywayStub(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "flyway-simple.sh")
	content := "#!/bin/sh\necho '" + minimalMigrateSuccessJSON + "'\nexit 0\n"
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
	env, err := buildEnvironmentVariables(&fm.config.Database)
	require.NoError(t, err)
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
	env, err = buildEnvironmentVariables(&fm.config.Database)
	require.NoError(t, err)
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

func TestRunFlywayCommandSuccessWithEnv(t *testing.T) {
	if runtime.GOOS == windowsOS {
		t.Skip("shell script stub not supported on windows CI")
	}

	cfg := &config.Config{Database: config.DatabaseConfig{
		Type:     "postgresql",
		Host:     "h",
		Port:     15432,
		Username: "user",
		Password: "longenough-pw",
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
	_, err := fm.Migrate(ctx, mcfg)
	require.NoError(t, err)
}

func TestDefaultMigrationConfig(t *testing.T) {
	tests := []struct {
		name         string
		dbType       string
		appEnv       string
		expectedConf func(dbType, appEnv string) *Config
	}{
		{
			name:   "postgresql_config",
			dbType: "postgresql",
			appEnv: "development",
			expectedConf: func(_, _ string) *Config {
				return &Config{
					FlywayPath:    "flyway",
					ConfigPath:    "flyway/flyway-postgresql.conf",
					MigrationPath: "migrations/postgresql",
					Timeout:       5 * 60_000_000_000, // 5 minutes
					Environment:   "development",
					DryRun:        false,
				}
			},
		},
		{
			name:   "oracle_config",
			dbType: "oracle",
			appEnv: "production",
			expectedConf: func(_, _ string) *Config {
				return &Config{
					FlywayPath:    "flyway",
					ConfigPath:    "flyway/flyway-oracle.conf",
					MigrationPath: "migrations/oracle",
					Timeout:       5 * 60_000_000_000, // 5 minutes
					Environment:   "production",
					DryRun:        false,
				}
			},
		},
		{
			// Unknown vendors fall through the whitelist to the "unknown"
			// sentinel so a malicious tenant Type can't escape the flyway/
			// and migrations/ directories via fmt.Sprintf interpolation.
			name:   "unknown_database_type_falls_back_to_sentinel",
			dbType: "mysql",
			appEnv: "testing",
			expectedConf: func(_, _ string) *Config {
				return &Config{
					FlywayPath:    "flyway",
					ConfigPath:    "flyway/flyway-unknown.conf",
					MigrationPath: "migrations/unknown",
					Timeout:       5 * 60_000_000_000, // 5 minutes
					Environment:   "testing",
					DryRun:        false,
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &config.Config{
				Database: config.DatabaseConfig{Type: tt.dbType},
				App:      config.AppConfig{Env: tt.appEnv},
			}

			fm := NewFlywayMigrator(cfg, logger.New("disabled", true))
			result := fm.DefaultMigrationConfig()

			expected := tt.expectedConf(tt.dbType, tt.appEnv)

			assert.Equal(t, expected.FlywayPath, result.FlywayPath)
			assert.Equal(t, expected.ConfigPath, result.ConfigPath)
			assert.Equal(t, expected.MigrationPath, result.MigrationPath)
			assert.Equal(t, expected.Timeout, result.Timeout)
			assert.Equal(t, expected.Environment, result.Environment)
			assert.Equal(t, expected.DryRun, result.DryRun)
		})
	}
}

func TestInfo(t *testing.T) {
	if runtime.GOOS == windowsOS {
		t.Skip("shell script stub not supported on windows CI")
	}

	cfg := &config.Config{
		Database: config.DatabaseConfig{Type: "postgresql"},
		App:      config.AppConfig{Env: "test"},
	}

	fm := NewFlywayMigrator(cfg, logger.New("disabled", true))

	t.Run("info_with_custom_config", func(t *testing.T) {
		stub := createSimpleFlywayStub(t)
		mcfg := &Config{
			FlywayPath:    stub,
			ConfigPath:    filepath.Join(t.TempDir(), "flyway.conf"),
			MigrationPath: filepath.Join(t.TempDir(), "migrations"),
			Timeout:       10_000_000_000, // 10s
		}

		// Ensure paths exist
		require.NoError(t, os.WriteFile(mcfg.ConfigPath, []byte(""), 0o644))
		require.NoError(t, os.MkdirAll(mcfg.MigrationPath, 0o755))

		ctx := context.Background()
		err := fm.Info(ctx, mcfg)
		assert.NoError(t, err)
	})

	t.Run("info_with_nil_config_logic", func(t *testing.T) {
		// Test the nil config handling logic by verifying default config generation
		defaultCfg := fm.DefaultMigrationConfig()
		assert.NotNil(t, defaultCfg)
		assert.Equal(t, "postgresql", cfg.Database.Type)
		assert.Contains(t, defaultCfg.ConfigPath, "postgresql")
		assert.Contains(t, defaultCfg.MigrationPath, "postgresql")

		// Create a working test to verify Info function with custom config
		stub := createSimpleFlywayStub(t)
		mcfg := &Config{
			FlywayPath:    stub,
			ConfigPath:    filepath.Join(t.TempDir(), "flyway.conf"),
			MigrationPath: filepath.Join(t.TempDir(), "migrations"),
			Timeout:       10_000_000_000,
		}

		// Ensure paths exist
		require.NoError(t, os.WriteFile(mcfg.ConfigPath, []byte(""), 0o644))
		require.NoError(t, os.MkdirAll(mcfg.MigrationPath, 0o755))

		ctx := context.Background()
		err := fm.Info(ctx, mcfg)
		assert.NoError(t, err)
	})
}

func TestValidate(t *testing.T) {
	if runtime.GOOS == windowsOS {
		t.Skip("shell script stub not supported on windows CI")
	}

	cfg := &config.Config{
		Database: config.DatabaseConfig{Type: "oracle"},
		App:      config.AppConfig{Env: "test"},
	}

	fm := NewFlywayMigrator(cfg, logger.New("disabled", true))

	t.Run("validate_with_custom_config", func(t *testing.T) {
		stub := createSimpleFlywayStub(t)
		mcfg := &Config{
			FlywayPath:    stub,
			ConfigPath:    filepath.Join(t.TempDir(), "flyway.conf"),
			MigrationPath: filepath.Join(t.TempDir(), "migrations"),
			Timeout:       10_000_000_000, // 10s
		}

		// Ensure paths exist
		require.NoError(t, os.WriteFile(mcfg.ConfigPath, []byte(""), 0o644))
		require.NoError(t, os.MkdirAll(mcfg.MigrationPath, 0o755))

		ctx := context.Background()
		err := fm.Validate(ctx, mcfg)
		assert.NoError(t, err)
	})

	t.Run("validate_with_nil_config", func(t *testing.T) {
		// This tests the nil config handling in Validate function
		// Since we can't use default paths in tests, we'll verify the logic indirectly
		defaultCfg := fm.DefaultMigrationConfig()
		assert.Equal(t, "oracle", cfg.Database.Type)
		assert.Contains(t, defaultCfg.ConfigPath, "oracle")
		assert.Contains(t, defaultCfg.MigrationPath, "oracle")
	})
}

func TestRunMigrationsAtStartup(t *testing.T) {
	if runtime.GOOS == windowsOS {
		t.Skip("shell script stub not supported on windows CI")
	}

	tests := []struct {
		name        string
		environment string
		expectedCmd string
	}{
		{
			name:        "development_environment_runs_migrate",
			environment: "development",
			expectedCmd: "migrate",
		},
		{
			name:        "production_environment_runs_validate",
			environment: "production",
			expectedCmd: "validate",
		},
		{
			name:        "test_environment_runs_validate",
			environment: "test",
			expectedCmd: "validate",
		},
		{
			name:        "staging_environment_runs_validate",
			environment: "staging",
			expectedCmd: "validate",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &config.Config{
				Database: config.DatabaseConfig{Type: "postgresql"},
				App:      config.AppConfig{Env: tt.environment},
			}

			fm := NewFlywayMigrator(cfg, logger.New("disabled", true))

			// dev env runs migrate (needs parseable JSON); other envs validate (stdout discarded).
			stub, capturePath := createCommandCapturingStub(t, minimalMigrateSuccessJSON)
			tempDir := t.TempDir()
			configPath := filepath.Join(tempDir, "flyway.conf")
			migrationPath := filepath.Join(tempDir, "migrations")

			require.NoError(t, os.WriteFile(configPath, []byte(""), 0o644))
			require.NoError(t, os.MkdirAll(migrationPath, 0o755))

			fm.defaultConfig = func(*FlywayMigrator) *Config {
				return &Config{
					FlywayPath:    stub,
					ConfigPath:    configPath,
					MigrationPath: migrationPath,
					Timeout:       10 * time.Second,
					Environment:   tt.environment,
				}
			}

			err := fm.RunMigrationsAtStartup(context.Background())
			assert.NoError(t, err)

			captured, readErr := os.ReadFile(capturePath)
			require.NoError(t, readErr)
			assert.Contains(t, string(captured), tt.expectedCmd)
		})
	}
}

// TestFlywayMigratorDryRunRunsValidate verifies that Config.DryRun is honored: a dry-run
// Migrate must invoke the Flyway `validate` verb (no schema mutation), not `migrate`.
// Regression test for the High audit finding — DryRun was documented ("Only validate, do
// not execute") and stamped into the audit event but never consumed, so DryRun=true ran a
// real migration while the audit recorded dry_run=true.
func TestFlywayMigratorDryRunRunsValidate(t *testing.T) {
	if runtime.GOOS == windowsOS {
		t.Skip("shell script stub not supported on windows CI")
	}

	cases := []struct {
		name         string
		dryRun       bool
		expectedCmd  string
		forbiddenCmd string
	}{
		{name: "dry_run_substitutes_validate", dryRun: true, expectedCmd: "validate", forbiddenCmd: "migrate"},
		{name: "non_dry_run_runs_migrate", dryRun: false, expectedCmd: "migrate"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &config.Config{
				Database: config.DatabaseConfig{
					Type: "postgresql", Host: "h", Port: 5432,
					Username: "u", Password: "longenough-pw", Database: "d",
				},
				App: config.AppConfig{Env: "test"},
			}
			fm := NewFlywayMigrator(cfg, logger.New("disabled", true))

			// migrate emits parseable JSON; the dry-run (validate) case discards stdout.
			stub, capturePath := createCommandCapturingStub(t, minimalMigrateSuccessJSON)
			tempDir := t.TempDir()
			configPath := filepath.Join(tempDir, "flyway.conf")
			migrationPath := filepath.Join(tempDir, "migrations")
			require.NoError(t, os.WriteFile(configPath, []byte(""), 0o644))
			require.NoError(t, os.MkdirAll(migrationPath, 0o755))

			mcfg := &Config{
				FlywayPath:    stub,
				ConfigPath:    configPath,
				MigrationPath: migrationPath,
				Timeout:       10 * time.Second,
				DryRun:        tc.dryRun,
			}

			_, err := fm.MigrateFor(context.Background(), &cfg.Database, mcfg)
			require.NoError(t, err)

			captured, readErr := os.ReadFile(capturePath)
			require.NoError(t, readErr)
			assert.Contains(t, string(captured), tc.expectedCmd)
			if tc.forbiddenCmd != "" {
				assert.NotContains(t, string(captured), tc.forbiddenCmd,
					"DryRun must not invoke the migrate verb (it would mutate the schema)")
			}
		})
	}
}

// createCommandCapturingStub builds a flyway-stub that writes the verbatim
// arg list to capturePath, then optionally emits stdoutPayload on stdout
// before exiting 0. Pass an empty stdoutPayload for the args-only variant.
// Path interpolations are shell-quoted so the script survives $TMPDIR values
// containing spaces or other shell-metacharacters.
func createCommandCapturingStub(t *testing.T, stdoutPayload string) (stubPath, capturePath string) {
	t.Helper()
	dir := t.TempDir()
	stubPath = filepath.Join(dir, "flyway-stub.sh")
	capturePath = filepath.Join(dir, "captured_command")

	script := fmt.Sprintf("#!/bin/sh\necho \"$@\" > %q\n", capturePath)
	if stdoutPayload != "" {
		payloadPath := filepath.Join(dir, "payload")
		require.NoError(t, os.WriteFile(payloadPath, []byte(stdoutPayload), 0o644))
		script += fmt.Sprintf("cat %q\n", payloadPath)
	}
	script += "exit 0\n"
	require.NoError(t, os.WriteFile(stubPath, []byte(script), 0o755))
	return stubPath, capturePath
}

// Helper function to create a failing stub for testing error scenarios
func createFailingFlywayStub(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "flyway-fail.sh")
	content := `#!/bin/sh
echo "Flyway command failed" >&2
exit 1
`
	require.NoError(t, os.WriteFile(path, []byte(content), 0o755))
	return path
}

// Helper function to create a slow stub for testing timeout scenarios
func createSlowFlywayStub(t *testing.T, delaySeconds int) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "flyway-slow.sh")
	content := fmt.Sprintf(`#!/bin/sh
sleep %d
exit 0
`, delaySeconds)
	require.NoError(t, os.WriteFile(path, []byte(content), 0o755))
	return path
}

func TestRunFlywayCommandErrorHandling(t *testing.T) {
	if runtime.GOOS == windowsOS {
		t.Skip("shell script stub not supported on windows CI")
	}

	cfg := &config.Config{
		Database: config.DatabaseConfig{Type: "postgresql"},
		App:      config.AppConfig{Env: "test"},
	}

	fm := NewFlywayMigrator(cfg, logger.New("disabled", true))

	t.Run("command_failure", func(t *testing.T) {
		stub := createFailingFlywayStub(t)
		mcfg := &Config{
			FlywayPath:    stub,
			ConfigPath:    filepath.Join(t.TempDir(), "flyway.conf"),
			MigrationPath: filepath.Join(t.TempDir(), "migrations"),
			Timeout:       10_000_000_000, // 10s
		}

		// Ensure paths exist
		require.NoError(t, os.WriteFile(mcfg.ConfigPath, []byte(""), 0o644))
		require.NoError(t, os.MkdirAll(mcfg.MigrationPath, 0o755))

		ctx := context.Background()
		_, err := fm.Migrate(ctx, mcfg)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "flyway command failed")
	})

	t.Run("invalid_flyway_path", func(t *testing.T) {
		mcfg := &Config{
			FlywayPath:    "../dangerous/path",
			ConfigPath:    filepath.Join(t.TempDir(), "flyway.conf"),
			MigrationPath: filepath.Join(t.TempDir(), "migrations"),
			Timeout:       10_000_000_000,
		}

		ctx := context.Background()
		_, err := fm.Migrate(ctx, mcfg)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "invalid flyway path")
	})

	t.Run("timeout_scenario", func(t *testing.T) {
		stub := createSlowFlywayStub(t, 5) // 5 second delay
		mcfg := &Config{
			FlywayPath:    stub,
			ConfigPath:    filepath.Join(t.TempDir(), "flyway.conf"),
			MigrationPath: filepath.Join(t.TempDir(), "migrations"),
			Timeout:       1_000_000_000, // 1 second timeout
		}

		// Ensure paths exist
		require.NoError(t, os.WriteFile(mcfg.ConfigPath, []byte(""), 0o644))
		require.NoError(t, os.MkdirAll(mcfg.MigrationPath, 0o755))

		ctx := context.Background()
		_, err := fm.Migrate(ctx, mcfg)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "flyway command failed")
	})
}

func TestValidateFlywayPathComprehensiveEdgeCases(t *testing.T) {
	cfg := &config.Config{Database: config.DatabaseConfig{Type: "postgresql"}, App: config.AppConfig{Env: "test"}}
	fm := NewFlywayMigrator(cfg, logger.New("disabled", true))

	tests := []struct {
		name        string
		flywayPath  string
		expectError bool
		setup       func(t *testing.T) string // optional setup function that returns path
	}{
		{
			name:        "empty_path",
			flywayPath:  "",
			expectError: true,
		},
		{
			name:        "path_with_double_dots",
			flywayPath:  "../bin/flyway",
			expectError: true,
		},
		{
			name:        "path_with_semicolon",
			flywayPath:  "/bin/flyway;rm -rf /",
			expectError: true,
		},
		{
			name:        "path_with_ampersand",
			flywayPath:  "/bin/flyway & malicious_command",
			expectError: true,
		},
		{
			name:        "non_existent_path",
			flywayPath:  "/definitely/does/not/exist/flyway",
			expectError: true,
		},
		{
			name:        "valid_absolute_path",
			expectError: false,
			setup:       createSimpleFlywayStub,
		},
		{
			name:        "valid_relative_path",
			expectError: false,
			setup:       createSimpleFlywayStub,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := tt.flywayPath
			if tt.setup != nil {
				path = tt.setup(t)
			}

			err := fm.validateFlywayPath(path)

			if tt.expectError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestBuildEnvironmentVariablesComprehensiveDrivers(t *testing.T) {
	tests := []struct {
		name            string
		dbConfig        config.DatabaseConfig
		expectedVars    []string
		notExpectedVars []string
	}{
		{
			name: "postgresql_full_config",
			dbConfig: config.DatabaseConfig{
				Type:     "postgresql",
				Host:     "postgres.example.com",
				Port:     5432,
				Username: "postgres_user",
				Password: "postgres_pass",
				Database: "postgres_db",
			},
			expectedVars: []string{
				"DB_HOST=postgres.example.com",
				"DB_PORT=5432",
				"DB_USER=postgres_user",
				"DB_PASSWORD=postgres_pass",
				"DB_NAME=postgres_db",
			},
			notExpectedVars: []string{"ORACLE_"},
		},
		{
			name: "oracle_full_config",
			dbConfig: config.DatabaseConfig{
				Type:     "oracle",
				Host:     "oracle.example.com",
				Port:     1521,
				Username: "oracle_user",
				Password: "oracle_pass",
				Database: "XEPDB1",
			},
			expectedVars: []string{
				"ORACLE_HOST=oracle.example.com",
				"ORACLE_PORT=1521",
				"ORACLE_USER=oracle_user",
				"ORACLE_PASSWORD=oracle_pass",
				"ORACLE_PDB=XEPDB1",
			},
			notExpectedVars: []string{"DB_"},
		},
		{
			name: "unsupported_database_type",
			dbConfig: config.DatabaseConfig{
				Type:     "mysql",
				Host:     "mysql.example.com",
				Port:     3306,
				Username: "mysql_user",
				Password: "mysql_pass",
				Database: "mysql_db",
			},
			expectedVars:    []string{}, // No environment variables for unsupported types
			notExpectedVars: []string{"DB_", "ORACLE_", "MYSQL_"},
		},
		{
			name: "postgresql_with_zero_port",
			dbConfig: config.DatabaseConfig{
				Type:     "postgresql",
				Host:     "localhost",
				Port:     0, // Zero port should still be handled
				Username: "user",
				Password: "pass",
				Database: "db",
			},
			expectedVars: []string{
				"DB_HOST=localhost",
				"DB_PORT=0",
				"DB_USER=user",
				"DB_PASSWORD=pass",
				"DB_NAME=db",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &config.Config{
				Database: tt.dbConfig,
				App:      config.AppConfig{Env: "test"},
			}

			fm := NewFlywayMigrator(cfg, logger.New("disabled", true))
			envVars, err := buildEnvironmentVariables(&fm.config.Database)
			require.NoError(t, err)
			assertEnvVarsContain(t, envVars, tt.expectedVars)
			assertEnvVarsLackPrefixes(t, envVars, tt.notExpectedVars)
		})
	}
}

func TestBuildEnvironmentVariablesRejectsControlChars(t *testing.T) {
	t.Run("accepts_equals_in_password", func(t *testing.T) {
		db := &config.DatabaseConfig{
			Type:     "postgresql",
			Host:     "h",
			Port:     5432,
			Username: "u",
			Password: "base64==", // legitimate base64-encoded secret tail
			Database: "d",
		}
		envVars, err := buildEnvironmentVariables(db)
		require.NoError(t, err)
		assert.Contains(t, envVars, "DB_PASSWORD=base64==")
	})

	cases := []struct {
		name, field, badChar string
	}{
		{"host_lf", envFieldHost, "\n"},
		{"host_cr", envFieldHost, "\r"},
		{"host_nul", envFieldHost, "\x00"},
		{"username_lf", envFieldUsername, "\n"},
		{"username_cr", envFieldUsername, "\r"},
		{"username_nul", envFieldUsername, "\x00"},
		{"password_lf", envFieldPassword, "\n"},
		{"password_cr", envFieldPassword, "\r"},
		{"password_nul", envFieldPassword, "\x00"},
		{"database_lf", envFieldDatabase, "\n"},
		{"database_cr", envFieldDatabase, "\r"},
		{"database_nul", envFieldDatabase, "\x00"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db := &config.DatabaseConfig{Type: "postgresql", Host: "h", Port: 5432, Username: "u", Password: "p", Database: "d"}
			switch tc.field {
			case envFieldHost:
				db.Host += tc.badChar
			case envFieldUsername:
				db.Username += tc.badChar
			case envFieldPassword:
				db.Password += tc.badChar
			case envFieldDatabase:
				db.Database += tc.badChar
			}
			_, err := buildEnvironmentVariables(db)
			require.ErrorIs(t, err, ErrEnvFieldHasControlChar)
			assert.Contains(t, err.Error(), tc.field, "error should name the offending field")
		})
	}

	t.Run("error_does_not_echo_value", func(t *testing.T) {
		db := &config.DatabaseConfig{
			Type: "postgresql", Host: "h", Port: 5432, Username: "u",
			Password: "leakySecret\n", Database: "d",
		}
		_, err := buildEnvironmentVariables(db)
		require.Error(t, err)
		assert.NotContains(t, err.Error(), "leakySecret", "error message must not echo the offending value")
	})
}

func assertEnvVarsContain(t *testing.T, envVars, expected []string) {
	t.Helper()
	// When the test pins zero expected vars, also assert envVars is empty —
	// otherwise assert.Subset(envVars, []) is vacuously true and would let
	// non-prefixed leakage (e.g. HOST=foo) pass an unsupported-driver test.
	if len(expected) == 0 {
		assert.Empty(t, envVars, "envVars should be empty when no entries are expected")
		return
	}
	assert.Subset(t, envVars, expected, "envVars missing expected entries")
}

// assertEnvVarsLackPrefixes asserts no env var key starts with any of the
// forbidden prefixes — used to verify e.g. that an Oracle-only config did not
// leak any generic DB_ entries. The check is against the key portion only
// (everything before the first '='), so values that happen to contain a
// forbidden substring (e.g. a password "MyDB_Pass") do not false-positive.
func assertEnvVarsLackPrefixes(t *testing.T, envVars, prefixes []string) {
	t.Helper()
	for _, envVar := range envVars {
		key := strings.SplitN(envVar, "=", 2)[0]
		for _, prefix := range prefixes {
			assert.False(t, strings.HasPrefix(key, prefix),
				"Unexpected environment variable prefix '%s' found in key '%s' of '%s'", prefix, key, envVar)
		}
	}
}

func TestMigrateEdgeCases(t *testing.T) {
	if runtime.GOOS == windowsOS {
		t.Skip("shell script stub not supported on windows CI")
	}

	cfg := &config.Config{
		Database: config.DatabaseConfig{Type: "postgresql"},
		App:      config.AppConfig{Env: "test"},
	}

	fm := NewFlywayMigrator(cfg, logger.New("disabled", true))

	t.Run("migrate_with_nil_config_uses_default", func(t *testing.T) {
		stub, capturePath := createCommandCapturingStub(t, minimalMigrateSuccessJSON)
		tempDir := t.TempDir()
		configPath := filepath.Join(tempDir, "flyway.conf")
		migrationPath := filepath.Join(tempDir, "migrations")

		require.NoError(t, os.WriteFile(configPath, []byte(""), 0o644))
		require.NoError(t, os.MkdirAll(migrationPath, 0o755))

		fm.defaultConfig = func(*FlywayMigrator) *Config {
			return &Config{
				FlywayPath:    stub,
				ConfigPath:    configPath,
				MigrationPath: migrationPath,
				Timeout:       5 * time.Second,
				Environment:   cfg.App.Env,
			}
		}

		_, err := fm.Migrate(context.Background(), nil)
		assert.NoError(t, err)

		captured, readErr := os.ReadFile(capturePath)
		require.NoError(t, readErr)
		assert.Contains(t, string(captured), "migrate")
	})

	t.Run("migrate_command_execution_success", func(t *testing.T) {
		stub := createSimpleFlywayStub(t)
		mcfg := &Config{
			FlywayPath:    stub,
			ConfigPath:    filepath.Join(t.TempDir(), "flyway.conf"),
			MigrationPath: filepath.Join(t.TempDir(), "migrations"),
			Timeout:       10_000_000_000,
		}

		// Ensure paths exist
		require.NoError(t, os.WriteFile(mcfg.ConfigPath, []byte(""), 0o644))
		require.NoError(t, os.MkdirAll(mcfg.MigrationPath, 0o755))

		ctx := context.Background()
		_, err := fm.Migrate(ctx, mcfg)
		assert.NoError(t, err)
	})
}

func TestInfoEdgeCases(t *testing.T) {
	if runtime.GOOS == windowsOS {
		t.Skip("shell script stub not supported on windows CI")
	}

	cfg := &config.Config{
		Database: config.DatabaseConfig{Type: "oracle"},
		App:      config.AppConfig{Env: "production"},
	}

	fm := NewFlywayMigrator(cfg, logger.New("disabled", true))

	t.Run("info_with_nil_config_executes_command", func(t *testing.T) {
		stub, capturePath := createCommandCapturingStub(t, "")
		tempDir := t.TempDir()
		configPath := filepath.Join(tempDir, "flyway.conf")
		migrationPath := filepath.Join(tempDir, "migrations")

		require.NoError(t, os.WriteFile(configPath, []byte(""), 0o644))
		require.NoError(t, os.MkdirAll(migrationPath, 0o755))

		fm.defaultConfig = func(*FlywayMigrator) *Config {
			return &Config{
				FlywayPath:    stub,
				ConfigPath:    configPath,
				MigrationPath: migrationPath,
				Timeout:       5 * time.Second,
				Environment:   cfg.App.Env,
			}
		}

		err := fm.Info(context.Background(), nil)
		assert.NoError(t, err)

		captured, readErr := os.ReadFile(capturePath)
		require.NoError(t, readErr)
		assert.Contains(t, string(captured), "info")
	})
}

func TestValidateEdgeCases(t *testing.T) {
	if runtime.GOOS == windowsOS {
		t.Skip("shell script stub not supported on windows CI")
	}

	cfg := &config.Config{
		Database: config.DatabaseConfig{Type: "postgresql"},
		App:      config.AppConfig{Env: "staging"},
	}

	fm := NewFlywayMigrator(cfg, logger.New("disabled", true))

	t.Run("validate_command_execution", func(t *testing.T) {
		stub := createSimpleFlywayStub(t)
		mcfg := &Config{
			FlywayPath:    stub,
			ConfigPath:    filepath.Join(t.TempDir(), "flyway.conf"),
			MigrationPath: filepath.Join(t.TempDir(), "migrations"),
			Timeout:       10_000_000_000,
		}

		// Ensure paths exist
		require.NoError(t, os.WriteFile(mcfg.ConfigPath, []byte(""), 0o644))
		require.NoError(t, os.MkdirAll(mcfg.MigrationPath, 0o755))

		ctx := context.Background()
		err := fm.Validate(ctx, mcfg)
		assert.NoError(t, err)
	})

	t.Run("validate_with_nil_config_executes_command", func(t *testing.T) {
		stub, capturePath := createCommandCapturingStub(t, "")
		tempDir := t.TempDir()
		configPath := filepath.Join(tempDir, "flyway.conf")
		migrationPath := filepath.Join(tempDir, "migrations")

		require.NoError(t, os.WriteFile(configPath, []byte(""), 0o644))
		require.NoError(t, os.MkdirAll(migrationPath, 0o755))

		fm.defaultConfig = func(*FlywayMigrator) *Config {
			return &Config{
				FlywayPath:    stub,
				ConfigPath:    configPath,
				MigrationPath: migrationPath,
				Timeout:       5 * time.Second,
				Environment:   cfg.App.Env,
			}
		}

		err := fm.Validate(context.Background(), nil)
		assert.NoError(t, err)

		captured, readErr := os.ReadFile(capturePath)
		require.NoError(t, readErr)
		assert.Contains(t, string(captured), "validate")
	})
}

func TestSafeVendorSegmentKnownPasses(t *testing.T) {
	assert.Equal(t, "postgresql", safeVendorSegment("postgresql", ""))
	assert.Equal(t, "oracle", safeVendorSegment("oracle", ""))
}

func TestSafeVendorSegmentUnknownFallsBack(t *testing.T) {
	// Path-traversal attempt falls back to the migrator's own configured vendor.
	assert.Equal(t, "postgresql", safeVendorSegment("../../tmp", "postgresql"))
	assert.Equal(t, "oracle", safeVendorSegment("rm -rf /", "oracle"))
}

func TestSafeVendorSegmentBothUnknownYieldsSentinel(t *testing.T) {
	assert.Equal(t, "unknown", safeVendorSegment("../../tmp", ""))
	assert.Equal(t, "unknown", safeVendorSegment("../../tmp", "garbage"))
}

func TestRedactPasswordReplacesAll(t *testing.T) {
	db := &config.DatabaseConfig{Password: "s3cr3tValue"}
	out := redactPassword("connecting with s3cr3tValue to host (s3cr3tValue)", db)
	assert.Equal(t, "connecting with [REDACTED] to host ([REDACTED])", out)
}

func TestRedactPasswordEmptyOrNilLeavesOutputUntouched(t *testing.T) {
	assert.Equal(t, "no secrets here", redactPassword("no secrets here", nil))
	assert.Equal(t, "no secrets here", redactPassword("no secrets here", &config.DatabaseConfig{}))
}

func TestRedactPasswordHandlesPercentEncodedJDBCURL(t *testing.T) {
	// JDBC URLs percent-encode reserved characters in the userinfo segment.
	// A password like "p@ssw0rd!" appears as "p%40ssw0rd%21" in Flyway error
	// output; the raw substring will not match, but PathEscape will.
	db := &config.DatabaseConfig{Password: "p@ssw0rd!"}
	output := "Unable to connect to jdbc:postgresql://user:p%40ssw0rd%21@host:5432/db"
	out := redactPassword(output, db)
	assert.NotContains(t, out, "p%40ssw0rd%21")
	assert.NotContains(t, out, "p@ssw0rd!")
	assert.Contains(t, out, "[REDACTED]")
}

func TestRedactPasswordHandlesQueryEncodedForm(t *testing.T) {
	// Form-encoded variant uses '+' for spaces; redaction must catch both.
	db := &config.DatabaseConfig{Password: "pass word!"}
	queryForm := "?password=" + url.QueryEscape(db.Password)
	out := redactPassword("dump: "+queryForm, db)
	assert.NotContains(t, out, url.QueryEscape(db.Password))
	assert.Contains(t, out, "[REDACTED]")
}

func TestRedactPasswordSuppressesOutputForShortPasswords(t *testing.T) {
	// Short passwords substring-collide with unrelated bytes; we drop the
	// whole output instead of risking partial redaction.
	db := &config.DatabaseConfig{Password: "abc"}
	out := redactPassword("connection refused: jdbc:postgresql://user:abc@host", db)
	assert.Equal(t, outputSuppressedSentinel, out)
	assert.NotContains(t, out, "abc")
}

func TestRedactPasswordKeepsAlphanumericLongPasswordsRedactedRaw(t *testing.T) {
	// Pure-alphanumeric passwords need no encoding; raw substring redaction
	// suffices.
	db := &config.DatabaseConfig{Password: "longalphanumeric1"}
	out := redactPassword("error: jdbc:postgresql://user:longalphanumeric1@host/db", db)
	assert.Contains(t, out, "[REDACTED]")
	assert.NotContains(t, out, "longalphanumeric1")
}

// jsonStringEscaped returns s as it would appear inside a JSON string value
// (backslash-escaped), without the surrounding quotes.
func jsonStringEscaped(t *testing.T, s string) string {
	t.Helper()
	b, err := json.Marshal(s)
	require.NoError(t, err)
	return string(b[1 : len(b)-1])
}

func TestRedactPasswordHandlesJSONEscapedForm(t *testing.T) {
	// A password containing " or \ appears backslash-escaped when Flyway embeds a
	// connection string inside a JSON error envelope. The raw and URL-encoded
	// forms won't match that; the JSON-escaped form must.
	db := &config.DatabaseConfig{Password: `pw"or\d123`}
	esc := jsonStringEscaped(t, db.Password)
	output := `{"error":{"message":"connect failed at ` + esc + ` now"}}`
	out := redactPassword(output, db)
	assert.NotContains(t, out, esc, "the JSON-escaped password form must be redacted")
	assert.Contains(t, out, "[REDACTED]")
}

func TestRedactPasswordDoesNotCorruptValidJSONOnNumericCollision(t *testing.T) {
	// An all-digit password that equals a JSON number token would, under a blind
	// ReplaceAll, corrupt an otherwise-valid envelope and turn a real success into
	// an unparsable-output failure (#673). Redaction must not break valid JSON.
	db := &config.DatabaseConfig{Password: "12345678"}
	output := `{"operation":"migrate","success":true,"totalMigrationTime":12345678}`
	out := redactPassword(output, db)
	assert.Equal(t, output, out,
		"a numeric password matching a JSON number token must not corrupt a valid envelope")
}

func TestRedactPasswordRedactsWithinJSONStringValue(t *testing.T) {
	// The credential legitimately appears inside a JSON string (an error envelope
	// echoing a JDBC URL); redaction stays inside the string, so the envelope
	// remains valid and the guard must NOT revert it.
	db := &config.DatabaseConfig{Password: "longpassword1"}
	output := `{"error":{"message":"jdbc:postgresql://u:longpassword1@h/db"}}`
	out := redactPassword(output, db)
	assert.NotContains(t, out, "longpassword1")
	assert.Contains(t, out, "[REDACTED]")
	assert.True(t, json.Valid([]byte(out)))
}

func TestRedactPasswordRedactsNumericPasswordInsideJSONString(t *testing.T) {
	// Safety: a numeric password that is a real credential inside a string value
	// must still be redacted.
	db := &config.DatabaseConfig{Password: "12345678"}
	output := `{"error":{"message":"auth failed for 12345678"}}`
	out := redactPassword(output, db)
	assert.NotContains(t, out, "12345678", "a numeric credential inside a string must not leak")
	assert.Contains(t, out, "[REDACTED]")
}

func TestRedactPasswordRedactsStringCredentialDespiteNumericCollision(t *testing.T) {
	// The dual-occurrence case: a numeric password appears BOTH as a real
	// credential inside a string value AND as a bare number token. Redaction must
	// mask the in-string credential while leaving the number token intact — no
	// leak, no corruption. (A revert-on-broken-JSON strategy would re-expose the
	// credential here; string-scoped redaction does not.)
	db := &config.DatabaseConfig{Password: "12345678"}
	output := `{"msg":"connect string user:12345678@host","totalMigrationTime":12345678}`
	out := redactPassword(output, db)
	assert.NotContains(t, out, "user:12345678@host", "the in-string credential must be redacted")
	assert.Contains(t, out, `"totalMigrationTime":12345678`, "the number token must be left intact")
	assert.True(t, json.Valid([]byte(out)))
}

func TestMigrateRequestsJSONOutputAndParsesResult(t *testing.T) {
	if runtime.GOOS == windowsOS {
		t.Skip("shell script stub not supported on windows CI")
	}

	payload := readFixture(t, "migrate_success.json")

	cfg := &config.Config{
		Database: config.DatabaseConfig{
			Type: "postgresql", Host: "h", Port: 15432,
			Username: "u", Password: "longenough-pw", Database: "db",
		},
		App: config.AppConfig{Env: "test"},
	}
	fm := NewFlywayMigrator(cfg, logger.New("disabled", true))

	stub, capturePath := createCommandCapturingStub(t, payload)
	mcfg := &Config{
		FlywayPath:    stub,
		ConfigPath:    filepath.Join(t.TempDir(), "flyway.conf"),
		MigrationPath: filepath.Join(t.TempDir(), "migrations"),
		Timeout:       10 * time.Second,
		Environment:   cfg.App.Env,
	}
	require.NoError(t, os.WriteFile(mcfg.ConfigPath, []byte(""), 0o644))
	require.NoError(t, os.MkdirAll(mcfg.MigrationPath, 0o755))

	result, err := fm.Migrate(context.Background(), mcfg)
	require.NoError(t, err)

	captured, readErr := os.ReadFile(capturePath)
	require.NoError(t, readErr)
	args := string(captured)
	assert.Contains(t, args, "-outputType=json", "migrate verb must request JSON output to populate Result")
	assert.Contains(t, args, "migrate")

	assert.True(t, result.Success)
	assert.Equal(t, []string{"1", "2"}, result.AppliedVersions)
	assert.Equal(t, "2", result.EndingVersion)
	assert.Equal(t, int64(8), result.DurationMillis)
}

func TestInfoAndValidateDoNotRequestJSONOutput(t *testing.T) {
	if runtime.GOOS == windowsOS {
		t.Skip("shell script stub not supported on windows CI")
	}

	cfg := &config.Config{
		Database: config.DatabaseConfig{
			Type: "postgresql", Host: "h", Port: 15432,
			Username: "u", Password: "longenough-pw", Database: "db",
		},
		App: config.AppConfig{Env: "test"},
	}
	fm := NewFlywayMigrator(cfg, logger.New("disabled", true))

	for _, tc := range []struct {
		name string
		call func(*Config) error
	}{
		{"info", func(c *Config) error { return fm.Info(context.Background(), c) }},
		{"validate", func(c *Config) error { return fm.Validate(context.Background(), c) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stub, capturePath := createCommandCapturingStub(t, "")
			mcfg := &Config{
				FlywayPath:    stub,
				ConfigPath:    filepath.Join(t.TempDir(), "flyway.conf"),
				MigrationPath: filepath.Join(t.TempDir(), "migrations"),
				Timeout:       10 * time.Second,
			}
			require.NoError(t, os.WriteFile(mcfg.ConfigPath, []byte(""), 0o644))
			require.NoError(t, os.MkdirAll(mcfg.MigrationPath, 0o755))

			require.NoError(t, tc.call(mcfg))

			captured, readErr := os.ReadFile(capturePath)
			require.NoError(t, readErr)
			assert.NotContains(t, string(captured), "-outputType=json",
				"info/validate keep default pretty-printed output for operator-facing sessions")
		})
	}
}

// minimalMigrateSuccessJSON is the smallest Flyway envelope that parses to a
// successful Result — enough for stubs that only need "migrate reported success".
const minimalMigrateSuccessJSON = `{"operation":"migrate","success":true,"targetSchemaVersion":"2","flywayVersion":"12.8.1"}`

// newMigrateFixture builds a FlywayMigrator plus a ready-to-run migrate Config
// pointed at stub. password sets the migrator's DB password, which drives
// redactPassword: pass a >=8-char value to keep output un-suppressed, or a
// shorter one to exercise the redaction-suppression path (#673).
func newMigrateFixture(t *testing.T, stub, password string) (*FlywayMigrator, *Config) {
	t.Helper()
	cfg := &config.Config{
		Database: config.DatabaseConfig{Type: "postgresql", Password: password},
		App:      config.AppConfig{Env: "test"},
	}
	fm := NewFlywayMigrator(cfg, logger.New("disabled", true))
	dir := t.TempDir()
	mcfg := &Config{
		FlywayPath:    stub,
		ConfigPath:    filepath.Join(dir, "flyway.conf"),
		MigrationPath: filepath.Join(dir, "migrations"),
		Timeout:       10 * time.Second,
		Environment:   cfg.App.Env,
	}
	require.NoError(t, os.WriteFile(mcfg.ConfigPath, []byte(""), 0o644))
	require.NoError(t, os.MkdirAll(mcfg.MigrationPath, 0o755))
	return fm, mcfg
}

func TestMigrateReturnsErrorOnUnparseableOutput(t *testing.T) {
	if runtime.GOOS == windowsOS {
		t.Skip("shell script stub not supported on windows CI")
	}
	stub, _ := createCommandCapturingStub(t, "SLF4J: warning\nboom, not json\n")
	fm, mcfg := newMigrateFixture(t, stub, "longenough-pw")
	_, err := fm.Migrate(context.Background(), mcfg)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrFlywayOutputUnparsed)
	assert.ErrorIs(t, err, errEmptyFlywayOutput, "no-'{' output maps to the empty sentinel underneath")
}

func TestMigrateReturnsErrorOnMalformedJSON(t *testing.T) {
	if runtime.GOOS == windowsOS {
		t.Skip("shell script stub not supported on windows CI")
	}
	stub, _ := createCommandCapturingStub(t, `{"success": notabool}`)
	fm, mcfg := newMigrateFixture(t, stub, "longenough-pw")
	_, err := fm.Migrate(context.Background(), mcfg)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrFlywayOutputUnparsed)
	assert.NotErrorIs(t, err, errEmptyFlywayOutput, "malformed JSON is distinct from empty output")
	assert.NotErrorIs(t, err, ErrFlywayReportedFailure)
}

func TestMigrateReturnsErrorOnErrorEnvelopeExitZero(t *testing.T) {
	if runtime.GOOS == windowsOS {
		t.Skip("shell script stub not supported on windows CI")
	}
	stub, _ := createCommandCapturingStub(t, readFixture(t, "migrate_checksum_fail.json"))
	fm, mcfg := newMigrateFixture(t, stub, "longenough-pw")
	res, err := fm.Migrate(context.Background(), mcfg)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrFlywayReportedFailure, "a success:false envelope must surface even at exit 0")
	assert.False(t, res.Success)
	assert.Equal(t, "VALIDATE_ERROR", res.ErrorCode)
	assert.NotContains(t, err.Error(), "checksum mismatch", "free-text ErrorMessage must not leak into the error")
	assert.NotContains(t, err.Error(), "longenough-pw", "password must never appear in the error")
}

func TestMigrateRejectsShortPassword(t *testing.T) {
	if runtime.GOOS == windowsOS {
		t.Skip("shell script stub not supported on windows CI")
	}
	// A <8-char password can't be safely redacted from Flyway output, so the
	// migrate path rejects it before running Flyway rather than suppressing the
	// output and mis-reporting the outcome (#675).
	stub, _ := createCommandCapturingStub(t, minimalMigrateSuccessJSON)
	fm, mcfg := newMigrateFixture(t, stub, "pw12345") // 7 bytes, distinct from the error text
	_, err := fm.Migrate(context.Background(), mcfg)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrDatabasePasswordTooShort)
	assert.NotErrorIs(t, err, ErrFlywayOutputUnparsed, "rejected before Flyway runs, not a parse failure")
	assert.NotContains(t, err.Error(), "pw12345", "the error must not echo the password")
}

func TestMigrateForRejectsShortTenantPassword(t *testing.T) {
	if runtime.GOOS == windowsOS {
		t.Skip("shell script stub not supported on windows CI")
	}
	// Per-tenant coverage: a tenant DatabaseConfig (from a store / AWS secrets)
	// never passes config.Validate(), so MigrateFor must reject a short password.
	stub, _ := createCommandCapturingStub(t, minimalMigrateSuccessJSON)
	fm, mcfg := newMigrateFixture(t, stub, "longenough-pw")
	tenantDB := &config.DatabaseConfig{Type: "postgresql", Password: "tiny"}
	_, err := fm.MigrateFor(context.Background(), tenantDB, mcfg)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrDatabasePasswordTooShort)
}

func TestMigrateNoopStillSucceeds(t *testing.T) {
	if runtime.GOOS == windowsOS {
		t.Skip("shell script stub not supported on windows CI")
	}
	// Non-breakage guard: a genuine no-op emits valid JSON, so strict parsing
	// must NOT turn "nothing to migrate" into a failure.
	stub, _ := createCommandCapturingStub(t, readFixture(t, "migrate_noop.json"))
	fm, mcfg := newMigrateFixture(t, stub, "longenough-pw")
	res, err := fm.Migrate(context.Background(), mcfg)
	require.NoError(t, err)
	assert.True(t, res.Success)
	assert.Equal(t, "2", res.EndingVersion)
}

// createOrphanSpawningFlywayStub models the orphaned-JVM shape: the stub
// spawns a background grandchild that inherits its stdout (the output pipe),
// sleeps past the caller's timeout, and then writes a marker file. The stub
// itself also sleeps past the timeout before exiting.
func createOrphanSpawningFlywayStub(t *testing.T, markerPath string, sleepSeconds int) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "flyway-orphan.sh")
	content := fmt.Sprintf(`#!/bin/sh
( sleep %d; touch '%s' ) &
sleep %d
exit 0
`, sleepSeconds, markerPath, sleepSeconds)
	require.NoError(t, os.WriteFile(path, []byte(content), 0o755))
	return path
}

// TestRunFlywayCommandKillsChildProcessGroup guards against the orphaned-JVM
// regression: without a process-group kill, a grandchild holding the output
// pipe either blocks CombinedOutput indefinitely or keeps running (and keeps
// mutating the schema) after the timeout fires. See createOrphanSpawningFlywayStub.
func TestRunFlywayCommandKillsChildProcessGroup(t *testing.T) {
	if runtime.GOOS == windowsOS {
		t.Skip("process-group kill is unix-only")
	}

	// Must outlast cfg.Timeout below so a working group-kill catches the
	// grandchild mid-sleep, before it writes its marker.
	const grandchildSleepSeconds = 1

	markerPath := filepath.Join(t.TempDir(), "grandchild-survived.marker")
	stub := createOrphanSpawningFlywayStub(t, markerPath, grandchildSleepSeconds)

	cfg := &config.Config{
		Database: config.DatabaseConfig{Type: "postgresql"},
		App:      config.AppConfig{Env: "test"},
	}
	fm := NewFlywayMigrator(cfg, logger.New("disabled", true))

	mcfg := &Config{
		FlywayPath:    stub,
		ConfigPath:    filepath.Join(t.TempDir(), "flyway.conf"),
		MigrationPath: filepath.Join(t.TempDir(), "migrations"),
		Timeout:       500 * time.Millisecond,
	}
	require.NoError(t, os.WriteFile(mcfg.ConfigPath, []byte(""), 0o644))
	require.NoError(t, os.MkdirAll(mcfg.MigrationPath, 0o755))

	done := make(chan error, 1)
	go func() {
		_, err := fm.Migrate(context.Background(), mcfg)
		done <- err
	}()

	select {
	case err := <-done:
		require.Error(t, err)
		require.ErrorIs(t, err, ErrFlywayTimeout)
		assert.Contains(t, err.Error(), "schema state is unknown")
		// The kill scope is build-tagged: the message must report what this platform
		// actually terminated, never a hardcoded process-group claim (false on Windows).
		assert.Contains(t, err.Error(), killScopeDesc)
	case <-time.After(mcfg.Timeout + flywayKillGraceDelay + 5*time.Second):
		t.Fatal("Migrate did not return within the guard deadline — process-group kill regressed to a hang")
	}

	// Give the grandchild's own sleep time to elapse in case it survived a
	// (supposedly) working group kill, then confirm it never wrote its marker.
	time.Sleep(grandchildSleepSeconds*time.Second + 500*time.Millisecond)
	_, statErr := os.Stat(markerPath)
	assert.True(t, os.IsNotExist(statErr), "grandchild marker file exists — orphaned grandchild survived the timeout kill")
}

// TestRunFlywayCommandParentCancelSignalsUnknownState guards the cancel-kill
// class: a fail-fast sibling abort in a parallel MigrateAll (or an operator
// Ctrl-C) kills the subprocess via parent-context cancellation, not the
// migration's own deadline. The SIGKILL leaves the schema state equally
// unknown as a timeout does, so the cancel path must classify distinctly
// (ErrFlywayCanceled) rather than collapsing into the generic
// "flyway command failed" catch-all — and it must NOT also match
// ErrFlywayTimeout, since nothing timed out.
func TestRunFlywayCommandParentCancelSignalsUnknownState(t *testing.T) {
	if runtime.GOOS == windowsOS {
		t.Skip("shell script stub not supported on windows CI")
	}

	stub := createSlowFlywayStub(t, 5) // 5 second delay, well past the parent cancel below
	cfg := &config.Config{
		Database: config.DatabaseConfig{Type: "postgresql"},
		App:      config.AppConfig{Env: "test"},
	}
	sink := newRecordingSink()
	fm := NewFlywayMigrator(cfg, logger.New("disabled", true)).WithAuditRecorder(sink)
	t.Cleanup(func() { _ = fm.Close(context.Background()) })

	mcfg := &Config{
		FlywayPath:    stub,
		ConfigPath:    filepath.Join(t.TempDir(), "flyway.conf"),
		MigrationPath: filepath.Join(t.TempDir(), "migrations"),
		Timeout:       10 * time.Second, // must not fire before the parent cancel below
		Environment:   cfg.App.Env,
		Audit:         AuditContext{Principal: "deployer@example.com", Target: "tenant_acme"},
	}
	require.NoError(t, os.WriteFile(mcfg.ConfigPath, []byte(""), 0o644))
	require.NoError(t, os.MkdirAll(mcfg.MigrationPath, 0o755))

	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(300*time.Millisecond, cancel)

	done := make(chan error, 1)
	go func() {
		_, err := fm.Migrate(ctx, mcfg)
		done <- err
	}()

	select {
	case err := <-done:
		require.Error(t, err)
		require.ErrorIs(t, err, ErrFlywayCanceled)
		assert.NotErrorIs(t, err, ErrFlywayTimeout, "a parent-cancel kill must not also classify as a timeout")
		assert.Contains(t, err.Error(), "schema state is unknown")
		// The kill scope is build-tagged: the message must report what this platform
		// actually terminated, never a hardcoded process-group claim (false on Windows).
		assert.Contains(t, err.Error(), killScopeDesc)
	case <-time.After(300*time.Millisecond + flywayKillGraceDelay + 5*time.Second):
		t.Fatal("Migrate did not return within the guard deadline — cancel-kill classification regressed to a hang")
	}

	sink.waitForFirst(t, 2*time.Second)
	events := sink.snapshot()
	require.Len(t, events, 1)

	got := events[0]
	assert.Equal(t, AuditOutcomeFailed, got.Outcome)
	assert.Equal(t, "true", got.Attributes["migration.canceled"],
		"a parent-canceled run must be machine-identifiable as canceled")
	_, hasTimedOut := got.Attributes["migration.timed_out"]
	assert.False(t, hasTimedOut, "a cancel-kill must not also assert migration.timed_out")
}
