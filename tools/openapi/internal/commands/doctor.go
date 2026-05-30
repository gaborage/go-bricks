package commands

import (
	"context"
	"fmt"
	"go/build"
	"go/version"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/gaborage/go-bricks/tools/openapi/internal/analyzer"
	"github.com/gaborage/go-bricks/tools/openapi/internal/models"
	"github.com/spf13/cobra"
	"golang.org/x/mod/module"
	"golang.org/x/mod/semver"
)

// errGoBricksMissing is the canonical error when go.mod has no go-bricks
// dependency (a hard failure, distinct from a merely-outdated version).
var errGoBricksMissing = fmt.Errorf("%s dependency not found in go.mod", goBricksDep)

const (
	goModFile   = "go.mod"
	goBricksDep = "go-bricks"

	// Version floors, single-sourced to match go.mod (go 1.25) and the README.
	// minGoVersion is the toolchain floor in Go's own version format (compared
	// at language/minor granularity so 1.25.x patches and 1.25 RCs all qualify);
	// minGoBricksVer is the semver floor for the go-bricks features the generator
	// relies on.
	minGoVersion   = "go1.25"
	minGoBricksVer = "v0.13.0"

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
  go-bricks-openapi doctor --project ./my-service`,
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

	// Skip the (full-tree) project analysis once a hard error is known: it would
	// only print caveat noise that the error banner overrides anyway.
	var hasWarnings bool
	if !hasErrors {
		hasWarnings = performDiagnosticsCheck(opts)
	}

	// Final summary. Errors fail the run; warnings (e.g. no modules discovered,
	// or a module whose RegisterRoutes signature is unrecognized) still allow
	// generation but flip the banner from unconditional green to a caveat.
	fmt.Println()
	switch {
	case hasErrors:
		fmt.Println("❌ Health check failed - please fix the issues above")
		return fmt.Errorf("health check failed")
	case hasWarnings:
		fmt.Println("⚠️  Ready with caveats - review the warnings above before generating")
		return nil
	default:
		fmt.Println("✅ All checks passed - ready to generate OpenAPI specs!")
		return nil
	}
}

// performGoVersionCheck validates Go version compatibility
func performGoVersionCheck(opts *DoctorOptions, hasErrors bool) bool {
	goVersion := opts.GoVersion
	if goVersion == "" {
		goVersion = runtimeVersionFn()
	}
	fmt.Printf("📋 Go Version: %s\n", goVersion)
	if !isGoVersionSupported(goVersion) {
		fmt.Printf("❌ Go %s+ required\n", strings.TrimPrefix(minGoVersion, "go"))
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

	// A missing go-bricks dependency, a below-floor version, or an unreadable
	// go.mod is fatal: without the framework at a supported version the generator
	// can't produce a faithful spec. checkGoBricksCompatibility reports the
	// specific reason (and only the version path mentions the floor).
	if err := checkGoBricksCompatibility(goModPath, opts.Verbose); err != nil {
		return true
	}
	return hasErrors
}

// performDiagnosticsCheck runs module diagnostics and displays build environment.
// It returns whether any non-fatal caveat (no modules, unrecognized module
// method, or analysis failure) was surfaced.
func performDiagnosticsCheck(opts *DoctorOptions) bool {
	// Module diagnostics (analyze project structure)
	fmt.Println()
	fmt.Println("📊 Project Diagnostics:")
	hasWarnings := runModuleDiagnostics(opts.ProjectRoot, opts.Verbose)

	// Check build environment
	fmt.Println()
	fmt.Printf("🔧 GOROOT: %s\n", build.Default.GOROOT)
	fmt.Printf("🔧 GOPATH: %s\n", build.Default.GOPATH)
	return hasWarnings
}

func isGoVersionSupported(goVer string) bool {
	// Use Go's own version parser so real toolchain strings parse, including
	// release candidates like "go1.25rc1" (which semver cannot represent).
	if !version.IsValid(goVer) {
		return false
	}
	// Compare at language (minor) granularity so any 1.25.x patch and the 1.25
	// release candidates satisfy the floor, while 1.24.x and below do not.
	return version.Compare(version.Lang(goVer), minGoVersion) >= 0
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

// checkGoBricksCompatibility reports go-bricks compatibility. It prints the
// specific ❌ reason for each failure mode (so an I/O error never gets a
// version-floor hint) and returns a non-nil error iff the run should fail.
func checkGoBricksCompatibility(goModPath string, verbose bool) error {
	// Validate and resolve path securely to prevent path traversal
	cleanPath, err := validateAndResolvePath(goModPath)
	if err != nil {
		fmt.Printf("❌ cannot resolve go.mod path: %v\n", err)
		return err
	}

	content, err := readFileFn(cleanPath)
	if err != nil {
		fmt.Printf("❌ failed to read go.mod: %v\n", err)
		return fmt.Errorf("failed to read go.mod: %w", err)
	}

	// Parse go-bricks version from go.mod
	gbVer, isReplace, err := parseGoBricksVersion(string(content))
	if err != nil {
		fmt.Printf("❌ %v\n", err) // dependency not found
		return err
	}

	// Local development: dependency is present via a replace directive, so the
	// local checkout governs behavior — skip the version floor (but still report
	// the dependency unconditionally).
	if isReplace {
		fmt.Printf("ℹ️  %s: local replace directive detected (%s)\n", goBricksDep, gbVer)
		if verbose {
			fmt.Println("   → Skipping version compatibility check (using local development version)")
		}
		return nil
	}

	// Display version unconditionally so the dependency status is always visible.
	fmt.Printf("📦 %s version: %s\n", goBricksDep, gbVer)

	// Pseudo-versions (e.g. from `go get @main` / untagged or fork builds) sort
	// below any tagged floor by semver rules yet may track a commit ahead of it —
	// treat them like a replace directive and skip the floor rather than failing
	// with a misleading "below minimum" message.
	if module.IsPseudoVersion(gbVer) {
		fmt.Printf("ℹ️  %s is a pseudo-version (untagged build) — skipping the version floor\n", gbVer)
		return nil
	}

	// A below-floor (or unparseable) version is fatal.
	if err := checkVersionCompatibility(gbVer); err != nil {
		fmt.Printf("❌ %v\n   → OpenAPI generation requires %s %s+\n", err, goBricksDep, minGoBricksVer)
		return err
	}
	fmt.Printf("✅ %s version compatible\n", goBricksDep)
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
	if ver := findRequireVersion(lines); ver != "" {
		return ver, false, nil
	}

	return "", false, errGoBricksMissing
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
		if ver := extractVersionFromFields(fields); ver != "" {
			return ver
		}
	}
	return ""
}

// extractVersionFromFields extracts version from go.mod require line fields
func extractVersionFromFields(fields []string) string {
	for i, field := range fields {
		if strings.Contains(field, goBricksDep) && i+1 < len(fields) {
			ver := fields[i+1]
			// Remove "// indirect" or other comments
			if commentIdx := strings.Index(ver, "//"); commentIdx != -1 {
				ver = strings.TrimSpace(ver[:commentIdx])
			}
			return ver
		}
	}
	return ""
}

// checkVersionCompatibility validates go-bricks version meets minimum requirements
func checkVersionCompatibility(ver string) error {
	// Ensure version starts with 'v'
	if !strings.HasPrefix(ver, "v") {
		ver = "v" + ver
	}

	// Validate semver format
	if !semver.IsValid(ver) {
		return fmt.Errorf("invalid semantic version format: %s", ver)
	}

	// Check minimum version for OpenAPI metadata support
	if semver.Compare(ver, minGoBricksVer) < 0 {
		return fmt.Errorf("version %s is below minimum %s", ver, minGoBricksVer)
	}

	return nil
}

// runModuleDiagnostics analyzes the project and reports module/route statistics.
// It returns whether a caveat was surfaced: an analysis failure, zero modules
// discovered, or any analyzer diagnostic (e.g. a struct that looks like a module
// but whose RegisterRoutes signature is unrecognized).
func runModuleDiagnostics(projectRoot string, verbose bool) bool {
	a := analyzer.New(projectRoot)

	project, err := a.AnalyzeProject()
	if err != nil {
		fmt.Printf("⚠️  Module analysis failed: %v\n", err)
		return true
	}

	stats := calculateProjectStats(project)
	displayProjectStats(stats, verbose)

	warned := false
	if stats.ModuleCount == 0 {
		fmt.Println("⚠️  No go-bricks modules discovered — the generated spec would have no operations")
		warned = true
	}
	// displayProjectStats already printed a ⚠️ for untyped routes; fold it into the
	// caveat flag so the banner stays consistent with what was printed.
	if len(stats.UntypedRoutes) > 0 {
		warned = true
	}

	// Surface analyzer diagnostics collected during analysis (unrecognized module
	// methods, unresolvable route paths, etc.).
	for _, w := range a.Warnings(context.Background()) {
		fmt.Printf("⚠️  %s\n", w)
		warned = true
	}

	return warned
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

// classifyRoute determines the type information for a route. A route counts as
// "typed" when the analyzer resolved a *named* request or response type — the
// same gate the generator uses to emit a component $ref (see
// responsePayloadSchema). Keying off the name rather than the field count means
// a named-but-fieldless type (e.g. `type Ack struct{}`) is correctly reported as
// typed instead of triggering a false "no resolved type" / --strict failure.
func classifyRoute(route *models.Route) routeClassification {
	hasRequest := route.Request != nil && route.Request.Name != ""
	hasResponse := route.Response != nil && route.Response.Name != ""

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
