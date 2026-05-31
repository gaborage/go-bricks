package main

import (
	"fmt"
	"io"
	"os"

	"github.com/spf13/cobra"

	"github.com/gaborage/go-bricks/tools/openapi/internal/commands"
)

var version = "dev" // Will be set during build via -ldflags "-X main.version=..."

// buildRootCmd constructs the go-bricks-openapi root command with all subcommands.
// The version (injected at build time) is surfaced by the --version flag and the
// version subcommand.
func buildRootCmd() *cobra.Command {
	rootCmd := &cobra.Command{
		Use:   "go-bricks-openapi",
		Short: "Generate OpenAPI specs for go-bricks services",
		Long: `Static analysis-based OpenAPI 3.0.1 specification generator for go-bricks applications.

This tool analyzes go-bricks services and generates OpenAPI specifications automatically
from route registrations, type definitions, and validation tags.`,
		Version: version,
		// Errors and usage are reported once by run() (a single "Error:" line, no
		// usage dump) rather than by cobra's default handler on a RunE error.
		SilenceUsage:  true,
		SilenceErrors: true,
	}

	rootCmd.AddCommand(
		commands.NewGenerateCommand(),
		commands.NewDoctorCommand(),
		commands.NewVersionCommand(version),
	)

	return rootCmd
}

// run is the testable seam behind main(): it builds the CLI, executes it against
// args (typically os.Args[1:]) writing to stdout/stderr, and returns the process
// exit code. A non-nil command error prints a single "Error:" line to stderr and
// returns 1.
func run(args []string, stdout, stderr io.Writer) int {
	rootCmd := buildRootCmd()
	rootCmd.SetArgs(args)
	rootCmd.SetOut(stdout)
	rootCmd.SetErr(stderr)

	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintf(stderr, "Error: %v\n", err)
		return 1
	}
	return 0
}

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}
