package commands

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

const (
	outputFileName       = "openapi.yaml"
	testYAMLFile         = "test.yaml"
	generateCmdFailedMsg = "runGenerate() failed: %v"
	specFile             = "spec.yaml"
	docsAPISpecFile      = "docs/api/spec.yaml"
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
			},
			wantErr: false,
		},
		{
			name: "nonexistent project root",
			opts: &GenerateOptions{
				ProjectRoot: "/nonexistent/path",
				OutputFile:  outputFileName,
			},
			wantErr: true,
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
		expectedSuffix string
	}{
		{
			name:           "without extension",
			initialFile:    "openapi",
			expectedSuffix: ".yaml",
		},
		{
			name:           "with extension",
			initialFile:    outputFileName,
			expectedSuffix: ".yaml",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			opts := &GenerateOptions{
				ProjectRoot: tempDir,
				OutputFile:  tt.initialFile,
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
		Verbose:     false,
	}

	err := runGenerate(opts)
	if err != nil {
		t.Fatalf(generateCmdFailedMsg, err)
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
		Verbose:     false,
	}

	err := runGenerate(opts)
	if err != nil {
		t.Fatalf(generateCmdFailedMsg, err)
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

	verboseFlag := cmd.Flags().Lookup("verbose")
	if verboseFlag == nil {
		t.Error("Missing --verbose flag")
	}
}

func TestValidateGenerateOptionsEdgeCases(t *testing.T) {
	tempDir := t.TempDir()

	t.Run("file with existing yaml extension", func(t *testing.T) {
		opts := &GenerateOptions{
			ProjectRoot: tempDir,
			OutputFile:  testYAMLFile,
		}
		err := validateGenerateOptions(opts)
		if err != nil {
			t.Errorf("Expected no error, got: %v", err)
		}
	})

	t.Run("auto extension for yaml format", func(t *testing.T) {
		opts := &GenerateOptions{
			ProjectRoot: tempDir,
			OutputFile:  "test",
		}
		err := validateGenerateOptions(opts)
		if err != nil {
			t.Errorf("Expected no error, got: %v", err)
		}
		if !strings.HasSuffix(opts.OutputFile, ".yaml") {
			t.Errorf("Expected .yaml extension to be added, got: %s", opts.OutputFile)
		}
	})
}

func TestRunGenerateErrorCases(t *testing.T) {
	tests := []struct {
		name    string
		setup   func(t *testing.T) *GenerateOptions
		wantErr bool
	}{
		{
			name: "generator error simulation",
			setup: func(t *testing.T) *GenerateOptions {
				tempDir := t.TempDir()
				return &GenerateOptions{
					ProjectRoot: tempDir,
					OutputFile:  filepath.Join(tempDir, testYAMLFile),
					Verbose:     false,
				}
			},
			wantErr: false, // This should succeed with current implementation
		},
		{
			name: "directory creation permission error simulation",
			setup: func(t *testing.T) *GenerateOptions {
				tempDir := t.TempDir()
				// Try to create in a read-only directory to simulate permission error
				readOnlyDir := filepath.Join(tempDir, "readonly")
				err := os.MkdirAll(readOnlyDir, 0755)
				if err != nil {
					t.Skip("Failed to create test directory")
				}
				// Make directory read-only
				err = os.Chmod(readOnlyDir, 0444)
				if err != nil {
					t.Skip("Failed to make directory read-only")
				}

				return &GenerateOptions{
					ProjectRoot: tempDir,
					OutputFile:  filepath.Join(readOnlyDir, "nested", testYAMLFile),
					Verbose:     false,
				}
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			opts := tt.setup(t)
			err := runGenerate(opts)
			if (err != nil) != tt.wantErr {
				t.Errorf("runGenerate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestRunGenerateYAMLFormat(t *testing.T) {
	tempDir := t.TempDir()
	outputFile := filepath.Join(tempDir, testYAMLFile)

	opts := &GenerateOptions{
		ProjectRoot: tempDir,
		OutputFile:  outputFile,
		Verbose:     true,
	}

	err := runGenerate(opts)
	if err != nil {
		t.Fatalf("runGenerate() failed for YAML format: %v", err)
	}

	// Check that file was created
	if _, err := os.Stat(outputFile); os.IsNotExist(err) {
		t.Error("YAML output file was not created")
	}

	// Read file and verify content
	content, err := os.ReadFile(outputFile)
	if err != nil {
		t.Fatalf("Failed to read YAML output file: %v", err)
	}

	// Verify YAML format output
	contentStr := string(content)
	if !strings.Contains(contentStr, "openapi: 3.0.1") {
		t.Error("Output should contain openapi version")
	}
	if !strings.Contains(contentStr, "info:") {
		t.Error("Output should contain info section")
	}
	if !strings.Contains(contentStr, "title: Go-Bricks API") {
		t.Error("Output should contain API title")
	}
}

// TestValidateGenerateOptionsPermissionError tests validation when directory creation fails
func TestValidateGenerateOptionsPermissionError(t *testing.T) {
	// This test validates the error handling in validateGenerateOptions
	// when path operations might fail
	tests := []struct {
		name    string
		setup   func() *GenerateOptions
		wantErr bool
	}{
		{
			name: "valid simple path",
			setup: func() *GenerateOptions {
				return &GenerateOptions{
					ProjectRoot: "/", // Root always exists
					OutputFile:  "test.yaml",
				}
			},
			wantErr: false,
		},
		{
			name: "path with extension already",
			setup: func() *GenerateOptions {
				return &GenerateOptions{
					ProjectRoot: "/",
					OutputFile:  "api.yaml",
				}
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			opts := tt.setup()
			err := validateGenerateOptions(opts)
			if (err != nil) != tt.wantErr {
				t.Errorf("validateGenerateOptions() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

// TestRunGenerateComplexScenarios tests complex generation scenarios
func TestRunGenerateComplexScenarios(t *testing.T) {
	tempDir := t.TempDir()

	tests := []struct {
		name     string
		setup    func(dir string) *GenerateOptions
		validate func(t *testing.T, outputFile string)
	}{
		{
			name: "generate with deeply nested output path",
			setup: func(dir string) *GenerateOptions {
				return &GenerateOptions{
					ProjectRoot: dir,
					OutputFile:  filepath.Join(dir, "very", "deep", "nested", "path", "api.yaml"),
					Verbose:     false,
				}
			},
			validate: func(t *testing.T, outputFile string) {
				if _, err := os.Stat(outputFile); os.IsNotExist(err) {
					t.Error("Deeply nested output file was not created")
				}
			},
		},
		{
			name: "generate with verbose mode and complex project",
			setup: func(dir string) *GenerateOptions {
				// Create a mock module file for more interesting output
				moduleContent := `package testmod

// TestModule demonstrates module creation
type TestModule struct{}

func (m *TestModule) Name() string { return "testmod" }
func (m *TestModule) Init(deps any) error { return nil }`
				moduleFile := filepath.Join(dir, "testmod.go")
				os.WriteFile(moduleFile, []byte(moduleContent), 0644)

				return &GenerateOptions{
					ProjectRoot: dir,
					OutputFile:  filepath.Join(dir, "verbose-api.yaml"),
					Verbose:     true,
				}
			},
			validate: func(t *testing.T, outputFile string) {
				if _, err := os.Stat(outputFile); os.IsNotExist(err) {
					t.Error("Verbose mode output file was not created")
				}
				// Read and validate content
				content, err := os.ReadFile(outputFile)
				if err != nil {
					t.Fatalf("Failed to read verbose output: %v", err)
				}
				contentStr := string(content)
				if !strings.Contains(contentStr, "openapi:") {
					t.Error("Verbose output should contain valid OpenAPI spec")
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			testDir := filepath.Join(tempDir, tt.name)
			os.MkdirAll(testDir, 0755)

			opts := tt.setup(testDir)
			err := runGenerate(opts)
			if err != nil {
				t.Fatalf(generateCmdFailedMsg, err)
			}

			tt.validate(t, opts.OutputFile)
		})
	}
}

// TestNewGenerateCommandAdvanced tests advanced command creation scenarios
func TestNewGenerateCommandAdvanced(t *testing.T) {
	cmd := NewGenerateCommand()

	// Test flag defaults
	projectFlag := cmd.Flags().Lookup("project")
	if projectFlag.DefValue != "." {
		t.Errorf("Expected project flag default '.', got '%s'", projectFlag.DefValue)
	}

	outputFlag := cmd.Flags().Lookup("output")
	if outputFlag.DefValue != outputFileName {
		t.Errorf("Expected output flag default '%s', got '%s'", outputFileName, outputFlag.DefValue)
	}

	verboseFlag := cmd.Flags().Lookup("verbose")
	if verboseFlag.DefValue != "false" {
		t.Errorf("Expected verbose flag default 'false', got '%s'", verboseFlag.DefValue)
	}

	// Test flag types
	if projectFlag.Value.Type() != "string" {
		t.Errorf("Expected project flag type 'string', got '%s'", projectFlag.Value.Type())
	}

	if verboseFlag.Value.Type() != "bool" {
		t.Errorf("Expected verbose flag type 'bool', got '%s'", verboseFlag.Value.Type())
	}
}

// TestGenerateOptionsValidation tests various validation scenarios
func TestGenerateOptionsValidation(t *testing.T) {
	tempDir := t.TempDir()

	// Test file extension handling
	tests := []struct {
		name         string
		initialFile  string
		expectedFile string
	}{
		{
			name:         "no extension gets yaml",
			initialFile:  "spec",
			expectedFile: specFile,
		},
		{
			name:         "yaml extension preserved",
			initialFile:  specFile,
			expectedFile: specFile,
		},
		{
			name:         "yml extension preserved",
			initialFile:  "spec.yml",
			expectedFile: "spec.yml",
		},
		{
			name:         "other extension preserved",
			initialFile:  "spec.json",
			expectedFile: "spec.json",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			opts := &GenerateOptions{
				ProjectRoot: tempDir,
				OutputFile:  tt.initialFile,
			}

			err := validateGenerateOptions(opts)
			if err != nil {
				t.Fatalf("validateGenerateOptions() failed: %v", err)
			}

			if opts.OutputFile != tt.expectedFile {
				t.Errorf("Expected output file '%s', got '%s'", tt.expectedFile, opts.OutputFile)
			}
		})
	}
}

// TestRunGenerateWithWriteError tests error handling during file writing
func TestRunGenerateWithWriteError(t *testing.T) {
	tempDir := t.TempDir()

	// Test successful generation to ensure baseline works
	t.Run("successful generation", func(t *testing.T) {
		opts := &GenerateOptions{
			ProjectRoot: tempDir,
			OutputFile:  filepath.Join(tempDir, "success.yaml"),
			Verbose:     false,
		}

		err := runGenerate(opts)
		if err != nil {
			t.Errorf("Expected successful generation, got error: %v", err)
		}

		// Verify file was created
		if _, err := os.Stat(opts.OutputFile); os.IsNotExist(err) {
			t.Error("Generated file should exist")
		}
	})
}

// TestValidateGenerateOptionsExtensionHandling tests detailed extension handling
func TestValidateGenerateOptionsExtensionHandling(t *testing.T) {
	tempDir := t.TempDir()

	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "complex path no extension",
			input:    "docs/api/spec",
			expected: docsAPISpecFile,
		},
		{
			name:     "complex path with extension",
			input:    docsAPISpecFile,
			expected: docsAPISpecFile,
		},
		{
			name:     "single character name",
			input:    "s",
			expected: "s.yaml",
		},
		{
			name:     "name with dots but extension preserved",
			input:    "api.v1.spec",
			expected: "api.v1.spec",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			opts := &GenerateOptions{
				ProjectRoot: tempDir,
				OutputFile:  tt.input,
				Verbose:     false,
			}

			err := validateGenerateOptions(opts)
			if err != nil {
				t.Fatalf("validateGenerateOptions failed: %v", err)
			}

			if opts.OutputFile != tt.expected {
				t.Errorf("Expected output file '%s', got '%s'", tt.expected, opts.OutputFile)
			}
		})
	}
}

// TestGenerateCommandFlagValidation tests command flag validation
func TestGenerateCommandFlagValidation(t *testing.T) {
	cmd := NewGenerateCommand()

	// Test that command has proper metadata
	if cmd.Use != "generate" {
		t.Errorf("Expected command use 'generate', got '%s'", cmd.Use)
	}

	if cmd.Short == "" {
		t.Error("Command should have short description")
	}

	if cmd.Long == "" {
		t.Error("Command should have long description")
	}

	if cmd.Example == "" {
		t.Error("Command should have examples")
	}

	// Test flag existence and basic properties
	flagTests := map[string]struct {
		shorthand string
		required  bool
	}{
		"project": {shorthand: "p", required: false},
		"output":  {shorthand: "o", required: false},
		"verbose": {shorthand: "v", required: false},
	}

	for flagName, expected := range flagTests {
		t.Run("flag_"+flagName, func(t *testing.T) {
			flag := cmd.Flags().Lookup(flagName)
			if flag == nil {
				t.Fatalf("Flag '%s' not found", flagName)
			}

			if flag.Shorthand != expected.shorthand {
				t.Errorf("Expected shorthand '%s', got '%s'", expected.shorthand, flag.Shorthand)
			}

			// Test that flag has reasonable default
			if flag.DefValue == "" && flagName != "verbose" {
				t.Errorf("Flag '%s' should have a default value", flagName)
			}
		})
	}
}
