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
	yamlParsingFailedMsg      = "Failed to parse generated YAML: %v"
	getUserByIDSummary        = "Get user by ID"
	createNewUserSummary      = "Create a new user"
	expectedTypeErrorMsg      = "Expected type %q, got %q"
	expectedFormatErrorMsg    = "Expected format %q, got %q"
	pageNumberDescription     = "Page number"
	parametersHeader          = "parameters:"
	IDHeader                  = "- name: id"
	requestBodyHeader         = "requestBody:"
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
		t.Fatalf(yamlParsingFailedMsg, err)
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
						Summary:     getUserByIDSummary,
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
						Summary:     createNewUserSummary,
						Tags:        []string{"users", "creation"},
					},
					{
						Method:      "GET",
						Path:        usersIDAPIPath,
						HandlerName: "getUser",
						Summary:     getUserByIDSummary,
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

func TestGenerateNilProject(t *testing.T) {
	gen := New(defaultTitle, "1.0.0", defaultDescription)

	// Test with nil project to cover the nil check branch
	spec, err := gen.Generate(nil)
	if err != nil {
		t.Fatalf("Generate() with nil project failed: %v", err)
	}

	// Should still produce valid spec with defaults
	parsed := validateBasicOpenAPIStructure(t, spec, defaultTitle, "1.0.0")

	// Should have empty paths
	if len(parsed.Paths) != 0 {
		t.Errorf("Expected empty paths for nil project, got %d paths", len(parsed.Paths))
	}
}

func TestMarshalYAMLSectionErrorCases(t *testing.T) {
	gen := New(defaultTitle, "1.0.0", defaultDescription)

	// Test marshaling of complex nested structures to ensure proper error handling
	complexData := map[string]any{
		"nested": map[string]any{
			"level1": map[string]any{
				"level2": "value",
			},
		},
	}

	result, err := gen.marshalYAMLSection("test", complexData)
	if err != nil {
		t.Errorf("marshalYAMLSection should handle complex data, got error: %v", err)
	}

	if !strings.Contains(result, "test:") {
		t.Error("Result should contain section name")
	}
	if !strings.Contains(result, "nested:") {
		t.Error("Result should contain nested structure")
	}
}

func TestGettersWithEmptyValues(t *testing.T) {
	gen := New("", "", "")

	tests := []struct {
		name     string
		project  *models.Project
		testFunc func(*models.Project) string
		expected string
	}{
		{
			name:     "empty title with empty project",
			project:  &models.Project{},
			testFunc: func(p *models.Project) string { return gen.getTitle(p) },
			expected: "", // Empty title from generator should be preserved
		},
		{
			name:     "empty version with empty project",
			project:  &models.Project{},
			testFunc: func(p *models.Project) string { return gen.getVersion(p) },
			expected: "", // Empty version from generator should be preserved
		},
		{
			name:     "empty description with empty project",
			project:  &models.Project{},
			testFunc: func(p *models.Project) string { return gen.getDescription(p) },
			expected: "", // Empty description from generator should be preserved
		},
		{
			name:     "project overrides empty generator title",
			project:  &models.Project{Name: "Project Title"},
			testFunc: func(p *models.Project) string { return gen.getTitle(p) },
			expected: "Project Title",
		},
		{
			name:     "project overrides empty generator version",
			project:  &models.Project{Version: "2.0.0"},
			testFunc: func(p *models.Project) string { return gen.getVersion(p) },
			expected: "2.0.0",
		},
		{
			name:     "project overrides empty generator description",
			project:  &models.Project{Description: "Project Description"},
			testFunc: func(p *models.Project) string { return gen.getDescription(p) },
			expected: "Project Description",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := tt.testFunc(tt.project)
			if result != tt.expected {
				t.Errorf("Expected %q, got %q", tt.expected, result)
			}
		})
	}
}

func TestCreateStandardSchemas(t *testing.T) {
	gen := New(defaultTitle, "1.0.0", defaultDescription)
	schemas := gen.createStandardSchemas()

	// Test that standard schemas are created correctly
	if len(schemas) != 2 {
		t.Errorf("Expected 2 schemas, got %d", len(schemas))
	}

	t.Run("SuccessResponse schema", func(t *testing.T) {
		validateSuccessResponseSchema(t, schemas)
	})

	t.Run("ErrorResponse schema", func(t *testing.T) {
		validateErrorResponseSchema(t, schemas)
	})
}

func validateSuccessResponseSchema(t *testing.T, schemas map[string]*OpenAPISchema) {
	t.Helper()
	successSchema, exists := schemas["SuccessResponse"]
	if !exists {
		t.Error("Missing SuccessResponse schema")
		return
	}

	if successSchema.Type != typeObject {
		t.Errorf("Expected SuccessResponse type 'object', got %s", successSchema.Type)
	}
	if len(successSchema.Properties) != 2 {
		t.Errorf("Expected SuccessResponse to have 2 properties, got %d", len(successSchema.Properties))
	}
	if _, hasData := successSchema.Properties["data"]; !hasData {
		t.Error("SuccessResponse should have 'data' property")
	}
	if _, hasMeta := successSchema.Properties["meta"]; !hasMeta {
		t.Error("SuccessResponse should have 'meta' property")
	}
}

func validateErrorResponseSchema(t *testing.T, schemas map[string]*OpenAPISchema) {
	t.Helper()
	errorSchema, exists := schemas["ErrorResponse"]
	if !exists {
		t.Error("Missing ErrorResponse schema")
		return
	}

	if errorSchema.Type != typeObject {
		t.Errorf("Expected ErrorResponse type 'object', got %s", errorSchema.Type)
	}
	if len(errorSchema.Properties) != 2 {
		t.Errorf("Expected ErrorResponse to have 2 properties, got %d", len(errorSchema.Properties))
	}
	if len(errorSchema.Required) != 1 || errorSchema.Required[0] != "error" {
		t.Errorf("Expected ErrorResponse to have 'error' as required field, got %v", errorSchema.Required)
	}
}

// Changeset 5: Schema Generation Tests

func TestGenerateSchemasFromTypes(t *testing.T) {
	gen := New(defaultTitle, "1.0.0", defaultDescription)

	tests := []struct {
		name            string
		routes          []models.Route
		expectedCount   int
		expectedSchemas []string
	}{
		{
			name:          "no routes",
			routes:        []models.Route{},
			expectedCount: 0,
		},
		{
			name: "single request type",
			routes: []models.Route{
				{
					Method: "POST",
					Path:   usersAPIPath,
					Request: &models.TypeInfo{
						Name:    "CreateUserReq",
						Package: "users",
						Fields: []models.FieldInfo{
							{Name: "Name", Type: "string", JSONName: "name"},
						},
					},
				},
			},
			expectedCount:   1,
			expectedSchemas: []string{"CreateUserReq"},
		},
		{
			name: "request and response types",
			routes: []models.Route{
				{
					Method: "POST",
					Path:   usersAPIPath,
					Request: &models.TypeInfo{
						Name:    "CreateUserReq",
						Package: "users",
						Fields: []models.FieldInfo{
							{Name: "Name", Type: "string", JSONName: "name"},
						},
					},
					Response: &models.TypeInfo{
						Name:    "User",
						Package: "users",
						Fields: []models.FieldInfo{
							{Name: "ID", Type: "int64", JSONName: "id"},
							{Name: "Name", Type: "string", JSONName: "name"},
						},
					},
				},
			},
			expectedCount:   2,
			expectedSchemas: []string{"CreateUserReq", "User"},
		},
		{
			name: "duplicate types across routes",
			routes: []models.Route{
				{
					Method: "POST",
					Path:   usersAPIPath,
					Request: &models.TypeInfo{
						Name:    "CreateUserReq",
						Package: "users",
						Fields: []models.FieldInfo{
							{Name: "Name", Type: "string", JSONName: "name"},
						},
					},
				},
				{
					Method: "PUT",
					Path:   usersIDAPIPath,
					Request: &models.TypeInfo{
						Name:    "CreateUserReq", // Same type as POST
						Package: "users",
						Fields: []models.FieldInfo{
							{Name: "Name", Type: "string", JSONName: "name"},
						},
					},
				},
			},
			expectedCount:   1, // Should deduplicate
			expectedSchemas: []string{"CreateUserReq"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			schemas := gen.generateSchemasFromTypes(tt.routes)

			if len(schemas) != tt.expectedCount {
				t.Errorf("Expected %d schemas, got %d", tt.expectedCount, len(schemas))
			}

			for _, name := range tt.expectedSchemas {
				if _, exists := schemas[name]; !exists {
					t.Errorf("Expected schema %q not found", name)
				}
			}
		})
	}
}

func TestTypeInfoToSchema(t *testing.T) {
	gen := New(defaultTitle, "1.0.0", defaultDescription)

	tests := []struct {
		name          string
		typeInfo      *models.TypeInfo
		expectNil     bool
		expectedType  string
		expectedProps int
		expectedReq   []string
	}{
		{
			name:      "nil type info",
			typeInfo:  nil,
			expectNil: true,
		},
		{
			name: "empty fields",
			typeInfo: &models.TypeInfo{
				Name:   "Empty",
				Fields: []models.FieldInfo{},
			},
			expectNil: true,
		},
		{
			name: "simple struct",
			typeInfo: &models.TypeInfo{
				Name:    "User",
				Package: "users",
				Fields: []models.FieldInfo{
					{Name: "ID", Type: "int64", JSONName: "id", Required: true},
					{Name: "Name", Type: "string", JSONName: "name", Required: true},
					{Name: "Email", Type: "string", JSONName: "email"},
				},
			},
			expectNil:     false,
			expectedType:  typeObject,
			expectedProps: 3,
			expectedReq:   []string{"id", "name"}, // Sorted
		},
		{
			name: "struct with pointer field",
			typeInfo: &models.TypeInfo{
				Name:    "UpdateReq",
				Package: "users",
				Fields: []models.FieldInfo{
					{Name: "Name", Type: "*string", JSONName: "name"},
				},
			},
			expectNil:     false,
			expectedType:  typeObject,
			expectedProps: 1,
			expectedReq:   []string{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			schema := gen.typeInfoToSchema(tt.typeInfo)

			if tt.expectNil {
				if schema != nil {
					t.Error("Expected nil schema")
				}
				return
			}

			if schema == nil {
				t.Fatal("Expected non-nil schema")
			}

			if schema.Type != tt.expectedType {
				t.Errorf(expectedTypeErrorMsg, tt.expectedType, schema.Type)
			}

			if len(schema.Properties) != tt.expectedProps {
				t.Errorf("Expected %d properties, got %d", tt.expectedProps, len(schema.Properties))
			}

			if len(schema.Required) != len(tt.expectedReq) {
				t.Errorf("Expected %d required fields, got %d", len(tt.expectedReq), len(schema.Required))
			}

			for i, req := range tt.expectedReq {
				if schema.Required[i] != req {
					t.Errorf("Expected required[%d] = %q, got %q", i, req, schema.Required[i])
				}
			}
		})
	}
}

func TestFieldInfoToProperty(t *testing.T) {
	gen := New(defaultTitle, "1.0.0", defaultDescription)

	tests := []struct {
		name            string
		field           *models.FieldInfo
		expectedType    string
		expectedFormat  string
		expectedDesc    string
		expectedExample any
		hasMinLength    bool
		minLengthValue  int
	}{
		{
			name: "string field",
			field: &models.FieldInfo{
				Name:     "Name",
				Type:     "string",
				JSONName: "name",
			},
			expectedType: "string",
		},
		{
			name: "string with description and example",
			field: &models.FieldInfo{
				Name:        "Email",
				Type:        "string",
				JSONName:    "email",
				Description: "User email address",
				Example:     "user@example.com",
			},
			expectedType:    "string",
			expectedDesc:    "User email address",
			expectedExample: "user@example.com",
		},
		{
			name: "string with min constraint",
			field: &models.FieldInfo{
				Name:        "Username",
				Type:        "string",
				JSONName:    "username",
				Constraints: map[string]string{"min": "3"},
			},
			expectedType:   "string",
			hasMinLength:   true,
			minLengthValue: 3,
		},
		{
			name: "integer field",
			field: &models.FieldInfo{
				Name:     "Age",
				Type:     "int",
				JSONName: "age",
			},
			expectedType:   "integer",
			expectedFormat: "int32",
		},
		{
			name: "int64 field",
			field: &models.FieldInfo{
				Name:     "ID",
				Type:     "int64",
				JSONName: "id",
			},
			expectedType:   "integer",
			expectedFormat: "int64",
		},
		{
			name: "float64 field",
			field: &models.FieldInfo{
				Name:     "Price",
				Type:     "float64",
				JSONName: "price",
			},
			expectedType:   "number",
			expectedFormat: "double",
		},
		{
			name: "boolean field",
			field: &models.FieldInfo{
				Name:     "Active",
				Type:     "bool",
				JSONName: "active",
			},
			expectedType: "boolean",
		},
		{
			name: "array field",
			field: &models.FieldInfo{
				Name:     "Tags",
				Type:     "[]string",
				JSONName: "tags",
			},
			expectedType: "array",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			prop := gen.fieldInfoToProperty(tt.field)

			if prop.Type != tt.expectedType {
				t.Errorf(expectedTypeErrorMsg, tt.expectedType, prop.Type)
			}

			if tt.expectedFormat != "" && prop.Format != tt.expectedFormat {
				t.Errorf(expectedFormatErrorMsg, tt.expectedFormat, prop.Format)
			}

			if tt.expectedDesc != "" && prop.Description != tt.expectedDesc {
				t.Errorf("Expected description %q, got %q", tt.expectedDesc, prop.Description)
			}

			if tt.expectedExample != nil && prop.Example != tt.expectedExample {
				t.Errorf("Expected example %v, got %v", tt.expectedExample, prop.Example)
			}

			if tt.hasMinLength {
				if prop.MinLength == nil {
					t.Error("Expected MinLength to be set")
				} else if *prop.MinLength != tt.minLengthValue {
					t.Errorf("Expected MinLength %d, got %d", tt.minLengthValue, *prop.MinLength)
				}
			}
		})
	}
}

func TestSetTypeAndFormat(t *testing.T) {
	gen := New(defaultTitle, "1.0.0", defaultDescription)

	tests := []struct {
		name           string
		goType         string
		expectedType   string
		expectedFormat string
		hasItems       bool
		itemType       string
	}{
		{name: "string", goType: "string", expectedType: "string"},
		{name: "pointer string", goType: "*string", expectedType: "string"},
		{name: "int", goType: "int", expectedType: "integer", expectedFormat: "int32"},
		{name: "int32", goType: "int32", expectedType: "integer", expectedFormat: "int32"},
		{name: "int64", goType: "int64", expectedType: "integer", expectedFormat: "int64"},
		{name: "uint", goType: "uint", expectedType: "integer", expectedFormat: "int32"},
		{name: "uint64", goType: "uint64", expectedType: "integer", expectedFormat: "int64"},
		{name: "float32", goType: "float32", expectedType: "number", expectedFormat: "float"},
		{name: "float64", goType: "float64", expectedType: "number", expectedFormat: "double"},
		{name: "bool", goType: "bool", expectedType: "boolean"},
		{name: "array of strings", goType: "[]string", expectedType: "array", hasItems: true, itemType: "string"},
		{name: "array of int", goType: "[]int", expectedType: "array", hasItems: true, itemType: "integer"},
		{name: "map", goType: "map[string]any", expectedType: typeObject},
		{name: "custom struct", goType: "CustomType", expectedType: typeObject},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			prop := &OpenAPIProperty{}
			gen.setTypeAndFormat(prop, tt.goType)

			if prop.Type != tt.expectedType {
				t.Errorf(expectedTypeErrorMsg, tt.expectedType, prop.Type)
			}

			if tt.expectedFormat != "" && prop.Format != tt.expectedFormat {
				t.Errorf(expectedFormatErrorMsg, tt.expectedFormat, prop.Format)
			}

			if tt.hasItems {
				if prop.Items == nil {
					t.Error("Expected Items to be set for array type")
				} else if prop.Items.Type != tt.itemType {
					t.Errorf("Expected item type %q, got %q", tt.itemType, prop.Items.Type)
				}
			}
		})
	}
}

func TestApplyConstraints(t *testing.T) {
	gen := New(defaultTitle, "1.0.0", defaultDescription)

	tests := []struct {
		name                 string
		field                *models.FieldInfo
		expectedFormat       string
		expectedMinLength    *int
		expectedMaxLength    *int
		expectedMinimum      *float64
		expectedMaximum      *float64
		expectedExclusiveMin *bool
		expectedPattern      string
		expectedEnumCount    int
	}{
		{
			name: "no constraints",
			field: &models.FieldInfo{
				Type:        "string",
				Constraints: map[string]string{},
			},
		},
		{
			name: "email format",
			field: &models.FieldInfo{
				Type:        "string",
				Constraints: map[string]string{"email": ""},
			},
			expectedFormat: "email",
		},
		{
			name: "string min/max length",
			field: &models.FieldInfo{
				Type:        "string",
				Constraints: map[string]string{"min": "5", "max": "50"},
			},
			expectedMinLength: intPtr(5),
			expectedMaxLength: intPtr(50),
		},
		{
			name: "integer min/max",
			field: &models.FieldInfo{
				Type:        "int64",
				Constraints: map[string]string{"min": "1", "max": "100"},
			},
			expectedMinimum: float64Ptr(1.0),
			expectedMaximum: float64Ptr(100.0),
		},
		{
			name: "integer gt (exclusive minimum)",
			field: &models.FieldInfo{
				Type:        "int",
				Constraints: map[string]string{"gt": "0"},
			},
			expectedMinimum:      float64Ptr(0.0),
			expectedExclusiveMin: boolPtr(true),
		},
		{
			name: "regexp pattern",
			field: &models.FieldInfo{
				Type:        "string",
				Constraints: map[string]string{"regexp": "^[A-Z][a-z]+$"},
			},
			expectedPattern: "^[A-Z][a-z]+$",
		},
		{
			name: "oneof enum",
			field: &models.FieldInfo{
				Type:        "string",
				Constraints: map[string]string{"oneof": "admin user guest"},
			},
			expectedEnumCount: 3,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			prop := &OpenAPIProperty{}
			gen.applyConstraints(prop, tt.field)

			if tt.expectedFormat != "" && prop.Format != tt.expectedFormat {
				t.Errorf(expectedFormatErrorMsg, tt.expectedFormat, prop.Format)
			}

			if tt.expectedMinLength != nil {
				if prop.MinLength == nil {
					t.Error("Expected MinLength to be set")
				} else if *prop.MinLength != *tt.expectedMinLength {
					t.Errorf("Expected MinLength %d, got %d", *tt.expectedMinLength, *prop.MinLength)
				}
			}

			if tt.expectedMaxLength != nil {
				if prop.MaxLength == nil {
					t.Error("Expected MaxLength to be set")
				} else if *prop.MaxLength != *tt.expectedMaxLength {
					t.Errorf("Expected MaxLength %d, got %d", *tt.expectedMaxLength, *prop.MaxLength)
				}
			}

			if tt.expectedMinimum != nil {
				if prop.Minimum == nil {
					t.Error("Expected Minimum to be set")
				} else if *prop.Minimum != *tt.expectedMinimum {
					t.Errorf("Expected Minimum %f, got %f", *tt.expectedMinimum, *prop.Minimum)
				}
			}

			if tt.expectedMaximum != nil {
				if prop.Maximum == nil {
					t.Error("Expected Maximum to be set")
				} else if *prop.Maximum != *tt.expectedMaximum {
					t.Errorf("Expected Maximum %f, got %f", *tt.expectedMaximum, *prop.Maximum)
				}
			}

			if tt.expectedExclusiveMin != nil {
				if prop.ExclusiveMinimum == nil {
					t.Error("Expected ExclusiveMinimum to be set")
				} else if *prop.ExclusiveMinimum != *tt.expectedExclusiveMin {
					t.Errorf("Expected ExclusiveMinimum %v, got %v", *tt.expectedExclusiveMin, *prop.ExclusiveMinimum)
				}
			}

			if tt.expectedPattern != "" && prop.Pattern != tt.expectedPattern {
				t.Errorf("Expected pattern %q, got %q", tt.expectedPattern, prop.Pattern)
			}

			if tt.expectedEnumCount > 0 {
				if len(prop.Enum) != tt.expectedEnumCount {
					t.Errorf("Expected %d enum values, got %d", tt.expectedEnumCount, len(prop.Enum))
				}
			}
		})
	}
}

func TestGenerateWithTypedRequestResponse(t *testing.T) {
	gen := New(defaultTitle, "1.0.0", defaultDescription)
	project := &models.Project{
		Modules: []models.Module{
			{
				Name: "users",
				Routes: []models.Route{
					{
						Method:      "POST",
						Path:        usersAPIPath,
						HandlerName: "createUser",
						Summary:     createNewUserSummary,
						Request: &models.TypeInfo{
							Name:    "CreateUserReq",
							Package: "users",
							Fields: []models.FieldInfo{
								{
									Name:        "Name",
									Type:        "string",
									JSONName:    "name",
									Required:    true,
									Description: "User's full name",
									Constraints: map[string]string{"min": "2", "max": "100"},
								},
								{
									Name:        "Email",
									Type:        "string",
									JSONName:    "email",
									Required:    true,
									Constraints: map[string]string{"email": ""},
								},
								{
									Name:        "Age",
									Type:        "int",
									JSONName:    "age",
									Constraints: map[string]string{"gte": "18", "lte": "120"},
								},
							},
						},
						Response: &models.TypeInfo{
							Name:    "User",
							Package: "users",
							Fields: []models.FieldInfo{
								{
									Name:     "ID",
									Type:     "int64",
									JSONName: "id",
									Required: true,
								},
								{
									Name:     "Name",
									Type:     "string",
									JSONName: "name",
									Required: true,
								},
								{
									Name:     "Email",
									Type:     "string",
									JSONName: "email",
									Required: true,
								},
							},
						},
					},
				},
			},
		},
	}

	spec, err := gen.Generate(project)
	if err != nil {
		t.Fatal(usersIDAPIPath, err)
	}

	// Parse the spec to validate structure
	var parsed OpenAPISpec
	err = yaml.Unmarshal([]byte(spec), &parsed)
	if err != nil {
		t.Fatalf(yamlParsingFailedMsg, err)
	}

	// Verify components/schemas section exists and contains generated types
	components, ok := parsed.Components["schemas"].(map[string]any)
	if !ok {
		t.Fatal("Components/schemas should be a map")
	}

	// Check that generated schemas exist
	schemaNames := []string{"CreateUserReq", "User", "SuccessResponse", "ErrorResponse"}
	for _, name := range schemaNames {
		if _, exists := components[name]; !exists {
			t.Errorf("Missing schema: %s", name)
		}
	}

	// Verify CreateUserReq schema has proper constraints
	if !strings.Contains(spec, "CreateUserReq:") {
		t.Error("Missing CreateUserReq schema definition")
	}
	if !strings.Contains(spec, "minLength:") {
		t.Error("Missing minLength constraint")
	}
	if !strings.Contains(spec, "format: email") {
		t.Error("Missing email format constraint")
	}
}

// Helper functions for pointer creation in tests
func intPtr(i int) *int {
	return &i
}

func float64Ptr(f float64) *float64 {
	return &f
}

func boolPtr(b bool) *bool {
	return &b
}

// Changeset 6: Parameter Extraction Tests

func TestExtractParameters(t *testing.T) {
	gen := New(defaultTitle, "1.0.0", defaultDescription)

	tests := []struct {
		name              string
		route             *models.Route
		expectedParams    int
		expectedBodyCount int
		checkParam        func(*testing.T, []Parameter)
	}{
		{
			name: "no request type",
			route: &models.Route{
				Method: "GET",
				Path:   usersAPIPath,
			},
			expectedParams:    0,
			expectedBodyCount: 0,
		},
		{
			name: "path parameter",
			route: &models.Route{
				Method: "GET",
				Path:   usersIDAPIPath,
				Request: &models.TypeInfo{
					Name: "GetUserReq",
					Fields: []models.FieldInfo{
						{
							Name:      "ID",
							Type:      "int64",
							ParamType: "path",
							ParamName: "id",
							Required:  true,
						},
					},
				},
			},
			expectedParams:    1,
			expectedBodyCount: 0,
			checkParam: func(t *testing.T, params []Parameter) {
				if params[0].Name != "id" {
					t.Errorf("Expected param name 'id', got %q", params[0].Name)
				}
				if params[0].In != "path" {
					t.Errorf("Expected param in 'path', got %q", params[0].In)
				}
				if !params[0].Required {
					t.Error("Path parameter should be required")
				}
			},
		},
		{
			name: "query parameters",
			route: &models.Route{
				Method: "GET",
				Path:   usersAPIPath,
				Request: &models.TypeInfo{
					Name: "ListUsersReq",
					Fields: []models.FieldInfo{
						{
							Name:        "Page",
							Type:        "int",
							ParamType:   "query",
							ParamName:   "page",
							Description: pageNumberDescription,
							Constraints: map[string]string{"min": "1"},
						},
						{
							Name:      "Limit",
							Type:      "int",
							ParamType: "query",
							ParamName: "limit",
						},
					},
				},
			},
			expectedParams:    2,
			expectedBodyCount: 0,
			checkParam: func(t *testing.T, params []Parameter) {
				if params[0].Name != "page" {
					t.Errorf("Expected first param 'page', got %q", params[0].Name)
				}
				if params[0].Description != pageNumberDescription {
					t.Errorf("Expected description 'Page number', got %q", params[0].Description)
				}
			},
		},
		{
			name: "header parameter",
			route: &models.Route{
				Method: "POST",
				Path:   "/api/upload",
				Request: &models.TypeInfo{
					Name: "UploadReq",
					Fields: []models.FieldInfo{
						{
							Name:      "ContentType",
							Type:      "string",
							ParamType: "header",
							ParamName: "Content-Type",
							Required:  true,
						},
					},
				},
			},
			expectedParams:    1,
			expectedBodyCount: 0,
			checkParam: func(t *testing.T, params []Parameter) {
				if params[0].In != "header" {
					t.Errorf("Expected param in 'header', got %q", params[0].In)
				}
				if params[0].Name != "Content-Type" {
					t.Errorf("Expected param name 'Content-Type', got %q", params[0].Name)
				}
			},
		},
		{
			name: "mixed parameters and body",
			route: &models.Route{
				Method: "POST",
				Path:   "/users/:id/update",
				Request: &models.TypeInfo{
					Name: "UpdateUserReq",
					Fields: []models.FieldInfo{
						{
							Name:      "ID",
							Type:      "int64",
							ParamType: "path",
							ParamName: "id",
						},
						{
							Name:      "Async",
							Type:      "bool",
							ParamType: "query",
							ParamName: "async",
						},
						{
							Name:     "Name",
							Type:     "string",
							JSONName: "name",
							Required: true,
						},
						{
							Name:     "Email",
							Type:     "string",
							JSONName: "email",
						},
					},
				},
			},
			expectedParams:    2,
			expectedBodyCount: 2,
			checkParam: func(t *testing.T, params []Parameter) {
				// Should have path and query params, not body fields
				paramNames := make(map[string]bool)
				for _, p := range params {
					paramNames[p.Name] = true
				}
				if !paramNames["id"] {
					t.Error("Expected 'id' path parameter")
				}
				if !paramNames["async"] {
					t.Error("Expected 'async' query parameter")
				}
			},
		},
		{
			name: "all body fields (no parameters)",
			route: &models.Route{
				Method: "POST",
				Path:   usersAPIPath,
				Request: &models.TypeInfo{
					Name: "CreateUserReq",
					Fields: []models.FieldInfo{
						{
							Name:     "Name",
							Type:     "string",
							JSONName: "name",
						},
						{
							Name:     "Email",
							Type:     "string",
							JSONName: "email",
						},
					},
				},
			},
			expectedParams:    0,
			expectedBodyCount: 2,
		},
		{
			name: "parameter with example",
			route: &models.Route{
				Method: "GET",
				Path:   usersIDAPIPath,
				Request: &models.TypeInfo{
					Name: "GetUserReq",
					Fields: []models.FieldInfo{
						{
							Name:      "ID",
							Type:      "int64",
							ParamType: "path",
							ParamName: "id",
							Example:   "123",
						},
					},
				},
			},
			expectedParams:    1,
			expectedBodyCount: 0,
			checkParam: func(t *testing.T, params []Parameter) {
				if params[0].Example == nil {
					t.Error("Expected parameter to have example")
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			params, bodyFields := gen.extractParameters(tt.route)

			if len(params) != tt.expectedParams {
				t.Errorf("Expected %d parameters, got %d", tt.expectedParams, len(params))
			}

			if len(bodyFields) != tt.expectedBodyCount {
				t.Errorf("Expected %d body fields, got %d", tt.expectedBodyCount, len(bodyFields))
			}

			if tt.checkParam != nil && len(params) > 0 {
				tt.checkParam(t, params)
			}
		})
	}
}

func TestWriteParameters(t *testing.T) {
	gen := New(defaultTitle, "1.0.0", defaultDescription)

	tests := []struct {
		name           string
		params         []Parameter
		expectedOutput []string // Strings that should appear in output
		notExpected    []string // Strings that should NOT appear
	}{
		{
			name: "single path parameter",
			params: []Parameter{
				{
					Name:     "id",
					In:       "path",
					Required: true,
					Schema: &OpenAPIProperty{
						Type:   "integer",
						Format: "int64",
					},
				},
			},
			expectedOutput: []string{
				parametersHeader,
				IDHeader,
				"in: path",
				"required: true",
				"type: integer",
				"format: int64",
			},
		},
		{
			name: "query parameter with constraints",
			params: []Parameter{
				{
					Name:        "page",
					In:          "query",
					Required:    false,
					Description: pageNumberDescription,
					Schema: &OpenAPIProperty{
						Type:    "integer",
						Minimum: float64Ptr(1.0),
					},
					Example: "1",
				},
			},
			expectedOutput: []string{
				parametersHeader,
				"- name: page",
				"in: query",
				"required: false",
				"description: Page number",
				"minimum: 1",
				"example: 1",
			},
		},
		{
			name: "multiple parameters",
			params: []Parameter{
				{
					Name:     "id",
					In:       "path",
					Required: true,
					Schema: &OpenAPIProperty{
						Type: "integer",
					},
				},
				{
					Name:     "format",
					In:       "query",
					Required: false,
					Schema: &OpenAPIProperty{
						Type: "string",
						Enum: []any{"json", "xml"},
					},
				},
			},
			expectedOutput: []string{
				IDHeader,
				"- name: format",
				"enum:",
				"- json",
				"- xml",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var sb strings.Builder
			gen.writeParameters(&sb, tt.params)
			output := sb.String()

			for _, expected := range tt.expectedOutput {
				if !strings.Contains(output, expected) {
					t.Errorf("Expected output to contain %q, but it didn't.\nOutput:\n%s", expected, output)
				}
			}

			for _, notExpected := range tt.notExpected {
				if strings.Contains(output, notExpected) {
					t.Errorf("Expected output NOT to contain %q, but it did.\nOutput:\n%s", notExpected, output)
				}
			}
		})
	}
}

func TestWriteRequestBody(t *testing.T) {
	gen := New(defaultTitle, "1.0.0", defaultDescription)

	tests := []struct {
		name           string
		bodyFields     []models.FieldInfo
		schemaName     string
		expectedOutput []string
	}{
		{
			name: "with schema name",
			bodyFields: []models.FieldInfo{
				{Name: "Name", Type: "string"},
			},
			schemaName: "CreateUserReq",
			expectedOutput: []string{
				requestBodyHeader,
				"required: true",
				"content:",
				"application/json:",
				"schema:",
				"$ref: '#/components/schemas/CreateUserReq'",
			},
		},
		{
			name: "without schema name (inline)",
			bodyFields: []models.FieldInfo{
				{Name: "Data", Type: "string"},
			},
			schemaName: "",
			expectedOutput: []string{
				requestBodyHeader,
				"type: object",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var sb strings.Builder
			gen.writeRequestBody(&sb, tt.bodyFields, tt.schemaName)
			output := sb.String()

			for _, expected := range tt.expectedOutput {
				if !strings.Contains(output, expected) {
					t.Errorf("Expected output to contain %q.\nOutput:\n%s", expected, output)
				}
			}
		})
	}
}

func TestWritePropertySchema(t *testing.T) {
	gen := New(defaultTitle, "1.0.0", defaultDescription)

	tests := []struct {
		name           string
		prop           *OpenAPIProperty
		indent         string
		expectedOutput []string
	}{
		{
			name: "simple string",
			prop: &OpenAPIProperty{
				Type: "string",
			},
			indent: "  ",
			expectedOutput: []string{
				"  type: string",
			},
		},
		{
			name: "integer with format and constraints",
			prop: &OpenAPIProperty{
				Type:    "integer",
				Format:  "int64",
				Minimum: float64Ptr(1.0),
				Maximum: float64Ptr(100.0),
			},
			indent: "    ",
			expectedOutput: []string{
				"    type: integer",
				"    format: int64",
				"    minimum: 1",
				"    maximum: 100",
			},
		},
		{
			name: "string with length and pattern",
			prop: &OpenAPIProperty{
				Type:      "string",
				MinLength: intPtr(3),
				MaxLength: intPtr(50),
				Pattern:   "^[a-zA-Z]+$",
			},
			indent: "  ",
			expectedOutput: []string{
				"minLength: 3",
				"maxLength: 50",
				"pattern: ^[a-zA-Z]+$",
			},
		},
		{
			name: "enum",
			prop: &OpenAPIProperty{
				Type: "string",
				Enum: []any{"red", "green", "blue"},
			},
			indent: "  ",
			expectedOutput: []string{
				"enum:",
				"- red",
				"- green",
				"- blue",
			},
		},
		{
			name: "array with items",
			prop: &OpenAPIProperty{
				Type: "array",
				Items: &OpenAPIProperty{
					Type:   "integer",
					Format: "int32",
				},
			},
			indent: "  ",
			expectedOutput: []string{
				"  type: array",
				"  items:",
				"    type: integer",
				"    format: int32",
			},
		},
		{
			name: "exclusive bounds",
			prop: &OpenAPIProperty{
				Type:             "number",
				Minimum:          float64Ptr(0.0),
				ExclusiveMinimum: boolPtr(true),
			},
			indent: "  ",
			expectedOutput: []string{
				"minimum: 0",
				"exclusiveMinimum: true",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var sb strings.Builder
			gen.writePropertySchema(&sb, tt.prop, tt.indent)
			output := sb.String()

			for _, expected := range tt.expectedOutput {
				if !strings.Contains(output, expected) {
					t.Errorf("Expected output to contain %q.\nOutput:\n%s", expected, output)
				}
			}
		})
	}
}

// TestToFloat64Ptr directly tests the toFloat64Ptr utility function
func TestToFloat64Ptr(t *testing.T) {
	tests := []struct {
		name     string
		input    any
		expected *float64
	}{
		{name: "int value", input: 42, expected: float64Ptr(42.0)},
		{name: "int64 value", input: int64(123), expected: float64Ptr(123.0)},
		{name: "float64 value", input: 3.14, expected: float64Ptr(3.14)},
		{name: "valid string", input: "99.5", expected: float64Ptr(99.5)},
		{name: "invalid string", input: "not-a-number", expected: nil},
		{name: "empty string", input: "", expected: nil},
		{name: "unsupported type (bool)", input: true, expected: nil},
		{name: "unsupported type (slice)", input: []int{1, 2, 3}, expected: nil},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := toFloat64Ptr(tt.input)

			if tt.expected == nil {
				if result != nil {
					t.Errorf("Expected nil, got %v", *result)
				}
			} else {
				if result == nil {
					t.Errorf("Expected %v, got nil", *tt.expected)
				} else if *result != *tt.expected {
					t.Errorf("Expected %v, got %v", *tt.expected, *result)
				}
			}
		})
	}
}

// TestWritePropertySchemaNilProp verifies nil property handling in writePropertySchema
func TestWritePropertySchemaNilProp(t *testing.T) {
	gen := New(defaultTitle, "1.0.0", defaultDescription)
	var sb strings.Builder

	// Should not panic and should produce empty output
	gen.writePropertySchema(&sb, nil, "  ")

	output := sb.String()
	if output != "" {
		t.Errorf("Expected empty output for nil property, got %q", output)
	}
}

// TestTypeInfoToSchemaSkipsIgnoredFields verifies fields with json:"-" are skipped
func TestTypeInfoToSchemaSkipsIgnoredFields(t *testing.T) {
	gen := New(defaultTitle, "1.0.0", defaultDescription)

	typeInfo := &models.TypeInfo{
		Name:    "TestStruct",
		Package: "test",
		Fields: []models.FieldInfo{
			{Name: "ID", Type: "int64", JSONName: "id", Required: true},
			{Name: "Internal", Type: "string", JSONName: "-"}, // Should be skipped
			{Name: "Name", Type: "string", JSONName: "name"},
		},
	}

	schema := gen.typeInfoToSchema(typeInfo)

	// Should have 2 properties, not 3 (Internal field should be skipped)
	if len(schema.Properties) != 2 {
		t.Errorf("Expected 2 properties (excluding json:\"-\" field), got %d", len(schema.Properties))
	}

	// Verify Internal field is not present
	if _, exists := schema.Properties["-"]; exists {
		t.Error("Field with json:\"-\" should not be included in properties")
	}
	if _, exists := schema.Properties["Internal"]; exists {
		t.Error("Field with json:\"-\" should not be included (even by original name)")
	}

	// Verify other fields are present
	if _, exists := schema.Properties["id"]; !exists {
		t.Error("Expected 'id' property to exist")
	}
	if _, exists := schema.Properties["name"]; !exists {
		t.Error("Expected 'name' property to exist")
	}
}

func TestGenerateWithParameters(t *testing.T) {
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
						Summary:     getUserByIDSummary,
						Request: &models.TypeInfo{
							Name: "GetUserReq",
							Fields: []models.FieldInfo{
								{
									Name:        "ID",
									Type:        "int64",
									ParamType:   "path",
									ParamName:   "id",
									Description: "User identifier",
									Constraints: map[string]string{"min": "1"},
								},
								{
									Name:      "Include",
									Type:      "string",
									ParamType: "query",
									ParamName: "include",
								},
							},
						},
					},
					{
						Method:      "POST",
						Path:        usersAPIPath,
						HandlerName: "createUser",
						Summary:     createNewUserSummary,
						Request: &models.TypeInfo{
							Name: "CreateUserReq",
							Fields: []models.FieldInfo{
								{
									Name:     "Name",
									Type:     "string",
									JSONName: "name",
									Required: true,
								},
								{
									Name:     "Email",
									Type:     "string",
									JSONName: "email",
									Required: true,
								},
							},
						},
					},
				},
			},
		},
	}

	spec, err := gen.Generate(project)
	if err != nil {
		t.Fatal(usersIDAPIPath, err)
	}

	// Verify GET /users/:id has parameters
	if !strings.Contains(spec, parametersHeader) {
		t.Error("Expected spec to contain 'parameters:' section")
	}
	if !strings.Contains(spec, IDHeader) {
		t.Error("Expected spec to contain path parameter 'id'")
	}
	if !strings.Contains(spec, "in: path") {
		t.Error("Expected spec to contain 'in: path'")
	}
	if !strings.Contains(spec, "- name: include") {
		t.Error("Expected spec to contain query parameter 'include'")
	}
	if !strings.Contains(spec, "in: query") {
		t.Error("Expected spec to contain 'in: query'")
	}

	// Verify POST /users has requestBody (no parameters)
	if !strings.Contains(spec, requestBodyHeader) {
		t.Error("Expected spec to contain 'requestBody:' section")
	}
	if !strings.Contains(spec, "$ref: '#/components/schemas/CreateUserReq'") {
		t.Error("Expected spec to reference CreateUserReq schema in requestBody")
	}

	// Parse and validate structure
	var parsed OpenAPISpec
	err = yaml.Unmarshal([]byte(spec), &parsed)
	if err != nil {
		t.Fatalf(yamlParsingFailedMsg, err)
	}
}
