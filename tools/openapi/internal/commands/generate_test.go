package commands

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

const (
	outputFileName = "openapi.yaml"
)

// OpenAPISpec represents the basic structure of an OpenAPI specification for testing
type OpenAPISpec struct {
	OpenAPI    string         `yaml:"openapi"`
	Info       OpenAPIInfo    `yaml:"info"`
	Paths      map[string]any `yaml:"paths"`
	Components map[string]any `yaml:"components"`
}

// OpenAPIInfo represents the info section of an OpenAPI specification
type OpenAPIInfo struct {
	Title       string `yaml:"title"`
	Version     string `yaml:"version"`
	Description string `yaml:"description"`
}

// validateOpenAPISpec parses YAML content and validates OpenAPI structure
func validateOpenAPISpec(t *testing.T, content []byte, expectedTitle, expectedVersion string) {
	t.Helper()

	var spec OpenAPISpec
	err := yaml.Unmarshal(content, &spec)
	if err != nil {
		t.Fatalf("Failed to parse YAML: %v", err)
	}

	// Validate OpenAPI version
	if spec.OpenAPI != "3.0.1" {
		t.Errorf("Expected OpenAPI version '3.0.1', got '%s'", spec.OpenAPI)
	}

	// Validate info section
	if spec.Info.Title != expectedTitle {
		t.Errorf("Expected title '%s', got '%s'", expectedTitle, spec.Info.Title)
	}
	if spec.Info.Version != expectedVersion {
		t.Errorf("Expected version '%s', got '%s'", expectedVersion, spec.Info.Version)
	}
	if spec.Info.Description == "" {
		t.Error("Missing description in info section")
	}

	// Validate paths section exists
	if spec.Paths == nil {
		t.Error("Missing paths section")
	}

	// Validate components section exists
	if spec.Components == nil {
		t.Error("Missing components section")
	}
}

func TestValidateGenerateOptions(t *testing.T) {
	// Create a temporary directory for testing
	tempDir := t.TempDir()

	tests := []struct {
		name    string
		opts    *GenerateOptions
		wantErr bool
	}{
		{
			name: "valid options",
			opts: &GenerateOptions{
				ProjectRoot: tempDir,
				OutputFile:  outputFileName,
				Format:      "yaml",
			},
			wantErr: false,
		},
		{
			name: "nonexistent project root",
			opts: &GenerateOptions{
				ProjectRoot: "/nonexistent/path",
				OutputFile:  outputFileName,
				Format:      "yaml",
			},
			wantErr: true,
		},
		{
			name: "invalid format",
			opts: &GenerateOptions{
				ProjectRoot: tempDir,
				OutputFile:  outputFileName,
				Format:      "xml",
			},
			wantErr: true,
		},
		{
			name: "json format",
			opts: &GenerateOptions{
				ProjectRoot: tempDir,
				OutputFile:  outputFileName,
				Format:      "json",
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateGenerateOptions(tt.opts)
			if (err != nil) != tt.wantErr {
				t.Errorf("validateGenerateOptions() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestValidateGenerateOptionsAutoExtension(t *testing.T) {
	tempDir := t.TempDir()

	tests := []struct {
		name           string
		initialFile    string
		format         string
		expectedSuffix string
	}{
		{
			name:           "yaml format without extension",
			initialFile:    "openapi",
			format:         "yaml",
			expectedSuffix: ".yaml",
		},
		{
			name:           "json format without extension",
			initialFile:    "openapi",
			format:         "json",
			expectedSuffix: ".json",
		},
		{
			name:           "yaml format with extension",
			initialFile:    outputFileName,
			format:         "yaml",
			expectedSuffix: ".yaml",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			opts := &GenerateOptions{
				ProjectRoot: tempDir,
				OutputFile:  tt.initialFile,
				Format:      tt.format,
			}

			err := validateGenerateOptions(opts)
			if err != nil {
				t.Fatalf("validateGenerateOptions() failed: %v", err)
			}

			if !strings.HasSuffix(opts.OutputFile, tt.expectedSuffix) {
				t.Errorf("Expected output file to end with %s, got %s", tt.expectedSuffix, opts.OutputFile)
			}
		})
	}
}

func TestRunGenerate(t *testing.T) {
	// Create a temporary directory for testing
	tempDir := t.TempDir()
	outputFile := filepath.Join(tempDir, "test-openapi.yaml")

	opts := &GenerateOptions{
		ProjectRoot: tempDir,
		OutputFile:  outputFile,
		Format:      "yaml",
		Verbose:     false,
	}

	err := runGenerate(opts)
	if err != nil {
		t.Fatalf("runGenerate() failed: %v", err)
	}

	// Check that the file was created
	if _, err := os.Stat(outputFile); os.IsNotExist(err) {
		t.Error("Output file was not created")
	}

	// Read and validate the generated OpenAPI specification
	content, err := os.ReadFile(outputFile)
	if err != nil {
		t.Fatalf("Failed to read output file: %v", err)
	}

	// Validate OpenAPI structure using YAML parsing
	validateOpenAPISpec(t, content, "Go-Bricks API", "1.0.0")

	// Additional validation for empty project (should have empty paths)
	var spec OpenAPISpec
	err = yaml.Unmarshal(content, &spec)
	if err != nil {
		t.Fatalf("Failed to parse YAML for additional validation: %v", err)
	}

	// For an empty project, paths should be an empty map
	if len(spec.Paths) != 0 {
		t.Errorf("Expected empty paths for project with no modules, got %d paths", len(spec.Paths))
	}
}

func TestRunGenerateDirectoryCreation(t *testing.T) {
	// Create a temporary directory for testing
	tempDir := t.TempDir()

	// Use a nested path that doesn't exist yet
	outputFile := filepath.Join(tempDir, "docs", "api", outputFileName)

	opts := &GenerateOptions{
		ProjectRoot: tempDir,
		OutputFile:  outputFile,
		Format:      "yaml",
		Verbose:     false,
	}

	err := runGenerate(opts)
	if err != nil {
		t.Fatalf("runGenerate() failed: %v", err)
	}

	// Check that the nested directories were created
	if _, err := os.Stat(filepath.Dir(outputFile)); os.IsNotExist(err) {
		t.Error("Output directory was not created")
	}

	// Check that the file was created
	if _, err := os.Stat(outputFile); os.IsNotExist(err) {
		t.Error("Output file was not created")
	}
}

func TestRunGenerateVerbose(t *testing.T) {
	tempDir := t.TempDir()
	outputFile := filepath.Join(tempDir, outputFileName)

	opts := &GenerateOptions{
		ProjectRoot: tempDir,
		OutputFile:  outputFile,
		Format:      "yaml",
		Verbose:     true, // Test verbose mode
	}

	// This should work without panicking even in verbose mode
	err := runGenerate(opts)
	if err != nil {
		t.Fatalf("runGenerate() failed in verbose mode: %v", err)
	}

	// Check that the file was still created
	if _, err := os.Stat(outputFile); os.IsNotExist(err) {
		t.Error("Output file was not created in verbose mode")
	}
}

func TestNewGenerateCommand(t *testing.T) {
	cmd := NewGenerateCommand()

	if cmd == nil {
		t.Fatal("NewGenerateCommand() returned nil")
	}

	if cmd.Use != "generate" {
		t.Errorf("Expected Use 'generate', got %s", cmd.Use)
	}

	if cmd.Short == "" {
		t.Error("Command should have a short description")
	}

	if cmd.Long == "" {
		t.Error("Command should have a long description")
	}

	if cmd.RunE == nil {
		t.Error("Command should have a RunE function")
	}

	// Check that flags are registered
	projectFlag := cmd.Flags().Lookup("project")
	if projectFlag == nil {
		t.Error("Missing --project flag")
	}

	outputFlag := cmd.Flags().Lookup("output")
	if outputFlag == nil {
		t.Error("Missing --output flag")
	}

	formatFlag := cmd.Flags().Lookup("format")
	if formatFlag == nil {
		t.Error("Missing --format flag")
	}

	verboseFlag := cmd.Flags().Lookup("verbose")
	if verboseFlag == nil {
		t.Error("Missing --verbose flag")
	}
}
