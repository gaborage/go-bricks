package commands

import (
	"fmt"
	"go/build"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/spf13/cobra"
	"golang.org/x/mod/semver"
)

// DoctorOptions holds options for the doctor command
type DoctorOptions struct {
	ProjectRoot string
	Verbose     bool
}

// NewDoctorCommand creates the doctor command
func NewDoctorCommand() *cobra.Command {
	opts := &DoctorOptions{}

	cmd := &cobra.Command{
		Use:   "doctor",
		Short: "Check environment and project compatibility",
		Long: `Performs health checks on the environment and project to ensure
the OpenAPI generator can run successfully.

Checks include:
- Go version compatibility
- go-bricks framework version
- Project structure validation
- Required dependencies`,
		Example: `  # Check current directory
  go-bricks-openapi doctor

  # Check specific project
  go-bricks-openapi doctor -project ./my-service`,
		RunE: func(_ *cobra.Command, _ []string) error {
			return runDoctor(opts)
		},
	}

	// Flags
	cmd.Flags().StringVarP(&opts.ProjectRoot, "project", "p", ".", "Project root directory")
	cmd.Flags().BoolVarP(&opts.Verbose, "verbose", "v", false, "Verbose output")

	return cmd
}

func runDoctor(opts *DoctorOptions) error {
	fmt.Println("🏥 Running go-bricks-openapi health check...")
	fmt.Println()

	var hasErrors bool

	// Check Go version
	goVersion := runtime.Version()
	fmt.Printf("📋 Go Version: %s\n", goVersion)
	if !isGoVersionSupported(goVersion) {
		fmt.Println("❌ Go version 1.21+ required")
		hasErrors = true
	} else {
		fmt.Println("✅ Go version compatible")
	}

	// Check project structure
	fmt.Printf("📁 Project Root: %s\n", opts.ProjectRoot)
	if err := checkProjectStructure(opts.ProjectRoot); err != nil {
		fmt.Printf("❌ Project structure: %v\n", err)
		hasErrors = true
	} else {
		fmt.Println("✅ Project structure valid")
	}

	// Check go.mod
	goModPath := filepath.Join(opts.ProjectRoot, "go.mod")
	if _, err := os.Stat(goModPath); err != nil {
		fmt.Println("❌ No go.mod found")
		hasErrors = true
	} else {
		fmt.Println("✅ go.mod found")

		// Basic go-bricks version check (expanded in Phase 1)
		if err := checkGoBricksCompatibility(goModPath, opts.Verbose); err != nil {
			if opts.Verbose {
				fmt.Printf("⚠️  go-bricks compatibility: %v\n", err)
			}
			// Non-fatal for now - just warn in verbose mode
		}
	}

	// Check build environment
	fmt.Printf("🔧 GOROOT: %s\n", build.Default.GOROOT)
	fmt.Printf("🔧 GOPATH: %s\n", build.Default.GOPATH)

	fmt.Println()
	if hasErrors {
		fmt.Println("❌ Health check failed - please fix the issues above")
		return fmt.Errorf("health check failed")
	}

	fmt.Println("✅ All checks passed - ready to generate OpenAPI specs!")
	return nil
}

func isGoVersionSupported(version string) bool {
	// Convert Go version (e.g., "go1.21.5") to semver format (e.g., "v1.21.5")
	if !strings.HasPrefix(version, "go") {
		return false
	}

	// Remove "go" prefix and add "v" prefix for semver
	semverVersion := "v" + strings.TrimPrefix(version, "go")

	// Check if version is valid semver format
	if !semver.IsValid(semverVersion) {
		return false
	}

	// Compare with minimum required version (1.21.0)
	minVersion := "v1.21.0"
	return semver.Compare(semverVersion, minVersion) >= 0
}

func checkProjectStructure(projectRoot string) error {
	// Resolve to absolute path and validate
	absRoot, err := resolveProjectPath(projectRoot)
	if err != nil {
		return err
	}

	if err := validatePath(absRoot); err != nil {
		return err
	}

	// Use filepath.WalkDir for more thorough Go file discovery
	var goFilesFound bool
	err = filepath.WalkDir(absRoot, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil // Skip directories with permission issues
		}

		// Skip hidden directories and vendor/node_modules
		if d.IsDir() {
			name := d.Name()
			if strings.HasPrefix(name, ".") || name == "vendor" || name == "node_modules" {
				return filepath.SkipDir
			}
			return nil
		}

		// Check for .go files (excluding test files for basic validation)
		if strings.HasSuffix(path, ".go") && !strings.HasSuffix(path, "_test.go") {
			goFilesFound = true
			return filepath.SkipAll // Found at least one, we can stop searching
		}

		return nil
	})

	if err != nil {
		return fmt.Errorf("failed to walk project directory: %w", err)
	}

	if !goFilesFound {
		return fmt.Errorf("no Go files found in project")
	}

	return nil
}

// checkGoBricksCompatibility performs basic compatibility check with go-bricks framework
// This is a placeholder implementation that will be expanded in Phase 1
func checkGoBricksCompatibility(goModPath string, verbose bool) error {
	// Validate and resolve path securely to prevent path traversal
	cleanPath, err := validateAndResolvePath(goModPath)
	if err != nil {
		return err
	}

	content, err := os.ReadFile(cleanPath)
	if err != nil {
		return fmt.Errorf("failed to read go.mod: %w", err)
	}

	goModContent := string(content)

	// Basic check for go-bricks presence
	if !strings.Contains(goModContent, "go-bricks") {
		return fmt.Errorf("go-bricks dependency not found in go.mod")
	}

	if verbose {
		fmt.Println("✅ go-bricks dependency detected")
	}

	// Phase 1 will add:
	// - Semantic version parsing of go-bricks version
	// - Compatibility matrix checking
	// - Module interface validation
	// - Route descriptor version checking

	return nil
}

// resolveProjectPath converts a relative project path to absolute path
func resolveProjectPath(projectRoot string) (string, error) {
	cleanPath := filepath.Clean(projectRoot)
	if filepath.IsAbs(cleanPath) {
		return cleanPath, nil
	}

	absPath, err := filepath.Abs(cleanPath)
	if err != nil {
		return "", fmt.Errorf("failed to resolve absolute path for %s: %w", projectRoot, err)
	}

	return absPath, nil
}

// validatePath ensures the path exists and is accessible
func validatePath(path string) error {
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return fmt.Errorf("path does not exist: %s", path)
	} else if err != nil {
		return fmt.Errorf("failed to access path %s: %w", path, err)
	}
	return nil
}

// validateAndResolvePath securely validates and resolves a go.mod file path
// to prevent path traversal attacks (addresses G304 security warning)
func validateAndResolvePath(goModPath string) (string, error) {
	// Clean and resolve to absolute path
	cleanPath := filepath.Clean(goModPath)
	absPath, err := filepath.Abs(cleanPath)
	if err != nil {
		return "", fmt.Errorf("failed to resolve absolute path: %w", err)
	}

	// Security validation: ensure the path ends with "go.mod"
	// This prevents reading arbitrary files
	if filepath.Base(absPath) != "go.mod" {
		return "", fmt.Errorf("invalid go.mod path: must end with 'go.mod'")
	}

	// Additional security: check for null bytes and other suspicious patterns
	if strings.Contains(goModPath, "\x00") {
		return "", fmt.Errorf("invalid path: contains null byte")
	}

	// Evaluate any symbolic links to get the final path
	// This prevents symlink-based attacks
	realPath, err := filepath.EvalSymlinks(absPath)
	if err != nil {
		// If EvalSymlinks fails, it might be because the file doesn't exist
		// In that case, we still want to validate the path structure
		if !os.IsNotExist(err) {
			return "", fmt.Errorf("failed to resolve symbolic links: %w", err)
		}
		realPath = absPath
	}

	// Final check: ensure the resolved path still ends with go.mod
	if filepath.Base(realPath) != "go.mod" {
		return "", fmt.Errorf("security violation: resolved path does not end with go.mod")
	}

	// Validate that the file exists and is accessible
	if err := validatePath(realPath); err != nil {
		return "", err
	}

	return realPath, nil
}
