package commands

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

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
				OutputFile:  "openapi.yaml",
				Format:      "yaml",
			},
			wantErr: false,
		},
		{
			name: "nonexistent project root",
			opts: &GenerateOptions{
				ProjectRoot: "/nonexistent/path",
				OutputFile:  "openapi.yaml",
				Format:      "yaml",
			},
			wantErr: true,
		},
		{
			name: "invalid format",
			opts: &GenerateOptions{
				ProjectRoot: tempDir,
				OutputFile:  "openapi.yaml",
				Format:      "xml",
			},
			wantErr: true,
		},
		{
			name: "json format",
			opts: &GenerateOptions{
				ProjectRoot: tempDir,
				OutputFile:  "openapi.yaml",
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

func TestValidateGenerateOptions_AutoExtension(t *testing.T) {
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
			initialFile:    "openapi.yaml",
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

	// Read and check the content
	content, err := os.ReadFile(outputFile)
	if err != nil {
		t.Fatalf("Failed to read output file: %v", err)
	}

	contentStr := string(content)

	// Check basic OpenAPI structure
	if !strings.Contains(contentStr, "openapi: 3.0.1") {
		t.Error("Missing OpenAPI version")
	}
	if !strings.Contains(contentStr, "title: Go-Bricks API") {
		t.Error("Missing title")
	}
	if !strings.Contains(contentStr, "version: 1.0.0") {
		t.Error("Missing version")
	}
	if !strings.Contains(contentStr, "paths:\n  {}") {
		t.Error("Should have empty paths for no modules")
	}
	if !strings.Contains(contentStr, "components:") {
		t.Error("Missing components section")
	}
}

func TestRunGenerate_DirectoryCreation(t *testing.T) {
	// Create a temporary directory for testing
	tempDir := t.TempDir()

	// Use a nested path that doesn't exist yet
	outputFile := filepath.Join(tempDir, "docs", "api", "openapi.yaml")

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

func TestRunGenerate_Verbose(t *testing.T) {
	tempDir := t.TempDir()
	outputFile := filepath.Join(tempDir, "openapi.yaml")

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
