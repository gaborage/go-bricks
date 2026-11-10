package main

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"runtime/debug"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gaborage/go-bricks/migration"
	"github.com/gaborage/go-bricks/tools/migration/internal/commands"
)

func TestResolveVersion(t *testing.T) {
	bi := func(v string) func() (*debug.BuildInfo, bool) {
		return func() (*debug.BuildInfo, bool) {
			return &debug.BuildInfo{Main: debug.Module{Version: v}}, true
		}
	}
	none := func() (*debug.BuildInfo, bool) { return nil, false }

	tests := []struct {
		name    string
		ldflags string
		read    func() (*debug.BuildInfo, bool)
		want    string
	}{
		{name: "ldflags_wins", ldflags: "v0.38.0", read: bi("v0.39.0"), want: "v0.38.0"},
		{name: "buildinfo_when_dev", ldflags: "dev", read: bi("v0.39.0"), want: "v0.39.0"},
		{name: "ignore_devel_pseudo", ldflags: "dev", read: bi("(devel)"), want: "dev"},
		{name: "no_buildinfo", ldflags: "dev", read: none, want: "dev"},
		{name: "empty_ldflags_buildinfo", ldflags: "", read: bi("v0.39.0"), want: "v0.39.0"},
		{name: "empty_ldflags_no_buildinfo", ldflags: "", read: none, want: "dev"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := resolveVersion(tc.ldflags, tc.read); got != tc.want {
				t.Errorf("resolveVersion(%q) = %q, want %q", tc.ldflags, got, tc.want)
			}
		})
	}
}

// run's own job is the stderr line and the delegation; exit_test.go owns the
// classification table.
func TestRunMapsErrorToExitCode(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		want     int
		wantText string
	}{
		{name: "clean_run_is_silent", err: nil, want: commands.ExitClean},
		{
			name: "error_is_printed_and_classified",
			err:  fmt.Errorf("list tenants: %w", migration.ErrNothingAttempted),
			want: commands.ExitNothingAttempted, wantText: "no tenant attempted",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			root := &cobra.Command{Use: "root", SilenceUsage: true, SilenceErrors: true}
			root.RunE = func(*cobra.Command, []string) error { return tc.err }
			root.SetArgs(nil)
			root.SetOut(&bytes.Buffer{})

			var stderr bytes.Buffer
			assert.Equal(t, tc.want, run(root, &stderr))
			if tc.wantText == "" {
				assert.Empty(t, stderr.String())
				return
			}
			assert.Contains(t, stderr.String(), tc.wantText)
		})
	}
}

func TestRunRendersUnparseableControlPlaneDSNWithoutIt(t *testing.T) {
	yaml := "multitenant:\n  enabled: true\n  tenants:\n    cpctl:\n      database:\n" +
		"        type: postgresql\n" +
		"        connectionstring: \"host=cpdb.example.internal user=u password = FAKEpw123 sslmode=verify-ful\"\n"
	path := filepath.Join(t.TempDir(), "t.yaml")
	require.NoError(t, os.WriteFile(path, []byte(yaml), 0o600))

	root := commands.NewRootCommand()
	root.AddCommand(commands.NewQuiesceCommand())
	root.SetArgs([]string{"quiesce", "status", "--tenant", "cpctl", "--source-config", path, "--credentials-from", "config-file"})
	root.SetOut(&bytes.Buffer{})

	var stderr bytes.Buffer
	assert.Equal(t, commands.ExitNothingAttempted, run(root, &stderr))
	assert.Equal(t,
		"Error: migration: no tenant attempted: connect control-plane db for tenant \"cpctl\": failed to parse PostgreSQL connection string\n",
		stderr.String())
}
