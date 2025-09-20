package commands

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/gaborage/go-bricks/tools/openapi/internal/analyzer"
	"github.com/gaborage/go-bricks/tools/openapi/internal/generator"
)

// GenerateOptions holds options for the generate command
type GenerateOptions struct {
	ProjectRoot string
	OutputFile  string
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
and produces a comprehensive YAML API specification with validation constraints.`,
		Example: `  # Generate spec for current directory
  go-bricks-openapi generate

  # Generate spec for specific project
  go-bricks-openapi generate -project ./my-service -output docs/openapi.yaml`,
		RunE: func(_ *cobra.Command, _ []string) error {
			return runGenerate(opts)
		},
	}

	// Flags
	cmd.Flags().StringVarP(&opts.ProjectRoot, "project", "p", ".", "Project root directory")
	cmd.Flags().StringVarP(&opts.OutputFile, "output", "o", "openapi.yaml", "Output file path")
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

	// Analyze the project to discover modules and routes
	projectAnalyzer := analyzer.New(opts.ProjectRoot)
	project, err := projectAnalyzer.AnalyzeProject()
	if err != nil {
		return fmt.Errorf("failed to analyze project: %w", err)
	}

	if opts.Verbose {
		fmt.Printf("Discovered %d modules\n", len(project.Modules))
		for _, module := range project.Modules {
			fmt.Printf("  Module: %s (%s) with %d routes\n", module.Name, module.Package, len(module.Routes))
		}
	}

	// Generate OpenAPI spec
	gen := generator.New("Go-Bricks API", "1.0.0", "Generated API specification")
	specContent, err := gen.Generate(project)
	if err != nil {
		return fmt.Errorf("failed to generate spec: %w", err)
	}

	// Write to file
	if err := os.MkdirAll(filepath.Dir(opts.OutputFile), 0750); err != nil {
		return fmt.Errorf("failed to create output directory: %w", err)
	}

	if err := os.WriteFile(opts.OutputFile, []byte(specContent), 0600); err != nil {
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

	// Ensure output file has .yaml extension
	ext := filepath.Ext(opts.OutputFile)
	if ext == "" {
		opts.OutputFile += ".yaml"
	}

	return nil
}
