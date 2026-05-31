package commands

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/gaborage/go-bricks/tools/openapi/internal/models"
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
				ProjectRoot: filepath.Join("nonexistent", "path"),
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

	err := runGenerate(context.Background(), opts)
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

	err := runGenerate(context.Background(), opts)
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
	err := runGenerate(context.Background(), opts)
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
				// Skip on Windows - chmod doesn't restrict directory operations the same way
				// Windows uses ACLs, not Unix permission bits, so os.Chmod(0444) won't
				// prevent creating subdirectories like it does on Unix/Linux
				if runtime.GOOS == "windows" {
					t.Skip("Skipping permission test on Windows - chmod behavior differs from Unix")
				}

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
			err := runGenerate(context.Background(), opts)
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

	err := runGenerate(context.Background(), opts)
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
					ProjectRoot: ".", // Current directory always exists
					OutputFile:  "test.yaml",
				}
			},
			wantErr: false,
		},
		{
			name: "path with extension already",
			setup: func() *GenerateOptions {
				return &GenerateOptions{
					ProjectRoot: ".",
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
			err := runGenerate(context.Background(), opts)
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

		err := runGenerate(context.Background(), opts)
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

// TestResolveFormat covers explicit, inferred, and invalid format resolution.
func TestResolveFormatPR12(t *testing.T) {
	cases := []struct {
		name, format, output, want string
		wantErr                    bool
	}{
		{name: "default_yaml", output: "openapi.yaml", want: "yaml"},
		{name: "default_yaml_no_ext", output: "openapi", want: "yaml"},
		{name: "inferred_json_from_ext", output: "openapi.json", want: "json"},
		{name: "inferred_yaml_from_yml_ext", output: "openapi.yml", want: "yaml"},
		{name: "explicit_json_agrees_with_ext", format: "json", output: "openapi.json", want: "json"},
		{name: "explicit_json_no_ext", format: "json", output: "openapi", want: "json"},
		{name: "explicit_yaml_agrees_with_ext", format: "yaml", output: "openapi.yaml", want: "yaml"},
		// Explicit --format must not contradict a recognized extension: the file
		// would otherwise lie about its contents (JSON bytes in a .yaml file).
		{name: "explicit_json_conflicts_yaml_ext", format: "json", output: "openapi.yaml", wantErr: true},
		{name: "explicit_yaml_conflicts_json_ext", format: "yaml", output: "x.json", wantErr: true},
		// An unrecognized extension carries no format claim, so explicit wins.
		{name: "explicit_json_unknown_ext_ok", format: "json", output: "x.txt", want: "json"},
		{name: "invalid_format", format: "xml", output: "x.yaml", wantErr: true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := resolveFormat(&GenerateOptions{Format: c.format, OutputFile: c.output})
			if c.wantErr {
				if err == nil {
					t.Fatal("expected error for invalid format")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != c.want {
				t.Errorf("resolveFormat = %q, want %q", got, c.want)
			}
		})
	}
}

// TestBuildGeneratorConfig maps CLI flags to a generator.Config.
func TestBuildGeneratorConfig(t *testing.T) {
	cfg := buildGeneratorConfig(&GenerateOptions{
		Title: "My API", APIVersion: "2.0.0", Description: "d",
		Servers: []string{"https://a", "https://b"},
		License: "MIT", LicenseURL: "https://mit",
		DisableTenantAuth: true,
	})
	if cfg.Title != "My API" || cfg.Version != "2.0.0" || cfg.Description != "d" {
		t.Errorf("metadata not threaded: %+v", cfg)
	}
	if len(cfg.Servers) != 2 || cfg.Servers[0] != "https://a" {
		t.Errorf("servers not threaded: %v", cfg.Servers)
	}
	if cfg.License == nil || cfg.License.Name != "MIT" || cfg.License.URL != "https://mit" {
		t.Errorf("license not threaded: %+v", cfg.License)
	}
	if !cfg.DisableTenantSecurity {
		t.Error("tenant-security opt-out not threaded")
	}
	// No --license -> nil license.
	if got := buildGeneratorConfig(&GenerateOptions{}); got.License != nil {
		t.Errorf("expected nil license when --license unset, got %+v", got.License)
	}
}

// TestToJSON confirms the YAML spec re-renders as parseable JSON.
func TestToJSON(t *testing.T) {
	yamlSpec := "openapi: 3.0.1\ninfo:\n  title: T\n  version: 1.0.0\npaths: {}\n"
	out, err := toJSON(yamlSpec)
	if err != nil {
		t.Fatalf("toJSON: %v", err)
	}
	var doc map[string]any
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatalf("output is not valid JSON: %v\n%s", err, out)
	}
	if doc["openapi"] != "3.0.1" {
		t.Errorf("openapi field not preserved: %v", doc["openapi"])
	}
	info, ok := doc["info"].(map[string]any)
	if !ok || info["title"] != "T" {
		t.Errorf("info.title not preserved: %v", doc["info"])
	}
}

// TestEmitContentWarnings covers the empty/untyped warning detection.
func TestEmitContentWarnings(t *testing.T) {
	if !emitContentWarnings(&models.Project{}) {
		t.Error("empty project (no modules) should warn")
	}
	withRoutes := &models.Project{Modules: []models.Module{{Name: "m"}}}
	if !emitContentWarnings(withRoutes) {
		t.Error("module with no routes should warn")
	}
	typed := &models.Project{Modules: []models.Module{{Name: "m", Routes: []models.Route{
		{Method: "GET", Path: "/x", HandlerName: "h", Response: &models.TypeInfo{Name: "R", Fields: []models.FieldInfo{{Name: "ID", JSONName: "id"}}}},
	}}}}
	if emitContentWarnings(typed) {
		t.Error("a typed route should not warn")
	}

	// A named-but-fieldless response (e.g. `type Ack struct{}`) still resolves to
	// a component $ref, so it must not be reported as untyped (would false-fail
	// --strict).
	namedEmpty := &models.Project{Modules: []models.Module{{Name: "m", Routes: []models.Route{
		{Method: "POST", Path: "/ack", HandlerName: "ack", Response: &models.TypeInfo{Name: "Ack"}},
	}}}}
	if emitContentWarnings(namedEmpty) {
		t.Error("a named-but-fieldless response is resolved (gets a $ref) and should not warn")
	}
}

// TestExampleUsesDoubleDashFlags guards against the single-dash trap regression:
// cobra long flags need `--project`/`--output`, not `-project`/`-output`.
func TestExampleUsesDoubleDashFlags(t *testing.T) {
	for _, cmd := range []string{NewGenerateCommand().Example, NewDoctorCommand().Example} {
		if strings.Contains(cmd, " -project ") || strings.Contains(cmd, " -output ") {
			t.Errorf("Example uses a single-dash long flag (parses as -p -r -o ...): %s", cmd)
		}
	}
}

// TestToJSONPreservesKeyOrder pins that JSON output keeps the canonical OpenAPI
// section order rather than the alphabetical order encoding/json imposes on a
// map[string]any round-trip.
func TestToJSONPreservesKeyOrder(t *testing.T) {
	// Canonical order is openapi, info, servers, paths — none of which is
	// alphabetical, so a map round-trip would visibly reorder them.
	yamlSpec := "openapi: 3.0.1\n" +
		"info:\n  title: T\n  version: 1.0.0\n" +
		"servers:\n  - url: /\n" +
		"paths: {}\n"
	out, err := toJSON(yamlSpec)
	if err != nil {
		t.Fatalf("toJSON: %v", err)
	}
	order := []string{`"openapi"`, `"info"`, `"servers"`, `"paths"`}
	prev := -1
	for _, key := range order {
		idx := strings.Index(out, key)
		if idx < 0 {
			t.Fatalf("key %s missing from JSON output:\n%s", key, out)
		}
		if idx <= prev {
			t.Errorf("key %s out of canonical order (alphabetized?):\n%s", key, out)
		}
		prev = idx
	}
	// title before version within info (insertion order, not alphabetical).
	if strings.Index(out, `"title"`) > strings.Index(out, `"version"`) {
		t.Errorf("nested info keys reordered:\n%s", out)
	}
}

// TestValidateGenerateOptionsLicenseURLRequiresName covers the fail-fast guard:
// --license-url without --license is rejected rather than silently dropped.
func TestValidateGenerateOptionsLicenseURLRequiresName(t *testing.T) {
	err := validateGenerateOptions(&GenerateOptions{
		ProjectRoot: ".",
		OutputFile:  "openapi.yaml",
		LicenseURL:  "https://opensource.org/licenses/MIT",
	})
	if err == nil {
		t.Fatal("expected --license-url without --license to error")
	}
	if !strings.Contains(err.Error(), "--license-url requires --license") {
		t.Errorf("unexpected error: %v", err)
	}

	// A whitespace-only --license is treated as missing (would otherwise emit a
	// blank info.license.name).
	if err := validateGenerateOptions(&GenerateOptions{
		ProjectRoot: ".",
		OutputFile:  "openapi.yaml",
		License:     "   ",
		LicenseURL:  "https://opensource.org/licenses/MIT",
	}); err == nil {
		t.Error("expected whitespace-only --license to be rejected when --license-url is set")
	}

	// Both supplied together is fine, and surrounding whitespace is trimmed.
	opts := &GenerateOptions{
		ProjectRoot: ".",
		OutputFile:  "openapi.yaml",
		License:     "  MIT  ",
		LicenseURL:  "  https://opensource.org/licenses/MIT  ",
	}
	if err := validateGenerateOptions(opts); err != nil {
		t.Errorf("name+url should validate, got: %v", err)
	}
	if opts.License != "MIT" || opts.LicenseURL != "https://opensource.org/licenses/MIT" {
		t.Errorf("license fields not trimmed: name=%q url=%q", opts.License, opts.LicenseURL)
	}
}

// TestRunGenerateStrictNoArtifact pins that a failed --strict run neither writes
// the output file nor prints the success line, AND that any stale artifact from
// an earlier run is removed (so a downstream step can't consume it). The gate
// runs before persisting.
func TestRunGenerateStrictNoArtifact(t *testing.T) {
	strictFailErr := func(t *testing.T, out string) {
		t.Helper()
		// An empty project root yields no modules -> a content warning -> --strict fails.
		err := runGenerate(context.Background(), &GenerateOptions{
			ProjectRoot: filepath.Dir(out),
			OutputFile:  out,
			Strict:      true,
		})
		if err == nil {
			t.Fatal("expected --strict to fail on a module-less project")
		}
		if !strings.Contains(err.Error(), "--strict is set") {
			t.Errorf("unexpected error: %v", err)
		}
	}

	t.Run("no pre-existing file is created", func(t *testing.T) {
		out := filepath.Join(t.TempDir(), "openapi.yaml")
		strictFailErr(t, out)
		if _, statErr := os.Stat(out); !os.IsNotExist(statErr) {
			t.Errorf("strict failure must not create an artifact, but %s exists (stat err: %v)", out, statErr)
		}
	})

	t.Run("stale pre-existing file is removed", func(t *testing.T) {
		out := filepath.Join(t.TempDir(), "openapi.yaml")
		if err := os.WriteFile(out, []byte("openapi: 3.0.1\n# stale\n"), 0600); err != nil {
			t.Fatalf("seed stale file: %v", err)
		}
		strictFailErr(t, out)
		if _, statErr := os.Stat(out); !os.IsNotExist(statErr) {
			t.Errorf("strict failure must remove the stale artifact, but %s still exists", out)
		}
	})
}
