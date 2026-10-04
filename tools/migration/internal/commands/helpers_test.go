package commands

import (
	"os"
	"testing"
)

// TestMain clears the operator environment variables that change what every
// action does, so a developer shell exporting one cannot flip unrelated tests.
// A test that needs one sets it with t.Setenv.
func TestMain(m *testing.M) {
	for _, k := range []string{envSharedMigrator, envMigratorUser, envMigratorPassword} {
		if err := os.Unsetenv(k); err != nil {
			panic(err)
		}
	}
	os.Exit(m.Run())
}
