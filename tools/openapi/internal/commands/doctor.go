package commands

import (
	"fmt"
	"go/build"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/gaborage/go-bricks/tools/openapi/internal/analyzer"
	"github.com/gaborage/go-bricks/tools/openapi/internal/models"
	"github.com/spf13/cobra"
	"golang.org/x/mod/semver"
)

const (
	goModFile      = "go.mod"
	goBricksDep    = "go-bricks"
	minGoBricksVer = "v0.5.0"

	// File patterns
	goFileExt   = ".go"
	testFileExt = "_test.go"

	// Skip directories
	vendorDir      = "vendor"
	nodeModulesDir = "node_modules"
)

var (
	runtimeVersionFn = runtime.Version
	statFn           = os.Stat
	readFileFn       = os.ReadFile
	evalSymlinksFn   = filepath.EvalSymlinks
	walkDirFn        = filepath.WalkDir
)

// DoctorOptions holds options for the doctor command
type DoctorOptions struct {
	ProjectRoot string
	Verbose     bool
	GoVersion   string
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

	// Perform all health checks
	hasErrors = performGoVersionCheck(opts, hasErrors)
	hasErrors = performProjectStructureCheck(opts, hasErrors)
	hasErrors = performGoModCheck(opts, hasErrors)
	performDiagnosticsCheck(opts)

	// Final summary
	fmt.Println()
	if hasErrors {
		fmt.Println("❌ Health check failed - please fix the issues above")
		return fmt.Errorf("health check failed")
	}

	fmt.Println("✅ All checks passed - ready to generate OpenAPI specs!")
	return nil
}

// performGoVersionCheck validates Go version compatibility
func performGoVersionCheck(opts *DoctorOptions, hasErrors bool) bool {
	goVersion := opts.GoVersion
	if goVersion == "" {
		goVersion = runtimeVersionFn()
	}
	fmt.Printf("📋 Go Version: %s\n", goVersion)
	if !isGoVersionSupported(goVersion) {
		fmt.Println("❌ Go version 1.21+ required")
		return true
	}
	fmt.Println("✅ Go version compatible")
	return hasErrors
}

// performProjectStructureCheck validates project directory structure
func performProjectStructureCheck(opts *DoctorOptions, hasErrors bool) bool {
	fmt.Printf("📁 Project Root: %s\n", opts.ProjectRoot)
	if err := checkProjectStructure(opts.ProjectRoot); err != nil {
		fmt.Printf("❌ Project structure: %v\n", err)
		return true
	}
	fmt.Println("✅ Project structure valid")
	return hasErrors
}

// performGoModCheck validates go.mod existence and go-bricks compatibility
func performGoModCheck(opts *DoctorOptions, hasErrors bool) bool {
	goModPath := filepath.Join(opts.ProjectRoot, goModFile)
	if _, err := statFn(goModPath); err != nil {
		fmt.Println("❌ No go.mod found")
		return true
	}
	fmt.Println("✅ go.mod found")

	// Basic go-bricks version check (expanded in Phase 1)
	if err := checkGoBricksCompatibility(goModPath, opts.Verbose); err != nil {
		if opts.Verbose {
			fmt.Printf("⚠️  go-bricks compatibility: %v\n", err)
		}
		// Non-fatal for now - just warn in verbose mode
	}
	return hasErrors
}

// performDiagnosticsCheck runs module diagnostics and displays build environment
func performDiagnosticsCheck(opts *DoctorOptions) {
	// Module diagnostics (analyze project structure)
	fmt.Println()
	fmt.Println("📊 Project Diagnostics:")
	if err := runModuleDiagnostics(opts.ProjectRoot, opts.Verbose); err != nil {
		if opts.Verbose {
			fmt.Printf("⚠️  Module diagnostics: %v\n", err)
		}
		// Non-fatal - just informational
	}

	// Check build environment
	fmt.Println()
	fmt.Printf("🔧 GOROOT: %s\n", build.Default.GOROOT)
	fmt.Printf("🔧 GOPATH: %s\n", build.Default.GOPATH)
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
	err = walkDirFn(absRoot, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil // Skip directories with permission issues
		}

		// Skip hidden directories and vendor/node_modules
		if d.IsDir() {
			name := d.Name()
			if strings.HasPrefix(name, ".") || name == vendorDir || name == nodeModulesDir {
				return filepath.SkipDir
			}
			return nil
		}

		// Check for .go files (excluding test files for basic validation)
		if strings.HasSuffix(path, goFileExt) && !strings.HasSuffix(path, testFileExt) {
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

// checkGoBricksCompatibility performs compatibility check with go-bricks framework
func checkGoBricksCompatibility(goModPath string, verbose bool) error {
	// Validate and resolve path securely to prevent path traversal
	cleanPath, err := validateAndResolvePath(goModPath)
	if err != nil {
		return err
	}

	content, err := readFileFn(cleanPath)
	if err != nil {
		return fmt.Errorf("failed to read go.mod: %w", err)
	}

	goModContent := string(content)

	// Parse go-bricks version from go.mod
	version, isReplace, err := parseGoBricksVersion(goModContent)
	if err != nil {
		return err
	}

	// Handle local replace directives
	if isReplace {
		if verbose {
			fmt.Printf("ℹ️  go-bricks: local replace directive detected (%s)\n", version)
			fmt.Println("   → Skipping version compatibility check (using local development version)")
		}
		return nil
	}

	// Display version
	fmt.Printf("📦 go-bricks version: %s\n", version)

	// Check version compatibility
	if err := checkVersionCompatibility(version); err != nil {
		fmt.Printf("⚠️  Version compatibility: %v\n", err)
		fmt.Printf("   → OpenAPI metadata features require %s %s+\n", goBricksDep, minGoBricksVer)
		// Non-fatal warning
	} else {
		fmt.Printf("✅ %s version compatible\n", goBricksDep)
	}

	return nil
}

// parseGoBricksVersion extracts the go-bricks version from go.mod content
// Returns (version, isReplaceDirective, error)
//
//nolint:gocritic // Named returns would reduce clarity for boolean flag parameter
func parseGoBricksVersion(goModContent string) (string, bool, error) {
	lines := strings.Split(goModContent, "\n")

	// First check for replace directives (local development)
	if replacePath := findReplaceDirective(lines); replacePath != "" {
		return replacePath, true, nil
	}

	// Look for require directive with version
	if version := findRequireVersion(lines); version != "" {
		return version, false, nil
	}

	return "", false, fmt.Errorf("%s dependency not found in go.mod", goBricksDep)
}

// findReplaceDirective searches for a replace directive in go.mod lines
func findReplaceDirective(lines []string) string {
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "replace") && strings.Contains(trimmed, goBricksDep) {
			// Format: replace github.com/gaborage/go-bricks => ../local-path
			parts := strings.Split(trimmed, "=>")
			if len(parts) == 2 {
				return strings.TrimSpace(parts[1])
			}
		}
	}
	return ""
}

// findRequireVersion searches for a require directive with version in go.mod lines
func findRequireVersion(lines []string) string {
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if !strings.Contains(trimmed, goBricksDep) || strings.HasPrefix(trimmed, "//") {
			continue
		}

		// Format: github.com/gaborage/go-bricks v0.5.0
		// or:     github.com/gaborage/go-bricks v0.5.0 // indirect
		fields := strings.Fields(trimmed)
		if version := extractVersionFromFields(fields); version != "" {
			return version
		}
	}
	return ""
}

// extractVersionFromFields extracts version from go.mod require line fields
func extractVersionFromFields(fields []string) string {
	for i, field := range fields {
		if strings.Contains(field, goBricksDep) && i+1 < len(fields) {
			version := fields[i+1]
			// Remove "// indirect" or other comments
			if commentIdx := strings.Index(version, "//"); commentIdx != -1 {
				version = strings.TrimSpace(version[:commentIdx])
			}
			return version
		}
	}
	return ""
}

// checkVersionCompatibility validates go-bricks version meets minimum requirements
func checkVersionCompatibility(version string) error {
	// Ensure version starts with 'v'
	if !strings.HasPrefix(version, "v") {
		version = "v" + version
	}

	// Validate semver format
	if !semver.IsValid(version) {
		return fmt.Errorf("invalid semantic version format: %s", version)
	}

	// Check minimum version for OpenAPI metadata support
	if semver.Compare(version, minGoBricksVer) < 0 {
		return fmt.Errorf("version %s is below minimum %s", version, minGoBricksVer)
	}

	return nil
}

// runModuleDiagnostics analyzes the project and reports module/route statistics
func runModuleDiagnostics(projectRoot string, verbose bool) error {
	// Create analyzer
	a := analyzer.New(projectRoot)

	// Analyze project
	project, err := a.AnalyzeProject()
	if err != nil {
		return fmt.Errorf("failed to analyze project: %w", err)
	}

	// Calculate statistics
	stats := calculateProjectStats(project)

	// Display results
	displayProjectStats(stats, verbose)

	return nil
}

// ProjectStats holds project analysis statistics
type ProjectStats struct {
	ModuleCount         int
	RouteCount          int
	TypedRoutes         int
	TypedRequestRoutes  int
	TypedResponseRoutes int
	UntypedRoutes       []string // Handler names of untyped routes
}

// calculateProjectStats computes statistics from analyzed project
func calculateProjectStats(project *models.Project) ProjectStats {
	stats := ProjectStats{
		ModuleCount:   len(project.Modules),
		UntypedRoutes: []string{},
	}

	for _, module := range project.Modules {
		stats.RouteCount += len(module.Routes)
		for i := range module.Routes {
			updateStatsForRoute(&stats, &module.Routes[i])
		}
	}

	return stats
}

// routeClassification holds the type information for a route
type routeClassification struct {
	hasRequest  bool
	hasResponse bool
	handlerID   string
}

// classifyRoute determines the type information for a route
func classifyRoute(route *models.Route) routeClassification {
	hasRequest := route.Request != nil && len(route.Request.Fields) > 0
	hasResponse := route.Response != nil && len(route.Response.Fields) > 0

	handlerID := route.HandlerName
	if handlerID == "" {
		handlerID = fmt.Sprintf("%s %s", route.Method, route.Path)
	}

	return routeClassification{
		hasRequest:  hasRequest,
		hasResponse: hasResponse,
		handlerID:   handlerID,
	}
}

// updateStatsForRoute updates statistics based on route classification
func updateStatsForRoute(stats *ProjectStats, route *models.Route) {
	classification := classifyRoute(route)

	// Update typed route counters
	if classification.hasRequest || classification.hasResponse {
		stats.TypedRoutes++
	} else {
		// Track untyped routes
		stats.UntypedRoutes = append(stats.UntypedRoutes, classification.handlerID)
	}

	// Update specific type counters
	if classification.hasRequest {
		stats.TypedRequestRoutes++
	}
	if classification.hasResponse {
		stats.TypedResponseRoutes++
	}
}

// displayProjectStats outputs formatted statistics
func displayProjectStats(stats ProjectStats, verbose bool) {
	fmt.Printf("   📦 Modules discovered: %d\n", stats.ModuleCount)
	fmt.Printf("   🛣️  Routes discovered: %d\n", stats.RouteCount)

	if stats.RouteCount > 0 {
		typeCoverage := (float64(stats.TypedRoutes) / float64(stats.RouteCount)) * 100
		fmt.Printf("   ✨ Typed routes: %d/%d (%.1f%%)\n", stats.TypedRoutes, stats.RouteCount, typeCoverage)

		if verbose {
			fmt.Printf("      • Request types: %d\n", stats.TypedRequestRoutes)
			fmt.Printf("      • Response types: %d\n", stats.TypedResponseRoutes)
		}

		// Warn about untyped routes
		if len(stats.UntypedRoutes) > 0 {
			fmt.Printf("   ⚠️  Routes without type information: %d\n", len(stats.UntypedRoutes))
			if verbose {
				fmt.Println("      Missing types for:")
				for _, handler := range stats.UntypedRoutes {
					fmt.Printf("      • %s\n", handler)
				}
			}
		}
	}
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
	if _, err := statFn(path); os.IsNotExist(err) {
		return fmt.Errorf("path does not exist: %s", path)
	} else if err != nil {
		return fmt.Errorf("failed to access path %s: %w", path, err)
	}
	return nil
}

// validateAndResolvePath securely validates and resolves a go.mod file path
// to prevent path traversal attacks (addresses G304 security warning)
func validateAndResolvePath(goModPath string) (string, error) {
	// Additional security: check for null bytes and other suspicious patterns early
	if strings.Contains(goModPath, "\x00") {
		return "", fmt.Errorf("invalid path: contains null byte")
	}

	// Clean and resolve to absolute path
	cleanPath := filepath.Clean(goModPath)
	absPath, err := filepath.Abs(cleanPath)
	if err != nil {
		return "", fmt.Errorf("failed to resolve absolute path: %w", err)
	}

	// Security validation: ensure the path ends with "go.mod"
	// This prevents reading arbitrary files
	if filepath.Base(absPath) != goModFile {
		return "", fmt.Errorf("invalid go.mod path: must end with 'go.mod'")
	}

	// Evaluate any symbolic links to get the final path
	// This prevents symlink-based attacks
	realPath, err := evalSymlinksFn(absPath)
	if err != nil {
		// If EvalSymlinks fails, it might be because the file doesn't exist
		// In that case, we still want to validate the path structure
		if !os.IsNotExist(err) {
			return "", fmt.Errorf("failed to resolve symbolic links: %w", err)
		}
		realPath = absPath
	}

	// Final check: ensure the resolved path still ends with go.mod
	if filepath.Base(realPath) != goModFile {
		return "", fmt.Errorf("security violation: resolved path does not end with go.mod")
	}

	// Validate that the file exists and is accessible
	if err := validatePath(realPath); err != nil {
		return "", err
	}

	return realPath, nil
}
