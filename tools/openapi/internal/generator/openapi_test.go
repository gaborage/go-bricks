package generator

import (
	"strings"
	"testing"

	"github.com/gaborage/go-bricks/tools/openapi/internal/models"
	"gopkg.in/yaml.v3"
)

const (
	defaultTitle              = "Test API"
	defaultVersion            = "1.0.0"
	defaultDescription        = "Test description"
	usersAPIPath              = "/users"
	usersIDAPIPath            = "/users/:id"
	listAllUsersSummary       = "List all users"
	generateFailedErrorMsg    = "Generate() failed: %v"
	resultNotExpectedErrorMsg = "Expected %q, got %q"
)

// OpenAPISpec represents the structure of an OpenAPI specification for validation
type OpenAPISpec struct {
	OpenAPI    string         `yaml:"openapi"`
	Info       OpenAPIInfo    `yaml:"info"`
	Paths      map[string]any `yaml:"paths"`
	Components map[string]any `yaml:"components"`
}

// validateBasicOpenAPIStructure parses and validates basic OpenAPI structure
func validateBasicOpenAPIStructure(t *testing.T, spec, expectedTitle, expectedVersion string) OpenAPISpec {
	t.Helper()

	var parsed OpenAPISpec
	err := yaml.Unmarshal([]byte(spec), &parsed)
	if err != nil {
		t.Fatalf("Failed to parse generated YAML: %v", err)
	}

	// Validate basic structure
	if parsed.OpenAPI != "3.0.1" {
		t.Errorf("Expected OpenAPI version '3.0.1', got '%s'", parsed.OpenAPI)
	}

	if parsed.Info.Title != expectedTitle {
		t.Errorf("Expected title '%s', got '%s'", expectedTitle, parsed.Info.Title)
	}

	if parsed.Info.Version != expectedVersion {
		t.Errorf("Expected version '%s', got '%s'", expectedVersion, parsed.Info.Version)
	}

	if parsed.Paths == nil {
		t.Error("Missing paths section")
	}

	if parsed.Components == nil {
		t.Error("Missing components section")
	}

	return parsed
}

func TestNew(t *testing.T) {
	gen := New(defaultTitle, defaultVersion, defaultDescription)

	if gen == nil {
		t.Fatal("New() returned nil")
	}

	if gen.title != defaultTitle {
		t.Errorf("Expected title 'Test API', got '%s'", gen.title)
	}
	if gen.version != "1.0.0" {
		t.Errorf("Expected version '1.0.0', got '%s'", gen.version)
	}
	if gen.description != defaultDescription {
		t.Errorf("Expected description 'Test description', got '%s'", gen.description)
	}
}

func TestGenerateEmptyProject(t *testing.T) {
	gen := New(defaultTitle, "1.0.0", defaultDescription)
	project := &models.Project{}

	spec, err := gen.Generate(project)

	if err != nil {
		t.Fatalf(generateFailedErrorMsg, err)
	}

	// Validate basic OpenAPI structure using YAML parsing
	parsed := validateBasicOpenAPIStructure(t, spec, defaultTitle, defaultVersion)

	// For empty project, paths should be empty
	if len(parsed.Paths) != 0 {
		t.Errorf("Expected empty paths for no routes, got %d paths", len(parsed.Paths))
	}
}

func TestGenerateWithProjectMetadata(t *testing.T) {
	gen := New("Default API", "0.1.0", "Default description")
	project := &models.Project{
		Name:        "Custom API",
		Version:     "2.0.0",
		Description: "Custom description",
		Modules:     []models.Module{},
	}

	spec, err := gen.Generate(project)

	if err != nil {
		t.Fatalf(generateFailedErrorMsg, err)
	}

	// Validate that project metadata is used over defaults
	parsed := validateBasicOpenAPIStructure(t, spec, "Custom API", "2.0.0")

	// Validate custom description
	if parsed.Info.Description != "Custom description" {
		t.Errorf("Expected description 'Custom description', got '%s'", parsed.Info.Description)
	}
}

func TestGenerateWithRoutes(t *testing.T) {
	gen := New(defaultTitle, "1.0.0", defaultDescription)
	project := &models.Project{
		Modules: []models.Module{
			{
				Name: "users",
				Routes: []models.Route{
					{
						Method:      "GET",
						Path:        usersIDAPIPath,
						HandlerName: "getUser",
						Summary:     "Get user by ID",
						Description: "Retrieves a user by their unique identifier",
						Tags:        []string{"users"},
					},
					{
						Method: "POST",
						Path:   usersAPIPath,
						Tags:   []string{"users", "creation"},
					},
				},
			},
		},
	}

	spec, err := gen.Generate(project)

	if err != nil {
		t.Fatalf(generateFailedErrorMsg, err)
	}

	// Check routes are present
	if !strings.Contains(spec, "/users/:id:") {
		t.Error("Missing GET /users/:id route")
	}
	if !strings.Contains(spec, "/users:") {
		t.Error("Missing POST /users route")
	}
	if !strings.Contains(spec, "get:") {
		t.Error("Missing GET method")
	}
	if !strings.Contains(spec, "post:") {
		t.Error("Missing POST method")
	}

	// Check operation details
	if !strings.Contains(spec, "operationId: getUser") {
		t.Error("Missing operation ID")
	}
	if !strings.Contains(spec, "summary: Get user by ID") {
		t.Error("Missing summary")
	}
	if !strings.Contains(spec, "description: Retrieves a user") {
		t.Error("Missing description")
	}
	if !strings.Contains(spec, "- users") {
		t.Error("Missing tags")
	}

	// Check standard responses
	if !strings.Contains(spec, "'200':") {
		t.Error("Missing 200 response")
	}
	if !strings.Contains(spec, "'400':") {
		t.Error("Missing 400 response")
	}
	if !strings.Contains(spec, "SuccessResponse") {
		t.Error("Missing success response schema")
	}
	if !strings.Contains(spec, "ErrorResponse") {
		t.Error("Missing error response schema")
	}
}

func TestGetOperationID(t *testing.T) {
	gen := New(defaultTitle, "1.0.0", defaultDescription)

	tests := []struct {
		name     string
		route    *models.Route
		expected string
	}{
		{
			name: "with handler name",
			route: &models.Route{
				Method:      "GET",
				Path:        usersIDAPIPath,
				HandlerName: "getUser",
			},
			expected: "getUser",
		},
		{
			name: "without handler name",
			route: &models.Route{
				Method: "POST",
				Path:   usersAPIPath,
			},
			expected: "post_users",
		},
		{
			name: "complex path",
			route: &models.Route{
				Method: "GET",
				Path:   "/users/:id/posts/:postId",
			},
			expected: "get_users_id_posts_postId",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := gen.getOperationID(tt.route)
			if result != tt.expected {
				t.Errorf(resultNotExpectedErrorMsg, tt.expected, result)
			}
		})
	}
}

func TestGetSummary(t *testing.T) {
	gen := New(defaultTitle, "1.0.0", defaultDescription)

	tests := []struct {
		name     string
		route    *models.Route
		expected string
	}{
		{
			name: "with summary",
			route: &models.Route{
				Method:  "GET",
				Path:    usersAPIPath,
				Summary: listAllUsersSummary,
			},
			expected: listAllUsersSummary,
		},
		{
			name: "without summary",
			route: &models.Route{
				Method: "POST",
				Path:   usersAPIPath,
			},
			expected: "POST /users",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := gen.getSummary(tt.route)
			if result != tt.expected {
				t.Errorf(resultNotExpectedErrorMsg, tt.expected, result)
			}
		})
	}
}

func TestGetResponseDescription(t *testing.T) {
	gen := New(defaultTitle, "1.0.0", defaultDescription)

	tests := []struct {
		method   string
		expected string
	}{
		{"GET", "Successful response"},
		{"POST", "Resource created successfully"},
		{"PUT", "Resource updated successfully"},
		{"DELETE", "Resource deleted successfully"},
		{"PATCH", "Resource partially updated"},
		{"UNKNOWN", "Successful response"},
	}

	for _, tt := range tests {
		t.Run(tt.method, func(t *testing.T) {
			result := gen.getResponseDescription(tt.method)
			if result != tt.expected {
				t.Errorf(resultNotExpectedErrorMsg, tt.expected, result)
			}
		})
	}
}

func TestGetAllRoutes(t *testing.T) {
	gen := New(defaultTitle, "1.0.0", defaultDescription)

	project := &models.Project{
		Modules: []models.Module{
			{
				Name: "users",
				Routes: []models.Route{
					{Method: "GET", Path: usersAPIPath},
					{Method: "POST", Path: usersAPIPath},
				},
			},
			{
				Name: "posts",
				Routes: []models.Route{
					{Method: "GET", Path: "/posts"},
				},
			},
		},
	}

	routes := gen.getAllRoutes(project)

	if len(routes) != 3 {
		t.Errorf("Expected 3 routes, got %d", len(routes))
	}

	// Check that all routes are included
	methods := make(map[string]bool)
	for _, route := range routes {
		key := route.Method + " " + route.Path
		methods[key] = true
	}

	expected := []string{"GET /users", "POST /users", "GET /posts"}
	for _, exp := range expected {
		if !methods[exp] {
			t.Errorf("Missing route: %s", exp)
		}
	}
}

// validatePathExists checks if a path exists in the parsed OpenAPI spec
func validatePathExists(t *testing.T, parsed *OpenAPISpec, path string) {
	t.Helper()
	if _, exists := parsed.Paths[path]; !exists {
		t.Errorf("Missing %s path", path)
	}
}

// validatePathMethods checks if expected HTTP methods exist for a given path
func validatePathMethods(t *testing.T, parsed *OpenAPISpec, path string, expectedMethods []string) {
	t.Helper()
	pathMethods, ok := parsed.Paths[path].(map[string]any)
	if !ok {
		t.Errorf("Path %s should be a map of methods", path)
		return
	}

	for _, method := range expectedMethods {
		if _, hasMethod := pathMethods[method]; !hasMethod {
			t.Errorf("Missing %s method for %s path", method, path)
		}
	}
}

// createTestProjectWithMultipleMethods creates a test project with multiple HTTP methods per path
func createTestProjectWithMultipleMethods() *models.Project {
	return &models.Project{
		Modules: []models.Module{
			{
				Name: "users",
				Routes: []models.Route{
					{
						Method:      "GET",
						Path:        usersAPIPath,
						HandlerName: "listUsers",
						Summary:     listAllUsersSummary,
						Tags:        []string{"users"},
					},
					{
						Method:      "POST",
						Path:        usersAPIPath,
						HandlerName: "createUser",
						Summary:     "Create a new user",
						Tags:        []string{"users", "creation"},
					},
					{
						Method:      "GET",
						Path:        usersIDAPIPath,
						HandlerName: "getUser",
						Summary:     "Get user by ID",
						Tags:        []string{"users"},
					},
					{
						Method:      "PUT",
						Path:        usersIDAPIPath,
						HandlerName: "updateUser",
						Summary:     "Update user",
						Tags:        []string{"users"},
					},
				},
			},
		},
	}
}

// TestGenerateWithMultipleMethodsPerPath verifies proper path grouping and no duplicate path keys
func TestGenerateWithMultipleMethodsPerPath(t *testing.T) {
	gen := New(defaultTitle, "1.0.0", defaultDescription)
	project := createTestProjectWithMultipleMethods()

	spec, err := gen.Generate(project)
	if err != nil {
		t.Fatalf(generateFailedErrorMsg, err)
	}

	// Parse and validate the structure
	parsed := validateBasicOpenAPIStructure(t, spec, defaultTitle, "1.0.0")

	// Verify we have exactly 2 paths (not 4 duplicate paths)
	if len(parsed.Paths) != 2 {
		t.Errorf("Expected 2 unique paths, got %d", len(parsed.Paths))
	}

	// Verify the paths contain the expected keys
	validatePathExists(t, &parsed, usersAPIPath)
	validatePathExists(t, &parsed, usersIDAPIPath)

	// Verify each path has the expected HTTP methods
	validatePathMethods(t, &parsed, usersAPIPath, []string{"get", "post"})
	validatePathMethods(t, &parsed, usersIDAPIPath, []string{"get", "put"})

	// Verify no duplicate path keys by checking the raw YAML doesn't have repeated path declarations
	pathOccurrences := strings.Count(spec, usersAPIPath+":")
	if pathOccurrences != 1 {
		t.Errorf("Path %s should appear exactly once as a key, found %d occurrences", usersAPIPath, pathOccurrences)
	}
}

// TestGenerateWithProblematicValues verifies proper YAML escaping for special characters
func TestGenerateWithProblematicValues(t *testing.T) {
	// Test with values that could break manual YAML concatenation
	gen := New(
		"API: Special Characters Test", // Contains colon
		"1.0.0-beta: yes",              // Contains colon and YAML boolean
		"Multi-line description\nWith: colons and\n\"quotes\" and #comments",
	)

	project := &models.Project{
		Name:        "Project: With Colons & Special \"Characters\"", // Problematic title
		Version:     "true",                                          // YAML boolean
		Description: "Description with:\n- YAML list syntax\n- More items\n# And comments",
	}

	spec, err := gen.Generate(project)
	if err != nil {
		t.Fatalf(generateFailedErrorMsg, err)
	}

	// Parse the generated YAML to ensure it's valid
	parsed := validateBasicOpenAPIStructure(t, spec, project.Name, project.Version)

	// Verify the problematic values were properly handled
	if parsed.Info.Title != project.Name {
		t.Errorf("Title with special characters not preserved: expected %q, got %q",
			project.Name, parsed.Info.Title)
	}

	if parsed.Info.Version != project.Version {
		t.Errorf("Version that looks like boolean not preserved: expected %q, got %q",
			project.Version, parsed.Info.Version)
	}

	if parsed.Info.Description != project.Description {
		t.Errorf("Multiline description not preserved: expected %q, got %q",
			project.Description, parsed.Info.Description)
	}

	// Verify the YAML doesn't contain unescaped problematic patterns
	if strings.Contains(spec, "Project: With Colons & Special \"Characters\"\n") {
		t.Error("Title with special characters should be properly quoted/escaped")
	}

	// Verify the spec is still valid YAML by parsing it
	var yamlCheck map[string]any
	err = yaml.Unmarshal([]byte(spec), &yamlCheck)
	if err != nil {
		t.Fatalf("Generated YAML is invalid: %v", err)
	}
}

// Test that the generated YAML is valid and parseable
func TestGenerateValidYAML(t *testing.T) {
	gen := New(defaultTitle, "1.0.0", defaultDescription)
	project := &models.Project{
		Modules: []models.Module{
			{
				Name: "test",
				Routes: []models.Route{
					{
						Method:      "GET",
						Path:        "/test",
						HandlerName: "testHandler",
						Summary:     "Test endpoint",
						Tags:        []string{"test"},
					},
				},
			},
		},
	}

	spec, err := gen.Generate(project)

	if err != nil {
		t.Fatalf(generateFailedErrorMsg, err)
	}

	// Basic YAML structure validation
	lines := strings.Split(spec, "\n")
	if len(lines) < 10 {
		t.Error("Generated spec seems too short")
	}

	// Check for proper YAML indentation (basic check)
	for i, line := range lines {
		if strings.TrimSpace(line) == "" {
			continue
		}
		if strings.HasPrefix(line, "\t") {
			t.Errorf("Line %d uses tabs instead of spaces: %q", i+1, line)
		}
	}
}
