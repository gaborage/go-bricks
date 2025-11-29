package analyzer

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/gaborage/go-bricks/tools/openapi/internal/models"
)

const (
	testModuleName         = "testmodule"
	testModuleDescription  = "Test module for API operations"
	getUserRoute           = "/users/:id"
	createUserRoute        = "/users"
	testHandlerName        = "getUser"
	testSummary            = "Get user by ID"
	testDescription        = "Retrieves a user by their unique identifier"
	testTag1               = "users"
	testTag2               = "management"
	splitListRoute         = "/split/users"
	splitCreateRoute       = "/split/users"
	splitModuleTag         = "split-module"
	splitListSummary       = "List split module users"
	splitCreateDescription = "Create split module user"

	// Test file names
	moduleFileName = "module.go"
	testFileName   = "test.go"

	// Test error message formats
	expectedGotFormat       = "Expected %q, got %q"
	parseFailedFormat       = "Failed to parse content: %v"
	expectedOneModuleFormat = "Expected 1 module, got %d"
	expectedTwoRoutesFormat = "Expected 2 routes, got %d"
	testServerImportPath    = "github.com/gaborage/go-bricks/server"

	testUserEmail    = "user@example.com"
	testUserIDHeader = "X-User-ID"
)

// createTestModuleFile creates a test Go file that represents a go-bricks module
func createTestModuleFile(t *testing.T, tempDir string) string {
	t.Helper()

	moduleContent := `// Package testmodule demonstrates go-bricks module implementation
// ` + testModuleDescription + `
package testmodule

import (
	"github.com/gaborage/go-bricks/app"
	"github.com/gaborage/go-bricks/messaging"
	"github.com/gaborage/go-bricks/server"
)

// Module implements the go-bricks Module interface
type Module struct {
	deps *app.ModuleDeps
}

// Name returns the module name
func (m *Module) Name() string {
	return "` + testModuleName + `"
}

// Init initializes the module with dependencies
func (m *Module) Init(deps *app.ModuleDeps) error {
	m.deps = deps
	return nil
}

// RegisterRoutes registers HTTP routes for this module
func (m *Module) RegisterRoutes(hr *server.HandlerRegistry, r server.RouteRegistrar) {
	// Simple route without metadata
	server.GET(hr, r, "` + getUserRoute + `", m.` + testHandlerName + `)

	// Enhanced route with metadata
	server.POST(hr, r, "` + createUserRoute + `", m.createUser,
		server.WithTags("` + testTag1 + `", "` + testTag2 + `"),
		server.WithSummary("` + testSummary + `"),
		server.WithDescription("` + testDescription + `"))
}

// DeclareMessaging declares messaging infrastructure for this module
func (m *Module) DeclareMessaging(decls *messaging.Declarations) {
	// No messaging in this test
}

// Shutdown cleans up module resources
func (m *Module) Shutdown() error {
	return nil
}

// Handler methods
func (m *Module) ` + testHandlerName + `(req GetUserReq, ctx server.HandlerContext) (UserResp, server.IAPIError) {
	return UserResp{}, nil
}

func (m *Module) createUser(req CreateUserReq, ctx server.HandlerContext) (UserResp, server.IAPIError) {
	return UserResp{}, nil
}

// Request/Response types
type GetUserReq struct {
	ID int ` + "`" + `param:"id" validate:"required,min=1" doc:"User ID"` + "`" + `
}

type CreateUserReq struct {
	Name  string ` + "`" + `json:"name" validate:"required,min=2" doc:"User name"` + "`" + `
	Email string ` + "`" + `json:"email" validate:"required,email" doc:"User email"` + "`" + `
}

type UserResp struct {
	ID    int    ` + "`" + `json:"id" doc:"User ID"` + "`" + `
	Name  string ` + "`" + `json:"name" doc:"User name"` + "`" + `
	Email string ` + "`" + `json:"email" doc:"User email"` + "`" + `
}
`

	moduleFile := filepath.Join(tempDir, moduleFileName)
	err := os.WriteFile(moduleFile, []byte(moduleContent), 0644)
	if err != nil {
		t.Fatalf("Failed to create test module file: %v", err)
	}

	return moduleFile
}

// createTestGoMod creates a test go.mod file
func createTestGoMod(t *testing.T, tempDir string) {
	t.Helper()

	goModContent := `module github.com/example/test-service

go 1.21

require (
	github.com/gaborage/go-bricks v0.6.0
)
`

	goModFile := filepath.Join(tempDir, "go.mod")
	err := os.WriteFile(goModFile, []byte(goModContent), 0644)
	if err != nil {
		t.Fatalf("Failed to create test go.mod file: %v", err)
	}
}

// createTestNonModuleFile creates a Go file that is not a go-bricks module
func createTestNonModuleFile(t *testing.T, tempDir string) {
	t.Helper()

	nonModuleContent := `package util

import "fmt"

// Helper function, not a module
func FormatMessage(msg string) string {
	return fmt.Sprintf("Message: %s", msg)
}
`

	utilFile := filepath.Join(tempDir, "util.go")
	err := os.WriteFile(utilFile, []byte(nonModuleContent), 0644)
	if err != nil {
		t.Fatalf("Failed to create test util file: %v", err)
	}
}

func TestNew(t *testing.T) {
	// Use a platform-agnostic path for testing
	testPath := filepath.Join("test", "path")
	analyzer := New(testPath)

	if analyzer == nil {
		t.Fatal("New() returned nil")
	}

	if analyzer.projectRoot != testPath {
		t.Errorf("Expected project root '%s', got '%s'", testPath, analyzer.projectRoot)
	}

	if analyzer.fileSet == nil {
		t.Error("FileSet should be initialized")
	}
}

func TestIsFrameworkType(t *testing.T) {
	analyzer := New("")

	tests := []struct {
		name     string
		typeName string
		pkgName  string
		expected bool
	}{
		{"HandlerContext without package", "HandlerContext", "", true},
		{"IAPIError without package", "IAPIError", "", true},
		{"error type", "error", "", true},
		{"server.HandlerContext qualified", "HandlerContext", "server", true},
		{"server.IAPIError qualified", "IAPIError", "server", true},
		{"user type without package", "CreateUserReq", "", false},
		{"user type with package", "CreateUserReq", "models", false},
		{"other package type", "SomeType", "otherpkg", false},
		{"HandlerContext with wrong package", "HandlerContext", "otherpkg", true},
		{"IAPIError with wrong package", "IAPIError", "otherpkg", true},
		{"unrelated type in server package", "UnrelatedType", "server", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := analyzer.isFrameworkType(tt.typeName, tt.pkgName)
			if result != tt.expected {
				t.Errorf("isFrameworkType(%q, %q) = %v, expected %v", tt.typeName, tt.pkgName, result, tt.expected)
			}
		})
	}
}

func TestAnalyzeProject(t *testing.T) {
	// Create a temporary directory for testing
	tempDir := t.TempDir()

	// Create test files
	createTestGoMod(t, tempDir)
	createTestModuleFile(t, tempDir)
	createTestNonModuleFile(t, tempDir)

	analyzer := New(tempDir)
	project, err := analyzer.AnalyzeProject()

	if err != nil {
		t.Fatalf("AnalyzeProject() failed: %v", err)
	}

	if project == nil {
		t.Fatal("AnalyzeProject() returned nil project")
	}

	// Validate project metadata
	if project.Name == "" {
		t.Error("Project name should not be empty")
	}

	if project.Version == "" {
		t.Error("Project version should not be empty")
	}

	// Should have discovered one module
	if len(project.Modules) != 1 {
		t.Errorf(expectedOneModuleFormat, len(project.Modules))
	}

	if len(project.Modules) > 0 {
		module := project.Modules[0]
		validateDiscoveredModule(t, &module)
	}
}

func validateDiscoveredModule(t *testing.T, module *models.Module) {
	t.Helper()

	if module.Name != testModuleName {
		t.Errorf("Expected module name '%s', got '%s'", testModuleName, module.Name)
	}

	if module.Package != testModuleName {
		t.Errorf("Expected package name '%s', got '%s'", testModuleName, module.Package)
	}

	if !containsSubstring(module.Description, "Test module") {
		t.Errorf("Expected module description to contain 'Test module', got '%s'", module.Description)
	}

	// Should have discovered routes
	if len(module.Routes) != 2 {
		t.Errorf(expectedTwoRoutesFormat, len(module.Routes))
	}

	// Validate routes
	for _, route := range module.Routes {
		validateDiscoveredRoute(t, &route)
	}
}

func validateDiscoveredRoute(t *testing.T, route *models.Route) {
	t.Helper()

	if route.Method == "" {
		t.Error("Route method should not be empty")
	}

	if route.Path == "" {
		t.Error("Route path should not be empty")
	}

	// Check specific routes
	switch route.Path {
	case getUserRoute:
		assertGetRoute(t, route)
	case createUserRoute:
		assertCreateRoute(t, route)
	}
}

func assertGetRoute(t *testing.T, route *models.Route) {
	t.Helper()
	if route.Method != "GET" {
		t.Errorf("Expected GET method for %s, got %s", getUserRoute, route.Method)
	}
	if route.HandlerName != testHandlerName {
		t.Errorf("Expected handler name '%s', got '%s'", testHandlerName, route.HandlerName)
	}
}

func assertCreateRoute(t *testing.T, route *models.Route) {
	t.Helper()
	if route.Method != "POST" {
		t.Errorf("Expected POST method for %s, got %s", createUserRoute, route.Method)
	}
	if route.Summary != testSummary {
		t.Errorf("Expected summary '%s', got '%s'", testSummary, route.Summary)
	}
	if route.Description != testDescription {
		t.Errorf("Expected description '%s', got '%s'", testDescription, route.Description)
	}
	if !slices.Contains(route.Tags, testTag1) || !slices.Contains(route.Tags, testTag2) {
		t.Errorf("Expected tags to contain '%s' and '%s', got %v", testTag1, testTag2, route.Tags)
	}
}

func TestDiscoverProjectMetadata(t *testing.T) {
	tempDir := t.TempDir()
	createTestGoMod(t, tempDir)

	analyzer := New(tempDir)
	project := &models.Project{}

	analyzer.discoverProjectMetadata(project)

	// Should have extracted project name from go.mod
	if project.Name == "" {
		t.Error("Project name should be extracted from go.mod")
	}

	expectedName := "Test-service API"
	if project.Name != expectedName {
		t.Errorf("Expected project name '%s', got '%s'", expectedName, project.Name)
	}
}

func TestAnalyzeGoFile(t *testing.T) {
	tempDir := t.TempDir()
	moduleFile := createTestModuleFile(t, tempDir)

	analyzer := New(tempDir)
	module, err := analyzer.analyzeGoFile(moduleFile)

	if err != nil {
		t.Fatalf("analyzeGoFile() failed: %v", err)
	}

	if module == nil {
		t.Fatal("analyzeGoFile() returned nil module")
	}

	validateDiscoveredModule(t, module)
}

func TestAnalyzeNonModuleFile(t *testing.T) {
	tempDir := t.TempDir()
	createTestNonModuleFile(t, tempDir)

	utilFile := filepath.Join(tempDir, "util.go")
	analyzer := New(tempDir)
	module, err := analyzer.analyzeGoFile(utilFile)

	if err != nil {
		t.Fatalf("analyzeGoFile() failed: %v", err)
	}

	// Should return nil for non-module files
	if module != nil {
		t.Error("analyzeGoFile() should return nil for non-module files")
	}
}

func TestIsHTTPMethod(t *testing.T) {
	analyzer := New("test")

	tests := []struct {
		method   string
		expected bool
	}{
		{"GET", true},
		{"POST", true},
		{"PUT", true},
		{"DELETE", true},
		{"PATCH", true},
		{"HEAD", true},
		{"OPTIONS", true},
		{"get", true}, // case insensitive
		{"Invalid", false},
		{"", false},
	}

	for _, tt := range tests {
		t.Run(tt.method, func(t *testing.T) {
			result := analyzer.isHTTPMethod(tt.method)
			if result != tt.expected {
				t.Errorf("isHTTPMethod(%s) = %v, expected %v", tt.method, result, tt.expected)
			}
		})
	}
}

func containsSubstring(str, substr string) bool {
	if str == "" || substr == "" {
		return false
	}
	return strings.Contains(str, substr)
}

// Additional comprehensive test coverage

// TestExtractCommentDescription tests comment extraction functionality
func TestExtractCommentDescription(t *testing.T) {
	analyzer := New("test")

	tests := []struct {
		name     string
		comments []string
		expected string
	}{
		{
			name:     "single line comment",
			comments: []string{"// This is a test comment"},
			expected: "This is a test comment",
		},
		{
			name:     "multiple line comments",
			comments: []string{"// First line", "// Second line"},
			expected: "First line Second line",
		},
		{
			name:     "mixed comment styles",
			comments: []string{"/* Block comment */", "// Line comment"},
			expected: "Block comment Line comment",
		},
		{
			name:     "empty comments",
			comments: []string{"//", "/* */"},
			expected: "",
		},
		{
			name:     "nil comment group",
			comments: nil,
			expected: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var commentGroup *ast.CommentGroup
			if tt.comments != nil {
				var comments []*ast.Comment
				for _, text := range tt.comments {
					comments = append(comments, &ast.Comment{Text: text})
				}
				commentGroup = &ast.CommentGroup{List: comments}
			}

			result := analyzer.extractCommentDescription(commentGroup)
			if result != tt.expected {
				t.Errorf(expectedGotFormat, tt.expected, result)
			}
		})
	}
}

// TestExtractStringFromExpr tests string extraction from expressions
func TestExtractStringFromExpr(t *testing.T) {
	analyzer := New("test")

	tests := []struct {
		name     string
		expr     ast.Expr
		expected string
	}{
		{
			name:     "string literal",
			expr:     &ast.BasicLit{Kind: token.STRING, Value: `"test string"`},
			expected: "test string",
		},
		{
			name:     "non-string literal",
			expr:     &ast.BasicLit{Kind: token.INT, Value: "123"},
			expected: "",
		},
		{
			name:     "non-literal expression",
			expr:     &ast.Ident{Name: "variable"},
			expected: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := analyzer.extractStringFromExpr(tt.expr)
			if result != tt.expected {
				t.Errorf(expectedGotFormat, tt.expected, result)
			}
		})
	}
}

// TestExtractPathFromArg tests path extraction from AST arguments
func TestExtractPathFromArg(t *testing.T) {
	analyzer := New("test")

	// Set up some test constants
	analyzer.constants["testRoute"] = "/api/test"
	analyzer.constants["userRoute"] = "/users/:id"

	tests := []struct {
		name     string
		arg      ast.Expr
		expected string
	}{
		{
			name:     "string literal",
			arg:      &ast.BasicLit{Kind: token.STRING, Value: `"/direct/path"`},
			expected: "/direct/path",
		},
		{
			name:     "constant reference found",
			arg:      &ast.Ident{Name: "testRoute"},
			expected: "/api/test",
		},
		{
			name:     "constant reference not found",
			arg:      &ast.Ident{Name: "unknownRoute"},
			expected: "unknownRoute",
		},
		{
			name:     "non-string literal",
			arg:      &ast.BasicLit{Kind: token.INT, Value: "123"},
			expected: "",
		},
		{
			name:     "unsupported expression",
			arg:      &ast.BinaryExpr{Op: token.ADD},
			expected: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := analyzer.extractPathFromArg(tt.arg)
			if result != tt.expected {
				t.Errorf(expectedGotFormat, tt.expected, result)
			}
		})
	}
}

// TestIsModuleDepsField tests module dependency field detection
func TestIsModuleDepsField(t *testing.T) {
	analyzer := New("test")

	tests := []struct {
		name     string
		field    *ast.Field
		expected bool
	}{
		{
			name: "valid ModuleDeps field",
			field: &ast.Field{
				Type: &ast.StarExpr{
					X: &ast.SelectorExpr{
						X:   &ast.Ident{Name: "app"},
						Sel: &ast.Ident{Name: "ModuleDeps"},
					},
				},
			},
			expected: true,
		},
		{
			name: "wrong package",
			field: &ast.Field{
				Type: &ast.StarExpr{
					X: &ast.SelectorExpr{
						X:   &ast.Ident{Name: "other"},
						Sel: &ast.Ident{Name: "ModuleDeps"},
					},
				},
			},
			expected: false,
		},
		{
			name: "wrong type name",
			field: &ast.Field{
				Type: &ast.StarExpr{
					X: &ast.SelectorExpr{
						X:   &ast.Ident{Name: "app"},
						Sel: &ast.Ident{Name: "Other"},
					},
				},
			},
			expected: false,
		},
		{
			name: "not a pointer",
			field: &ast.Field{
				Type: &ast.SelectorExpr{
					X:   &ast.Ident{Name: "app"},
					Sel: &ast.Ident{Name: "ModuleDeps"},
				},
			},
			expected: false,
		},
		{
			name: "not a selector expression",
			field: &ast.Field{
				Type: &ast.StarExpr{
					X: &ast.Ident{Name: "ModuleDeps"},
				},
			},
			expected: false,
		},
		{
			name: "invalid selector X",
			field: &ast.Field{
				Type: &ast.StarExpr{
					X: &ast.SelectorExpr{
						X:   &ast.BasicLit{Kind: token.STRING, Value: "invalid"},
						Sel: &ast.Ident{Name: "ModuleDeps"},
					},
				},
			},
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := analyzer.isModuleDepsField(tt.field)
			if result != tt.expected {
				t.Errorf("Expected %v, got %v", tt.expected, result)
			}
		})
	}
}

// TestIsMethodOnStruct tests method receiver detection
func TestIsMethodOnStruct(t *testing.T) {
	analyzer := New("test")

	tests := []struct {
		name       string
		recv       *ast.FieldList
		structName string
		expected   bool
	}{
		{
			name: "pointer receiver match",
			recv: &ast.FieldList{
				List: []*ast.Field{
					{
						Type: &ast.StarExpr{
							X: &ast.Ident{Name: "Module"},
						},
					},
				},
			},
			structName: "Module",
			expected:   true,
		},
		{
			name: "value receiver match",
			recv: &ast.FieldList{
				List: []*ast.Field{
					{
						Type: &ast.Ident{Name: "Module"},
					},
				},
			},
			structName: "Module",
			expected:   true,
		},
		{
			name: "no match",
			recv: &ast.FieldList{
				List: []*ast.Field{
					{
						Type: &ast.Ident{Name: "Other"},
					},
				},
			},
			structName: "Module",
			expected:   false,
		},
		{
			name:       "nil receiver",
			recv:       nil,
			structName: "Module",
			expected:   false,
		},
		{
			name: "empty receiver list",
			recv: &ast.FieldList{
				List: []*ast.Field{},
			},
			structName: "Module",
			expected:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := analyzer.isMethodOnStruct(tt.recv, tt.structName)
			if result != tt.expected {
				t.Errorf("Expected %v, got %v", tt.expected, result)
			}
		})
	}
}

// TestExtractConstants tests constant extraction from AST
func TestExtractConstants(t *testing.T) {
	analyzer := New("test")

	// Create test AST file with constants
	constContent := `package test

const (
	apiPath = "/api/v1"
	userPath = "/users"
	testValue = "test"
	intConst = 42
)

const singleConst = "/single"`

	// Parse the content
	astFile, err := parser.ParseFile(token.NewFileSet(), testFileName, constContent, parser.ParseComments)
	if err != nil {
		t.Fatalf(parseFailedFormat, err)
	}

	// Extract constants
	analyzer.extractConstants(astFile)

	// Verify constants were extracted (only string constants)
	expected := map[string]string{
		"apiPath":     "/api/v1",
		"userPath":    "/users",
		"testValue":   "test",
		"singleConst": "/single",
	}

	for name, expectedValue := range expected {
		if value, exists := analyzer.constants[name]; !exists {
			t.Errorf("Expected constant %s to exist", name)
		} else if value != expectedValue {
			t.Errorf("Expected constant %s to have value %q, got %q", name, expectedValue, value)
		}
	}

	// Non-string constants should not be extracted
	if _, exists := analyzer.constants["intConst"]; exists {
		t.Error("Non-string constants should not be extracted")
	}
}

// TestExtractPackageDescription tests package comment extraction
func TestExtractPackageDescription(t *testing.T) {
	analyzer := New("test")

	tests := []struct {
		name     string
		content  string
		expected string
	}{
		{
			name: "package with description",
			content: `// Package test demonstrates testing functionality.
// This package provides comprehensive test utilities.
package test`,
			expected: "demonstrates testing functionality. This package provides comprehensive test utilities.",
		},
		{
			name: "simple package comment",
			content: `// Package test is for testing
package test`,
			expected: "is for testing",
		},
		{
			name:     "no package comments",
			content:  `package test`,
			expected: "",
		},
		{
			name: "mixed comment styles",
			content: `/* Package test provides utilities */
// Additional information
package test`,
			expected: "provides utilities Additional information",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			astFile, err := parser.ParseFile(token.NewFileSet(), testFileName, tt.content, parser.ParseComments)
			if err != nil {
				t.Fatalf(parseFailedFormat, err)
			}

			result := analyzer.extractPackageDescription(astFile)
			if result != tt.expected {
				t.Errorf(expectedGotFormat, tt.expected, result)
			}
		})
	}
}

// TestAnalyzeProjectEdgeCases tests edge cases for AnalyzeProject
func TestAnalyzeProjectEdgeCases(t *testing.T) {
	tempDir := t.TempDir()

	t.Run("invalid project path", func(_ *testing.T) {
		// Create analyzer with invalid path
		invalidAnalyzer := New(filepath.Join("nonexistent", "path"))
		_, err := invalidAnalyzer.AnalyzeProject()
		// Note: AnalyzeProject may not error on invalid paths, it just won't find modules
		_ = err // Ignore error for now as implementation may vary
	})

	t.Run("project with no go files", func(t *testing.T) {
		emptyDir := filepath.Join(tempDir, "empty")
		if err := os.MkdirAll(emptyDir, 0755); err != nil {
			t.Fatalf("failed to create empty directory: %v", err)
		}

		// Create analyzer with empty directory
		emptyAnalyzer := New(emptyDir)
		result, err := emptyAnalyzer.AnalyzeProject()
		if err != nil {
			t.Errorf("Did not expect error for empty project: %v", err)
		}
		if len(result.Modules) != 0 {
			t.Error("Expected empty modules for empty project")
		}
	})
}

// TestExtractModuleFromASTEdgeCases tests edge cases for extractModuleFromAST
func TestExtractModuleFromASTEdgeCases(t *testing.T) {
	analyzer := New("test")

	tests := []struct {
		name     string
		content  string
		expected bool // whether a module should be found
	}{
		{
			name: "struct without init method",
			content: `package test
type Module struct{}
func (m *Module) Name() string { return "test" }`,
			expected: false,
		},
		{
			name: "struct without name method",
			content: `package test
type Module struct{}
func (m *Module) Init(deps *app.ModuleDeps) error { return nil }`,
			expected: false,
		},
		{
			name: "struct with wrong init signature",
			content: `package test
type Module struct{}
func (m *Module) Name() string { return "test" }
func (m *Module) Init() error { return nil }`,
			expected: false,
		},
		{
			name: "interface instead of struct",
			content: `package test
type Module any`,
			expected: false,
		},
		{
			name: "struct with incorrect register routes method",
			content: `package test
type Module struct{}
func (m *Module) Name() string { return "test" }
func (m *Module) Init(deps *app.ModuleDeps) error { return nil }
func (m *Module) RegisterRoutes() {}`,
			expected: false, // This should now be false due to stricter signature validation
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			astFile, err := parser.ParseFile(token.NewFileSet(), testFileName, tt.content, parser.ParseComments)
			if err != nil {
				t.Fatalf(parseFailedFormat, err)
			}

			result, _ := analyzer.extractModuleFromAST(astFile, "test")
			found := (result != nil)
			if found != tt.expected {
				t.Errorf("Expected module found=%v, got %v", tt.expected, found)
			}
		})
	}
}

// TestExtractRouteFromStatementEdgeCases tests edge cases for extractRouteFromStatement
func TestExtractRouteFromStatementEdgeCases(t *testing.T) {
	analyzer := New("test")

	tests := []struct {
		name     string
		content  string
		expected int // number of routes expected
	}{
		{
			name: "invalid call expression",
			content: `package test
func test() { invalidCall() }`,
			expected: 0,
		},
		{
			name: "non-server function call",
			content: `package test
func test() { other.GET("/path", handler) }`,
			expected: 0,
		},
		{
			name: "server call with wrong arguments",
			content: `package test
func test() { server.GET() }`,
			expected: 0,
		},
		{
			name: "server call with too many arguments",
			content: `package test
func test() { server.GET("/path", handler, extra1, extra2, extra3) }`,
			expected: 1, // This actually creates a valid route since it has path and handler
		},
		{
			name: "non-existent HTTP method",
			content: `package test
func test() { server.INVALID("/path", handler) }`,
			expected: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			astFile, err := parser.ParseFile(token.NewFileSet(), testFileName, tt.content, parser.ParseComments)
			if err != nil {
				t.Fatalf(parseFailedFormat, err)
			}

			var routes []models.Route
			for _, decl := range astFile.Decls {
				if funcDecl, ok := decl.(*ast.FuncDecl); ok {
					routes = append(routes, analyzer.extractRoutesFromFuncBody(funcDecl.Body)...)
				}
			}

			if len(routes) != tt.expected {
				t.Errorf("Expected %d routes, got %d", tt.expected, len(routes))
			}
		})
	}
}

// TestDiscoverModulesErrorHandling tests error handling in discoverModules
func TestDiscoverModulesErrorHandling(t *testing.T) {
	tempDir := t.TempDir()

	// Create a file with parse errors
	invalidFile := filepath.Join(tempDir, "invalid.go")
	invalidContent := `package test
func invalid syntax {`
	if err := os.WriteFile(invalidFile, []byte(invalidContent), 0644); err != nil {
		t.Fatalf("failed to write invalid content: %v", err)
	}

	// This should not fail completely but should handle the parse error gracefully
	// Create analyzer with temp directory
	tempAnalyzer := New(tempDir)
	_, err := tempAnalyzer.discoverModules()
	if err != nil {
		t.Errorf("discoverModules should handle parse errors gracefully: %v", err)
	}
}

// TestIsMethodOnStructMissingCase tests the missing case in isMethodOnStruct
func TestIsMethodOnStructMissingCase(t *testing.T) {
	// Test nil receiver list
	analyzer := New("test")
	result := analyzer.isMethodOnStruct(nil, "TestStruct")
	if result {
		t.Error("Expected false for nil receiver list")
	}
}

// TestAnalyzeGoFileErrorHandling tests error handling in analyzeGoFile
func TestAnalyzeGoFileErrorHandling(t *testing.T) {
	tempDir := t.TempDir()
	analyzer := New(tempDir)

	// Create a valid module file
	moduleFile := filepath.Join(tempDir, moduleFileName)
	moduleContent := `package test
type Module struct{}
func (m *Module) Name() string { return "test" }
func (m *Module) Init(deps *app.ModuleDeps) error { return nil }
func (m *Module) RegisterRoutes(hr *server.HandlerRegistry, e *echo.Echo) {}
`
	if err := os.WriteFile(moduleFile, []byte(moduleContent), 0644); err != nil {
		t.Fatalf("failed to write module file: %v", err)
	}

	// This should successfully analyze the file
	_, err := analyzer.analyzeGoFile(moduleFile)
	if err != nil {
		t.Errorf("analyzeGoFile should succeed for valid module: %v", err)
	}
}

// TestSplitFileModuleDetection tests that modules and routes are correctly detected
// when the module struct and RegisterRoutes method are in different files within the same package
func TestSplitFileModuleDetection(t *testing.T) {
	tempDir := t.TempDir()

	// Create module.go with just the Module struct definition and some methods
	moduleFile := filepath.Join(tempDir, moduleFileName)
	moduleContent := `package splitmodule

import (
	"github.com/gaborage/go-bricks/app"
)

// Module represents a split module where struct and routes are in different files
type Module struct {
	deps *app.ModuleDeps
}

// Name returns the module name
func (m *Module) Name() string {
	return "splitmodule"
}

// Init initializes the module
func (m *Module) Init(deps *app.ModuleDeps) error {
	m.deps = deps
	return nil
}

// Shutdown cleans up the module
func (m *Module) Shutdown() error {
	return nil
}
`

	// Create routes.go with the RegisterRoutes method and route definitions
	routesFile := filepath.Join(tempDir, "routes.go")
	routesContent := `package splitmodule

import (
	srv "github.com/gaborage/go-bricks/server"
)

const (
	splitListRoute = "/split/users"
	splitCreateRoute = "/split/users"
)

// RegisterRoutes registers HTTP routes for the split module
func (m *Module) RegisterRoutes(hr *srv.HandlerRegistry, r srv.RouteRegistrar) {
	srv.GET(hr, r, splitListRoute, m.listUsers,
		srv.WithTags("` + splitModuleTag + `"),
		srv.WithSummary("` + splitListSummary + `"))
	srv.POST(hr, r, splitCreateRoute, m.createUser,
		srv.WithTags("` + splitModuleTag + `"),
		srv.WithDescription("` + splitCreateDescription + `"))
}

func (m *Module) listUsers() {}
func (m *Module) createUser() {}
`

	// Write both files
	if err := os.WriteFile(moduleFile, []byte(moduleContent), 0644); err != nil {
		t.Fatalf("failed to write module file: %v", err)
	}
	if err := os.WriteFile(routesFile, []byte(routesContent), 0644); err != nil {
		t.Fatalf("failed to write routes file: %v", err)
	}

	// Analyze the module file
	analyzer := New(tempDir)
	module, err := analyzer.analyzeGoFile(moduleFile)

	if err != nil {
		t.Fatalf("Failed to analyze split module: %v", err)
	}

	if module == nil {
		t.Fatal("Expected to find a module, but got nil")
	}

	// Verify module metadata
	if module.Name != "splitmodule" {
		t.Errorf("Expected module name 'splitmodule', got '%s'", module.Name)
	}

	if module.Package != "splitmodule" {
		t.Errorf("Expected package name 'splitmodule', got '%s'", module.Package)
	}

	// Verify routes are discovered from the separate routes.go file
	if len(module.Routes) != 2 {
		t.Fatalf(expectedTwoRoutesFormat, len(module.Routes))
	}

	// Check first route (GET)
	getRoute := findRouteByMethod(module.Routes, "GET")
	if getRoute == nil {
		t.Fatal("Expected to find GET route")
	}

	if getRoute.Path != splitListRoute {
		t.Errorf("Expected GET route path '%s', got '%s'", splitListRoute, getRoute.Path)
	}

	if getRoute.Summary != splitListSummary {
		t.Errorf("Expected GET route summary '%s', got '%s'", splitListSummary, getRoute.Summary)
	}

	if !slices.Contains(getRoute.Tags, splitModuleTag) {
		t.Errorf("Expected GET route to have tag '%s', got %v", splitModuleTag, getRoute.Tags)
	}

	// Check second route (POST)
	postRoute := findRouteByMethod(module.Routes, "POST")
	if postRoute == nil {
		t.Fatal("Expected to find POST route")
	}

	if postRoute.Path != splitCreateRoute {
		t.Errorf("Expected POST route path '%s', got '%s'", splitCreateRoute, postRoute.Path)
	}

	if postRoute.Description != splitCreateDescription {
		t.Errorf("Expected POST route description '%s', got '%s'", splitCreateDescription, postRoute.Description)
	}

	if !slices.Contains(postRoute.Tags, splitModuleTag) {
		t.Errorf("Expected POST route to have tag '%s', got %v", splitModuleTag, postRoute.Tags)
	}
}

// Helper function to find a route by HTTP method
func findRouteByMethod(routes []models.Route, method string) *models.Route {
	for i := range routes {
		if routes[i].Method == method {
			return &routes[i]
		}
	}
	return nil
}

// TestValidateProjectPath tests the security validation function for project paths
func TestValidateProjectPath(t *testing.T) {
	tempDir := t.TempDir()
	analyzer := New(tempDir)

	tests := []struct {
		name    string
		path    string
		wantErr bool
	}{
		{
			name:    "valid path within project",
			path:    filepath.Join(tempDir, "subdir", "file.go"),
			wantErr: false,
		},
		{
			name:    "path outside project root",
			path:    "/etc/passwd",
			wantErr: true,
		},
		{
			name:    "path traversal attempt",
			path:    filepath.Join(tempDir, "..", "..", "etc", "passwd"),
			wantErr: true,
		},
		{
			name:    "relative path within project",
			path:    filepath.Join(tempDir, "subdir"),
			wantErr: false,
		},
		{
			name:    "current directory",
			path:    tempDir,
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := analyzer.validateProjectPath(tt.path)
			if (err != nil) != tt.wantErr {
				t.Errorf("validateProjectPath() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

// TestValidateGoFilePath tests the security validation function for Go file paths
func TestValidateGoFilePath(t *testing.T) {
	tempDir := t.TempDir()
	analyzer := New(tempDir)

	// Create a test Go file
	testFile := filepath.Join(tempDir, testFileName)
	if err := os.WriteFile(testFile, []byte("package test"), 0644); err != nil {
		t.Fatalf("failed to write test file: %v", err)
	}

	tests := []struct {
		name    string
		path    string
		wantErr bool
	}{
		{
			name:    "valid Go file",
			path:    testFile,
			wantErr: false,
		},
		{
			name:    "non-Go file",
			path:    filepath.Join(tempDir, "test.txt"),
			wantErr: true,
		},
		{
			name:    "nonexistent file",
			path:    filepath.Join(tempDir, "nonexistent.go"),
			wantErr: false, // validateGoFilePath doesn't check existence
		},
		{
			name:    "path outside project",
			path:    filepath.Join("tmp", "external.go"),
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := analyzer.validateGoFilePath(tt.path)
			if (err != nil) != tt.wantErr {
				t.Errorf("validateGoFilePath() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

// TestParsePackageErrorHandling tests parsePackage function with various error scenarios
func TestParsePackageErrorHandling(t *testing.T) {
	tempDir := t.TempDir()
	analyzer := New(tempDir)

	// Test with a non-existent path
	_, err := analyzer.parsePackage("/non/existent/path", "test")
	if err == nil {
		t.Error("Expected an error for non-existent path, got nil")
	}

	// Test with a directory that doesn't contain the package
	otherPkgDir := filepath.Join(tempDir, "other")
	if err := os.Mkdir(otherPkgDir, 0755); err != nil {
		t.Fatalf("failed to mkdir: %v", err)
	}
	otherFile := filepath.Join(otherPkgDir, "other.go")
	if err := os.WriteFile(otherFile, []byte("package other"), 0644); err != nil {
		t.Fatalf("failed to write other.go: %v", err)
	}

	_, err = analyzer.parsePackage(otherFile, "test")
	if err == nil {
		t.Error("Expected an error for package not found, got nil")
	}
}

// TestExtractModuleFromASTComplexCases tests complex module extraction scenarios
func TestExtractModuleFromASTComplexCases(t *testing.T) {
	tempDir := t.TempDir()
	analyzer := New(tempDir)

	t.Run("simple valid module detection", func(t *testing.T) {
		// First test with a pattern we know works from createTestModuleFile
		content := `package testmodule

import (
	"github.com/gaborage/go-bricks/app"
	"github.com/gaborage/go-bricks/messaging"
	"github.com/gaborage/go-bricks/server"
)

type Module struct {
	deps *app.ModuleDeps
}

func (m *Module) Name() string {
	return "testmodule"
}

func (m *Module) Init(deps *app.ModuleDeps) error {
	m.deps = deps
	return nil
}

func (m *Module) RegisterRoutes(hr *server.HandlerRegistry, r server.RouteRegistrar) {
	server.GET(hr, r, "/test", m.testHandler)
}

func (m *Module) DeclareMessaging(decls *messaging.Declarations) {
}

func (m *Module) Shutdown() error {
	return nil
}

func (m *Module) testHandler() {}`

		astFile, err := parser.ParseFile(token.NewFileSet(), testFileName, content, parser.ParseComments)
		if err != nil {
			t.Fatalf(parseFailedFormat, err)
		}

		result, structName := analyzer.extractModuleFromAST(astFile, filepath.Join(tempDir, "testmodule.go"))
		if result == nil {
			t.Error("Expected to find a valid module")
		}
		if structName != "Module" {
			t.Errorf("Expected struct name 'Module', got '%s'", structName)
		}
	})

	t.Run("struct with wrong init parameter type", func(t *testing.T) {
		content := `package test

import "github.com/gaborage/go-bricks/config"

type Module struct{}

func (m *Module) Name() string { return "test" }
func (m *Module) Init(deps *config.Config) error { return nil }`

		astFile, err := parser.ParseFile(token.NewFileSet(), testFileName, content, parser.ParseComments)
		if err != nil {
			t.Fatalf(parseFailedFormat, err)
		}

		//nolint:S8148 // NOSONAR: Error intentionally ignored - test verifies module detection, not error conditions
		result, _ := analyzer.extractModuleFromAST(astFile, filepath.Join(tempDir, testFileName))
		if result != nil {
			t.Error("Expected no module found for wrong init parameter type")
		}
	})
}

// TestExtractRoutesFromFuncBodyComplex tests complex route extraction scenarios
func TestExtractRoutesFromFuncBodyComplex(t *testing.T) {
	analyzer := New("test")

	tests := []struct {
		name     string
		content  string
		expected int
	}{
		{
			name: "routes with complex metadata",
			content: `package test
func (m *Module) RegisterRoutes(hr *server.HandlerRegistry, r server.RouteRegistrar) {
	server.GET(hr, r, "/users", m.getUsers,
		server.WithTags("users", "api"),
		server.WithSummary("Get all users"),
		server.WithDescription("Retrieves all users from the system"))
	server.POST(hr, r, "/users", m.createUser,
		server.WithTags("users"),
		server.WithSummary("Create user"))
}`,
			expected: 2,
		},
		{
			name: "routes with constants",
			content: `package test
const userPath = "/users"
func (m *Module) RegisterRoutes(hr *server.HandlerRegistry, r server.RouteRegistrar) {
	server.GET(hr, r, userPath, m.getUsers)
}`,
			expected: 1,
		},
		{
			name: "mixed valid and invalid routes",
			content: `package test
func (m *Module) RegisterRoutes(hr *server.HandlerRegistry, r server.RouteRegistrar) {
	server.GET(hr, r, "/valid", m.handler)
	server.INVALID(hr, r, "/invalid", m.handler)
	other.GET(hr, r, "/other", m.handler)
}`,
			expected: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			astFile, err := parser.ParseFile(token.NewFileSet(), testFileName, tt.content, parser.ParseComments)
			if err != nil {
				t.Fatalf(parseFailedFormat, err)
			}

			// Extract constants first
			analyzer.extractConstants(astFile)

			var routes []models.Route
			for _, decl := range astFile.Decls {
				if funcDecl, ok := decl.(*ast.FuncDecl); ok {
					routes = append(routes, analyzer.extractRoutesFromFuncBody(funcDecl.Body)...)
				}
			}

			if len(routes) != tt.expected {
				t.Errorf("Expected %d routes, got %d", tt.expected, len(routes))
			}
		})
	}
}

// TestAnalyzeProjectWithModule tests full project analysis with a real module
func TestAnalyzeProjectWithModule(t *testing.T) {
	tempDir := t.TempDir()

	// Create go.mod
	createTestGoMod(t, tempDir)

	// Create a more complex module
	moduleContent := `package usermodule

import (
	"github.com/gaborage/go-bricks/app"
	"github.com/gaborage/go-bricks/messaging"
	"github.com/gaborage/go-bricks/server"
)

// UserModule handles user-related operations
type UserModule struct {
	deps *app.ModuleDeps
}

func (m *UserModule) Name() string { return "usermodule" }

func (m *UserModule) Init(deps *app.ModuleDeps) error {
	m.deps = deps
	return nil
}

func (m *UserModule) RegisterRoutes(hr *server.HandlerRegistry, r server.RouteRegistrar) {
	server.GET(hr, r, "/api/users", m.listUsers,
		server.WithTags("users"),
		server.WithSummary("List users"))
	server.POST(hr, r, "/api/users", m.createUser,
		server.WithTags("users"),
		server.WithSummary("Create user"))
}

func (m *UserModule) DeclareMessaging(decls *messaging.Declarations) {}
func (m *UserModule) Shutdown() error { return nil }
func (m *UserModule) listUsers() {}
func (m *UserModule) createUser() {}`

	moduleFile := filepath.Join(tempDir, "usermodule.go")
	if err := os.WriteFile(moduleFile, []byte(moduleContent), 0644); err != nil {
		t.Fatalf("failed to write usermodule.go: %v", err)
	}

	analyzer := New(tempDir)
	project, err := analyzer.AnalyzeProject()

	if err != nil {
		t.Fatalf("AnalyzeProject() failed: %v", err)
	}

	if len(project.Modules) != 1 {
		t.Errorf(expectedOneModuleFormat, len(project.Modules))
	}

	if len(project.Modules) > 0 {
		module := project.Modules[0]
		if module.Name != "usermodule" {
			t.Errorf("Expected module name 'usermodule', got '%s'", module.Name)
		}
		if len(module.Routes) != 2 {
			t.Errorf("Expected 2 routes, got %d", len(module.Routes))
		}
	}
}

// TestCollectMethodFlagsFromFile tests method flag collection functionality
func TestCollectMethodFlagsFromFile(t *testing.T) {
	analyzer := New("test")

	// Create test content with various method signatures
	content := `package test

import (
	"github.com/gaborage/go-bricks/app"
	"github.com/gaborage/go-bricks/server"
)

type Module struct{}

// Valid methods
func (m *Module) Name() string { return "test" }
func (m *Module) Init(deps *app.ModuleDeps) error { return nil }
func (m *Module) RegisterRoutes(hr *server.HandlerRegistry, r server.RouteRegistrar) {}
func (m *Module) Shutdown() error { return nil }

// Invalid method signatures
func (m *Module) InvalidInit() error { return nil }
func (m *Module) InvalidRegisterRoutes() {}`

	astFile, err := parser.ParseFile(token.NewFileSet(), testFileName, content, parser.ParseComments)
	if err != nil {
		t.Fatalf(parseFailedFormat, err)
	}

	requiredMethods := map[string]bool{
		"Name":           false,
		"Init":           false,
		"RegisterRoutes": false,
		"Shutdown":       false,
	}

	// Mock server and app aliases
	serverAliases := map[string]struct{}{"server": {}}
	appAliases := map[string]struct{}{"app": {}}

	analyzer.collectMethodFlagsFromFile(astFile, "Module", requiredMethods, serverAliases, appAliases)

	// Verify that valid methods were detected
	if !requiredMethods["Name"] {
		t.Error("Expected Name method to be detected")
	}
	if !requiredMethods["Init"] {
		t.Error("Expected Init method to be detected")
	}
	if !requiredMethods["RegisterRoutes"] {
		t.Error("Expected RegisterRoutes method to be detected")
	}
	if !requiredMethods["Shutdown"] {
		t.Error("Expected Shutdown method to be detected")
	}
}

// TestExtractImportAliases tests import alias extraction
func TestExtractImportAliases(t *testing.T) {
	analyzer := New("test")

	tests := []struct {
		name       string
		content    string
		importPath string
		expected   map[string]struct{}
	}{
		{
			name: "standard import",
			content: `package test
import "github.com/gaborage/go-bricks/server"`,
			importPath: testServerImportPath,
			expected:   map[string]struct{}{"server": {}},
		},
		{
			name: "aliased import",
			content: `package test
import srv "github.com/gaborage/go-bricks/server"`,
			importPath: testServerImportPath,
			expected:   map[string]struct{}{"srv": {}},
		},
		{
			name: "dot import",
			content: `package test
import . "github.com/gaborage/go-bricks/server"`,
			importPath: testServerImportPath,
			expected:   map[string]struct{}{"server": {}}, // dot imports fall back to base name
		},
		{
			name: "no matching import",
			content: `package test
import "fmt"`,
			importPath: testServerImportPath,
			expected:   map[string]struct{}{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			astFile, err := parser.ParseFile(token.NewFileSet(), testFileName, tt.content, parser.ParseComments)
			if err != nil {
				t.Fatalf(parseFailedFormat, err)
			}

			aliases := analyzer.extractImportAliases(astFile, tt.importPath)
			if len(aliases) != len(tt.expected) {
				t.Errorf("Expected %d aliases, got %d", len(tt.expected), len(aliases))
			}

			for expectedAlias := range tt.expected {
				if _, exists := aliases[expectedAlias]; !exists {
					t.Errorf("Expected alias '%s' not found", expectedAlias)
				}
			}
		})
	}
}

// TestProjectMetadataExtraction tests project name extraction from go.mod
func TestProjectMetadataExtraction(t *testing.T) {
	tempDir := t.TempDir()

	tests := []struct {
		name         string
		goModContent string
		expectedName string
	}{
		{
			name:         "github module",
			goModContent: "module github.com/user/my-awesome-service\n\ngo 1.21",
			expectedName: "My-awesome-service API",
		},
		{
			name:         "simple module name",
			goModContent: "module myservice\n\ngo 1.21",
			expectedName: "Myservice API",
		},
		{
			name:         "complex path",
			goModContent: "module internal/tools/api-generator\n\ngo 1.21",
			expectedName: "Api-generator API",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			goModFile := filepath.Join(tempDir, "go.mod")
			os.WriteFile(goModFile, []byte(tt.goModContent), 0644)

			analyzer := New(tempDir)
			project := &models.Project{}
			analyzer.discoverProjectMetadata(project)

			if project.Name != tt.expectedName {
				t.Errorf("Expected project name '%s', got '%s'", tt.expectedName, project.Name)
			}
		})
	}

	// Test missing go.mod file
	t.Run("missing go.mod", func(t *testing.T) {
		emptyDir := t.TempDir()
		analyzer := New(emptyDir)
		project := &models.Project{}
		analyzer.discoverProjectMetadata(project)

		// Should leave fields empty when go.mod is missing
		if project.Name != "" {
			t.Errorf("Expected empty project name for missing go.mod, got '%s'", project.Name)
		}
		if project.Version != "" {
			t.Errorf("Expected empty version for missing go.mod, got '%s'", project.Version)
		}
	})
}

// TestMethodSignatureValidation tests validation of method signatures
func TestMethodSignatureValidation(t *testing.T) {
	analyzer := New("test")

	tests := []struct {
		name     string
		content  string
		expected bool // whether valid module methods are found
	}{
		{
			name: "valid method signatures",
			content: `package test

import (
	"github.com/gaborage/go-bricks/app"
	"github.com/gaborage/go-bricks/server"
)

type Module struct{}

func (m *Module) Name() string { return "test" }
func (m *Module) Init(deps *app.ModuleDeps) error { return nil }
func (m *Module) RegisterRoutes(hr *server.HandlerRegistry, r server.RouteRegistrar) {}`,
			expected: true,
		},
		{
			name: "wrong number of parameters",
			content: `package test

import "github.com/gaborage/go-bricks/app"

type Module struct{}

func (m *Module) Name() string { return "test" }
func (m *Module) Init() error { return nil }
func (m *Module) RegisterRoutes() {}`,
			expected: false,
		},
		{
			name: "wrong return types",
			content: `package test

import "github.com/gaborage/go-bricks/app"

type Module struct{}

func (m *Module) Name() int { return 0 }
func (m *Module) Init(deps *app.ModuleDeps) string { return "" }`,
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			astFile, err := parser.ParseFile(token.NewFileSet(), testFileName, tt.content, parser.ParseComments)
			if err != nil {
				t.Fatalf(parseFailedFormat, err)
			}

			//nolint:S8148 // NOSONAR: Error intentionally ignored - test verifies module detection, not error conditions
			result, _ := analyzer.extractModuleFromAST(astFile, filepath.Join("test", "file.go"))
			found := (result != nil)
			if found != tt.expected {
				t.Errorf("Expected module found=%v, got %v", tt.expected, found)
			}
		})
	}
}

// TestExtractStringFromExprEdgeCases tests string extraction from various expression types
func TestExtractStringFromExprEdgeCases(t *testing.T) {
	analyzer := New("test")

	tests := []struct {
		name     string
		expr     ast.Expr
		expected string
	}{
		{
			name:     "nil expression",
			expr:     nil,
			expected: "",
		},
		{
			name:     "non-basic literal",
			expr:     &ast.BinaryExpr{Op: token.ADD},
			expected: "",
		},
		{
			name:     "integer literal",
			expr:     &ast.BasicLit{Kind: token.INT, Value: "42"},
			expected: "",
		},
		{
			name:     "string with quotes",
			expr:     &ast.BasicLit{Kind: token.STRING, Value: `"hello world"`},
			expected: "hello world",
		},
		{
			name:     "empty string literal",
			expr:     &ast.BasicLit{Kind: token.STRING, Value: `""`},
			expected: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := analyzer.extractStringFromExpr(tt.expr)
			if result != tt.expected {
				t.Errorf("Expected '%s', got '%s'", tt.expected, result)
			}
		})
	}
}

// TestAnalyzeGoFileErrorCases tests error handling in analyzeGoFile
func TestAnalyzeGoFileErrorCases(t *testing.T) {
	tempDir := t.TempDir()
	analyzer := New(tempDir)

	t.Run("valid file with no module", func(t *testing.T) {
		// Create a valid Go file that's not a module
		nonModuleFile := filepath.Join(tempDir, "helper.go")
		nonModuleContent := `package helper

import "fmt"

func Helper() {
	fmt.Println("This is not a module")
}`
		if err := os.WriteFile(nonModuleFile, []byte(nonModuleContent), 0644); err != nil {
			t.Fatalf("failed to write helper.go: %v", err)
		}

		module, err := analyzer.analyzeGoFile(nonModuleFile)
		if err != nil {
			t.Errorf("analyzeGoFile should not error for non-module files: %v", err)
		}
		if module != nil {
			t.Error("analyzeGoFile should return nil for non-module files")
		}
	})

	t.Run("file with complex struct", func(t *testing.T) {
		// Create a file with a struct that has ModuleDeps field but no proper methods
		complexFile := filepath.Join(tempDir, "complex.go")
		complexContent := `package complex

import (
	"github.com/gaborage/go-bricks/app"
)

type ComplexStruct struct {
	deps *app.ModuleDeps
	name string
	id   int
}

// This has ModuleDeps but doesn't implement the module interface properly
func (c *ComplexStruct) SomeMethod() string {
	return "not a module"
}`
		if err := os.WriteFile(complexFile, []byte(complexContent), 0644); err != nil {
			t.Fatalf("failed to write complex.go: %v", err)
		}

		module, err := analyzer.analyzeGoFile(complexFile)
		if err != nil {
			t.Errorf("analyzeGoFile should not error for complex structs: %v", err)
		}
		// Might detect as a module due to ModuleDeps field, which is valid behavior
		t.Logf("Complex struct detection result: %v", module != nil)
	})
}

// TestDiscoverModulesDeduplication verifies that modules are deduplicated by package name
func TestDiscoverModulesDeduplication(t *testing.T) {
	tempDir := t.TempDir()

	// Create a package with multiple Go files that both contain a module
	testDir := filepath.Join(tempDir, "testmodule")
	err := os.MkdirAll(testDir, 0755)
	if err != nil {
		t.Fatalf("Failed to create test directory: %v", err)
	}

	// First file with a module
	file1Content := `package testmodule

import (
	"github.com/gaborage/go-bricks/app"
	"github.com/gaborage/go-bricks/server"
	"github.com/labstack/echo/v4"
)

type Module struct {
	deps *app.ModuleDeps
}

func (m *Module) Name() string {
	return "testmodule"
}

func (m *Module) Init(deps *app.ModuleDeps) error {
	m.deps = deps
	return nil
}

func (m *Module) RegisterRoutes(hr *server.HandlerRegistry, r server.RouteRegistrar) {
	// Routes from file 1
}

func (m *Module) Shutdown() error {
	return nil
}`

	// Second file with same module (different content but same package)
	file2Content := `package testmodule

import (
	"github.com/gaborage/go-bricks/app"
	"github.com/gaborage/go-bricks/server"
	"github.com/labstack/echo/v4"
)

type Module struct {
	deps *app.ModuleDeps
}

func (m *Module) Name() string {
	return "testmodule"
}

func (m *Module) Init(deps *app.ModuleDeps) error {
	m.deps = deps
	return nil
}

func (m *Module) RegisterRoutes(hr *server.HandlerRegistry, r server.RouteRegistrar) {
	// Routes from file 2
}

func (m *Module) Shutdown() error {
	return nil
}`

	// Write both files
	file1Path := filepath.Join(testDir, "module1.go")
	file2Path := filepath.Join(testDir, "module2.go")

	err = os.WriteFile(file1Path, []byte(file1Content), 0644)
	if err != nil {
		t.Fatalf("Failed to write file1: %v", err)
	}

	err = os.WriteFile(file2Path, []byte(file2Content), 0644)
	if err != nil {
		t.Fatalf("Failed to write file2: %v", err)
	}

	// Create analyzer and discover modules
	analyzer := New(tempDir)
	modules, err := analyzer.discoverModules()
	if err != nil {
		t.Fatalf("discoverModules failed: %v", err)
	}

	// Should only find ONE module, not two, due to deduplication by package name
	if len(modules) != 1 {
		t.Errorf("Expected 1 module, got %d", len(modules))
		for i, mod := range modules {
			t.Logf("Module %d: Name=%s, Package=%s", i, mod.Name, mod.Package)
		}
	}

	// Verify the module has the correct package name
	if len(modules) > 0 {
		module := modules[0]
		if module.Package != "testmodule" {
			t.Errorf("Expected module package 'testmodule', got '%s'", module.Package)
		}
		if module.Name != "testmodule" {
			t.Errorf("Expected module name 'testmodule', got '%s'", module.Name)
		}
	}
}

// TestConstantsNoLeakageBetweenPackages verifies that constants from one package don't leak into another
func TestConstantsNoLeakageBetweenPackages(t *testing.T) {
	tempDir := t.TempDir()

	// Create first package with a constant
	pkg1Dir := filepath.Join(tempDir, "package1")
	err := os.MkdirAll(pkg1Dir, 0755)
	if err != nil {
		t.Fatalf("Failed to create package1 directory: %v", err)
	}

	pkg1Content := `package package1

import (
	"github.com/gaborage/go-bricks/app"
	"github.com/gaborage/go-bricks/server"
	"github.com/labstack/echo/v4"
)

const PackageConstant = "/package1/path"

type Module struct {
	deps *app.ModuleDeps
}

func (m *Module) Name() string {
	return "package1"
}

func (m *Module) Init(deps *app.ModuleDeps) error {
	m.deps = deps
	return nil
}

func (m *Module) RegisterRoutes(hr *server.HandlerRegistry, r server.RouteRegistrar) {
	server.GET(hr, r, PackageConstant, m.getHandler)
}

func (m *Module) getHandler() {}

func (m *Module) Shutdown() error {
	return nil
}`

	// Create second package with same constant name but different value
	pkg2Dir := filepath.Join(tempDir, "package2")
	err = os.MkdirAll(pkg2Dir, 0755)
	if err != nil {
		t.Fatalf("Failed to create package2 directory: %v", err)
	}

	pkg2Content := `package package2

import (
	"github.com/gaborage/go-bricks/app"
	"github.com/gaborage/go-bricks/server"
	"github.com/labstack/echo/v4"
)

const PackageConstant = "/package2/path"

type Module struct {
	deps *app.ModuleDeps
}

func (m *Module) Name() string {
	return "package2"
}

func (m *Module) Init(deps *app.ModuleDeps) error {
	m.deps = deps
	return nil
}

func (m *Module) RegisterRoutes(hr *server.HandlerRegistry, r server.RouteRegistrar) {
	server.GET(hr, r, PackageConstant, m.getHandler)
}

func (m *Module) getHandler() {}

func (m *Module) Shutdown() error {
	return nil
}`

	// Write package files
	err = os.WriteFile(filepath.Join(pkg1Dir, moduleFileName), []byte(pkg1Content), 0644)
	if err != nil {
		t.Fatalf("Failed to write package1 file: %v", err)
	}

	err = os.WriteFile(filepath.Join(pkg2Dir, moduleFileName), []byte(pkg2Content), 0644)
	if err != nil {
		t.Fatalf("Failed to write package2 file: %v", err)
	}

	// Analyze the project
	analyzer := New(tempDir)
	modules, err := analyzer.discoverModules()
	if err != nil {
		t.Fatalf("discoverModules failed: %v", err)
	}

	// Should find both modules
	if len(modules) != 2 {
		t.Errorf("Expected 2 modules, got %d", len(modules))
		for i, mod := range modules {
			t.Logf("Module %d: Name=%s, Package=%s, Routes=%d", i, mod.Name, mod.Package, len(mod.Routes))
		}
		return
	}

	// Verify each module has the correct routes with proper path resolution
	for _, module := range modules {
		if len(module.Routes) != 1 {
			t.Errorf("Module %s should have 1 route, got %d", module.Name, len(module.Routes))
			continue
		}

		route := module.Routes[0]
		expectedPath := "/" + module.Package + "/path"

		if route.Path != expectedPath {
			t.Errorf("Module %s route path should be %s, got %s", module.Name, expectedPath, route.Path)
			t.Logf("This would indicate constants leakage between packages")
		}
	}
}

// TestTypeInfoFromExpr tests AST type expression parsing
func TestTypeInfoFromExpr(t *testing.T) {
	analyzer := New("")

	tests := []struct {
		name        string
		typeExpr    string
		expected    *models.TypeInfo
		description string
	}{
		{
			name:        "simple identifier",
			typeExpr:    "CreateUserReq",
			expected:    &models.TypeInfo{Name: "CreateUserReq", Package: "test", IsPointer: false},
			description: "should extract simple type name",
		},
		{
			name:        "pointer type",
			typeExpr:    "*CreateUserReq",
			expected:    &models.TypeInfo{Name: "CreateUserReq", Package: "test", IsPointer: true},
			description: "should extract pointer type",
		},
		{
			name:        "qualified type",
			typeExpr:    "models.CreateUserReq",
			expected:    &models.TypeInfo{Name: "CreateUserReq", Package: "models", IsPointer: false},
			description: "should extract qualified type with package",
		},
		{
			name:        "qualified pointer type",
			typeExpr:    "*models.CreateUserReq",
			expected:    &models.TypeInfo{Name: "CreateUserReq", Package: "models", IsPointer: true},
			description: "should extract qualified pointer type",
		},
		{
			name:        "HandlerContext (skip)",
			typeExpr:    "HandlerContext",
			expected:    nil,
			description: "should skip framework HandlerContext type",
		},
		{
			name:        "qualified HandlerContext (skip)",
			typeExpr:    "server.HandlerContext",
			expected:    nil,
			description: "should skip qualified server.HandlerContext type",
		},
		{
			name:        "IAPIError (skip)",
			typeExpr:    "IAPIError",
			expected:    nil,
			description: "should skip framework IAPIError type",
		},
		{
			name:        "qualified IAPIError (skip)",
			typeExpr:    "server.IAPIError",
			expected:    nil,
			description: "should skip qualified server.IAPIError type",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create a minimal function with the type expression
			code := `package test
import "github.com/gaborage/go-bricks/server"
func test(param ` + tt.typeExpr + `) {}`

			// Parse the code
			fset := token.NewFileSet()
			astFile, err := parser.ParseFile(fset, testFileName, code, 0)
			if err != nil {
				t.Fatalf("Failed to parse code: %v", err)
			}

			// Find the function and extract the parameter type
			var expr ast.Expr
			for _, decl := range astFile.Decls {
				if funcDecl, ok := decl.(*ast.FuncDecl); ok {
					if funcDecl.Type.Params != nil && len(funcDecl.Type.Params.List) > 0 {
						expr = funcDecl.Type.Params.List[0].Type
						break
					}
				}
			}

			if expr == nil {
				t.Fatal("Failed to find parameter type expression")
			}

			// Test the typeInfoFromExpr function
			result := analyzer.typeInfoFromExpr(expr, "test")

			// Verify the result
			if tt.expected == nil {
				if result != nil {
					t.Errorf("%s: expected nil, got %+v", tt.description, result)
				}
			} else {
				if result == nil {
					t.Errorf("%s: expected %+v, got nil", tt.description, tt.expected)
				} else {
					if result.Name != tt.expected.Name {
						t.Errorf("%s: expected name %q, got %q", tt.description, tt.expected.Name, result.Name)
					}
					if result.Package != tt.expected.Package {
						t.Errorf("%s: expected package %q, got %q", tt.description, tt.expected.Package, result.Package)
					}
					if result.IsPointer != tt.expected.IsPointer {
						t.Errorf("%s: expected IsPointer %v, got %v", tt.description, tt.expected.IsPointer, result.IsPointer)
					}
				}
			}
		})
	}
}

// TestExtractHandlerSignature tests handler signature extraction
func TestExtractHandlerSignature(t *testing.T) {
	tests := []struct {
		name              string
		handlerCode       string
		handlerName       string
		structName        string
		expectedRequest   *models.TypeInfo
		expectedResponse  *models.TypeInfo
		shouldFindHandler bool
		description       string
	}{
		{
			name:        "standard handler with request and response",
			structName:  "Handler",
			handlerName: "createUser",
			handlerCode: `package test

import "github.com/gaborage/go-bricks/server"

type Handler struct{}

type CreateUserReq struct {
	Name string
}

type UserResp struct {
	ID int
}

func (h *Handler) createUser(req CreateUserReq, ctx server.HandlerContext) (UserResp, server.IAPIError) {
	return UserResp{}, nil
}`,
			expectedRequest:   &models.TypeInfo{Name: "CreateUserReq", Package: "test", IsPointer: false},
			expectedResponse:  &models.TypeInfo{Name: "UserResp", Package: "test", IsPointer: false},
			shouldFindHandler: true,
			description:       "should extract request and response types from standard handler",
		},
		{
			name:        "handler with pointer types",
			structName:  "Handler",
			handlerName: "updateUser",
			handlerCode: `package test

import "github.com/gaborage/go-bricks/server"

type Handler struct{}

type UpdateUserReq struct {
	Name string
}

type UserResp struct {
	ID int
}

func (h *Handler) updateUser(req *UpdateUserReq, ctx server.HandlerContext) (*UserResp, server.IAPIError) {
	return nil, nil
}`,
			expectedRequest:   &models.TypeInfo{Name: "UpdateUserReq", Package: "test", IsPointer: true},
			expectedResponse:  &models.TypeInfo{Name: "UserResp", Package: "test", IsPointer: true},
			shouldFindHandler: true,
			description:       "should extract pointer types correctly",
		},
		{
			name:        "handler with no request type",
			structName:  "Handler",
			handlerName: "listUsers",
			handlerCode: `package test

import "github.com/gaborage/go-bricks/server"

type Handler struct{}

type UserListResp struct {
	Users []string
}

func (h *Handler) listUsers(ctx server.HandlerContext) (UserListResp, server.IAPIError) {
	return UserListResp{}, nil
}`,
			expectedRequest:   nil,
			expectedResponse:  &models.TypeInfo{Name: "UserListResp", Package: "test", IsPointer: false},
			shouldFindHandler: true,
			description:       "should handle handler with only HandlerContext parameter",
		},
		{
			name:        "handler with error return only",
			structName:  "Handler",
			handlerName: "deleteUser",
			handlerCode: `package test

import "github.com/gaborage/go-bricks/server"

type Handler struct{}

type DeleteUserReq struct {
	ID int
}

func (h *Handler) deleteUser(req DeleteUserReq, ctx server.HandlerContext) error {
	return nil
}`,
			expectedRequest:   &models.TypeInfo{Name: "DeleteUserReq", Package: "test", IsPointer: false},
			expectedResponse:  nil,
			shouldFindHandler: true,
			description:       "should handle handler with error-only return",
		},
		{
			name:        "handler not found",
			structName:  "Handler",
			handlerName: "nonExistentHandler",
			handlerCode: `package test

type Handler struct{}

func (h *Handler) actualHandler() {}`,
			expectedRequest:   nil,
			expectedResponse:  nil,
			shouldFindHandler: false,
			description:       "should return error when handler not found",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create a temporary directory
			tempDir, err := os.MkdirTemp("", "openapi-test-*")
			if err != nil {
				t.Fatalf("Failed to create temp dir: %v", err)
			}
			defer os.RemoveAll(tempDir)

			// Write the test file
			testFilePath := filepath.Join(tempDir, "handler.go")
			if err := os.WriteFile(testFilePath, []byte(tt.handlerCode), 0600); err != nil {
				t.Fatalf("Failed to write test file: %v", err)
			}

			// Parse the file
			fset := token.NewFileSet()
			astFile, err := parser.ParseFile(fset, testFilePath, nil, parser.ParseComments)
			if err != nil {
				t.Fatalf("Failed to parse file: %v", err)
			}

			// Create analyzer and extract handler signature
			analyzer := New(tempDir)
			reqType, respType, err := analyzer.extractHandlerSignature(astFile, testFilePath, tt.structName, tt.handlerName)

			// Verify results
			if tt.shouldFindHandler {
				if err != nil {
					t.Errorf("%s: unexpected error: %v", tt.description, err)
				}

				// Check request type
				if tt.expectedRequest == nil {
					if reqType != nil {
						t.Errorf("%s: expected nil request type, got %+v", tt.description, reqType)
					}
				} else {
					if reqType == nil {
						t.Errorf("%s: expected request type %+v, got nil", tt.description, tt.expectedRequest)
					} else {
						if reqType.Name != tt.expectedRequest.Name {
							t.Errorf("%s: expected request name %q, got %q", tt.description, tt.expectedRequest.Name, reqType.Name)
						}
						if reqType.IsPointer != tt.expectedRequest.IsPointer {
							t.Errorf("%s: expected request IsPointer %v, got %v", tt.description, tt.expectedRequest.IsPointer, reqType.IsPointer)
						}
					}
				}

				// Check response type
				if tt.expectedResponse == nil {
					if respType != nil {
						t.Errorf("%s: expected nil response type, got %+v", tt.description, respType)
					}
				} else {
					if respType == nil {
						t.Errorf("%s: expected response type %+v, got nil", tt.description, tt.expectedResponse)
					} else {
						if respType.Name != tt.expectedResponse.Name {
							t.Errorf("%s: expected response name %q, got %q", tt.description, tt.expectedResponse.Name, respType.Name)
						}
						if respType.IsPointer != tt.expectedResponse.IsPointer {
							t.Errorf("%s: expected response IsPointer %v, got %v", tt.description, tt.expectedResponse.IsPointer, respType.IsPointer)
						}
					}
				}
			} else if err == nil {
				t.Errorf("%s: expected error for missing handler, got nil", tt.description)
			}
		})
	}
}

// TestExtractRequestTypeContextFirst tests request type extraction with context-first signatures
// Addresses CodeRabbit issue: Request type extraction misses ctx-first signatures
func TestExtractRequestTypeContextFirst(t *testing.T) {
	analyzer := New("test")

	tests := []struct {
		name        string
		code        string
		expectedReq *models.TypeInfo
		description string
	}{
		{
			name: "request first, context second",
			code: `package test
import "github.com/gaborage/go-bricks/server"
type Handler struct{}
type CreateReq struct{}
func (h *Handler) create(req CreateReq, ctx server.HandlerContext) {}`,
			expectedReq: &models.TypeInfo{Name: "CreateReq", Package: "test", IsPointer: false},
			description: "should extract request from first parameter",
		},
		{
			name: "context first, request second",
			code: `package test
import "github.com/gaborage/go-bricks/server"
type Handler struct{}
type CreateReq struct{}
func (h *Handler) create(ctx server.HandlerContext, req CreateReq) {}`,
			expectedReq: &models.TypeInfo{Name: "CreateReq", Package: "test", IsPointer: false},
			description: "should extract request from second parameter (ctx-first signature)",
		},
		{
			name: "pointer request with context first",
			code: `package test
import "github.com/gaborage/go-bricks/server"
type Handler struct{}
type UpdateReq struct{}
func (h *Handler) update(ctx server.HandlerContext, req *UpdateReq) {}`,
			expectedReq: &models.TypeInfo{Name: "UpdateReq", Package: "test", IsPointer: true},
			description: "should handle pointer request type with ctx-first",
		},
		{
			name: "no request type, only context",
			code: `package test
import "github.com/gaborage/go-bricks/server"
type Handler struct{}
func (h *Handler) list(ctx server.HandlerContext) {}`,
			expectedReq: nil,
			description: "should return nil when only framework types present",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fset := token.NewFileSet()
			astFile, err := parser.ParseFile(fset, "test.go", tt.code, parser.ParseComments)
			if err != nil {
				t.Fatalf("%s: failed to parse code: %v", tt.description, err)
			}

			// Find the function declaration
			for _, decl := range astFile.Decls {
				funcDecl, ok := decl.(*ast.FuncDecl)
				if !ok {
					continue
				}

				reqType := analyzer.extractRequestType(funcDecl.Type.Params, astFile.Name.Name)

				if tt.expectedReq == nil {
					if reqType != nil {
						t.Errorf("%s: expected nil request type, got %+v", tt.description, reqType)
					}
				} else {
					if reqType == nil {
						t.Errorf("%s: expected request type %+v, got nil", tt.description, tt.expectedReq)
					} else {
						if reqType.Name != tt.expectedReq.Name {
							t.Errorf("%s: expected request name %q, got %q", tt.description, tt.expectedReq.Name, reqType.Name)
						}
						if reqType.IsPointer != tt.expectedReq.IsPointer {
							t.Errorf("%s: expected IsPointer %v, got %v", tt.description, tt.expectedReq.IsPointer, reqType.IsPointer)
						}
					}
				}
			}
		})
	}
}

// TestParseStructTagsJSONSkip tests json:"-" tag preservation
// Addresses CodeRabbit issue: Preserve json:"-" sentinel so generator can skip fields
func TestParseStructTagsJSONSkip(t *testing.T) {
	analyzer := New("test")

	tests := []struct {
		name         string
		tag          string
		expectedJSON string
		description  string
	}{
		{
			name:         "json skip sentinel",
			tag:          `json:"-"`,
			expectedJSON: "-",
			description:  "should preserve json:\"-\" sentinel",
		},
		{
			name:         "json skip with other tags",
			tag:          `json:"-" validate:"required"`,
			expectedJSON: "-",
			description:  "should preserve json:\"-\" even with other tags",
		},
		{
			name:         "normal json tag",
			tag:          `json:"user_id"`,
			expectedJSON: "user_id",
			description:  "should parse normal json tag",
		},
		{
			name:         "json tag with omitempty",
			tag:          `json:"email,omitempty"`,
			expectedJSON: "email",
			description:  "should extract field name from json tag with options",
		},
		{
			name:         "empty json tag",
			tag:          `json:""`,
			expectedJSON: "",
			description:  "should handle empty json tag",
		},
		{
			name:         "no json tag",
			tag:          `validate:"required"`,
			expectedJSON: "",
			description:  "should return empty when no json tag present",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tags := analyzer.parseStructTags(tt.tag)
			if tags.jsonName != tt.expectedJSON {
				t.Errorf("%s: expected JSONName %q, got %q", tt.description, tt.expectedJSON, tags.jsonName)
			}
		})
	}
}

// TestParseStructTagsComprehensive tests all tag parsing combinations
func TestParseStructTagsComprehensive(t *testing.T) {
	analyzer := New("test")

	tests := []struct {
		name              string
		tag               string
		expectedJSONName  string
		expectedParamType string
		expectedParamName string
		expectedDesc      string
		expectedExample   string
		expectedValidate  string
	}{
		{
			name:             "empty tag",
			tag:              "",
			expectedJSONName: "",
		},
		{
			name:             "json tag only",
			tag:              `json:"user_id"`,
			expectedJSONName: "user_id",
		},
		{
			name:              "param tag",
			tag:               `param:"id"`,
			expectedParamType: "path",
			expectedParamName: "id",
		},
		{
			name:              "query tag",
			tag:               `query:"page"`,
			expectedParamType: "query",
			expectedParamName: "page",
		},
		{
			name:              "header tag",
			tag:               `header:"Authorization"`,
			expectedParamType: "header",
			expectedParamName: "Authorization",
		},
		{
			name:         "doc tag",
			tag:          `doc:"User email address"`,
			expectedDesc: "User email address",
		},
		{
			name:            "example tag",
			tag:             `example:"user@example.com"`,
			expectedExample: testUserEmail,
		},
		{
			name:             "validate tag",
			tag:              `validate:"required,email"`,
			expectedValidate: "required,email",
		},
		{
			name:             "multiple tags",
			tag:              `json:"email" validate:"required,email" doc:"User email" example:"user@example.com"`,
			expectedJSONName: "email",
			expectedDesc:     "User email",
			expectedExample:  testUserEmail,
			expectedValidate: "required,email",
		},
		{
			name:              "json with param",
			tag:               `json:"user_id" param:"id"`,
			expectedJSONName:  "user_id",
			expectedParamType: "path",
			expectedParamName: "id",
		},
		{
			name:              "param query precedence (query wins)",
			tag:               `param:"id" query:"user_id"`,
			expectedParamType: "query",
			expectedParamName: "user_id",
		},
		{
			name:              "param query header precedence (header wins)",
			tag:               "param:\"id\" query:\"user_id\" header:\"X-User-ID\"",
			expectedParamType: "header",
			expectedParamName: testUserIDHeader,
		},
		{
			name:              "query header precedence (header wins)",
			tag:               `query:"page" header:"X-Page"`,
			expectedParamType: "header",
			expectedParamName: "X-Page",
		},
		{
			name:             "json with omitempty",
			tag:              `json:"email,omitempty"`,
			expectedJSONName: "email",
		},
		{
			name:              "complex combination",
			tag:               `json:"email,omitempty" query:"email" validate:"required,email,min=5,max=100" doc:"User email address" example:"user@example.com"`,
			expectedJSONName:  "email",
			expectedParamType: "query",
			expectedParamName: "email",
			expectedDesc:      "User email address",
			expectedExample:   testUserEmail,
			expectedValidate:  "required,email,min=5,max=100",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tags := analyzer.parseStructTags(tt.tag)

			if tags.jsonName != tt.expectedJSONName {
				t.Errorf("expected JSONName %q, got %q", tt.expectedJSONName, tags.jsonName)
			}
			if tags.paramType != tt.expectedParamType {
				t.Errorf("expected ParamType %q, got %q", tt.expectedParamType, tags.paramType)
			}
			if tags.paramName != tt.expectedParamName {
				t.Errorf("expected ParamName %q, got %q", tt.expectedParamName, tags.paramName)
			}
			if tags.description != tt.expectedDesc {
				t.Errorf("expected Description %q, got %q", tt.expectedDesc, tags.description)
			}
			if tags.example != tt.expectedExample {
				t.Errorf("expected Example %q, got %q", tt.expectedExample, tags.example)
			}
			if tags.rawValidation != tt.expectedValidate {
				t.Errorf("expected RawValidation %q, got %q", tt.expectedValidate, tags.rawValidation)
			}
		})
	}
}

// TestParseJSONTagName tests the JSON tag name extraction helper
func TestParseJSONTagName(t *testing.T) {
	analyzer := New("test")

	tests := []struct {
		name     string
		jsonTag  string
		expected string
	}{
		{
			name:     "empty tag",
			jsonTag:  "",
			expected: "",
		},
		{
			name:     "simple field name",
			jsonTag:  "user_id",
			expected: "user_id",
		},
		{
			name:     "field with omitempty",
			jsonTag:  "email,omitempty",
			expected: "email",
		},
		{
			name:     "skip sentinel",
			jsonTag:  "-",
			expected: "-",
		},
		{
			name:     "skip with options",
			jsonTag:  "-,omitempty",
			expected: "-",
		},
		{
			name:     "empty field name",
			jsonTag:  ",omitempty",
			expected: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := analyzer.parseJSONTagName(tt.jsonTag)
			if result != tt.expected {
				t.Errorf("expected %q, got %q", tt.expected, result)
			}
		})
	}
}

// TestParseParameterTags tests the parameter tag extraction helper
func TestParseParameterTags(t *testing.T) {
	analyzer := New("test")

	tests := []struct {
		name              string
		tag               string
		expectedParamType string
		expectedParamName string
	}{
		{
			name:              "no parameter tags",
			tag:               `json:"id"`,
			expectedParamType: "",
			expectedParamName: "",
		},
		{
			name:              "param tag only",
			tag:               `param:"id"`,
			expectedParamType: "path",
			expectedParamName: "id",
		},
		{
			name:              "query tag only",
			tag:               `query:"page"`,
			expectedParamType: "query",
			expectedParamName: "page",
		},
		{
			name:              "header tag only",
			tag:               `header:"Authorization"`,
			expectedParamType: "header",
			expectedParamName: "Authorization",
		},
		{
			name:              "param and query - query wins",
			tag:               `param:"id" query:"user_id"`,
			expectedParamType: "query",
			expectedParamName: "user_id",
		},
		{
			name:              "param and header - header wins",
			tag:               `param:"id" header:"X-User-ID"`,
			expectedParamType: "header",
			expectedParamName: testUserIDHeader,
		},
		{
			name:              "query and header - header wins",
			tag:               `query:"page" header:"X-Page"`,
			expectedParamType: "header",
			expectedParamName: "X-Page",
		},
		{
			name:              "all three tags - header wins",
			tag:               `param:"id" query:"user_id" header:"X-User-ID"`,
			expectedParamType: "header",
			expectedParamName: testUserIDHeader,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			paramType, paramName := analyzer.parseParameterTags(tt.tag)
			if paramType != tt.expectedParamType {
				t.Errorf("expected ParamType %q, got %q", tt.expectedParamType, paramType)
			}
			if paramName != tt.expectedParamName {
				t.Errorf("expected ParamName %q, got %q", tt.expectedParamName, paramName)
			}
		})
	}
}
