package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"go-bricks/tools/openapi/internal/commands"
)

var version = "dev" // Will be set during build

func main() {
	rootCmd := &cobra.Command{
		Use:   "go-bricks-openapi",
		Short: "Generate OpenAPI specs for go-bricks services",
		Long: `Static analysis-based OpenAPI 3.0.1 specification generator for go-bricks applications.

This tool analyzes go-bricks services and generates OpenAPI specifications automatically
from route registrations, type definitions, and validation tags.`,
		Version: version,
	}

	// Add commands
	rootCmd.AddCommand(
		commands.NewGenerateCommand(),
		commands.NewDoctorCommand(),
		commands.NewVersionCommand(version),
	)

	// Execute
	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}