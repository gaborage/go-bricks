package commands

import (
	"os"
	"path/filepath"
	"testing"
)

func TestIsGoVersionSupported(t *testing.T) {
	tests := []struct {
		name     string
		version  string
		expected bool
	}{
		{
			name:     "supported version - go1.21.0",
			version:  "go1.21.0",
			expected: true,
		},
		{
			name:     "supported version - go1.21.5",
			version:  "go1.21.5",
			expected: true,
		},
		{
			name:     "supported version - go1.22.0",
			version:  "go1.22.0",
			expected: true,
		},
		{
			name:     "supported version - go1.25.1",
			version:  "go1.25.1",
			expected: true,
		},
		{
			name:     "unsupported version - go1.20.0",
			version:  "go1.20.0",
			expected: false,
		},
		{
			name:     "unsupported version - go1.19.5",
			version:  "go1.19.5",
			expected: false,
		},
		{
			name:     "unsupported version - go1.18.10",
			version:  "go1.18.10",
			expected: false,
		},
		{
			name:     "edge case - exactly minimum version",
			version:  "go1.21.0", // semver requires patch version
			expected: true,
		},
		{
			name:     "invalid format - missing go prefix",
			version:  "1.21.0",
			expected: false,
		},
		{
			name:     "invalid format - empty string",
			version:  "",
			expected: false,
		},
		{
			name:     "invalid format - malformed version",
			version:  "go1.21.x",
			expected: false,
		},
		{
			name:     "pre-release version - go1.22.0-rc1",
			version:  "go1.22.0-rc1",
			expected: true,
		},
		{
			name:     "beta version - go1.23.0-beta1",
			version:  "go1.23.0-beta1",
			expected: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := isGoVersionSupported(tt.version)
			if result != tt.expected {
				t.Errorf("isGoVersionSupported(%q) = %v, expected %v", tt.version, result, tt.expected)
			}
		})
	}
}

func TestCheckGoBricksCompatibility(t *testing.T) {
	// Create a temporary directory for testing
	tempDir := t.TempDir()

	tests := []struct {
		name         string
		goModContent string
		verbose      bool
		expectError  bool
	}{
		{
			name: "valid go-bricks dependency",
			goModContent: `module test-project

go 1.21

require (
	go-bricks v1.0.0
	github.com/spf13/cobra v1.8.0
)
`,
			verbose:     false,
			expectError: false,
		},
		{
			name: "missing go-bricks dependency",
			goModContent: `module test-project

go 1.21

require (
	github.com/spf13/cobra v1.8.0
)
`,
			verbose:     false,
			expectError: true,
		},
		{
			name: "verbose mode with go-bricks",
			goModContent: `module test-project

go 1.21

require (
	go-bricks v2.0.0
)
`,
			verbose:     true,
			expectError: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create temporary go.mod file
			goModPath := filepath.Join(tempDir, "go.mod")
			err := os.WriteFile(goModPath, []byte(tt.goModContent), 0644)
			if err != nil {
				t.Fatalf("Failed to create test go.mod: %v", err)
			}

			// Test the function
			err = checkGoBricksCompatibility(goModPath, tt.verbose)

			if tt.expectError && err == nil {
				t.Error("Expected error but got none")
			}
			if !tt.expectError && err != nil {
				t.Errorf("Expected no error but got: %v", err)
			}

			// Clean up
			os.Remove(goModPath)
		})
	}
}

func TestCheckGoBricksCompatibility_FileNotFound(t *testing.T) {
	err := checkGoBricksCompatibility("/nonexistent/go.mod", false)
	if err == nil {
		t.Error("Expected error for nonexistent file, but got none")
	}
}

func TestCheckProjectStructure(t *testing.T) {
	tests := []struct {
		name        string
		setupFunc   func(t *testing.T) string
		expectError bool
	}{
		{
			name: "valid project with go files",
			setupFunc: func(t *testing.T) string {
				tempDir := t.TempDir()
				// Create a test Go file
				err := os.WriteFile(filepath.Join(tempDir, "main.go"), []byte("package main"), 0644)
				if err != nil {
					t.Fatalf("Failed to create test file: %v", err)
				}
				return tempDir
			},
			expectError: false,
		},
		{
			name: "valid project with go files in subdirectory",
			setupFunc: func(t *testing.T) string {
				tempDir := t.TempDir()
				subDir := filepath.Join(tempDir, "internal")
				err := os.MkdirAll(subDir, 0755)
				if err != nil {
					t.Fatalf("Failed to create subdirectory: %v", err)
				}
				// Create a test Go file in subdirectory
				err = os.WriteFile(filepath.Join(subDir, "handler.go"), []byte("package internal"), 0644)
				if err != nil {
					t.Fatalf("Failed to create test file: %v", err)
				}
				return tempDir
			},
			expectError: false,
		},
		{
			name: "nonexistent directory",
			setupFunc: func(_ *testing.T) string {
				return "/nonexistent/directory"
			},
			expectError: true,
		},
		{
			name: "directory with no go files",
			setupFunc: func(t *testing.T) string {
				tempDir := t.TempDir()
				// Create a non-Go file
				err := os.WriteFile(filepath.Join(tempDir, "README.md"), []byte("# Test"), 0644)
				if err != nil {
					t.Fatalf("Failed to create test file: %v", err)
				}
				return tempDir
			},
			expectError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			projectRoot := tt.setupFunc(t)
			err := checkProjectStructure(projectRoot)

			if tt.expectError && err == nil {
				t.Error("Expected error but got none")
			}
			if !tt.expectError && err != nil {
				t.Errorf("Expected no error but got: %v", err)
			}
		})
	}
}

func TestRunDoctor(t *testing.T) {
	// Create a temporary project structure
	tempDir := t.TempDir()

	// Create a valid go.mod
	goModContent := `module test-project

go 1.21

require go-bricks v1.0.0
`
	err := os.WriteFile(filepath.Join(tempDir, "go.mod"), []byte(goModContent), 0644)
	if err != nil {
		t.Fatalf("Failed to create go.mod: %v", err)
	}

	// Create a test Go file
	err = os.WriteFile(filepath.Join(tempDir, "main.go"), []byte("package main"), 0644)
	if err != nil {
		t.Fatalf("Failed to create main.go: %v", err)
	}

	tests := []struct {
		name        string
		opts        *DoctorOptions
		expectError bool
	}{
		{
			name: "valid project structure",
			opts: &DoctorOptions{
				ProjectRoot: tempDir,
				Verbose:     false,
			},
			expectError: false,
		},
		{
			name: "verbose mode",
			opts: &DoctorOptions{
				ProjectRoot: tempDir,
				Verbose:     true,
			},
			expectError: false,
		},
		{
			name: "nonexistent project",
			opts: &DoctorOptions{
				ProjectRoot: "/nonexistent/path",
				Verbose:     false,
			},
			expectError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := runDoctor(tt.opts)

			if tt.expectError && err == nil {
				t.Error("Expected error but got none")
			}
			if !tt.expectError && err != nil {
				t.Errorf("Expected no error but got: %v", err)
			}
		})
	}
}

func TestNewDoctorCommand(t *testing.T) {
	cmd := NewDoctorCommand()

	if cmd == nil {
		t.Fatal("NewDoctorCommand() returned nil")
	}

	if cmd.Use != "doctor" {
		t.Errorf("Expected Use 'doctor', got %s", cmd.Use)
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

	verboseFlag := cmd.Flags().Lookup("verbose")
	if verboseFlag == nil {
		t.Error("Missing --verbose flag")
	}
}
