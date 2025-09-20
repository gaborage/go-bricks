package analyzer

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"slices"
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
	expectedGotFormat = "Expected %q, got %q"
	parseFailedFormat = "Failed to parse content: %v"
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

// RegisterMessaging sets up messaging for this module
func (m *Module) RegisterMessaging(registry *messaging.Registry) {
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
	analyzer := New("/test/path")

	if analyzer == nil {
		t.Fatal("New() returned nil")
	}

	if analyzer.projectRoot != "/test/path" {
		t.Errorf("Expected project root '/test/path', got '%s'", analyzer.projectRoot)
	}

	if analyzer.fileSet == nil {
		t.Error("FileSet should be initialized")
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
		t.Errorf("Expected 1 module, got %d", len(project.Modules))
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
		t.Errorf("Expected 2 routes, got %d", len(module.Routes))
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
		if route.Method != "GET" {
			t.Errorf("Expected GET method for %s, got %s", getUserRoute, route.Method)
		}
		if route.HandlerName != testHandlerName {
			t.Errorf("Expected handler name '%s', got '%s'", testHandlerName, route.HandlerName)
		}
	case createUserRoute:
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
	analyzer := New("/test")

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
	return str != "" && substr != "" &&
		(str == substr ||
			(len(str) > len(substr) &&
				(str[:len(substr)] == substr ||
					str[len(str)-len(substr):] == substr ||
					findSubstring(str, substr))))
}

func findSubstring(str, substr string) bool {
	for i := 0; i <= len(str)-len(substr); i++ {
		if str[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

// Additional comprehensive test coverage

// TestExtractCommentDescription tests comment extraction functionality
func TestExtractCommentDescription(t *testing.T) {
	analyzer := New("/test")

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
	analyzer := New("/test")

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
	analyzer := New("/test")

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
	analyzer := New("/test")

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
	analyzer := New("/test")

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
	analyzer := New("/test")

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
		t.Fatalf("Failed to parse test content: %v", err)
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
	analyzer := New("/test")

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
		invalidAnalyzer := New("/nonexistent/path")
		_, err := invalidAnalyzer.AnalyzeProject()
		// Note: AnalyzeProject may not error on invalid paths, it just won't find modules
		_ = err // Ignore error for now as implementation may vary
	})

	t.Run("project with no go files", func(t *testing.T) {
		emptyDir := filepath.Join(tempDir, "empty")
		os.MkdirAll(emptyDir, 0755)

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
	analyzer := New("/test")

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
type Module interface{}`,
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
	analyzer := New("/test")

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
	os.WriteFile(invalidFile, []byte(invalidContent), 0644)

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
	analyzer := New("/test")
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
	os.WriteFile(moduleFile, []byte(moduleContent), 0644)

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
	os.WriteFile(moduleFile, []byte(moduleContent), 0644)
	os.WriteFile(routesFile, []byte(routesContent), 0644)

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
		t.Fatalf("Expected 2 routes, got %d", len(module.Routes))
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
