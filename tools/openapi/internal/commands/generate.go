package commands

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"
)

// GenerateOptions holds options for the generate command
type GenerateOptions struct {
	ProjectRoot string
	OutputFile  string
	Format      string
	Verbose     bool
}

// NewGenerateCommand creates the generate command
func NewGenerateCommand() *cobra.Command {
	opts := &GenerateOptions{}

	cmd := &cobra.Command{
		Use:   "generate",
		Short: "Generate OpenAPI specification from go-bricks service",
		Long: `Analyzes a go-bricks service and generates an OpenAPI 3.0.1 specification.

The tool discovers modules, analyzes route registrations and type definitions,
and produces a comprehensive API specification with validation constraints.`,
		Example: `  # Generate spec for current directory
  go-bricks-openapi generate

  # Generate spec for specific project
  go-bricks-openapi generate -project ./my-service -output docs/openapi.yaml

  # Generate JSON format
  go-bricks-openapi generate -format json`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runGenerate(opts)
		},
	}

	// Flags
	cmd.Flags().StringVarP(&opts.ProjectRoot, "project", "p", ".", "Project root directory")
	cmd.Flags().StringVarP(&opts.OutputFile, "output", "o", "openapi.yaml", "Output file path")
	cmd.Flags().StringVarP(&opts.Format, "format", "f", "yaml", "Output format (yaml|json)")
	cmd.Flags().BoolVarP(&opts.Verbose, "verbose", "v", false, "Verbose output")

	return cmd
}

func runGenerate(opts *GenerateOptions) error {
	// Validate options
	if err := validateGenerateOptions(opts); err != nil {
		return err
	}

	fmt.Printf("Generating OpenAPI spec for project: %s\n", opts.ProjectRoot)
	fmt.Printf("Output file: %s\n", opts.OutputFile)
	fmt.Printf("Format: %s\n", opts.Format)

	// TODO: Implement actual generation logic in Phase 1
	// For now, create a placeholder spec to validate the pipeline

	placeholder := `openapi: 3.0.1
info:
  title: Go-Bricks API
  version: 1.0.0
  description: Generated API specification
paths: {}
components:
  schemas: {}
`

	// Ensure output directory exists
	if err := os.MkdirAll(filepath.Dir(opts.OutputFile), 0755); err != nil {
		return fmt.Errorf("failed to create output directory: %w", err)
	}

	// Write placeholder spec
	if err := os.WriteFile(opts.OutputFile, []byte(placeholder), 0644); err != nil {
		return fmt.Errorf("failed to write output file: %w", err)
	}

	fmt.Printf("✓ OpenAPI specification generated: %s\n", opts.OutputFile)
	return nil
}

func validateGenerateOptions(opts *GenerateOptions) error {
	// Check project root exists
	if _, err := os.Stat(opts.ProjectRoot); os.IsNotExist(err) {
		return fmt.Errorf("project root does not exist: %s", opts.ProjectRoot)
	}

	// Validate format
	switch opts.Format {
	case "yaml", "yml", "json":
		// Valid formats
	default:
		return fmt.Errorf("unsupported format: %s (supported: yaml, json)", opts.Format)
	}

	// Ensure output file has correct extension
	ext := filepath.Ext(opts.OutputFile)
	if ext == "" {
		// Add extension based on format
		switch opts.Format {
		case "json":
			opts.OutputFile += ".json"
		default:
			opts.OutputFile += ".yaml"
		}
	}

	return nil
}