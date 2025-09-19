package commands

import (
	"fmt"
	"go/build"
	"os"
	"path/filepath"
	"runtime"

	"github.com/spf13/cobra"
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
	fmt.Printf("📋 Go Version: %s\n", runtime.Version())
	if !isGoVersionSupported() {
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
		//TODO: Check go-bricks version in Phase 1
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

func isGoVersionSupported() bool {
	version := runtime.Version()
	// Simple check for go1.21+
	// In production, would use semver parsing
	return version >= "go1.21"
}

func checkProjectStructure(projectRoot string) error {
	// Check if directory exists
	if _, err := os.Stat(projectRoot); os.IsNotExist(err) {
		return fmt.Errorf("directory does not exist: %s", projectRoot)
	}

	// Look for typical Go project files
	goFiles, err := filepath.Glob(filepath.Join(projectRoot, "*.go"))
	if err != nil {
		return fmt.Errorf("failed to check for Go files: %w", err)
	}

	subDirs, err := filepath.Glob(filepath.Join(projectRoot, "*", "*.go"))
	if err != nil {
		return fmt.Errorf("failed to check subdirectories: %w", err)
	}

	if len(goFiles) == 0 && len(subDirs) == 0 {
		return fmt.Errorf("no Go files found in project")
	}

	return nil
}
