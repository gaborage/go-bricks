package commands

import (
	"bytes"
	"io"
	"os"
	"runtime"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Test constants to avoid string duplication
const (
	builtWithPrefix    = "Built with "
	openAPIVersionLine = "OpenAPI specification version: 3.0.1\n"
)

func TestNewVersionCommand(t *testing.T) {
	tests := []struct {
		name           string
		version        string
		expectedOutput string
	}{
		{
			name:    "development version",
			version: "dev",
			expectedOutput: "go-bricks-openapi version dev\n" +
				builtWithPrefix + runtime.Version() + " " + runtime.GOOS + "/" + runtime.GOARCH + "\n" +
				openAPIVersionLine,
		},
		{
			name:    "release version",
			version: "v1.2.3",
			expectedOutput: "go-bricks-openapi version v1.2.3\n" +
				builtWithPrefix + runtime.Version() + " " + runtime.GOOS + "/" + runtime.GOARCH + "\n" +
				openAPIVersionLine,
		},
		{
			name:    "empty version",
			version: "",
			expectedOutput: "go-bricks-openapi version \n" +
				builtWithPrefix + runtime.Version() + " " + runtime.GOOS + "/" + runtime.GOARCH + "\n" +
				openAPIVersionLine,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Capture stdout
			old := os.Stdout
			r, w, _ := os.Pipe()
			os.Stdout = w

			// Create and execute command
			cmd := NewVersionCommand(tt.version)
			require.NotNil(t, cmd)

			// Verify command properties
			assert.Equal(t, "version", cmd.Use)
			assert.Equal(t, "Show version information", cmd.Short)
			assert.Equal(t, "Display version information for go-bricks-openapi tool", cmd.Long)
			assert.NotNil(t, cmd.Run)

			// Execute command
			err := cmd.Execute()
			require.NoError(t, err)

			// Restore stdout and capture output
			w.Close()
			os.Stdout = old

			out, _ := io.ReadAll(r)
			output := string(out)

			assert.Equal(t, tt.expectedOutput, output)
		})
	}
}

func TestPrintVersion(t *testing.T) {
	tests := []struct {
		name    string
		version string
	}{
		{
			name:    "standard version",
			version: "v1.0.0",
		},
		{
			name:    "development version",
			version: "dev",
		},
		{
			name:    "pre-release version",
			version: "v1.0.0-rc1",
		},
		{
			name:    "empty version",
			version: "",
		},
		{
			name:    "version with metadata",
			version: "v1.0.0+build.123",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Capture stdout
			old := os.Stdout
			r, w, _ := os.Pipe()
			os.Stdout = w

			// Call function
			printVersion(tt.version)

			// Restore stdout and capture output
			w.Close()
			os.Stdout = old

			out, _ := io.ReadAll(r)
			output := string(out)

			// Verify output format
			lines := strings.Split(strings.TrimSpace(output), "\n")
			require.Len(t, lines, 3)

			// Check version line
			assert.Equal(t, "go-bricks-openapi version "+tt.version, lines[0])

			// Check build info line
			expectedBuildInfo := builtWithPrefix + runtime.Version() + " " + runtime.GOOS + "/" + runtime.GOARCH
			assert.Equal(t, expectedBuildInfo, lines[1])

			// Check OpenAPI version line
			assert.Equal(t, strings.TrimSuffix(openAPIVersionLine, "\n"), lines[2])
		})
	}
}

func TestVersionCommandIntegration(t *testing.T) {
	// Test that version command works as a subcommand
	rootCmd := &cobra.Command{Use: "test-root"}
	versionCmd := NewVersionCommand("test-version")
	rootCmd.AddCommand(versionCmd)

	// Capture stdout
	var buf bytes.Buffer
	rootCmd.SetOut(&buf)
	rootCmd.SetErr(&buf)

	// Redirect stdout for printVersion
	old := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	// Execute version subcommand
	rootCmd.SetArgs([]string{"version"})
	err := rootCmd.Execute()
	require.NoError(t, err)

	// Restore stdout and get output
	w.Close()
	os.Stdout = old
	out, _ := io.ReadAll(r)
	output := string(out)

	// Verify output contains expected elements
	assert.Contains(t, output, "go-bricks-openapi version test-version")
	assert.Contains(t, output, builtWithPrefix+runtime.Version())
	assert.Contains(t, output, strings.TrimSuffix(openAPIVersionLine, "\n"))
}
