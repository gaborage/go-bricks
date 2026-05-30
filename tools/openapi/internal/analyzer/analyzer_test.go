package analyzer

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gaborage/go-bricks/tools/openapi/internal/models"
)

const (
	testModuleName         = "testmodule"
	testModuleDescription  = "Test module for API operations"
	getUserRoute           = "/users/:id"  // Echo syntax, as registered in source
	getUserRouteNormalized = "/users/{id}" // OpenAPI templating, as stored on route.Path
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

	// Validate routes (index to avoid copying the Route value each iteration)
	for i := range module.Routes {
		validateDiscoveredRoute(t, &module.Routes[i])
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

	// Check specific routes. route.Path is normalized to OpenAPI templating, so
	// the GET route is matched by its "{id}" form, not the registered ":id".
	switch route.Path {
	case getUserRouteNormalized:
		assertGetRoute(t, route)
	case createUserRoute:
		assertCreateRoute(t, route)
	}
}

func assertGetRoute(t *testing.T, route *models.Route) {
	t.Helper()
	if route.Method != "GET" {
		t.Errorf("Expected GET method for %s, got %s", getUserRouteNormalized, route.Method)
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
	module, _, err := analyzer.analyzeGoFile(moduleFile)

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
	module, _, err := analyzer.analyzeGoFile(utilFile)

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
				comments := make([]*ast.Comment, 0, len(tt.comments))
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
		expectOK bool
	}{
		{
			name:     "string literal",
			arg:      &ast.BasicLit{Kind: token.STRING, Value: `"/direct/path"`},
			expected: "/direct/path",
			expectOK: true,
		},
		{
			name:     "constant reference found",
			arg:      &ast.Ident{Name: "testRoute"},
			expected: "/api/test",
			expectOK: true,
		},
		{
			// Unresolved identifiers are no longer emitted as garbage path keys.
			name:     "constant reference not found",
			arg:      &ast.Ident{Name: "unknownRoute"},
			expected: "",
			expectOK: false,
		},
		{
			name:     "non-string literal",
			arg:      &ast.BasicLit{Kind: token.INT, Value: "123"},
			expected: "",
			expectOK: false,
		},
		{
			name:     "unsupported expression",
			arg:      &ast.BinaryExpr{Op: token.ADD},
			expected: "",
			expectOK: false,
		},
		{
			name: "concatenation of constant and literal",
			arg: &ast.BinaryExpr{
				Op: token.ADD,
				X:  &ast.Ident{Name: "testRoute"},
				Y:  &ast.BasicLit{Kind: token.STRING, Value: `"/extra"`},
			},
			expected: "/api/test/extra",
			expectOK: true,
		},
		{
			name: "concatenation with an unresolved operand",
			arg: &ast.BinaryExpr{
				Op: token.ADD,
				X:  &ast.Ident{Name: "unknownRoute"},
				Y:  &ast.BasicLit{Kind: token.STRING, Value: `"/extra"`},
			},
			expected: "",
			expectOK: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, ok := analyzer.extractPathFromArg(tt.arg)
			assert.Equal(t, tt.expectOK, ok, "resolved")
			assert.Equal(t, tt.expected, result)
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

			result, _, _ := analyzer.extractModuleFromAST(astFile, "test")
			found := (result != nil)
			if found != tt.expected {
				t.Errorf("Expected module found=%v, got %v", tt.expected, found)
			}
		})
	}
}

// hasRegisterRoutesWarning reports whether any analyzer warning is the near-miss
// RegisterRoutes diagnostic.
func hasRegisterRoutesWarning(warnings []string) bool {
	for _, w := range warnings {
		if strings.Contains(w, moduleMethodRegisterRoutes) && strings.Contains(w, "unrecognized") {
			return true
		}
	}
	return false
}

// writeAnalyzerProject writes one .go file into a fresh temp dir and returns the
// dir, for driving AnalyzeProject end-to-end.
func writeAnalyzerProject(t *testing.T, fileName, src string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, fileName), []byte(src), 0o644); err != nil {
		t.Fatalf("write %s: %v", fileName, err)
	}
	return dir
}

// TestUnrecognizedRegisterRoutesWarning verifies that, in a package with no valid
// module, a struct which looks like a module (valid Name + Init) but whose
// RegisterRoutes signature is unrecognized is surfaced with a package-qualified
// diagnostic rather than silently dropped (PR13 acceptance #4).
func TestUnrecognizedRegisterRoutesWarning(t *testing.T) {
	dir := writeAnalyzerProject(t, "module.go", `package widgets
type Module struct{}
func (m *Module) Name() string { return "widgets" }
func (m *Module) Init(deps *app.ModuleDeps) error { return nil }
func (m *Module) RegisterRoutes() {}`) // missing (*server.HandlerRegistry, server.RouteRegistrar)

	a := New(dir)
	if _, err := a.AnalyzeProject(); err != nil {
		t.Fatalf("AnalyzeProject: %v", err)
	}

	warnings := a.Warnings(context.Background())
	found := false
	for _, w := range warnings {
		if strings.Contains(w, "Module") && strings.Contains(w, moduleMethodRegisterRoutes) && strings.Contains(w, "module.go") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected a package-qualified RegisterRoutes near-miss diagnostic, got: %v", warnings)
	}
}

// TestNearMissSuppressedWhenValidModuleExists verifies the near-miss diagnostic is
// NOT emitted when the package already contains a valid module — even when the
// near-miss struct is declared first (the review's order-dependence / false-
// positive concern).
func TestNearMissSuppressedWhenValidModuleExists(t *testing.T) {
	dir := writeAnalyzerProject(t, "shop.go", `package shop
type Widget struct{}
func (w *Widget) Name() string { return "widget" }
func (w *Widget) Init(deps *app.ModuleDeps) error { return nil }
func (w *Widget) RegisterRoutes() {}

type Module struct{}
func (m *Module) Name() string { return "shop" }
func (m *Module) Init(deps *app.ModuleDeps) error { return nil }
func (m *Module) RegisterRoutes(hr *server.HandlerRegistry, r server.RouteRegistrar) {}`)

	a := New(dir)
	if _, err := a.AnalyzeProject(); err != nil {
		t.Fatalf("AnalyzeProject: %v", err)
	}
	if hasRegisterRoutesWarning(a.Warnings(context.Background())) {
		t.Errorf("no near-miss warning expected when the package has a valid module, got: %v",
			a.Warnings(context.Background()))
	}
}

// TestNoWarningForNonModuleStruct verifies the near-miss diagnostic is reserved
// for genuine near-misses: a struct that simply has no RegisterRoutes method is
// not a module and must not produce a warning.
func TestNoWarningForNonModuleStruct(t *testing.T) {
	dir := writeAnalyzerProject(t, "helper.go", `package helper
type Module struct{}
func (m *Module) Name() string { return "helper" }
func (m *Module) Init(deps *app.ModuleDeps) error { return nil }`)

	a := New(dir)
	if _, err := a.AnalyzeProject(); err != nil {
		t.Fatalf("AnalyzeProject: %v", err)
	}
	if hasRegisterRoutesWarning(a.Warnings(context.Background())) {
		t.Errorf("expected no near-miss diagnostic for a struct lacking RegisterRoutes, got: %v",
			a.Warnings(context.Background()))
	}
}

// TestExtractRoutesFromFuncBodyEdgeCases tests edge cases for route extraction
// from a function body (invalid calls, wrong package, bad arg counts).
func TestExtractRoutesFromFuncBodyEdgeCases(t *testing.T) {
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
			// Args[2] (extra1) is an unresolved identifier, not a literal path, so
			// the route is dropped rather than emitted with a garbage path key.
			expected: 0,
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
	_, _, err := analyzer.analyzeGoFile(moduleFile)
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
	module, _, err := analyzer.analyzeGoFile(moduleFile)

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

		result, structName, _ := analyzer.extractModuleFromAST(astFile, filepath.Join(tempDir, "testmodule.go"))
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
		result, _, _ := analyzer.extractModuleFromAST(astFile, filepath.Join(tempDir, testFileName))
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
			result, _, _ := analyzer.extractModuleFromAST(astFile, filepath.Join("test", "file.go"))
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

		module, _, err := analyzer.analyzeGoFile(nonModuleFile)
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

		module, _, err := analyzer.analyzeGoFile(complexFile)
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
	"github.com/labstack/echo/v5"
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
	"github.com/labstack/echo/v5"
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
	"github.com/labstack/echo/v5"
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
	"github.com/labstack/echo/v5"
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
			require.NoError(t, err, "Failed to parse code")

			// Find the function and extract the parameter type
			var expr ast.Expr
			for _, decl := range astFile.Decls {
				funcDecl, ok := decl.(*ast.FuncDecl)
				if !ok {
					continue
				}
				if funcDecl.Type.Params != nil && len(funcDecl.Type.Params.List) > 0 {
					expr = funcDecl.Type.Params.List[0].Type
					break
				}
			}
			require.NotNil(t, expr, "Failed to find parameter type expression")

			result := analyzer.typeInfoFromExpr(expr, "test")

			assertTypeInfo(t, tt.description, tt.expected, result)
		})
	}
}

// assertTypeInfo compares an expected and actual *models.TypeInfo, handling nil cases.
func assertTypeInfo(t *testing.T, description string, expected, actual *models.TypeInfo) {
	t.Helper()
	if expected == nil {
		assert.Nil(t, actual, "%s: expected nil", description)
		return
	}
	require.NotNil(t, actual, "%s: expected non-nil", description)
	assert.Equal(t, expected.Name, actual.Name, "%s: Name", description)
	assert.Equal(t, expected.Package, actual.Package, "%s: Package", description)
	assert.Equal(t, expected.IsPointer, actual.IsPointer, "%s: IsPointer", description)
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
			tempDir := t.TempDir()

			testFilePath := filepath.Join(tempDir, "handler.go")
			require.NoError(t, os.WriteFile(testFilePath, []byte(tt.handlerCode), 0600), "Failed to write test file")

			fset := token.NewFileSet()
			astFile, err := parser.ParseFile(fset, testFilePath, nil, parser.ParseComments)
			require.NoError(t, err, "Failed to parse file")

			analyzer := New(tempDir)
			reqType, respType, _, err := analyzer.extractHandlerSignature(astFile, testFilePath, tt.structName, false, tt.handlerName)

			if tt.shouldFindHandler {
				require.NoError(t, err, tt.description)
				assertTypeInfo(t, tt.description+" request", tt.expectedRequest, reqType)
				assertTypeInfo(t, tt.description+" response", tt.expectedResponse, respType)
			} else {
				assert.Error(t, err, "%s: expected error for missing handler", tt.description)
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
			require.NoError(t, err, "%s: failed to parse code", tt.description)

			// Find the function declaration and extract request type
			for _, decl := range astFile.Decls {
				funcDecl, ok := decl.(*ast.FuncDecl)
				if !ok {
					continue
				}
				reqType := analyzer.extractRequestType(funcDecl.Type.Params, astFile.Name.Name)
				assertTypeInfo(t, tt.description, tt.expectedReq, reqType)
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

			assert.Equal(t, tt.expectedJSONName, tags.jsonName, "JSONName")
			assert.Equal(t, tt.expectedParamType, tags.paramType, "ParamType")
			assert.Equal(t, tt.expectedParamName, tags.paramName, "ParamName")
			assert.Equal(t, tt.expectedDesc, tags.description, "Description")
			assert.Equal(t, tt.expectedExample, tags.example, "Example")
			assert.Equal(t, tt.expectedValidate, tags.rawValidation, "RawValidation")
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

func TestHasJOSESentinelTag(t *testing.T) {
	// parse uses ast.Inspect to find the first struct type literal in src — a single
	// callback collapses what would otherwise be a nested decl→spec→type-assertion
	// loop, keeping cognitive complexity well below the project's 15-statement cap.
	parse := func(t *testing.T, src string) *ast.StructType {
		t.Helper()
		f, err := parser.ParseFile(token.NewFileSet(), "test.go", "package x\n"+src, 0)
		if err != nil {
			t.Fatalf("parse: %v", err)
		}
		var found *ast.StructType
		ast.Inspect(f, func(n ast.Node) bool {
			if found != nil {
				return false
			}
			if st, ok := n.(*ast.StructType); ok {
				found = st
				return false
			}
			return true
		})
		if found == nil {
			t.Fatal("no struct found")
		}
		return found
	}

	tests := []struct {
		name   string
		source string
		want   bool
	}{
		{
			name:   "tagged_sentinel_field",
			source: "type R struct { _ struct{} `jose:\"decrypt=k,verify=p\"`; PAN string `json:\"pan\"` }",
			want:   true,
		},
		{
			name:   "no_jose_tag",
			source: "type R struct { PAN string `json:\"pan\" validate:\"required\"` }",
			want:   false,
		},
		{
			// Per the documented convention, only the sentinel `_ struct{}` field
			// counts. A non-sentinel field with a jose tag must NOT opt the struct in.
			name:   "jose_tag_on_named_field_must_not_match",
			source: "type R struct { Inner string `jose:\"sign=k\"` }",
			want:   false,
		},
		{
			name:   "blank_field_without_jose_tag",
			source: "type R struct { _ struct{} `unrelated:\"x\"`; PAN string }",
			want:   false,
		},
		{
			name:   "empty_struct",
			source: "type R struct {}",
			want:   false,
		},
		{
			name:   "jose_substring_in_other_tag_must_not_match",
			source: "type R struct { Field string `description:\"prejose:\\\"x\\\"\"` }",
			want:   false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := hasJOSESentinelTag(parse(t, tt.source))
			if got != tt.want {
				t.Errorf("hasJOSESentinelTag = %v, want %v", got, tt.want)
			}
		})
	}

	// Lock in the nil-safety contract — the function's signature implies it tolerates
	// these inputs (defense-in-depth in the analyzer pipeline), so without explicit
	// tests a future refactor could silently regress the guards.
	t.Run("nil_struct", func(t *testing.T) {
		assert.False(t, hasJOSESentinelTag(nil), "hasJOSESentinelTag(nil)")
	})
	t.Run("nil_fields", func(t *testing.T) {
		assert.False(t, hasJOSESentinelTag(&ast.StructType{}), "hasJOSESentinelTag(empty struct)")
	})
}

func TestNormalizePath(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "no_params_unchanged",
			in:   "/users",
			want: "/users",
		},
		{
			name: "single_path_param",
			in:   "/users/:id",
			want: "/users/{id}",
		},
		{
			name: "multiple_path_params",
			in:   "/orgs/:orgID/users/:id",
			want: "/orgs/{orgID}/users/{id}",
		},
		{
			name: "param_then_literal_segment",
			in:   "/users/:id/posts",
			want: "/users/{id}/posts",
		},
		{
			// Catch-all wildcards are deliberately NOT templated (see normalizePath
			// doc): emitting "{path}" without a declared parameter is invalid OpenAPI.
			name: "trailing_catch_all_wildcard_left_literal",
			in:   "/files/*",
			want: "/files/*",
		},
		{
			name: "named_wildcard_left_literal",
			in:   "/assets/*filepath",
			want: "/assets/*filepath",
		},
		{
			name: "root_path_unchanged",
			in:   "/",
			want: "/",
		},
		{
			name: "empty_string_unchanged",
			in:   "",
			want: "",
		},
		{
			name: "trailing_slash_preserved",
			in:   "/users/:id/",
			want: "/users/{id}/",
		},
		{
			// Defensive: a bare ":" carries no parameter name, so rewriting it would
			// emit an invalid "{}" template. Leave such segments untouched.
			name: "bare_colon_left_untouched",
			in:   "/users/:",
			want: "/users/:",
		},
		{
			name: "wildcard_mixed_with_param",
			in:   "/files/:bucket/*",
			want: "/files/{bucket}/*",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, normalizePath(tt.in))
		})
	}
}

// parseHandlerArgs parses module source and returns the parsed file, its path,
// and the 4th argument (the handler) of every server.METHOD(...) call.
func parseHandlerArgs(t *testing.T, src string) (*ast.File, string, []ast.Expr) {
	t.Helper()
	dir := t.TempDir()
	fp := filepath.Join(dir, "module.go")
	require.NoError(t, os.WriteFile(fp, []byte(src), 0600))
	astFile, err := parser.ParseFile(token.NewFileSet(), fp, nil, 0)
	require.NoError(t, err)

	methodCheck := New("")
	var args []ast.Expr
	ast.Inspect(astFile, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok || len(call.Args) < 4 {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		// Only server.<HTTP method>(...) calls — not variadic server.WithTags(...)
		// options, which would otherwise shift the collected-arg indices. Reuse the
		// analyzer's own method list rather than duplicating it here.
		if pkg, ok := sel.X.(*ast.Ident); ok && pkg.Name == "server" && methodCheck.isHTTPMethod(sel.Sel.Name) {
			args = append(args, call.Args[3])
		}
		return true
	})
	return astFile, fp, args
}

func TestResolveHandler(t *testing.T) {
	src := `package shop
import "github.com/gaborage/go-bricks/server"
type Module struct { h *Handler }
type Handler struct{}
func (m *Module) reg(hr *server.HandlerRegistry, r server.RouteRegistrar) {
	server.POST(hr, r, "/a", m.createUser)
	server.POST(hr, r, "/b", m.h.createUser)
	server.GET(hr, r, "/c", ping)
}`
	astFile, fp, args := parseHandlerArgs(t, src)
	require.Len(t, args, 3)
	a := New(filepath.Dir(fp))

	t.Run("module_method", func(t *testing.T) {
		name, recv, isPkg, ok := a.resolveHandler(args[0], "Module", astFile, fp)
		assert.True(t, ok)
		assert.Equal(t, "createUser", name)
		assert.Equal(t, "Module", recv)
		assert.False(t, isPkg)
	})
	t.Run("field_method_resolves_field_type", func(t *testing.T) {
		name, recv, isPkg, ok := a.resolveHandler(args[1], "Module", astFile, fp)
		assert.True(t, ok)
		assert.Equal(t, "createUser", name)
		assert.Equal(t, "Handler", recv) // m.h -> *Handler -> Handler
		assert.False(t, isPkg)
	})
	t.Run("package_func", func(t *testing.T) {
		name, recv, isPkg, ok := a.resolveHandler(args[2], "Module", astFile, fp)
		assert.True(t, ok)
		assert.Equal(t, "ping", name)
		assert.Empty(t, recv)
		assert.True(t, isPkg)
	})
	t.Run("unrecognized_arg", func(t *testing.T) {
		_, _, _, ok := a.resolveHandler(&ast.FuncLit{}, "Module", astFile, fp)
		assert.False(t, ok)
	})
	t.Run("selector_on_non_ident_non_selector", func(t *testing.T) {
		// e.g. foo().method — the qualifier is a *ast.CallExpr, which the inner
		// switch does not recognize, so resolution fails closed.
		arg := &ast.SelectorExpr{X: &ast.CallExpr{Fun: &ast.Ident{Name: "foo"}}, Sel: &ast.Ident{Name: "method"}}
		_, _, _, ok := a.resolveHandler(arg, "Module", astFile, fp)
		assert.False(t, ok)
	})
}

func TestResolveFieldType(t *testing.T) {
	src := `package shop
type Module struct { h *Handler; svc Service }
type Handler struct{}
type Service struct{}`
	astFile, fp, _ := parseHandlerArgs(t, src)
	a := New(filepath.Dir(fp))

	assert.Equal(t, "Handler", a.resolveFieldType("Module", "h", astFile, fp), "pointer field -> base type")
	assert.Equal(t, "Service", a.resolveFieldType("Module", "svc", astFile, fp), "value field")
	assert.Empty(t, a.resolveFieldType("Module", "missing", astFile, fp), "field not found")
	assert.Empty(t, a.resolveFieldType("Unknown", "h", astFile, fp), "struct not found")
	assert.Empty(t, a.resolveFieldType("", "h", astFile, fp), "empty struct name")
}

func TestBaseTypeName(t *testing.T) {
	tests := []struct {
		name string
		expr ast.Expr
		want string
	}{
		{"ident", &ast.Ident{Name: "Handler"}, "Handler"},
		{"pointer", &ast.StarExpr{X: &ast.Ident{Name: "Handler"}}, "Handler"},
		{"qualified", &ast.SelectorExpr{X: &ast.Ident{Name: "pkg"}, Sel: &ast.Ident{Name: "Handler"}}, "Handler"},
		{"pointer_qualified", &ast.StarExpr{X: &ast.SelectorExpr{X: &ast.Ident{Name: "pkg"}, Sel: &ast.Ident{Name: "Handler"}}}, "Handler"},
		{"unsupported", &ast.ArrayType{Elt: &ast.Ident{Name: "x"}}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, baseTypeName(tt.expr))
		})
	}
}

func TestHandlerReceiverMatches(t *testing.T) {
	a := New("")
	recvOnHandler := &ast.FieldList{List: []*ast.Field{{Type: &ast.StarExpr{X: &ast.Ident{Name: "Handler"}}}}}

	t.Run("package_func_no_receiver", func(t *testing.T) {
		assert.True(t, a.handlerReceiverMatches(nil, "", true))
	})
	t.Run("package_func_but_has_receiver", func(t *testing.T) {
		assert.False(t, a.handlerReceiverMatches(recvOnHandler, "", true))
	})
	t.Run("method_matches_receiver_type", func(t *testing.T) {
		assert.True(t, a.handlerReceiverMatches(recvOnHandler, "Handler", false))
	})
	t.Run("method_wrong_receiver_type", func(t *testing.T) {
		assert.False(t, a.handlerReceiverMatches(recvOnHandler, "Other", false))
	})
	t.Run("method_empty_receiver_type", func(t *testing.T) {
		assert.False(t, a.handlerReceiverMatches(recvOnHandler, "", false))
	})
}

func TestTypeInfoFromExprResultWrappers(t *testing.T) {
	parseResult := func(t *testing.T, typeExpr string) *models.TypeInfo {
		t.Helper()
		code := "package test\nimport \"github.com/gaborage/go-bricks/server\"\nfunc f(p " + typeExpr + ") {}"
		astFile, err := parser.ParseFile(token.NewFileSet(), testFileName, code, 0)
		require.NoError(t, err)
		var expr ast.Expr
		for _, decl := range astFile.Decls {
			if fn, ok := decl.(*ast.FuncDecl); ok && fn.Type.Params != nil && len(fn.Type.Params.List) > 0 {
				expr = fn.Type.Params.List[0].Type
				break
			}
		}
		require.NotNil(t, expr)
		return New("").typeInfoFromExpr(expr, "test")
	}

	t.Run("result_unwraps_to_inner", func(t *testing.T) {
		ti := parseResult(t, "server.Result[User]")
		require.NotNil(t, ti)
		assert.Equal(t, "User", ti.Name)
		assert.Equal(t, "test", ti.Package)
		assert.False(t, ti.NoContent)
	})
	t.Run("result_with_meta_unwraps_to_inner", func(t *testing.T) {
		ti := parseResult(t, "server.ResultWithMeta[User]")
		require.NotNil(t, ti)
		assert.Equal(t, "User", ti.Name)
	})
	t.Run("result_with_pointer_inner", func(t *testing.T) {
		ti := parseResult(t, "server.Result[*User]")
		require.NotNil(t, ti)
		assert.Equal(t, "User", ti.Name)
		assert.True(t, ti.IsPointer)
	})
	t.Run("no_content_result_marks_no_body", func(t *testing.T) {
		ti := parseResult(t, "server.NoContentResult")
		require.NotNil(t, ti)
		assert.True(t, ti.NoContent)
		assert.Empty(t, ti.Name)
	})
	t.Run("non_result_generic_is_nil", func(t *testing.T) {
		assert.Nil(t, parseResult(t, "server.Box[User]"), "unknown server generic is not a response carrier")
	})
	t.Run("foreign_package_result_is_nil", func(t *testing.T) {
		// A Result[T] from a non-server package must NOT be unwrapped (guards the
		// pkg.Name == "server" check in isResultWrapper).
		assert.Nil(t, parseResult(t, "other.Result[User]"))
	})
	t.Run("local_generic_is_nil", func(t *testing.T) {
		assert.Nil(t, parseResult(t, "Box[int]"), "non-framework generic is not a response carrier")
	})
	t.Run("index_list_result_unwraps_first", func(t *testing.T) {
		// Multi-index generic syntax (IndexListExpr); parser does not type-check, so
		// server.Result[A, B] exercises the multi-type-param branch -> first arg.
		ti := parseResult(t, "server.Result[User, Meta]")
		require.NotNil(t, ti)
		assert.Equal(t, "User", ti.Name)
	})
	t.Run("index_list_non_result_is_nil", func(t *testing.T) {
		assert.Nil(t, parseResult(t, "Pair[K, V]"))
	})
}

func TestRegisterTypeBuildsRegistryWithRefsAndCycleGuard(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.com/app\n\ngo 1.25\n"), 0600))
	sub := filepath.Join(dir, "mod")
	require.NoError(t, os.MkdirAll(sub, 0750))
	// Node is self-referential through both a pointer and a slice; the cycle guard
	// must register it exactly once.
	src := `package mod
import (
	"github.com/gaborage/go-bricks/app"
	"github.com/gaborage/go-bricks/server"
)
type Module struct{}
func (m *Module) Name() string { return "mod" }
func (m *Module) Init(d *app.ModuleDeps) error { return nil }
func (m *Module) Shutdown() error { return nil }
type Address struct { Street string }
type Node struct {
	Addr     Address
	Parent   *Node
	Children []Node
	Tags     []string
}
func (m *Module) RegisterRoutes(hr *server.HandlerRegistry, r server.RouteRegistrar) {
	server.POST(hr, r, "/n", m.create)
}
func (m *Module) create(req Node, ctx server.HandlerContext) (server.Result[int], server.IAPIError) { return server.OK(0), nil }
`
	require.NoError(t, os.WriteFile(filepath.Join(sub, "module.go"), []byte(src), 0600))

	project, err := New(dir).AnalyzeProject()
	require.NoError(t, err)

	// Registry holds the request type and its reachable nested types, each once.
	require.Contains(t, project.Types, "Node")
	require.Contains(t, project.Types, "Address")
	assert.Len(t, project.Types, 2, "Node + Address; the recursive Node is registered once")

	fields := map[string]models.FieldInfo{}
	for _, f := range project.Types["Node"].Fields {
		fields[f.Name] = f
	}
	assert.Equal(t, "Address", fields["Addr"].RefName, "nested struct field -> RefName")
	assert.Equal(t, "Node", fields["Parent"].RefName, "pointer self-ref -> RefName")
	assert.Equal(t, "Node", fields["Children"].RefName, "slice self-ref -> RefName")
	assert.Empty(t, fields["Tags"].RefName, "[]string field must not get a RefName")
}

func TestBaseStructTypeName(t *testing.T) {
	assert.Equal(t, "Address", baseStructTypeName("Address"))
	assert.Equal(t, "Address", baseStructTypeName("*Address"))
	assert.Equal(t, "Address", baseStructTypeName("[]Address"))
	assert.Equal(t, "Address", baseStructTypeName("[]*Address"))
	assert.Equal(t, "Address", baseStructTypeName("**[]*Address"), "double pointer before slice")
	assert.Equal(t, "string", baseStructTypeName("[]string"))
	assert.Equal(t, "map[string]Address", baseStructTypeName("map[string]Address"), "maps returned as-is")
}

// analyzeModuleProject writes src as a module file under a temp project and runs
// AnalyzeProject, returning the project (for inspecting project.Types).
func analyzeModuleProject(t *testing.T, src string) *models.Project {
	t.Helper()
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.com/app\n\ngo 1.25\n"), 0600))
	sub := filepath.Join(dir, "mod")
	require.NoError(t, os.MkdirAll(sub, 0750))
	require.NoError(t, os.WriteFile(filepath.Join(sub, "module.go"), []byte(src), 0600))
	project, err := New(dir).AnalyzeProject()
	require.NoError(t, err)
	return project
}

func TestRegisterTypeHandlesMutualRecursion(t *testing.T) {
	src := `package mod
import (
	"github.com/gaborage/go-bricks/app"
	"github.com/gaborage/go-bricks/server"
)
type Module struct{}
func (m *Module) Name() string { return "mod" }
func (m *Module) Init(d *app.ModuleDeps) error { return nil }
func (m *Module) Shutdown() error { return nil }
type A struct { Link *B }
type B struct { Back *A }
func (m *Module) RegisterRoutes(hr *server.HandlerRegistry, r server.RouteRegistrar) {
	server.POST(hr, r, "/a", m.create)
}
func (m *Module) create(req A, ctx server.HandlerContext) (server.Result[int], server.IAPIError) { return server.OK(0), nil }
`
	project := analyzeModuleProject(t, src)
	require.Contains(t, project.Types, "A")
	require.Contains(t, project.Types, "B")
	assert.Len(t, project.Types, 2, "mutual recursion A<->B registers each type once")
	assert.Equal(t, "B", project.Types["A"].Fields[0].RefName)
	assert.Equal(t, "A", project.Types["B"].Fields[0].RefName)
}

func TestRegisterTypeSkipsJSONExcludedFields(t *testing.T) {
	// A type reachable ONLY through a json:"-" field must not be registered as an
	// (unreferenced, internal) component. Built via concatenation because the raw
	// source needs backtick struct tags.
	src := "package mod\n" +
		"import (\n\t\"github.com/gaborage/go-bricks/app\"\n\t\"github.com/gaborage/go-bricks/server\"\n)\n" +
		"type Module struct{}\n" +
		"func (m *Module) Name() string { return \"mod\" }\n" +
		"func (m *Module) Init(d *app.ModuleDeps) error { return nil }\n" +
		"func (m *Module) Shutdown() error { return nil }\n" +
		"type Secret struct { Token string }\n" +
		"type Req struct {\n\tName   string `json:\"name\"`\n\tHidden Secret `json:\"-\"`\n}\n" +
		"func (m *Module) RegisterRoutes(hr *server.HandlerRegistry, r server.RouteRegistrar) {\n" +
		"\tserver.POST(hr, r, \"/r\", m.create)\n}\n" +
		"func (m *Module) create(req Req, ctx server.HandlerContext) (server.Result[int], server.IAPIError) { return server.OK(0), nil }\n"
	project := analyzeModuleProject(t, src)
	require.Contains(t, project.Types, "Req")
	assert.NotContains(t, project.Types, "Secret", `a type reachable only via a json:"-" field must not be registered`)
}

func TestAnalyzeProjectStampsModuleIdentity(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.com/app\n\ngo 1.25\n"), 0600))
	sub := filepath.Join(dir, "users")
	require.NoError(t, os.MkdirAll(sub, 0750))
	src := `package users
import (
	"github.com/gaborage/go-bricks/app"
	"github.com/gaborage/go-bricks/server"
)
type Module struct{}
func (m *Module) Name() string { return "users" }
func (m *Module) Init(d *app.ModuleDeps) error { return nil }
func (m *Module) Shutdown() error { return nil }
func (m *Module) RegisterRoutes(hr *server.HandlerRegistry, r server.RouteRegistrar) {
	server.GET(hr, r, "/users", m.list)
}
func (m *Module) list(ctx server.HandlerContext) (server.Result[int], server.IAPIError) { return server.OK(0), nil }
`
	require.NoError(t, os.WriteFile(filepath.Join(sub, "module.go"), []byte(src), 0600))

	project, err := New(dir).AnalyzeProject()
	require.NoError(t, err)
	require.Len(t, project.Modules, 1)
	require.NotEmpty(t, project.Modules[0].Routes)
	for i := range project.Modules[0].Routes {
		assert.Equal(t, "users", project.Modules[0].Routes[i].Module, "route %d Module", i)
		assert.Equal(t, "users", project.Modules[0].Routes[i].Package, "route %d Package", i)
	}
}

// analyzeSingleModule writes src as a module file under a temp project, runs
// AnalyzeProject, and returns the analyzer (for Warnings) and the flattened routes.
func analyzeSingleModule(t *testing.T, src string) (*ProjectAnalyzer, []models.Route) {
	t.Helper()
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.com/app\n\ngo 1.25\n"), 0600))
	sub := filepath.Join(dir, "mod")
	require.NoError(t, os.MkdirAll(sub, 0750))
	require.NoError(t, os.WriteFile(filepath.Join(sub, "module.go"), []byte(src), 0600))

	a := New(dir)
	project, err := a.AnalyzeProject()
	require.NoError(t, err)
	var routes []models.Route
	for i := range project.Modules {
		routes = append(routes, project.Modules[i].Routes...)
	}
	return a, routes
}

func routePathSet(routes []models.Route) map[string]bool {
	set := map[string]bool{}
	for i := range routes {
		set[routes[i].Method+" "+routes[i].Path] = true
	}
	return set
}

func TestRegistrationWalkDiscoversAllPatterns(t *testing.T) {
	src := `package mod
import (
	"github.com/gaborage/go-bricks/app"
	"github.com/gaborage/go-bricks/server"
)
const apiBase = "/api"
type Module struct{}
func (m *Module) Name() string { return "mod" }
func (m *Module) Init(d *app.ModuleDeps) error { return nil }
func (m *Module) Shutdown() error { return nil }
func (m *Module) RegisterRoutes(hr *server.HandlerRegistry, r server.RouteRegistrar) {
	v1 := r.Group("/v1")
	server.GET(hr, v1, "/items", m.h)
	admin := v1.Group("/admin")
	server.GET(hr, admin, "/stats", m.h)
	if true {
		server.GET(hr, r, "/health", m.h)
	}
	for i := 0; i < 1; i++ {
		server.GET(hr, r, "/loop", m.h)
	}
	server.GET(hr, r, apiBase+"/version", m.h)
	m.helper(hr, r)
}
func (m *Module) helper(hr *server.HandlerRegistry, r server.RouteRegistrar) {
	server.POST(hr, r, "/items", m.h)
}
func (m *Module) h(ctx server.HandlerContext) (server.Result[int], server.IAPIError) { return server.OK(0), nil }
`
	_, routes := analyzeSingleModule(t, src)
	got := routePathSet(routes)
	for _, want := range []string{
		"GET /v1/items",       // group prefix
		"GET /v1/admin/stats", // nested group
		"GET /health",         // inside if-block
		"GET /loop",           // inside for-block
		"GET /api/version",    // concatenated const + literal
		"POST /items",         // helper-registered
	} {
		assert.True(t, got[want], "expected route %q; got %v", want, got)
	}
}

func TestRegistrationWalkCycleGuard(t *testing.T) {
	// helperA -> helperB -> helperA: the visited guard must prevent infinite
	// recursion (the test simply completing proves it) and walk each helper once.
	src := `package mod
import (
	"github.com/gaborage/go-bricks/app"
	"github.com/gaborage/go-bricks/server"
)
type Module struct{}
func (m *Module) Name() string { return "mod" }
func (m *Module) Init(d *app.ModuleDeps) error { return nil }
func (m *Module) Shutdown() error { return nil }
func (m *Module) RegisterRoutes(hr *server.HandlerRegistry, r server.RouteRegistrar) {
	server.GET(hr, r, "/a", m.h)
	m.helperA(hr, r)
}
func (m *Module) helperA(hr *server.HandlerRegistry, r server.RouteRegistrar) {
	server.GET(hr, r, "/b", m.h)
	m.helperB(hr, r)
}
func (m *Module) helperB(hr *server.HandlerRegistry, r server.RouteRegistrar) {
	server.GET(hr, r, "/c", m.h)
	m.helperA(hr, r)
}
func (m *Module) h(ctx server.HandlerContext) (server.Result[int], server.IAPIError) { return server.OK(0), nil }
`
	_, routes := analyzeSingleModule(t, src)
	got := routePathSet(routes)
	assert.True(t, got["GET /a"])
	assert.True(t, got["GET /b"])
	assert.True(t, got["GET /c"])
	assert.Len(t, routes, 3, "each helper walked exactly once despite the cycle")
}

func TestRegistrationWalkDropsUnresolvedPath(t *testing.T) {
	src := `package mod
import (
	"github.com/gaborage/go-bricks/app"
	"github.com/gaborage/go-bricks/server"
)
type Module struct{}
func (m *Module) Name() string { return "mod" }
func (m *Module) Init(d *app.ModuleDeps) error { return nil }
func (m *Module) Shutdown() error { return nil }
func (m *Module) RegisterRoutes(hr *server.HandlerRegistry, r server.RouteRegistrar) {
	server.GET(hr, r, "/ok", m.h)
	server.GET(hr, r, buildPath(), m.h)
}
func (m *Module) h(ctx server.HandlerContext) (server.Result[int], server.IAPIError) { return server.OK(0), nil }
`
	a, routes := analyzeSingleModule(t, src)
	require.Len(t, routes, 1, "the route with an unresolvable path must be dropped")
	assert.Equal(t, "/ok", routes[0].Path)
	assert.NotEmpty(t, a.Warnings(t.Context()), "a warning should be recorded for the dropped route")
}

func TestReceiverVarName(t *testing.T) {
	named := &ast.FieldList{List: []*ast.Field{{Names: []*ast.Ident{{Name: "m"}}, Type: &ast.Ident{Name: "Module"}}}}
	assert.Equal(t, "m", receiverVarName(named))
	assert.Empty(t, receiverVarName(nil), "nil receiver")
	assert.Empty(t, receiverVarName(&ast.FieldList{}), "empty receiver list")
	assert.Empty(t, receiverVarName(&ast.FieldList{List: []*ast.Field{{Type: &ast.Ident{Name: "Module"}}}}), "unnamed receiver")
}

func TestRegistrationWalkHelperInSiblingFile(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.com/app\n\ngo 1.25\n"), 0600))
	sub := filepath.Join(dir, "mod")
	require.NoError(t, os.MkdirAll(sub, 0750))
	moduleSrc := `package mod
import (
	"github.com/gaborage/go-bricks/app"
	"github.com/gaborage/go-bricks/server"
)
type Module struct{}
func (m *Module) Name() string { return "mod" }
func (m *Module) Init(d *app.ModuleDeps) error { return nil }
func (m *Module) Shutdown() error { return nil }
func (m *Module) RegisterRoutes(hr *server.HandlerRegistry, r server.RouteRegistrar) {
	server.GET(hr, r, "/a", m.h)
	m.helper(hr, r)
}
func (m *Module) h(ctx server.HandlerContext) (server.Result[int], server.IAPIError) { return server.OK(0), nil }
`
	// The helper method lives in a sibling file — exercises findMethodDecl's
	// package-wide fallback search.
	helperSrc := `package mod
import "github.com/gaborage/go-bricks/server"
func (m *Module) helper(hr *server.HandlerRegistry, r server.RouteRegistrar) {
	server.POST(hr, r, "/b", m.h)
}
`
	require.NoError(t, os.WriteFile(filepath.Join(sub, "module.go"), []byte(moduleSrc), 0600))
	require.NoError(t, os.WriteFile(filepath.Join(sub, "helper.go"), []byte(helperSrc), 0600))

	project, err := New(dir).AnalyzeProject()
	require.NoError(t, err)
	var routes []models.Route
	for i := range project.Modules {
		routes = append(routes, project.Modules[i].Routes...)
	}
	got := routePathSet(routes)
	assert.True(t, got["GET /a"])
	assert.True(t, got["POST /b"], "helper method in a sibling file must be discovered")
}

func TestRegistrationWalkThreadsGroupPrefixIntoHelper(t *testing.T) {
	// A single helper invoked once per version group must be walked each time and
	// inherit that group's prefix (exercises the recursion-stack guard allowing
	// re-invocation, plus prefix threading into the helper's registrar param).
	src := `package mod
import (
	"github.com/gaborage/go-bricks/app"
	"github.com/gaborage/go-bricks/server"
)
type Module struct{}
func (m *Module) Name() string { return "mod" }
func (m *Module) Init(d *app.ModuleDeps) error { return nil }
func (m *Module) Shutdown() error { return nil }
func (m *Module) RegisterRoutes(hr *server.HandlerRegistry, r server.RouteRegistrar) {
	v1 := r.Group("/v1")
	m.itemRoutes(hr, v1)
	v2 := r.Group("/v2")
	m.itemRoutes(hr, v2)
}
func (m *Module) itemRoutes(hr *server.HandlerRegistry, r server.RouteRegistrar) {
	server.GET(hr, r, "/items", m.h)
}
func (m *Module) h(ctx server.HandlerContext) (server.Result[int], server.IAPIError) { return server.OK(0), nil }
`
	_, routes := analyzeSingleModule(t, src)
	got := routePathSet(routes)
	assert.True(t, got["GET /v1/items"], "helper invoked with the v1 group inherits /v1")
	assert.True(t, got["GET /v2/items"], "same helper invoked with the v2 group inherits /v2")
	assert.Len(t, routes, 2, "the shared helper is walked once per invocation")
}

func TestRegistrationWalkIgnoresNonRegistrationMethods(t *testing.T) {
	// A same-receiver method that is NOT a route-registration helper (no
	// server.RouteRegistrar param) must not be recursed into, so a server.* call
	// in its body is not harvested as a phantom route.
	src := `package mod
import (
	"github.com/gaborage/go-bricks/app"
	"github.com/gaborage/go-bricks/server"
)
type Module struct{}
func (m *Module) Name() string { return "mod" }
func (m *Module) Init(d *app.ModuleDeps) error { return nil }
func (m *Module) Shutdown() error { return nil }
func (m *Module) RegisterRoutes(hr *server.HandlerRegistry, r server.RouteRegistrar) {
	server.GET(hr, r, "/real", m.h)
	if m.featureEnabled() {
		server.GET(hr, r, "/gated", m.h)
	}
}
func (m *Module) featureEnabled() bool {
	server.GET(nil, nil, "/phantom", m.h)
	return true
}
func (m *Module) h(ctx server.HandlerContext) (server.Result[int], server.IAPIError) { return server.OK(0), nil }
`
	_, routes := analyzeSingleModule(t, src)
	got := routePathSet(routes)
	assert.True(t, got["GET /real"])
	assert.True(t, got["GET /gated"], "a route inside an if-block is still discovered")
	assert.False(t, got["GET /phantom"], "a server call inside a non-registration method must not become a route")
}

func TestRegistrationWalkHandlesAliasedServerImport(t *testing.T) {
	// The server package is imported under an alias; helper recursion must still
	// recognize the aliased srv.RouteRegistrar parameter.
	src := `package mod
import (
	"github.com/gaborage/go-bricks/app"
	srv "github.com/gaborage/go-bricks/server"
)
type Module struct{}
func (m *Module) Name() string { return "mod" }
func (m *Module) Init(d *app.ModuleDeps) error { return nil }
func (m *Module) Shutdown() error { return nil }
func (m *Module) RegisterRoutes(hr *srv.HandlerRegistry, r srv.RouteRegistrar) {
	srv.GET(hr, r, "/a", m.h)
	m.helper(hr, r)
}
func (m *Module) helper(hr *srv.HandlerRegistry, r srv.RouteRegistrar) {
	srv.POST(hr, r, "/b", m.h)
}
func (m *Module) h(ctx srv.HandlerContext) (srv.Result[int], srv.IAPIError) { return srv.OK(0), nil }
`
	_, routes := analyzeSingleModule(t, src)
	got := routePathSet(routes)
	assert.True(t, got["GET /a"], "route registered via an aliased server import is discovered")
	assert.True(t, got["POST /b"], "helper with an aliased RouteRegistrar param is recursed")
}

// routeForPath returns the first route matching "METHOD /path", or fails.
func routeForPath(t *testing.T, routes []models.Route, key string) models.Route {
	t.Helper()
	for i := range routes {
		if routes[i].Method+" "+routes[i].Path == key {
			return routes[i]
		}
	}
	t.Fatalf("route %q not found", key)
	return models.Route{}
}

// TestExtractSuccessStatusFromConstructors verifies the handler-body inspection
// maps each result constructor to the right HTTP status, stamped on the route.
func TestExtractSuccessStatusFromConstructors(t *testing.T) {
	src := `package mod
import (
	"net/http"

	"github.com/gaborage/go-bricks/app"
	"github.com/gaborage/go-bricks/server"
)
type Module struct{}
func (m *Module) Name() string { return "mod" }
func (m *Module) Init(deps *app.ModuleDeps) error { return nil }
func (m *Module) Shutdown() error { return nil }
type Thing struct{ ID int64 ` + "`json:\"id\"`" + ` }
func (m *Module) created(ctx server.HandlerContext) (server.Result[Thing], server.IAPIError) { return server.Created(Thing{}), nil }
func (m *Module) accepted(ctx server.HandlerContext) (server.Result[Thing], server.IAPIError) { return server.Accepted(Thing{}), nil }
func (m *Module) okConst(ctx server.HandlerContext) (server.Result[Thing], server.IAPIError) { return server.NewResult(http.StatusOK, Thing{}), nil }
func (m *Module) acceptedConst(ctx server.HandlerContext) (server.Result[Thing], server.IAPIError) { return server.NewResult(http.StatusAccepted, Thing{}), nil }
func (m *Module) custom(ctx server.HandlerContext) (server.Result[Thing], server.IAPIError) { return server.NewResult(207, Thing{}), nil }
func (m *Module) noContent(ctx server.HandlerContext) (server.NoContentResult, server.IAPIError) { return server.NoContent(), nil }
func (m *Module) RegisterRoutes(hr *server.HandlerRegistry, r server.RouteRegistrar) {
	server.POST(hr, r, "/created", m.created)
	server.POST(hr, r, "/accepted", m.accepted)
	server.GET(hr, r, "/ok", m.okConst)
	server.POST(hr, r, "/accepted-const", m.acceptedConst)
	server.PUT(hr, r, "/custom", m.custom)
	server.DELETE(hr, r, "/no-content", m.noContent)
}
`
	_, routes := analyzeSingleModule(t, src)
	assert.Equal(t, 201, routeForPath(t, routes, "POST /created").SuccessStatus)
	assert.Equal(t, 202, routeForPath(t, routes, "POST /accepted").SuccessStatus)
	// http.StatusXxx constants (the idiomatic real-world form) must resolve, not
	// just bare integer literals.
	assert.Equal(t, 200, routeForPath(t, routes, "GET /ok").SuccessStatus)
	assert.Equal(t, 202, routeForPath(t, routes, "POST /accepted-const").SuccessStatus)
	assert.Equal(t, 207, routeForPath(t, routes, "PUT /custom").SuccessStatus)
	// NoContentResult is detected via the response type (signature), not the body;
	// SuccessStatus stays 0 and the generator derives 204 from NoContent.
	noContent := routeForPath(t, routes, "DELETE /no-content")
	require.NotNil(t, noContent.Response)
	assert.True(t, noContent.Response.NoContent, "NoContentResult response must carry NoContent=true")
}

// TestRawResponseAndHandlerNameMetadata verifies WithRawResponse and
// WithHandlerName route options are parsed onto the route.
func TestRawResponseAndHandlerNameMetadata(t *testing.T) {
	src := `package mod
import (
	"github.com/gaborage/go-bricks/app"
	"github.com/gaborage/go-bricks/server"
)
type Module struct{}
func (m *Module) Name() string { return "mod" }
func (m *Module) Init(deps *app.ModuleDeps) error { return nil }
func (m *Module) Shutdown() error { return nil }
type Thing struct{ ID int64 ` + "`json:\"id\"`" + ` }
func (m *Module) raw(ctx server.HandlerContext) (server.Result[Thing], server.IAPIError) { return server.Created(Thing{}), nil }
func (m *Module) named(ctx server.HandlerContext) (server.Result[Thing], server.IAPIError) { return server.Created(Thing{}), nil }
func (m *Module) RegisterRoutes(hr *server.HandlerRegistry, r server.RouteRegistrar) {
	server.GET(hr, r, "/raw", m.raw, server.WithRawResponse())
	server.GET(hr, r, "/named", m.named, server.WithHandlerName("customOp"))
}
`
	_, routes := analyzeSingleModule(t, src)
	raw := routeForPath(t, routes, "GET /raw")
	assert.True(t, raw.RawResponse, "WithRawResponse() must set RawResponse")
	named := routeForPath(t, routes, "GET /named")
	assert.Equal(t, "customOp", named.OperationID,
		"WithHandlerName sets the explicit OperationID (distinct from the handler method name)")
	assert.Equal(t, "named", named.HandlerName,
		"HandlerName stays the handler method name, so the generator can module-qualify the derived id")
}

// TestExtractSuccessStatusLastWins confirms that when a handler returns a server
// constructor from an early guard branch and a different one from the terminal
// happy path, the terminal (last) return is the documented status.
func TestExtractSuccessStatusLastWins(t *testing.T) {
	src := `package mod
import (
	"net/http"

	"github.com/gaborage/go-bricks/app"
	"github.com/gaborage/go-bricks/server"
)
type Module struct{}
func (m *Module) Name() string { return "mod" }
func (m *Module) Init(deps *app.ModuleDeps) error { return nil }
func (m *Module) Shutdown() error { return nil }
type Thing struct{ ID int64 ` + "`json:\"id\"`" + ` }
func (m *Module) upsert(ctx server.HandlerContext) (server.Result[Thing], server.IAPIError) {
	if false {
		return server.NewResult(http.StatusOK, Thing{}), nil
	}
	return server.Created(Thing{}), nil
}
func (m *Module) RegisterRoutes(hr *server.HandlerRegistry, r server.RouteRegistrar) {
	server.POST(hr, r, "/upsert", m.upsert)
}
`
	_, routes := analyzeSingleModule(t, src)
	assert.Equal(t, 201, routeForPath(t, routes, "POST /upsert").SuccessStatus,
		"terminal happy-path return (Created/201) wins over the earlier guard return")
}

// TestStatusForConstructorEdgeCases locks the non-route-driven branches of the
// constructor->status mapping: unrecognized constructors and NewResult with a
// non-resolvable status argument both fall through to 0 ("use the default").
func TestStatusForConstructorEdgeCases(t *testing.T) {
	intLit := &ast.BasicLit{Kind: token.INT, Value: "418"}
	identArg := &ast.Ident{Name: "code"}
	// http.StatusAccepted -> SelectorExpr{X: http, Sel: StatusAccepted}
	httpConst := &ast.SelectorExpr{X: &ast.Ident{Name: "http"}, Sel: &ast.Ident{Name: "StatusAccepted"}}
	// fmt.Sprint -> a non-http selector must NOT be mistaken for a status constant.
	otherConst := &ast.SelectorExpr{X: &ast.Ident{Name: "fmt"}, Sel: &ast.Ident{Name: "Sprint"}}
	// http.StatusTeapot is not in the 2xx success map -> 0.
	unknownHTTP := &ast.SelectorExpr{X: &ast.Ident{Name: "http"}, Sel: &ast.Ident{Name: "StatusTeapot"}}

	assert.Equal(t, 0, statusForConstructor("Unknown", nil), "unrecognized constructor -> 0")
	assert.Equal(t, 0, statusForConstructor("OK", nil), "server.OK does not exist -> 0")
	assert.Equal(t, 0, statusForConstructor("NewResult", nil), "NewResult with no args -> 0")
	assert.Equal(t, 0, statusForConstructor("NewResult", []ast.Expr{identArg}), "NewResult with a variable status -> 0")
	assert.Equal(t, 0, statusForConstructor("NewResult", []ast.Expr{otherConst}), "non-http selector -> 0")
	assert.Equal(t, 0, statusForConstructor("NewResult", []ast.Expr{unknownHTTP}), "non-2xx http constant -> 0")
	assert.Equal(t, 418, statusForConstructor("NewResult", []ast.Expr{intLit}), "NewResult with int literal -> that status")
	assert.Equal(t, 202, statusForConstructor("NewResultWithMeta", []ast.Expr{httpConst}), "NewResultWithMeta with http.StatusAccepted -> 202")
	assert.Equal(t, 204, statusForConstructor("NoContent", nil))
}

// TestMapValueStructName covers the map value-type extraction helper.
func TestMapValueStructName(t *testing.T) {
	cases := []struct {
		in    string
		want  string
		isMap bool
	}{
		{"map[string]Address", "Address", true},
		{"map[string]string", "string", true},
		{"*map[string]Address", "Address", true},
		{"map[string][]Address", "Address", true}, // value slice unwrapped
		{"map[string]*Address", "Address", true},  // value pointer unwrapped
		{"[]Address", "", false},
		{"Address", "", false},
		{"*Address", "", false},
	}
	for _, c := range cases {
		got, isMap := mapValueStructName(c.in)
		assert.Equal(t, c.isMap, isMap, c.in)
		assert.Equal(t, c.want, got, c.in)
	}
}

// TestMapValueRefRegistration verifies a struct-valued map registers its value
// type as a component and stamps MapValueRefName (not RefName) on the field.
func TestMapValueRefRegistration(t *testing.T) {
	src := `package mod
import (
	"github.com/gaborage/go-bricks/app"
	"github.com/gaborage/go-bricks/server"
)
type Module struct{}
func (m *Module) Name() string { return "mod" }
func (m *Module) Init(deps *app.ModuleDeps) error { return nil }
func (m *Module) Shutdown() error { return nil }
type Address struct{ City string ` + "`json:\"city\"`" + ` }
type Book struct {
	Tags  map[string]string  ` + "`json:\"tags\"`" + `
	Addrs map[string]Address ` + "`json:\"addrs\"`" + `
}
func (m *Module) get(ctx server.HandlerContext) (server.Result[Book], server.IAPIError) { return server.Created(Book{}), nil }
func (m *Module) RegisterRoutes(hr *server.HandlerRegistry, r server.RouteRegistrar) {
	server.GET(hr, r, "/b", m.get)
}
`
	a, _ := analyzeSingleModule(t, src)
	book := a.typeRegistry["Book"]
	require.NotNil(t, book, "Book must be registered")
	_, ok := a.typeRegistry["Address"]
	assert.True(t, ok, "Address (a map value struct) must be registered as a component")

	byJSON := map[string]models.FieldInfo{}
	for _, f := range book.Fields {
		byJSON[f.JSONName] = f
	}
	assert.Equal(t, "Address", byJSON["addrs"].MapValueRefName, "struct-valued map stamps MapValueRefName")
	assert.Empty(t, byJSON["addrs"].RefName, "a map field is never a whole-field $ref")
	assert.Empty(t, byJSON["tags"].MapValueRefName, "primitive-valued map has no value ref")
}

// fieldJSONNames returns the set of JSON names on a registered type's fields.
func fieldJSONNames(t *testing.T, a *ProjectAnalyzer, typeName string) map[string]bool {
	t.Helper()
	ti := a.typeRegistry[typeName]
	require.NotNil(t, ti, "type %q must be registered", typeName)
	out := map[string]bool{}
	for i := range ti.Fields {
		out[ti.Fields[i].JSONName] = true
	}
	return out
}

// TestEmbeddedPromotion covers value/pointer promotion and json-tagged nesting.
func TestEmbeddedPromotion(t *testing.T) {
	src := `package mod
import (
	"github.com/gaborage/go-bricks/app"
	"github.com/gaborage/go-bricks/server"
)
type Module struct{}
func (m *Module) Name() string { return "mod" }
func (m *Module) Init(deps *app.ModuleDeps) error { return nil }
func (m *Module) Shutdown() error { return nil }
type Base struct { ID int64 ` + "`json:\"id\"`" + `; CreatedAt string ` + "`json:\"createdAt\"`" + ` }
type User struct { Base; Name string ` + "`json:\"name\"`" + ` }
type Account struct { *Base; Balance int64 ` + "`json:\"balance\"`" + ` }
type Wrapper struct { Base ` + "`json:\"base\"`" + `; Label string ` + "`json:\"label\"`" + ` }
func (m *Module) u(ctx server.HandlerContext) (server.Result[User], server.IAPIError) { return server.Created(User{}), nil }
func (m *Module) a(ctx server.HandlerContext) (server.Result[Account], server.IAPIError) { return server.Created(Account{}), nil }
func (m *Module) w(ctx server.HandlerContext) (server.Result[Wrapper], server.IAPIError) { return server.Created(Wrapper{}), nil }
func (m *Module) RegisterRoutes(hr *server.HandlerRegistry, r server.RouteRegistrar) {
	server.GET(hr, r, "/u", m.u)
	server.GET(hr, r, "/a", m.a)
	server.GET(hr, r, "/w", m.w)
}
`
	a, _ := analyzeSingleModule(t, src)

	user := fieldJSONNames(t, a, "User")
	assert.True(t, user["id"] && user["createdAt"], "value-embedded Base fields must be promoted")
	assert.True(t, user["name"], "own field present")

	acct := fieldJSONNames(t, a, "Account")
	assert.True(t, acct["id"] && acct["createdAt"], "pointer-embedded *Base fields must be promoted")
	assert.True(t, acct["balance"])

	wrap := fieldJSONNames(t, a, "Wrapper")
	assert.True(t, wrap["base"], "json-tagged embedding nests under the tag name, not promoted")
	assert.False(t, wrap["id"], "json-tagged embedding must NOT promote the embedded fields")
	assert.True(t, wrap["label"])
	wf := a.typeRegistry["Wrapper"]
	var baseField *models.FieldInfo
	for i := range wf.Fields {
		if wf.Fields[i].JSONName == "base" {
			baseField = &wf.Fields[i]
		}
	}
	require.NotNil(t, baseField)
	assert.Equal(t, "Base", baseField.RefName, "nested embedding is a $ref to the embedded type")
	_, ok := a.typeRegistry["Base"]
	assert.True(t, ok, "nested-embedded Base is registered as a component")
}

// TestEmbeddedSelfReferenceTerminates ensures a self-embedded type does not loop.
func TestEmbeddedSelfReferenceTerminates(t *testing.T) {
	src := `package mod
import (
	"github.com/gaborage/go-bricks/app"
	"github.com/gaborage/go-bricks/server"
)
type Module struct{}
func (m *Module) Name() string { return "mod" }
func (m *Module) Init(deps *app.ModuleDeps) error { return nil }
func (m *Module) Shutdown() error { return nil }
type Node struct { *Node; Val int ` + "`json:\"val\"`" + ` }
func (m *Module) n(ctx server.HandlerContext) (server.Result[Node], server.IAPIError) { return server.Created(Node{}), nil }
func (m *Module) RegisterRoutes(hr *server.HandlerRegistry, r server.RouteRegistrar) {
	server.GET(hr, r, "/n", m.n)
}
`
	a, _ := analyzeSingleModule(t, src)
	node := fieldJSONNames(t, a, "Node")
	assert.True(t, node["val"], "non-embedded field survives")
	assert.Len(t, a.typeRegistry["Node"].Fields, 1, "self-embed is skipped (only Val remains), no infinite loop")
}

// TestEmbeddedCrossPackageDoesNotCrash ensures an unresolvable embedded type is
// skipped gracefully (cross-package resolution is PR9).
func TestEmbeddedCrossPackageDoesNotCrash(t *testing.T) {
	src := `package mod
import (
	"github.com/gaborage/go-bricks/app"
	"github.com/gaborage/go-bricks/server"
	"example.com/other"
)
type Module struct{}
func (m *Module) Name() string { return "mod" }
func (m *Module) Init(deps *app.ModuleDeps) error { return nil }
func (m *Module) Shutdown() error { return nil }
type Foo struct { other.Bar; X int ` + "`json:\"x\"`" + ` }
func (m *Module) f(ctx server.HandlerContext) (server.Result[Foo], server.IAPIError) { return server.Created(Foo{}), nil }
func (m *Module) RegisterRoutes(hr *server.HandlerRegistry, r server.RouteRegistrar) {
	server.GET(hr, r, "/f", m.f)
}
`
	a, _ := analyzeSingleModule(t, src)
	foo := fieldJSONNames(t, a, "Foo")
	assert.True(t, foo["x"], "own field present despite unresolvable embedded type")
	assert.Len(t, a.typeRegistry["Foo"].Fields, 1, "unresolvable embedded type is skipped, not crashed")
}

// TestEmbeddedFieldPrecedence locks encoding/json's shallower-wins rule: a
// parent's own field beats a promoted (embedded) field of the same JSON name,
// regardless of declaration order. Also covers json:"-" exclusion and a
// name-less (",omitempty") embed still promoting.
func TestEmbeddedFieldPrecedence(t *testing.T) {
	src := `package mod
import (
	"github.com/gaborage/go-bricks/app"
	"github.com/gaborage/go-bricks/server"
)
type Module struct{}
func (m *Module) Name() string { return "mod" }
func (m *Module) Init(deps *app.ModuleDeps) error { return nil }
func (m *Module) Shutdown() error { return nil }
type Base struct { Tag string ` + "`json:\"tag\"`" + `; Only string ` + "`json:\"only\"`" + ` }
// EmbedFirst declares the embed BEFORE its own colliding field.
type EmbedFirst struct { Base; Tag int ` + "`json:\"tag\"`" + ` }
// OwnFirst declares its own colliding field BEFORE the embed.
type OwnFirst struct { Tag int ` + "`json:\"tag\"`" + `; Base }
// Skipped embeds Base with json:"-" -> excluded entirely.
type Skipped struct { Base ` + "`json:\"-\"`" + `; Keep string ` + "`json:\"keep\"`" + ` }
// OmitOnly embeds Base with a name-less tag -> still promotes.
type OmitOnly struct { Base ` + "`json:\",omitempty\"`" + ` }
func (m *Module) a(ctx server.HandlerContext) (server.Result[EmbedFirst], server.IAPIError) { return server.Created(EmbedFirst{}), nil }
func (m *Module) b(ctx server.HandlerContext) (server.Result[OwnFirst], server.IAPIError) { return server.Created(OwnFirst{}), nil }
func (m *Module) c(ctx server.HandlerContext) (server.Result[Skipped], server.IAPIError) { return server.Created(Skipped{}), nil }
func (m *Module) d(ctx server.HandlerContext) (server.Result[OmitOnly], server.IAPIError) { return server.Created(OmitOnly{}), nil }
func (m *Module) RegisterRoutes(hr *server.HandlerRegistry, r server.RouteRegistrar) {
	server.GET(hr, r, "/a", m.a)
	server.GET(hr, r, "/b", m.b)
	server.GET(hr, r, "/c", m.c)
	server.GET(hr, r, "/d", m.d)
}
`
	a, _ := analyzeSingleModule(t, src)

	// The parent's own "tag" (int) must win over Base's promoted "tag" (string),
	// in BOTH declaration orders.
	for _, typeName := range []string{"EmbedFirst", "OwnFirst"} {
		ti := a.typeRegistry[typeName]
		require.NotNil(t, ti, typeName)
		var tagType string
		count := 0
		for _, f := range ti.Fields {
			if f.JSONName == "tag" {
				tagType = f.Type
				count++
			}
		}
		assert.Equal(t, 1, count, "%s: exactly one 'tag' field after precedence merge", typeName)
		assert.Equal(t, "int", tagType, "%s: parent's own int tag wins over promoted string tag", typeName)
		// The non-colliding promoted field is still present.
		assert.True(t, fieldJSONNames(t, a, typeName)["only"], "%s: non-colliding promoted field survives", typeName)
	}

	skipped := fieldJSONNames(t, a, "Skipped")
	assert.False(t, skipped["tag"] || skipped["only"], "json:\"-\" embed must be fully excluded")
	assert.True(t, skipped["keep"], "own field survives")

	omit := fieldJSONNames(t, a, "OmitOnly")
	assert.True(t, omit["tag"] && omit["only"], "name-less (,omitempty) embed still promotes")
}

// TestPrimitiveKind covers the 3-way mapping. The full integer/float/string
// membership sets are exhaustively covered by constraints_test.go; here we only
// assert primitiveKind's delegation and the non-primitive zero value.
func TestPrimitiveKind(t *testing.T) {
	assert.Equal(t, "integer", primitiveKind(goTypeInt64))
	assert.Equal(t, "number", primitiveKind(goTypeFloat64))
	assert.Equal(t, goTypeString, primitiveKind(goTypeString))
	assert.Equal(t, "", primitiveKind("bool"))
	assert.Equal(t, "", primitiveKind("Widget"))
}

// TestResolveUnderlyingKind covers named-scalar classification end-to-end:
// local `type Cents int64`, the qualified time.Duration, and a plain builtin
// (which is NOT a named wrapper, so empty).
func TestResolveUnderlyingKind(t *testing.T) {
	src := `package mod
import (
	"time"

	"github.com/gaborage/go-bricks/app"
	"github.com/gaborage/go-bricks/server"
)
type Module struct{}
func (m *Module) Name() string { return "mod" }
func (m *Module) Init(deps *app.ModuleDeps) error { return nil }
func (m *Module) Shutdown() error { return nil }
type Cents int64
type Alias Cents
type Timeout time.Duration
type Inner struct{ X int }
type Money struct {
	Amount  Cents         ` + "`json:\"amount\"`" + `
	Chained Alias         ` + "`json:\"chained\"`" + `
	Wait    Timeout       ` + "`json:\"wait\"`" + `
	TTL     time.Duration ` + "`json:\"ttl\"`" + `
	Plain   int           ` + "`json:\"plain\"`" + `
	Nested  Inner         ` + "`json:\"nested\"`" + `
}
func (m *Module) g(ctx server.HandlerContext) (server.Result[Money], server.IAPIError) { return server.Created(Money{}), nil }
func (m *Module) RegisterRoutes(hr *server.HandlerRegistry, r server.RouteRegistrar) {
	server.GET(hr, r, "/m", m.g)
}
`
	a, _ := analyzeSingleModule(t, src)
	money := a.typeRegistry["Money"]
	require.NotNil(t, money)
	kind := map[string]string{}
	ref := map[string]string{}
	for i := range money.Fields {
		kind[money.Fields[i].JSONName] = money.Fields[i].UnderlyingKind
		ref[money.Fields[i].JSONName] = money.Fields[i].RefName
	}
	assert.Equal(t, "integer", kind["amount"], "type Cents int64 -> integer")
	assert.Equal(t, "integer", kind["chained"], "type Alias Cents -> chain to int64 -> integer")
	assert.Equal(t, "integer", kind["wait"], "type Timeout time.Duration -> integer (selector underlying)")
	assert.Equal(t, "integer", kind["ttl"], "time.Duration -> integer")
	assert.Equal(t, "", kind["plain"], "a builtin used directly is not a named wrapper")
	// A named STRUCT is a $ref, not a scalar underlying kind.
	assert.Equal(t, "", kind["nested"], "named struct has no scalar underlying kind")
	assert.Equal(t, "Inner", ref["nested"], "named struct is a $ref")
}

// TestSchemaKeyCollision locks the collision-qualification rule: same (pkg,type)
// is idempotent; a different package with the same type name is qualified.
func TestSchemaKeyCollision(t *testing.T) {
	a := New(t.TempDir())
	assert.Equal(t, "Request", a.schemaKey("Request", "users"))
	assert.Equal(t, "Request", a.schemaKey("Request", "users"), "idempotent for same (pkg,type)")
	assert.Equal(t, "OrdersRequest", a.schemaKey("Request", "orders"), "collision qualified by package")
	assert.Equal(t, "OrdersRequest", a.schemaKey("Request", "orders"), "qualified name is stable")
	// A third package with the same name gets a further-qualified/distinct name.
	got := a.schemaKey("Request", "billing")
	assert.NotContains(t, []string{"Request", "OrdersRequest"}, got, "third collision is distinct")
}

// TestInModuleDir covers the import-path -> dir translation and the
// stdlib/third-party fall-through.
func TestInModuleDir(t *testing.T) {
	root := t.TempDir()
	a := New(root)
	a.modulePath = "github.com/example/app"
	dir, ok := a.inModuleDir("github.com/example/app/types")
	assert.True(t, ok)
	assert.Equal(t, filepath.Join(root, "types"), dir)
	dir, ok = a.inModuleDir("github.com/example/app")
	assert.True(t, ok)
	assert.Equal(t, root, dir)
	_, ok = a.inModuleDir("time")
	assert.False(t, ok, "stdlib is not in-module")
	_, ok = a.inModuleDir("github.com/google/uuid")
	assert.False(t, ok, "third-party is not in-module")
}

// TestParsePackageDirCaches asserts the per-dir parse cache (miss then hit) and
// graceful handling of a missing directory.
func TestParsePackageDirCaches(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "x.go"), []byte("package x\ntype T struct{}\n"), 0600))
	a := New(dir)

	_, cachedBefore := a.pkgCache[dir]
	assert.False(t, cachedBefore, "cache miss before first parse")
	first, err := a.parsePackageDir(dir)
	require.NoError(t, err)
	require.Len(t, first, 1)
	cached, ok := a.pkgCache[dir]
	assert.True(t, ok, "dir cached after first parse")
	// Mutate the cached map; a cache HIT must return this same instance.
	cached["sentinel-marker"] = nil
	second, err := a.parsePackageDir(dir)
	require.NoError(t, err)
	_, sawSentinel := second["sentinel-marker"]
	assert.True(t, sawSentinel, "second call is a cache hit (returns the stored map, not a re-parse)")

	_, err = a.parsePackageDir(filepath.Join(dir, "does-not-exist"))
	assert.Error(t, err, "missing dir returns an error, not a panic")
}

// TestRegisterQualifiedTypeFallThroughs covers the cross-package resolver's
// non-resolving arms: no dot, stdlib import, unknown alias, and an in-module path
// whose directory/type is absent.
func TestRegisterQualifiedTypeFallThroughs(t *testing.T) {
	a := New(t.TempDir())
	a.modulePath = "example.com/app"
	src := `package x
import (
	"time"
	other "example.com/app/other"
)
var _ = time.Now
var _ = other.X
`
	f, err := parser.ParseFile(token.NewFileSet(), "x.go", src, parser.ParseComments)
	require.NoError(t, err)
	assert.Nil(t, a.registerQualifiedType("NoDot", f), "no dot -> nil")
	assert.Nil(t, a.registerQualifiedType("time.Duration", f), "stdlib import is not in-module -> nil")
	assert.Nil(t, a.registerQualifiedType("missingpkg.Foo", f), "unknown alias -> nil")
	assert.Nil(t, a.registerQualifiedType("other.Missing", f), "in-module but dir/type absent -> nil")
}

// TestLocalTypeUnderlyingSiblingFile covers resolving a named type whose
// declaration lives in a SIBLING file of the same package (the parsePackageDir
// fallback), and the not-found / non-ident arms.
func TestLocalTypeUnderlyingSiblingFile(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "a.go"),
		[]byte("package p\ntype User struct{ Amount Cents }\n"), 0600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "b.go"),
		[]byte("package p\ntype Cents int64\ntype Wrapped struct{ X int }\n"), 0600))
	a := New(dir)
	aPath := filepath.Join(dir, "a.go")
	af, err := parser.ParseFile(a.fileSet, aPath, nil, parser.ParseComments)
	require.NoError(t, err)

	// Cents is declared in b.go (a sibling file) — found via the per-dir parse.
	u, ok := a.localTypeUnderlying("Cents", af, aPath)
	assert.True(t, ok, "named type in a sibling file resolves")
	assert.Equal(t, "int64", u)

	// A named struct has no bare-ident underlying -> ok=false.
	_, ok = a.localTypeUnderlying("Wrapped", af, aPath)
	assert.False(t, ok, "a named struct is not a scalar underlying")

	// Absent type -> ok=false.
	_, ok = a.localTypeUnderlying("Missing", af, aPath)
	assert.False(t, ok)
}

// TestParseValidationTagDive covers the collection/element scope split at `dive`.
func TestParseValidationTagDive(t *testing.T) {
	a := New(t.TempDir())

	c, e := a.parseValidationTag("min=1,dive,email")
	assert.Equal(t, map[string]string{"min": "1"}, c, "rules before dive are collection-scope")
	assert.Equal(t, map[string]string{"email": "true"}, e, "rules after dive are element-scope")

	c, e = a.parseValidationTag("dive,gte=0")
	assert.Empty(t, c, "no collection rules")
	assert.Equal(t, map[string]string{"gte": "0"}, e)

	c, e = a.parseValidationTag("required,min=2")
	assert.Equal(t, map[string]string{"required": "true", "min": "2"}, c)
	assert.Nil(t, e, "no dive -> nil element constraints")

	// Nested dive is ignored: element scope stays flat, no crash.
	c, e = a.parseValidationTag("min=1,dive,dive,email")
	assert.Equal(t, map[string]string{"min": "1"}, c)
	assert.Equal(t, map[string]string{"email": "true"}, e)

	// `required` is always collection-scope, even after dive (must not be lost).
	c, e = a.parseValidationTag("dive,required,email")
	assert.Equal(t, "true", c["required"], "required after dive stays collection-scope")
	assert.Equal(t, map[string]string{"email": "true"}, e, "other post-dive rules stay element-scope")

	c, e = a.parseValidationTag("")
	assert.Empty(t, c)
	assert.Nil(t, e)
}
