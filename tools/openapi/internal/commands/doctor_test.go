package commands

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"io/fs"
)

// Test constants to avoid string duplication
const (
	msgExpectedError   = "Expected error but got none"
	msgExpectedNoError = "Expected no error but got: %v"
	msgFailedToCreate  = "Failed to create test file: %v"
	testMainGoFile     = "main.go"
	packageMainContent = "package main"
	goVersion          = "go1.21.0"
	msgUnexpectedError = "Unexpected error: %v"
)

// Helper function to assert error expectations
func assertError(t *testing.T, err error, expectError bool) {
	t.Helper()
	if expectError && err == nil {
		t.Error(msgExpectedError)
	}
	if !expectError && err != nil {
		t.Errorf(msgExpectedNoError, err)
	}
}

// Helper function to create a test Go file
func createTestGoFile(t *testing.T, dir, filename, content string) {
	t.Helper()
	filePath := filepath.Join(dir, filename)
	err := os.WriteFile(filePath, []byte(content), 0644)
	if err != nil {
		t.Fatalf(msgFailedToCreate, err)
	}
}

func TestIsGoVersionSupported(t *testing.T) {
	tests := []struct {
		name     string
		version  string
		expected bool
	}{
		{
			name:     "supported version - go1.21.0",
			version:  goVersion,
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
			version:  goVersion, // semver requires patch version
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
			goModPath := filepath.Join(tempDir, goModFile)
			err := os.WriteFile(goModPath, []byte(tt.goModContent), 0644)
			if err != nil {
				t.Fatalf("Failed to create test go.mod: %v", err)
			}

			// Test the function
			err = checkGoBricksCompatibility(goModPath, tt.verbose)
			assertError(t, err, tt.expectError)

			// Clean up
			os.Remove(goModPath)
		})
	}
}

func TestCheckGoBricksCompatibilityFileNotFound(t *testing.T) {
	err := checkGoBricksCompatibility(filepath.Join("nonexistent", "go.mod"), false)
	if err == nil {
		t.Error("Expected error for nonexistent file, but got none")
	}
}

func TestCheckProjectStructureValidProject(t *testing.T) {
	tempDir := t.TempDir()
	createTestGoFile(t, tempDir, testMainGoFile, packageMainContent)

	err := checkProjectStructure(tempDir)
	assertError(t, err, false)
}

func TestCheckProjectStructureValidProjectWithSubdirectory(t *testing.T) {
	tempDir := t.TempDir()
	subDir := filepath.Join(tempDir, "internal")
	err := os.MkdirAll(subDir, 0755)
	if err != nil {
		t.Fatalf("Failed to create subdirectory: %v", err)
	}

	createTestGoFile(t, subDir, "handler.go", "package internal")

	err = checkProjectStructure(tempDir)
	assertError(t, err, false)
}

func TestCheckProjectStructureNonexistentDirectory(t *testing.T) {
	err := checkProjectStructure(filepath.Join("nonexistent", "directory"))
	assertError(t, err, true)
}

func TestCheckProjectStructureNoGoFiles(t *testing.T) {
	tempDir := t.TempDir()
	createTestGoFile(t, tempDir, "README.md", "# Test")

	err := checkProjectStructure(tempDir)
	assertError(t, err, true)
}

func TestRunDoctor(t *testing.T) {
	// Create a temporary project structure
	tempDir := t.TempDir()

	// Create a valid go.mod
	goModContent := `module test-project

go 1.21

require go-bricks v1.0.0
`
	err := os.WriteFile(filepath.Join(tempDir, goModFile), []byte(goModContent), 0644)
	if err != nil {
		t.Fatalf("Failed to create go.mod: %v", err)
	}

	// Create a test Go file
	err = os.WriteFile(filepath.Join(tempDir, testMainGoFile), []byte(packageMainContent), 0644)
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
				ProjectRoot: filepath.Join("nonexistent", "path"),
				Verbose:     false,
			},
			expectError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := runDoctor(tt.opts)
			assertError(t, err, tt.expectError)
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

func TestResolveProjectPath(t *testing.T) {
	tests := []struct {
		name        string
		projectRoot string
		expectError bool
		checkResult func(t *testing.T, result string)
	}{
		{
			name:        "absolute path unchanged",
			projectRoot: "/tmp/test",
			expectError: false,
			checkResult: func(t *testing.T, result string) {
				// On Windows, absolute paths include drive letters
				// Convert both to absolute for comparison
				expected, err := filepath.Abs("/tmp/test")
				if err != nil {
					t.Fatalf("Failed to resolve expected path: %v", err)
				}
				if result != expected {
					t.Errorf("Expected %s, got %s", expected, result)
				}
			},
		},
		{
			name:        "relative path converted",
			projectRoot: ".",
			expectError: false,
			checkResult: func(t *testing.T, result string) {
				if !filepath.IsAbs(result) {
					t.Errorf("Expected absolute path, got %s", result)
				}
			},
		},
		{
			name:        "relative subdirectory",
			projectRoot: "./subdir",
			expectError: false,
			checkResult: func(t *testing.T, result string) {
				if !filepath.IsAbs(result) {
					t.Errorf("Expected absolute path, got %s", result)
				}
				if !strings.HasSuffix(result, "subdir") {
					t.Errorf("Expected path ending with 'subdir', got %s", result)
				}
			},
		},
		{
			name:        "path cleaning",
			projectRoot: "/tmp/test/../project",
			expectError: false,
			checkResult: func(t *testing.T, result string) {
				// On Windows, absolute paths include drive letters
				// Convert both to absolute for comparison
				expected, err := filepath.Abs("/tmp/project")
				if err != nil {
					t.Fatalf("Failed to resolve expected path: %v", err)
				}
				if result != expected {
					t.Errorf("Expected %s, got %s", expected, result)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := resolveProjectPath(tt.projectRoot)
			assertError(t, err, tt.expectError)
			if !tt.expectError && tt.checkResult != nil {
				tt.checkResult(t, result)
			}
		})
	}
}

func TestValidatePath(t *testing.T) {
	tempDir := t.TempDir()

	tests := []struct {
		name        string
		path        string
		expectError bool
	}{
		{
			name:        "existing directory",
			path:        tempDir,
			expectError: false,
		},
		{
			name:        "nonexistent path",
			path:        filepath.Join("nonexistent", "path"),
			expectError: true,
		},
		{
			name:        "empty path",
			path:        "",
			expectError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validatePath(tt.path)
			assertError(t, err, tt.expectError)
		})
	}
}

func TestCheckGoBricksCompatibilityWithRelativePaths(t *testing.T) {
	// Create a temporary directory and go.mod
	tempDir := t.TempDir()
	goModContent := `module test-project

go 1.21

require go-bricks v1.0.0
`
	goModPath := filepath.Join(tempDir, goModFile)
	err := os.WriteFile(goModPath, []byte(goModContent), 0644)
	if err != nil {
		t.Fatalf("Failed to create test go.mod: %v", err)
	}

	// Change to temp directory to test relative paths
	originalWd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Failed to get working directory: %v", err)
	}
	defer func() {
		os.Chdir(originalWd)
	}()

	err = os.Chdir(tempDir)
	if err != nil {
		t.Fatalf("Failed to change to temp directory: %v", err)
	}

	tests := []struct {
		name        string
		goModPath   string
		expectError bool
	}{
		{
			name:        "relative path - should work now",
			goModPath:   "./go.mod",
			expectError: false,
		},
		{
			name:        "current directory go.mod",
			goModPath:   goModFile,
			expectError: false,
		},
		{
			name:        "absolute path",
			goModPath:   goModPath,
			expectError: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := checkGoBricksCompatibility(tt.goModPath, false)
			assertError(t, err, tt.expectError)
		})
	}
}

func TestCheckGoBricksCompatibilitySecurityCases(t *testing.T) {
	tests := []struct {
		name        string
		goModPath   string
		expectError bool
		errorMsg    string
	}{
		{
			name:        "path traversal attempt",
			goModPath:   filepath.Join("tmp", "..", "etc", "passwd"),
			expectError: true,
			errorMsg:    "invalid go.mod path: must end with 'go.mod'",
		},
		{
			name:        "invalid filename",
			goModPath:   filepath.Join("tmp", "notgomod.txt"),
			expectError: true,
			errorMsg:    "invalid go.mod path: must end with 'go.mod'",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := checkGoBricksCompatibility(tt.goModPath, false)
			if !tt.expectError {
				t.Errorf("Expected no error, got: %v", err)
				return
			}
			if err == nil {
				t.Error("Expected error but got none")
				return
			}
			if !strings.Contains(err.Error(), tt.errorMsg) {
				t.Errorf("Expected error containing %q, got: %v", tt.errorMsg, err)
			}
		})
	}
}

func TestCheckProjectStructureWithRelativePaths(t *testing.T) {
	tempDir := t.TempDir()
	createTestGoFile(t, tempDir, testMainGoFile, packageMainContent)

	// Change to parent directory to test relative paths
	parentDir := filepath.Dir(tempDir)
	originalWd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Failed to get working directory: %v", err)
	}
	defer func() {
		os.Chdir(originalWd)
	}()

	err = os.Chdir(parentDir)
	if err != nil {
		t.Fatalf("Failed to change to parent directory: %v", err)
	}

	// Test with relative path
	relativePath := "./" + filepath.Base(tempDir)
	err = checkProjectStructure(relativePath)
	assertError(t, err, false)
}

func TestCheckProjectStructureWithDeepDirectories(t *testing.T) {
	tempDir := t.TempDir()

	// Create nested directory structure
	deepDir := filepath.Join(tempDir, "internal", "handlers", "v1")
	err := os.MkdirAll(deepDir, 0755)
	if err != nil {
		t.Fatalf("Failed to create deep directory: %v", err)
	}

	// Create Go file in deep directory
	createTestGoFile(t, deepDir, "handler.go", "package v1")

	// Should find Go files even in deeply nested directories
	err = checkProjectStructure(tempDir)
	assertError(t, err, false)
}

func TestCheckProjectStructureSkipsVendorAndHidden(t *testing.T) {
	tempDir := t.TempDir()

	// Create vendor directory with Go files (should be skipped)
	vendorDir := filepath.Join(tempDir, "vendor")
	err := os.MkdirAll(vendorDir, 0755)
	if err != nil {
		t.Fatalf("Failed to create vendor directory: %v", err)
	}
	createTestGoFile(t, vendorDir, "vendor.go", "package vendor")

	// Create hidden directory with Go files (should be skipped)
	hiddenDir := filepath.Join(tempDir, ".git")
	err = os.MkdirAll(hiddenDir, 0755)
	if err != nil {
		t.Fatalf("Failed to create .git directory: %v", err)
	}
	createTestGoFile(t, hiddenDir, "git.go", "package git")

	// Should report no Go files found since vendor and hidden dirs are skipped
	err = checkProjectStructure(tempDir)
	assertError(t, err, true) // Should error because no valid Go files found

	// Now add a valid Go file
	createTestGoFile(t, tempDir, "main.go", "package main")
	err = checkProjectStructure(tempDir)
	assertError(t, err, false) // Should succeed now
}

func TestRunDoctorMissingGoMod(t *testing.T) {
	tempDir := t.TempDir()

	// Create a test Go file but no go.mod
	createTestGoFile(t, tempDir, testMainGoFile, packageMainContent)

	opts := &DoctorOptions{
		ProjectRoot: tempDir,
		Verbose:     false,
	}

	err := runDoctor(opts)
	if err == nil {
		t.Error("Expected error for missing go.mod, but got none")
	}
}

func TestCheckProjectStructureErrors(t *testing.T) {
	tests := []struct {
		name        string
		setup       func(t *testing.T) string
		expectError bool
	}{
		{
			name: "glob error simulation - empty directory",
			setup: func(t *testing.T) string {
				tempDir := t.TempDir()
				// Create a directory with no Go files to trigger the "no Go files found" error
				return tempDir
			},
			expectError: true,
		},
		{
			name: "directory with only non-Go files",
			setup: func(t *testing.T) string {
				tempDir := t.TempDir()
				// Create some non-Go files
				err := os.WriteFile(filepath.Join(tempDir, "README.md"), []byte("# Test"), 0644)
				if err != nil {
					t.Fatalf(msgFailedToCreate, err)
				}
				err = os.WriteFile(filepath.Join(tempDir, "config.json"), []byte("{}"), 0644)
				if err != nil {
					t.Fatalf(msgFailedToCreate, err)
				}
				return tempDir
			},
			expectError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := tt.setup(t)
			err := checkProjectStructure(dir)
			assertError(t, err, tt.expectError)
		})
	}
}

func TestRunDoctorUnsupportedGoVersion(t *testing.T) {
	tempDir := t.TempDir()
	createTestGoFile(t, tempDir, testMainGoFile, packageMainContent)
	err := os.WriteFile(filepath.Join(tempDir, goModFile), []byte(`module test

go 1.21

require go-bricks v1.0.0
`), 0644)
	if err != nil {
		t.Fatalf(msgFailedToCreate, err)
	}

	opts := &DoctorOptions{
		ProjectRoot: tempDir,
		GoVersion:   "go1.20.5",
	}

	err = runDoctor(opts)
	assertError(t, err, true)
}

func TestRunDoctorWarnsAboutMissingGoBricks(t *testing.T) {
	tempDir := t.TempDir()
	createTestGoFile(t, tempDir, testMainGoFile, packageMainContent)
	err := os.WriteFile(filepath.Join(tempDir, goModFile), []byte(`module test

go 1.21

require github.com/spf13/cobra v1.8.0
`), 0644)
	if err != nil {
		t.Fatalf(msgFailedToCreate, err)
	}

	opts := &DoctorOptions{
		ProjectRoot: tempDir,
		GoVersion:   goVersion,
		Verbose:     true,
	}

	err = runDoctor(opts)
	assertError(t, err, false)
}

func TestCheckProjectStructureWalkError(t *testing.T) {
	tempDir := t.TempDir()
	originalWalk := walkDirFn
	walkDirFn = func(string, fs.WalkDirFunc) error {
		return errors.New("walk failure")
	}
	t.Cleanup(func() { walkDirFn = originalWalk })

	err := checkProjectStructure(tempDir)
	if err == nil {
		t.Fatal("Expected error from checkProjectStructure when walkDir fails")
	}
	if !strings.Contains(err.Error(), "failed to walk project directory") {
		t.Errorf(msgUnexpectedError, err)
	}
}

func TestValidatePathPropagatesStatError(t *testing.T) {
	originalStat := statFn
	statFn = func(string) (os.FileInfo, error) {
		return nil, errors.New("permission denied")
	}
	t.Cleanup(func() { statFn = originalStat })

	err := validatePath(filepath.Join("restricted", "path"))
	if err == nil {
		t.Fatal("Expected error from validatePath when stat fails")
	}
	if !strings.Contains(err.Error(), "permission denied") {
		t.Errorf(msgUnexpectedError, err)
	}
}

func TestValidateAndResolvePathSymlinkError(t *testing.T) {
	originalEval := evalSymlinksFn
	evalSymlinksFn = func(string) (string, error) {
		return "", errors.New("symlink loop")
	}
	t.Cleanup(func() { evalSymlinksFn = originalEval })

	_, err := validateAndResolvePath(filepath.Join(t.TempDir(), goModFile))
	if err == nil {
		t.Fatal("Expected error from validateAndResolvePath when EvalSymlinks fails")
	}
	if !strings.Contains(err.Error(), "failed to resolve symbolic links") {
		t.Errorf(msgUnexpectedError, err)
	}
}

func TestValidateAndResolvePathNullByte(t *testing.T) {
	_, err := validateAndResolvePath("go\x00.mod")
	if err == nil {
		t.Fatal("Expected error for path containing null byte")
	}
	if !strings.Contains(err.Error(), "invalid path") {
		t.Errorf(msgUnexpectedError, err)
	}
}

func TestCheckGoBricksCompatibilityReadError(t *testing.T) {
	originalRead := readFileFn
	readFileFn = func(string) ([]byte, error) {
		return nil, errors.New("boom")
	}
	t.Cleanup(func() { readFileFn = originalRead })

	tempDir := t.TempDir()
	path := filepath.Join(tempDir, goModFile)
	if err := os.WriteFile(path, []byte("module test\n"), 0644); err != nil {
		t.Fatalf(msgFailedToCreate, err)
	}

	err := checkGoBricksCompatibility(path, false)
	if err == nil {
		t.Fatal("Expected error when reading go.mod fails")
	}
	if !strings.Contains(err.Error(), "failed to read go.mod") {
		t.Errorf(msgUnexpectedError, err)
	}
}
