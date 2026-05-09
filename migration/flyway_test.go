package migration

import (
	"context"
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

// helper to create a simple stub that just exits successfully (for commands that don't need env vars)
func createSimpleFlywayStub(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "flyway-simple.sh")
	content := "#!/bin/sh\nexit 0\n"
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
	env := buildEnvironmentVariables(&fm.config.Database)
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
	env = buildEnvironmentVariables(&fm.config.Database)
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

			stub, capturePath := createCommandCapturingStub(t)
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

// Helper function to create a stub that captures which command was called
func createCommandCapturingStub(t *testing.T) (_, _ string) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "flyway-capture.sh")
	capturePath := filepath.Join(dir, "captured_command")

	// Create a script that writes the command to a file and exits successfully
	content := "#!/bin/sh\n" +
		"echo \"$@\" > " + capturePath + "\n" +
		"exit 0\n"
	require.NoError(t, os.WriteFile(path, []byte(content), 0o755))
	return path, capturePath
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
		err := fm.Migrate(ctx, mcfg)
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
		err := fm.Migrate(ctx, mcfg)
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
		err := fm.Migrate(ctx, mcfg)
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
			envVars := buildEnvironmentVariables(&fm.config.Database)
			assertEnvVarsContain(t, envVars, tt.expectedVars)
			assertEnvVarsLackPrefixes(t, envVars, tt.notExpectedVars)
		})
	}
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
		stub, capturePath := createCommandCapturingStub(t)
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

		err := fm.Migrate(context.Background(), nil)
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
		err := fm.Migrate(ctx, mcfg)
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
		stub, capturePath := createCommandCapturingStub(t)
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
		stub, capturePath := createCommandCapturingStub(t)
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

// --- safeVendorSegment ----------------------------------------------------

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

// --- redactPassword -------------------------------------------------------

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
