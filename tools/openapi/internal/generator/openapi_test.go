package generator

import (
	"bytes"
	"strings"
	"testing"

	"github.com/gaborage/go-bricks/tools/openapi/internal/models"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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
	// No explicit document overrides, so analyzer-derived project metadata wins
	// (precedence: explicit override > project metadata > built-in default).
	gen := New("", "", "")
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

	// Check operation details: operationId is module-qualified (users + GetUser).
	if !strings.Contains(spec, "operationId: usersGetUser") {
		t.Error("Missing module-qualified operation ID")
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

	// Check standard responses (status keys are quoted strings in the emitted YAML)
	if !strings.Contains(spec, `"200":`) {
		t.Error("Missing 200 response")
	}
	if !strings.Contains(spec, `"400":`) {
		t.Error("Missing 400 response")
	}
	// Non-JOSE routes use the inline data/meta envelope (SuccessResponse is gated
	// to JOSE-untyped fallbacks now), so assert the inline envelope + ErrorResponse.
	if !strings.Contains(spec, "data:") {
		t.Error("Missing inline data/meta success envelope")
	}
	if !strings.Contains(spec, "ErrorResponse") {
		t.Error("Missing error response schema")
	}
	// Conformance additions: servers + tenant security.
	if !strings.Contains(spec, "servers:") {
		t.Error("Missing servers block")
	}
	if !strings.Contains(spec, "X-Tenant-ID") {
		t.Error("Missing X-Tenant-ID security scheme")
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
			result := gen.getOperationID(tt.route, nil)
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
		// POST maps to the generic phrase, NOT "created": a 201 is described as
		// created by successDescription's status branch, so a POST returning 200 is
		// not mislabeled as a creation.
		{"POST", "Successful response"},
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

// TestGetAllRoutesStampsModuleIdentity verifies that flattening stamps each
// route's owning module identity when absent, and preserves it when already set
// (acceptance criterion: Module/Package survive the module->route flatten).
func TestGetAllRoutesStampsModuleIdentity(t *testing.T) {
	gen := New(defaultTitle, "1.0.0", defaultDescription)
	project := &models.Project{
		Modules: []models.Module{
			{
				Name:    "users",
				Package: "users",
				Routes: []models.Route{
					{Method: "GET", Path: "/a"},                                         // unset -> stamped
					{Method: "GET", Path: "/b", Module: "custom", Package: "custompkg"}, // preserved
				},
			},
		},
	}

	routes := gen.getAllRoutes(project)
	require.Len(t, routes, 2)
	assert.Equal(t, "users", routes[0].Module)
	assert.Equal(t, "users", routes[0].Package)
	assert.Equal(t, "custom", routes[1].Module, "explicit Module must not be overwritten")
	assert.Equal(t, "custompkg", routes[1].Package, "explicit Package must not be overwritten")

	// The flatten must not mutate the originals (route values are copied).
	assert.Empty(t, project.Modules[0].Routes[0].Module, "original route must remain unstamped")
}

func TestBuildPathsDropsNonRootedPath(t *testing.T) {
	gen := New(defaultTitle, "1.0.0", defaultDescription)
	paths := gen.buildPaths([]models.Route{
		{Method: "GET", Path: "/ok"},
		{Method: "GET", Path: "broken"}, // no leading slash
	}, nil)
	_, hasOK := paths["/ok"]
	_, hasBroken := paths["broken"]
	assert.True(t, hasOK)
	assert.False(t, hasBroken, "a path key without a leading slash must be dropped")
}

func TestBuildPathsDedupesSameMethodAndPath(t *testing.T) {
	gen := New(defaultTitle, "1.0.0", defaultDescription)
	paths := gen.buildPaths([]models.Route{
		{Method: "GET", Path: "/x", HandlerName: "first"},
		{Method: "GET", Path: "/x", HandlerName: "second"}, // same (method,path)
	}, nil)
	require.NotNil(t, paths["/x"])
	require.NotNil(t, paths["/x"].Get)
	assert.Equal(t, "first", paths["/x"].Get.OperationID, "first registration wins on a duplicate (method,path)")
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
	// Test with values that could break manual YAML concatenation. No explicit
	// overrides, so the problematic project-derived metadata flows through the
	// escape path under test.
	gen := New("", "", "")

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
			expected: defaultDocTitle, // no override, no project name -> built-in default
		},
		{
			name:     "empty version with empty project",
			project:  &models.Project{},
			testFunc: func(p *models.Project) string { return gen.getVersion(p) },
			expected: defaultDocVersion, // no override, no project version -> built-in default
		},
		{
			name:     "empty description with empty project",
			project:  &models.Project{},
			testFunc: func(p *models.Project) string { return gen.getDescription(p) },
			expected: defaultDocDescription, // no override, no project description -> built-in default
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

// TestGetterPrecedenceExplicitOverride pins the top of the precedence chain:
// an explicit document override (from --title/--api-version/--description)
// wins over analyzer-derived project metadata.
func TestGetterPrecedenceExplicitOverride(t *testing.T) {
	gen := New("Override Title", "9.9.9", "Override description")
	project := &models.Project{
		Name:        "Project Title",
		Version:     "2.0.0",
		Description: "Project description",
	}

	if got := gen.getTitle(project); got != "Override Title" {
		t.Errorf("title: explicit override should win, got %q", got)
	}
	if got := gen.getVersion(project); got != "9.9.9" {
		t.Errorf("version: explicit override should win, got %q", got)
	}
	if got := gen.getDescription(project); got != "Override description" {
		t.Errorf("description: explicit override should win, got %q", got)
	}
}

func TestCreateStandardSchemas(t *testing.T) {
	gen := New(defaultTitle, "1.0.0", defaultDescription)
	// Routes that, between them, reference all four standard schemas: a normal
	// route (ErrorResponse), a JOSE route with an untyped response (SuccessResponse
	// + JOSEErrorEnvelope), and a raw route (RawErrorResponse).
	routes := []models.Route{
		{Method: "GET", Path: "/a", Response: &models.TypeInfo{Name: "Thing"}},
		{Method: "POST", Path: "/j", Response: &models.TypeInfo{JOSE: true}},
		{Method: "GET", Path: "/r", RawResponse: true, Response: &models.TypeInfo{Name: "Legacy"}},
	}
	schemas := gen.createStandardSchemas(routes)

	if len(schemas) != 4 {
		t.Errorf("Expected 4 schemas, got %d", len(schemas))
	}

	t.Run("SuccessResponse schema", func(t *testing.T) {
		validateSuccessResponseSchema(t, schemas)
	})
	t.Run("ErrorResponse schema", func(t *testing.T) {
		validateErrorResponseSchema(t, schemas)
	})
	// JOSEErrorEnvelope (pre-trust JOSE failures) and RawErrorResponse (raw-mode
	// routes) are both minimal {code,message} envelopes that MUST NOT leak meta.
	t.Run("JOSEErrorEnvelope schema", func(t *testing.T) {
		validateMinimalErrorEnvelope(t, schemas, schemaJOSEErrorEnvelope)
	})
	t.Run("RawErrorResponse schema", func(t *testing.T) {
		validateMinimalErrorEnvelope(t, schemas, schemaRawErrorResponse)
	})
}

func TestCreateStandardSchemasGating(t *testing.T) {
	gen := New(defaultTitle, "1.0.0", defaultDescription)

	t.Run("normal routes only -> just ErrorResponse", func(t *testing.T) {
		s := gen.createStandardSchemas([]models.Route{{Method: "GET", Path: "/a", Response: &models.TypeInfo{Name: "Thing"}}})
		assert.Contains(t, s, schemaErrorResponse)
		assert.NotContains(t, s, schemaJOSEErrorEnvelope, "no JOSE route -> no JOSEErrorEnvelope")
		assert.NotContains(t, s, schemaRawErrorResponse, "no raw route -> no RawErrorResponse")
		assert.NotContains(t, s, schemaSuccessResponse, "typed responses use inline envelopes")
	})

	t.Run("empty project -> no standard schemas", func(t *testing.T) {
		assert.Empty(t, gen.createStandardSchemas(nil))
	})
}

// validateMinimalErrorEnvelope asserts a {code,message}-only error schema: the
// named component exists, carries no meta (leaking nothing beyond code/message),
// and includes both required properties.
func validateMinimalErrorEnvelope(t *testing.T, schemas map[string]*OpenAPISchema, name string) {
	t.Helper()
	schema, ok := schemas[name]
	if !ok {
		t.Fatalf("%s schema missing — a minimal {code,message} error envelope is required", name)
	}
	if _, hasMeta := schema.Properties[propNameMeta]; hasMeta {
		t.Errorf("%s must NOT carry meta — the minimal envelope leaks nothing beyond code/message", name)
	}
	for _, p := range []string{propNameCode, propNameMessage} {
		if _, ok := schema.Properties[p]; !ok {
			t.Errorf("%s must include %q property", name, p)
		}
	}
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

	createUserReq := &models.TypeInfo{
		Name: "CreateUserReq", Package: "users",
		Fields: []models.FieldInfo{{Name: "Name", Type: "string", JSONName: "name"}},
	}
	user := &models.TypeInfo{
		Name: "User", Package: "users",
		Fields: []models.FieldInfo{
			{Name: "ID", Type: "int64", JSONName: "id"},
			{Name: "Name", Type: "string", JSONName: "name"},
		},
	}

	tests := []struct {
		name            string
		types           map[string]*models.TypeInfo
		expectedCount   int
		expectedSchemas []string
	}{
		{
			name:          "no types",
			types:         map[string]*models.TypeInfo{},
			expectedCount: 0,
		},
		{
			name:            "single type",
			types:           map[string]*models.TypeInfo{"CreateUserReq": createUserReq},
			expectedCount:   1,
			expectedSchemas: []string{"CreateUserReq"},
		},
		{
			name:            "request and response types",
			types:           map[string]*models.TypeInfo{"CreateUserReq": createUserReq, "User": user},
			expectedCount:   2,
			expectedSchemas: []string{"CreateUserReq", "User"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			schemas := gen.generateSchemasFromTypes(tt.types, nil)
			assert.Len(t, schemas, tt.expectedCount)
			for _, name := range tt.expectedSchemas {
				_, exists := schemas[name]
				assert.True(t, exists, "expected schema %q", name)
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
			assertSchemaShape(t, tt.expectedType, tt.expectedProps, tt.expectedReq, schema)
		})
	}
}

// assertSchemaShape validates that an emitted *OpenAPISchema has the expected
// type, property count, and required-field list (compared in order).
func assertSchemaShape(t *testing.T, expectedType string, expectedProps int, expectedReq []string, schema *OpenAPISchema) {
	t.Helper()
	if schema == nil {
		t.Fatal("Expected non-nil schema")
	}
	if schema.Type != expectedType {
		t.Errorf(expectedTypeErrorMsg, expectedType, schema.Type)
	}
	if len(schema.Properties) != expectedProps {
		t.Errorf("Expected %d properties, got %d", expectedProps, len(schema.Properties))
	}
	if len(schema.Required) != len(expectedReq) {
		t.Errorf("Expected %d required fields, got %d", len(expectedReq), len(schema.Required))
		return
	}
	for i, req := range expectedReq {
		if schema.Required[i] != req {
			t.Errorf("Expected required[%d] = %q, got %q", i, req, schema.Required[i])
		}
	}
}

func TestFieldInfoToPropertyRef(t *testing.T) {
	gen := New(defaultTitle, "1.0.0", defaultDescription)

	t.Run("struct_field_is_ref", func(t *testing.T) {
		prop := gen.fieldInfoToProperty(&models.FieldInfo{Name: "Addr", Type: "Address", RefName: "Address"})
		assert.Equal(t, refPath("Address"), prop.Ref)
		assert.Empty(t, prop.Type, "a $ref must not carry a sibling type")
		assert.Nil(t, prop.Items)
	})
	t.Run("slice_of_struct_is_items_ref", func(t *testing.T) {
		prop := gen.fieldInfoToProperty(&models.FieldInfo{Name: "Addrs", Type: "[]Address", RefName: "Address"})
		assert.Equal(t, typeArray, prop.Type)
		require.NotNil(t, prop.Items)
		assert.Equal(t, refPath("Address"), prop.Items.Ref)
		assert.Empty(t, prop.Ref)
	})
	t.Run("slice_of_pointer_struct_is_items_ref", func(t *testing.T) {
		prop := gen.fieldInfoToProperty(&models.FieldInfo{Name: "Reports", Type: "[]*User", RefName: "User"})
		assert.Equal(t, typeArray, prop.Type)
		require.NotNil(t, prop.Items)
		assert.Equal(t, refPath("User"), prop.Items.Ref)
	})
	t.Run("slice_of_ref_keeps_array_level_docs", func(t *testing.T) {
		prop := gen.fieldInfoToProperty(&models.FieldInfo{
			Name: "Addrs", Type: "[]Address", RefName: "Address",
			Description: "the user's addresses", Example: "n/a",
		})
		assert.Equal(t, typeArray, prop.Type)
		assert.Equal(t, "the user's addresses", prop.Description, "array wrapper keeps the field description")
		assert.Equal(t, "n/a", prop.Example)
		require.NotNil(t, prop.Items)
		assert.Equal(t, refPath("Address"), prop.Items.Ref)
		assert.Empty(t, prop.Items.Description, "the inner $ref must stand alone")
	})
	t.Run("non_ref_field_keeps_type_and_constraints", func(t *testing.T) {
		prop := gen.fieldInfoToProperty(&models.FieldInfo{
			Name: "Name", Type: "string",
			Constraints: map[string]string{"min": "2"},
		})
		assert.Equal(t, typeString, prop.Type)
		assert.Empty(t, prop.Ref)
		require.NotNil(t, prop.MinLength)
		assert.Equal(t, 2, *prop.MinLength)
	})
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
			assertOptionalString(t, "format", tt.expectedFormat, prop.Format)
			assertOptionalString(t, "description", tt.expectedDesc, prop.Description)
			assertOptionalAny(t, "example", tt.expectedExample, prop.Example)
			if tt.hasMinLength {
				assertOptionalPtr(t, "MinLength", &tt.minLengthValue, prop.MinLength)
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
			assertOptionalString(t, "format", tt.expectedFormat, prop.Format)
			if !tt.hasItems {
				return
			}
			if prop.Items == nil {
				t.Error("Expected Items to be set for array type")
				return
			}
			if prop.Items.Type != tt.itemType {
				t.Errorf("Expected item type %q, got %q", tt.itemType, prop.Items.Type)
			}
		})
	}
}

func TestSetTypeAndFormatWellKnownTypes(t *testing.T) {
	gen := New(defaultTitle, "1.0.0", defaultDescription)
	tests := []struct {
		name, goType, wantType, wantFormat string
	}{
		{"time.Time", goTypeTimeTime, typeString, formatDateTime},
		{"pointer time.Time", "*" + goTypeTimeTime, typeString, formatDateTime},
		// encoding/json marshals a Duration as its int64 ns count, NOT a string.
		{"time.Duration", goTypeTimeDuration, typeInteger, formatInt64},
		{"byte slice", goTypeByteSlice, typeString, formatBinary},
		{"uint8 slice alias", goTypeUint8Slice, typeString, formatBinary},
		{"uuid.UUID", goTypeUUID, typeString, formatUUID},
		{"json.RawMessage", goTypeRawMessage, typeObject, ""},
		// []byte must win over the generic []T array branch (not become an array).
		{"byte slice not array", goTypeByteSlice, typeString, formatBinary},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			prop := &OpenAPIProperty{}
			gen.setTypeAndFormat(prop, tt.goType)
			assert.Equal(t, tt.wantType, prop.Type)
			assert.Equal(t, tt.wantFormat, prop.Format)
			assert.Nil(t, prop.Items, "well-known types must not be modeled as arrays")
		})
	}
}

func TestSetTypeAndFormatUnsignedMinimum(t *testing.T) {
	gen := New(defaultTitle, "1.0.0", defaultDescription)
	// Unsigned integers carry minimum:0; signed integers do not.
	for _, ut := range []struct {
		goType, format string
	}{{"uint", formatInt32}, {"uint8", formatInt32}, {"uint16", formatInt32}, {"uint32", formatInt32}, {"uint64", formatInt64}} {
		prop := &OpenAPIProperty{}
		gen.setTypeAndFormat(prop, ut.goType)
		assert.Equal(t, typeInteger, prop.Type, ut.goType)
		assert.Equal(t, ut.format, prop.Format, ut.goType)
		if assert.NotNil(t, prop.Minimum, "%s must carry minimum:0", ut.goType) {
			assert.Equal(t, 0.0, *prop.Minimum, ut.goType)
		}
	}
	for _, st := range []string{"int", "int8", "int16", "int32", "int64"} {
		prop := &OpenAPIProperty{}
		gen.setTypeAndFormat(prop, st)
		assert.Nil(t, prop.Minimum, "%s (signed) must NOT carry minimum", st)
	}
}

func TestSetTypeAndFormatMaps(t *testing.T) {
	gen := New(defaultTitle, "1.0.0", defaultDescription)

	t.Run("primitive value", func(t *testing.T) {
		prop := &OpenAPIProperty{}
		gen.setTypeAndFormat(prop, "map[string]string")
		assert.Equal(t, typeObject, prop.Type)
		require.NotNil(t, prop.AdditionalProperties)
		assert.Equal(t, typeString, prop.AdditionalProperties.Type)
	})

	t.Run("integer value carries format", func(t *testing.T) {
		prop := &OpenAPIProperty{}
		gen.setTypeAndFormat(prop, "map[string]int64")
		require.NotNil(t, prop.AdditionalProperties)
		assert.Equal(t, typeInteger, prop.AdditionalProperties.Type)
		assert.Equal(t, formatInt64, prop.AdditionalProperties.Format)
	})
}

func TestFieldInfoToPropertyMapValueRef(t *testing.T) {
	gen := New(defaultTitle, "1.0.0", defaultDescription)

	t.Run("struct value", func(t *testing.T) {
		// object + additionalProperties.$ref (NOT a bare $ref for the whole field,
		// which is the RefName path).
		prop := gen.fieldInfoToProperty(&models.FieldInfo{
			Name: "Addrs", Type: "map[string]Address", JSONName: "addrs", MapValueRefName: "Address",
		})
		assert.Equal(t, typeObject, prop.Type)
		assert.Empty(t, prop.Ref, "a map field must not be a whole-field $ref")
		require.NotNil(t, prop.AdditionalProperties)
		assert.Equal(t, refPath("Address"), prop.AdditionalProperties.Ref)
	})

	t.Run("slice-of-struct value wraps in array", func(t *testing.T) {
		// map[string][]Address -> additionalProperties is an ARRAY of $ref, not a
		// bare $ref (the array layer must survive).
		prop := gen.fieldInfoToProperty(&models.FieldInfo{
			Name: "History", Type: "map[string][]Address", JSONName: "history", MapValueRefName: "Address",
		})
		assert.Equal(t, typeObject, prop.Type)
		require.NotNil(t, prop.AdditionalProperties)
		assert.Equal(t, typeArray, prop.AdditionalProperties.Type)
		assert.Empty(t, prop.AdditionalProperties.Ref, "the array wrapper itself is not a $ref")
		require.NotNil(t, prop.AdditionalProperties.Items)
		assert.Equal(t, refPath("Address"), prop.AdditionalProperties.Items.Ref)
	})
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

			assertOptionalString(t, "format", tt.expectedFormat, prop.Format)
			assertOptionalPtr(t, "MinLength", tt.expectedMinLength, prop.MinLength)
			assertOptionalPtr(t, "MaxLength", tt.expectedMaxLength, prop.MaxLength)
			assertOptionalPtr(t, "Minimum", tt.expectedMinimum, prop.Minimum)
			assertOptionalPtr(t, "Maximum", tt.expectedMaximum, prop.Maximum)
			assertOptionalPtr(t, "ExclusiveMinimum", tt.expectedExclusiveMin, prop.ExclusiveMinimum)
			assertOptionalString(t, "pattern", tt.expectedPattern, prop.Pattern)
			if tt.expectedEnumCount > 0 && len(prop.Enum) != tt.expectedEnumCount {
				t.Errorf("Expected %d enum values, got %d", tt.expectedEnumCount, len(prop.Enum))
			}
		})
	}
}

// assertOptionalPtr asserts that an optional pointer field equals the expected
// value. When the expected pointer is nil, no assertion is performed (the
// caller didn't pin a value). When set but the actual is nil, or values
// differ, a t.Errorf is raised that names the field for diagnosis.
func assertOptionalPtr[T comparable](t *testing.T, name string, expected, actual *T) {
	t.Helper()
	if expected == nil {
		return
	}
	if actual == nil {
		t.Errorf("Expected %s to be set", name)
		return
	}
	if *actual != *expected {
		t.Errorf("Expected %s %v, got %v", name, *expected, *actual)
	}
}

// assertOptionalString asserts equality of a string field only when the
// expected value is non-empty. The empty-string sentinel mirrors the existing
// table convention ("" means caller didn't pin a value").
func assertOptionalString(t *testing.T, name, expected, actual string) {
	t.Helper()
	if expected == "" {
		return
	}
	if actual != expected {
		t.Errorf("Expected %s %q, got %q", name, expected, actual)
	}
}

// assertOptionalAny asserts equality of an any-typed field only when expected
// is non-nil. Both sides must be comparable (strings, numbers, etc.) — this
// helper preserves the original test's direct == comparison semantics.
func assertOptionalAny(t *testing.T, name string, expected, actual any) {
	t.Helper()
	if expected == nil {
		return
	}
	if actual != expected {
		t.Errorf("Expected %s %v, got %v", name, expected, actual)
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

	// Check that generated schemas exist. SuccessResponse is intentionally absent:
	// typed routes use the inline data/meta envelope, and the generic SuccessResponse
	// is gated to JOSE-untyped fallbacks (no-unused-components).
	schemaNames := []string{"CreateUserReq", "User", "ErrorResponse"}
	for _, name := range schemaNames {
		if _, exists := components[name]; !exists {
			t.Errorf("Missing schema: %s", name)
		}
	}
	if _, exists := components["SuccessResponse"]; exists {
		t.Error("SuccessResponse should be gated out for non-JOSE typed routes")
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

// mustMarshalYAML marshals v with the same 2-space indent the generator uses,
// so tests can assert on the exact serialized form the single yaml.Marshal path
// produces (replacing the old hand-rolled text-writer assertions).
func mustMarshalYAML(t *testing.T, v any) string {
	t.Helper()
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(v); err != nil {
		t.Fatalf("marshal yaml: %v", err)
	}
	if err := enc.Close(); err != nil {
		t.Fatalf("close yaml encoder: %v", err)
	}
	return buf.String()
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
			checkParam:        checkPathParameter,
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
			checkParam:        checkQueryParameters,
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
			checkParam:        checkHeaderParameter,
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
			checkParam:        checkMixedParameters,
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
			checkParam:        checkParameterWithExample,
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

// Per-case parameter verifiers for TestExtractParameters.

func checkPathParameter(t *testing.T, params []Parameter) {
	if params[0].Name != "id" {
		t.Errorf("Expected param name 'id', got %q", params[0].Name)
	}
	if params[0].In != "path" {
		t.Errorf("Expected param in 'path', got %q", params[0].In)
	}
	if !params[0].Required {
		t.Error("Path parameter should be required")
	}
}

func checkQueryParameters(t *testing.T, params []Parameter) {
	if params[0].Name != "page" {
		t.Errorf("Expected first param 'page', got %q", params[0].Name)
	}
	if params[0].Description != pageNumberDescription {
		t.Errorf("Expected description 'Page number', got %q", params[0].Description)
	}
}

func checkHeaderParameter(t *testing.T, params []Parameter) {
	if params[0].In != "header" {
		t.Errorf("Expected param in 'header', got %q", params[0].In)
	}
	if params[0].Name != "Content-Type" {
		t.Errorf("Expected param name 'Content-Type', got %q", params[0].Name)
	}
}

func checkMixedParameters(t *testing.T, params []Parameter) {
	// Should have path and query params, not body fields
	paramNames := make(map[string]bool, len(params))
	for _, p := range params {
		paramNames[p.Name] = true
	}
	if !paramNames["id"] {
		t.Error("Expected 'id' path parameter")
	}
	if !paramNames["async"] {
		t.Error("Expected 'async' query parameter")
	}
}

func checkParameterWithExample(t *testing.T, params []Parameter) {
	if params[0].Example == nil {
		t.Error("Expected parameter to have example")
	}
}

func TestParameterMarshaling(t *testing.T) {
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
				// example values come from string struct tags, so the single
				// yaml.Marshal path now (correctly) quotes them as strings.
				"example: \"1\"",
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
			// Parameters now serialize through the single yaml.Marshal path; marshal
			// them under a "parameters:" key to mirror their position in an operation.
			output := mustMarshalYAML(t, struct {
				Parameters []Parameter `yaml:"parameters"`
			}{Parameters: tt.params})

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

func TestBuildRequestBody(t *testing.T) {
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
			output := mustMarshalYAML(t, map[string]*OpenAPIRequestBody{
				"requestBody": gen.buildRequestBody(&models.TypeInfo{Name: tt.schemaName}),
			})

			for _, expected := range tt.expectedOutput {
				if !strings.Contains(output, expected) {
					t.Errorf("Expected output to contain %q.\nOutput:\n%s", expected, output)
				}
			}
		})
	}
}

// TestPropertySchemaMarshaling proves the single yaml.Marshal path (PR2) honors
// $ref, items.$ref, and the constraint fields the old hand-rolled writer emitted
// by hand — both inline (in an operation/parameter) and under components, since
// it is the same OpenAPIProperty type in either position.
func TestPropertySchemaMarshaling(t *testing.T) {
	tests := []struct {
		name     string
		prop     *OpenAPIProperty
		expected []string
	}{
		{
			name:     "ref",
			prop:     &OpenAPIProperty{Ref: refPath("Address")},
			expected: []string{"$ref: '#/components/schemas/Address'"},
		},
		{
			name:     "array of refs",
			prop:     &OpenAPIProperty{Type: typeArray, Items: &OpenAPIProperty{Ref: refPath("Tag")}},
			expected: []string{"type: array", "items:", "$ref: '#/components/schemas/Tag'"},
		},
		{
			name:     "integer with numeric constraints",
			prop:     &OpenAPIProperty{Type: typeInteger, Format: formatInt64, Minimum: float64Ptr(1.0), Maximum: float64Ptr(100.0)},
			expected: []string{"type: integer", "format: int64", "minimum: 1", "maximum: 100"},
		},
		{
			name:     "string length and exclusive bound",
			prop:     &OpenAPIProperty{Type: typeString, MinLength: intPtr(3), MaxLength: intPtr(50), ExclusiveMinimum: boolPtr(true)},
			expected: []string{"type: string", "minLength: 3", "maxLength: 50", "exclusiveMinimum: true"},
		},
		{
			// Locks pattern serialization (the yaml tag) — split into two substrings
			// so the assertion holds whether or not yaml.v3 quotes the regex.
			name:     "pattern",
			prop:     &OpenAPIProperty{Type: typeString, Pattern: "^[a-zA-Z]+$"},
			expected: []string{"pattern:", "^[a-zA-Z]+$"},
		},
		{
			name:     "number with exclusive maximum",
			prop:     &OpenAPIProperty{Type: typeNumber, Maximum: float64Ptr(100.0), ExclusiveMaximum: boolPtr(true)},
			expected: []string{"type: number", "maximum: 100", "exclusiveMaximum: true"},
		},
		{
			name:     "enum",
			prop:     &OpenAPIProperty{Type: typeString, Enum: []any{"red", "green", "blue"}},
			expected: []string{"enum:", "- red", "- green", "- blue"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out := mustMarshalYAML(t, tt.prop)
			for _, want := range tt.expected {
				assert.Contains(t, out, want, "marshaled schema:\n%s", out)
			}
		})
	}
}

// TestAssignOperationByMethod verifies each HTTP method routes to the correct
// path-item field, and that an unrecognized method is a no-op (the analyzer only
// emits the standard methods, but the switch must not panic on anything else).
func TestAssignOperationByMethod(t *testing.T) {
	gen := New(defaultTitle, "1.0.0", defaultDescription)
	cases := []struct {
		method string
		op     func(*OpenAPIPathItem) *OpenAPIOperation
	}{
		{httpMethodGet, func(p *OpenAPIPathItem) *OpenAPIOperation { return p.Get }},
		{httpMethodPut, func(p *OpenAPIPathItem) *OpenAPIOperation { return p.Put }},
		{httpMethodPost, func(p *OpenAPIPathItem) *OpenAPIOperation { return p.Post }},
		{httpMethodDelete, func(p *OpenAPIPathItem) *OpenAPIOperation { return p.Delete }},
		{httpMethodPatch, func(p *OpenAPIPathItem) *OpenAPIOperation { return p.Patch }},
		{httpMethodHead, func(p *OpenAPIPathItem) *OpenAPIOperation { return p.Head }},
		{httpMethodOptions, func(p *OpenAPIPathItem) *OpenAPIOperation { return p.Options }},
	}
	for _, tc := range cases {
		t.Run(tc.method, func(t *testing.T) {
			item := &OpenAPIPathItem{}
			gen.assignOperation(item, &models.Route{Method: tc.method, Path: "/x", HandlerName: "h"}, nil)
			assert.NotNil(t, tc.op(item), "operation should be attached under %s", tc.method)
		})
	}

	t.Run("unknown_method_noop", func(t *testing.T) {
		item := &OpenAPIPathItem{}
		gen.assignOperation(item, &models.Route{Method: "TRACE", Path: "/x", HandlerName: "h"}, nil)
		assert.Nil(t, item.Get)
		assert.Nil(t, item.Post)
	})
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

func TestBuildRequestBodyJOSEContentType(t *testing.T) {
	gen := New(defaultTitle, "1.0.0", defaultDescription)
	out := mustMarshalYAML(t, map[string]*OpenAPIRequestBody{
		"requestBody": gen.buildRequestBody(&models.TypeInfo{Name: "CreateTokenRequest", JOSE: true}),
	})

	// Wire format: per OpenAPI 3.0.1, application/jose Media Type schema MUST describe
	// the on-the-wire payload (a string token), not the decrypted plaintext shape.
	assert.Contains(t, out, "application/jose:")
	assert.NotContains(t, out, "application/json:")
	assert.Contains(t, out, "type: string")
	assert.Contains(t, out, "format: jose")

	// Plaintext schema MUST NOT appear under content.application/jose — the spec
	// reserves Media Type Object schema for the wire shape only. The plaintext
	// component schema is still emitted (via generateSchemasFromTypes) and is
	// referenced from the description text.
	assert.NotContains(t, out, "application/jose:\n            schema:\n              $ref:")

	// Description on the parent RequestBody Object (spec-compliant location) names
	// the plaintext schema so consumers know what to expect after decrypt+verify.
	assert.Contains(t, out, "JOSE compact serialization")
	assert.Contains(t, out, "CreateTokenRequest schema")
	assert.Contains(t, out, "#/components/schemas/CreateTokenRequest")
}

func TestWriteMethodEmitsRequestBodyForJOSEEvenWithEmptyBodyFields(t *testing.T) {
	// Real bug caught by CodeRabbit: a JOSE-tagged request type whose plaintext
	// fields all live in path/query/header params (or are absent entirely) would
	// have produced no requestBody at all, even though the route still expects an
	// application/jose payload on the wire. The fix drives the requestBody emission
	// from `route.Request.JOSE || len(bodyFields) > 0`.
	gen := New(defaultTitle, "1.0.0", defaultDescription)
	spec, err := gen.Generate(&models.Project{
		Name:    "JOSEOnlyApp",
		Version: "1.0.0",
		Modules: []models.Module{{
			Name:    "vts",
			Package: "vts",
			Routes: []models.Route{{
				Method:      "POST",
				Path:        "/v1/tokens",
				HandlerName: "createToken",
				Request:     &models.TypeInfo{Name: "CreateTokenRequest", JOSE: true},
				Response:    &models.TypeInfo{Name: "CreateTokenResponse", JOSE: true},
			}},
		}},
	})
	assert.NoError(t, err)
	assert.Contains(t, spec, "requestBody:", "JOSE-only routes must still emit a requestBody")
	assert.Contains(t, spec, "application/jose:", "the JOSE requestBody must be present even with no plaintext body fields")
}

func TestBuildResponsesJOSEResponse(t *testing.T) {
	gen := New(defaultTitle, "1.0.0", defaultDescription)
	resps := gen.buildResponses(&models.Route{
		Method:   "POST",
		Path:     "/v1/tokens",
		Response: &models.TypeInfo{Name: "CreateTokenResponse", JOSE: true},
	})

	// JOSE-flagged 200 response uses application/jose with a string-token schema.
	// require the content-type key first so a regression fails cleanly here rather
	// than panicking on a nil media-type deref below.
	require.Contains(t, resps["200"].Content, mediaJOSE, "JOSE-flagged 200 response must use application/jose")
	assert.Equal(t, typeString, resps["200"].Content[mediaJOSE].Schema.Type)
	assert.Equal(t, "jose", resps["200"].Content[mediaJOSE].Schema.Format)

	// Pre-trust failure path: peer is unauthenticated so the envelope must be the
	// minimal JOSEErrorEnvelope, NOT the framework's standard ErrorResponse (which
	// carries traceId/meta and would leak fingerprint information).
	require.Contains(t, resps["400"].Content, mediaJSON)
	assert.Equal(t, refPath("JOSEErrorEnvelope"), resps["400"].Content[mediaJSON].Schema.Ref,
		"JOSE 4xx response must reference JOSEErrorEnvelope (security invariant)")
}

func TestBuildResponsesAsymmetricJOSERequestStillUsesJOSEErrorEnvelope(t *testing.T) {
	// The runtime enforces bidirectional symmetry (request and response must both
	// carry jose tags or neither). The analyzer runs statically against source —
	// it can encounter asymmetric setups that a developer is in the middle of
	// writing. In any such case the pre-trust failure path on the request side is
	// still routed through the JOSE plaintext-minimal envelope by the runtime, so
	// the OpenAPI 4xx schema must reference JOSEErrorEnvelope, NOT the standard
	// ErrorResponse (which would leak traceId/meta).
	gen := New(defaultTitle, "1.0.0", defaultDescription)
	resps := gen.buildResponses(&models.Route{
		Method:   "POST",
		Path:     "/v1/tokens",
		Request:  &models.TypeInfo{Name: "TokenRequest", JOSE: true},
		Response: &models.TypeInfo{Name: "TokenResponse", JOSE: false},
	})

	require.Contains(t, resps["400"].Content, mediaJSON)
	assert.Equal(t, refPath("JOSEErrorEnvelope"), resps["400"].Content[mediaJSON].Schema.Ref,
		"4xx path on JOSE-request route must reference JOSEErrorEnvelope (pre-trust failures fire on the request side)")
}

func TestBuildResponsesNonJOSE(t *testing.T) {
	gen := New(defaultTitle, "1.0.0", defaultDescription)
	resps := gen.buildResponses(&models.Route{
		Method:   "POST",
		Path:     "/v1/users",
		Response: &models.TypeInfo{Name: "User", JOSE: false},
	})

	// Non-JOSE success uses application/json and the inline {data,meta} envelope
	// whose data is a $ref to the response component (closes the orphan-component
	// window: the discovered User schema is finally referenced).
	require.Contains(t, resps, "200")
	assert.NotContains(t, resps["200"].Content, mediaJOSE, "non-JOSE response must NOT use application/jose")
	require.Contains(t, resps["200"].Content, mediaJSON)
	envelope := resps["200"].Content[mediaJSON].Schema
	require.NotNil(t, envelope)
	assert.Equal(t, typeObject, envelope.Type)
	require.Contains(t, envelope.Properties, propNameData)
	assert.Equal(t, refPath("User"), envelope.Properties[propNameData].Ref, "data must $ref the response component")
	require.Contains(t, envelope.Properties, propNameMeta)
	require.Contains(t, envelope.Properties[propNameMeta].Properties, "timestamp")
	require.Contains(t, envelope.Properties[propNameMeta].Properties, "traceId")

	// Standard error set: 400 and 500 always present; both reference ErrorResponse.
	require.Contains(t, resps["400"].Content, mediaJSON)
	assert.Equal(t, refPath(schemaErrorResponse), resps["400"].Content[mediaJSON].Schema.Ref,
		"non-JOSE 4xx must reference standard ErrorResponse")
	require.Contains(t, resps, "500")
	assert.Equal(t, refPath(schemaErrorResponse), resps["500"].Content[mediaJSON].Schema.Ref)

	// No request validation -> no 422.
	assert.NotContains(t, resps, "422", "route without a validated request must not advertise 422")
}

func TestBuildResponsesStatusCodes(t *testing.T) {
	gen := New(defaultTitle, "1.0.0", defaultDescription)

	t.Run("created_201", func(t *testing.T) {
		resps := gen.buildResponses(&models.Route{
			Method:        "POST",
			Response:      &models.TypeInfo{Name: "User"},
			SuccessStatus: 201,
		})
		require.Contains(t, resps, "201")
		assert.NotContains(t, resps, "200")
		assert.Equal(t, "Resource created successfully", resps["201"].Description)
	})

	t.Run("accepted_202", func(t *testing.T) {
		resps := gen.buildResponses(&models.Route{Method: "POST", SuccessStatus: 202})
		require.Contains(t, resps, "202")
		assert.Equal(t, "Request accepted for processing", resps["202"].Description)
	})

	t.Run("no_content_204_has_no_body", func(t *testing.T) {
		resps := gen.buildResponses(&models.Route{
			Method:   "DELETE",
			Response: &models.TypeInfo{NoContent: true},
		})
		require.Contains(t, resps, "204")
		assert.Nil(t, resps["204"].Content, "204 must not carry a response body")
		assert.Equal(t, "No Content", resps["204"].Description)
	})

	t.Run("custom_status_from_NewResult", func(t *testing.T) {
		resps := gen.buildResponses(&models.Route{Method: "GET", SuccessStatus: 207})
		require.Contains(t, resps, "207")
	})
}

func TestBuildResponsesRawMode(t *testing.T) {
	gen := New(defaultTitle, "1.0.0", defaultDescription)
	resps := gen.buildResponses(&models.Route{
		Method:      "GET",
		Response:    &models.TypeInfo{Name: "LegacyUser"},
		RawResponse: true,
	})

	// Raw mode emits the bare payload schema ($ref), NOT the data/meta envelope.
	schema := resps["200"].Content[mediaJSON].Schema
	require.NotNil(t, schema)
	assert.Equal(t, refPath("LegacyUser"), schema.Ref, "raw response must $ref the payload directly")
	assert.NotContains(t, schema.Properties, propNameData, "raw response must NOT wrap in a data/meta envelope")

	// Raw routes use the minimal RawErrorResponse for errors.
	assert.Equal(t, refPath(schemaRawErrorResponse), resps["400"].Content[mediaJSON].Schema.Ref)
	assert.Equal(t, refPath(schemaRawErrorResponse), resps["500"].Content[mediaJSON].Schema.Ref)
}

func TestResponsePayloadSchemaFallbacks(t *testing.T) {
	// nil and unnamed responses fall back to a generic object (no $ref).
	for _, ti := range []*models.TypeInfo{nil, {Name: ""}} {
		schema := responsePayloadSchema(ti)
		assert.Equal(t, typeObject, schema.Type)
		assert.Empty(t, schema.Ref)
	}
	// Named responses become a $ref.
	assert.Equal(t, refPath("Widget"), responsePayloadSchema(&models.TypeInfo{Name: "Widget"}).Ref)
}

func TestSuccessPlaintextSchemaFallsBackToSuccessResponse(t *testing.T) {
	gen := New(defaultTitle, "1.0.0", defaultDescription)
	// A JOSE response with no named type falls back to the generic envelope name.
	got := gen.successPlaintextSchema(&models.Route{Response: &models.TypeInfo{JOSE: true}})
	assert.Equal(t, schemaSuccessResponse, got)
	// A named response is used verbatim.
	got = gen.successPlaintextSchema(&models.Route{Response: &models.TypeInfo{Name: "TokenResponse"}})
	assert.Equal(t, "TokenResponse", got)
}

func TestBuildResponsesValidationAdds422(t *testing.T) {
	gen := New(defaultTitle, "1.0.0", defaultDescription)
	resps := gen.buildResponses(&models.Route{
		Method: "POST",
		Request: &models.TypeInfo{Name: "CreateReq", Fields: []models.FieldInfo{
			{Name: "Email", Required: true},
		}},
		Response: &models.TypeInfo{Name: "User"},
	})
	require.Contains(t, resps, "422", "a validated request body must advertise 422")
	assert.Equal(t, refPath(schemaErrorResponse), resps["422"].Content[mediaJSON].Schema.Ref)
}

// TestAssignOperationIDs covers module-qualification, explicit WithHandlerName
// override, and deterministic de-duplication.
func TestAssignOperationIDs(t *testing.T) {
	gen := New(defaultTitle, "1.0.0", defaultDescription)
	routes := []models.Route{
		{Method: "GET", Path: "/users/:id", Module: "users", HandlerName: "get"},
		{Method: "GET", Path: "/orders/:id", Module: "orders", HandlerName: "get"},
		{Method: "POST", Path: "/jobs", Module: "jobs", HandlerName: "create", OperationID: "enqueueJob"},
		{Method: "GET", Path: "/bare"}, // no module/handler -> method+path fallback
	}
	ids := gen.assignOperationIDs(routes)

	assert.Equal(t, "usersGet", ids["GET /users/:id"], "module-qualified")
	assert.Equal(t, "ordersGet", ids["GET /orders/:id"], "different module -> distinct, no collision")
	assert.Equal(t, "enqueueJob", ids["POST /jobs"], "explicit WithHandlerName wins verbatim")
	assert.Equal(t, "get_bare", ids["GET /bare"], "no handler -> method+path fallback")

	// All ids are unique.
	seen := map[string]bool{}
	for _, id := range ids {
		assert.False(t, seen[id], "duplicate operationId %q", id)
		seen[id] = true
	}
}

// TestAssignOperationIDsDedup verifies a deterministic numeric suffix when two
// routes derive the same base id (same module + handler).
func TestAssignOperationIDsDedup(t *testing.T) {
	gen := New(defaultTitle, "1.0.0", defaultDescription)
	routes := []models.Route{
		{Method: "GET", Path: "/a", Module: "m", HandlerName: "list"},
		{Method: "GET", Path: "/b", Module: "m", HandlerName: "list"},
	}
	ids := gen.assignOperationIDs(routes)
	// Deterministic, sorted first-wins: "GET /a" sorts before "GET /b", so /a keeps
	// the bare id and /b gets the suffix.
	assert.Equal(t, "mList", ids["GET /a"], "first sorted key keeps the bare id")
	assert.Equal(t, "mList2", ids["GET /b"], "later collision gets the numeric suffix")
}

// TestFieldInfoToPropertyConstraintsPR11 covers PR11's constraint additions:
// numeric exclusive bounds, string length comparisons, named-numeric via
// UnderlyingKind, slice cardinality, and dive element constraints.
func TestFieldInfoToPropertyConstraintsPR11(t *testing.T) {
	gen := New(defaultTitle, "1.0.0", defaultDescription)

	t.Run("numeric lt -> maximum + exclusiveMaximum", func(t *testing.T) {
		p := gen.fieldInfoToProperty(&models.FieldInfo{Type: "int", JSONName: "n", Constraints: map[string]string{"lt": "100"}})
		require.NotNil(t, p.Maximum)
		assert.Equal(t, 100.0, *p.Maximum)
		require.NotNil(t, p.ExclusiveMaximum)
		assert.True(t, *p.ExclusiveMaximum)
	})

	t.Run("string gt -> minLength, not minimum", func(t *testing.T) {
		p := gen.fieldInfoToProperty(&models.FieldInfo{Type: "string", JSONName: "s", Constraints: map[string]string{"gt": "3"}})
		require.NotNil(t, p.MinLength)
		assert.Equal(t, 4, *p.MinLength)
		assert.Nil(t, p.Minimum, "a string gt must not leak a numeric minimum")
	})

	t.Run("named numeric via UnderlyingKind -> minimum/maximum", func(t *testing.T) {
		p := gen.fieldInfoToProperty(&models.FieldInfo{Type: "Cents", UnderlyingKind: "integer", JSONName: "amt", Constraints: map[string]string{"min": "1", "max": "100"}})
		require.NotNil(t, p.Minimum)
		assert.Equal(t, 1.0, *p.Minimum)
		require.NotNil(t, p.Maximum)
		assert.Equal(t, 100.0, *p.Maximum)
	})

	t.Run("slice cardinality + dive element format", func(t *testing.T) {
		p := gen.fieldInfoToProperty(&models.FieldInfo{
			Type: "[]string", JSONName: "tags",
			Constraints:        map[string]string{"min": "1", "max": "10"},
			ElementConstraints: map[string]string{"email": "true"},
		})
		assert.Equal(t, typeArray, p.Type)
		require.NotNil(t, p.MinItems)
		assert.Equal(t, 1, *p.MinItems)
		require.NotNil(t, p.MaxItems)
		assert.Equal(t, 10, *p.MaxItems)
		require.NotNil(t, p.Items)
		assert.Equal(t, "email", p.Items.Format, "dive,email puts format:email under items")
	})

	t.Run("named-scalar slice element keeps numeric kind (dive)", func(t *testing.T) {
		// []Cents -> field.UnderlyingKind=="integer" (analyzer strips the slice), so
		// dive,gte=0 maps to a numeric minimum on the items, not a dropped constraint.
		p := gen.fieldInfoToProperty(&models.FieldInfo{
			Type: "[]Cents", UnderlyingKind: "integer", JSONName: "amounts",
			ElementConstraints: map[string]string{"gte": "0"},
		})
		assert.Equal(t, typeArray, p.Type)
		require.NotNil(t, p.Items)
		require.NotNil(t, p.Items.Minimum, "element gte must map to a numeric minimum on items")
		assert.Equal(t, 0.0, *p.Items.Minimum)
	})

	t.Run("ref-slice carries minItems on the array wrapper", func(t *testing.T) {
		p := gen.fieldInfoToProperty(&models.FieldInfo{
			Type: "[]Address", RefName: "Address", JSONName: "addrs",
			Constraints: map[string]string{"min": "1"},
		})
		assert.Equal(t, typeArray, p.Type)
		require.NotNil(t, p.Items)
		assert.Equal(t, refPath("Address"), p.Items.Ref, "$ref element stands alone")
		require.NotNil(t, p.MinItems)
		assert.Equal(t, 1, *p.MinItems)
	})
}

// TestNewWithConfig covers the CLI-driven document metadata: custom servers,
// license, and the tenant-security opt-out.
func TestNewWithConfig(t *testing.T) {
	t.Run("custom servers + license", func(t *testing.T) {
		gen := NewWithConfig(&Config{
			Title: "My API", Version: "2.1.0", Description: "d",
			Servers: []string{"https://api.example.com", "https://staging.example.com"},
			License: &License{Name: "MIT", URL: "https://opensource.org/licenses/MIT"},
		})
		spec, err := gen.Generate(&models.Project{})
		require.NoError(t, err)
		assert.Contains(t, spec, "title: My API")
		assert.Contains(t, spec, "url: https://api.example.com")
		assert.Contains(t, spec, "url: https://staging.example.com")
		assert.Contains(t, spec, "name: MIT")
		assert.Contains(t, spec, "url: https://opensource.org/licenses/MIT")
		assert.Contains(t, spec, "X-Tenant-ID", "tenant security on by default")
	})

	t.Run("tenant security opt-out", func(t *testing.T) {
		gen := NewWithConfig(&Config{Title: "T", Version: "1.0.0", DisableTenantSecurity: true})
		spec, err := gen.Generate(&models.Project{})
		require.NoError(t, err)
		assert.NotContains(t, spec, "X-Tenant-ID", "opt-out omits the tenant scheme")
		assert.NotContains(t, spec, "securitySchemes", "no schemes when security disabled")
		// Default relative-root server still present.
		assert.Contains(t, spec, "url: /")
	})

	t.Run("blank server is dropped, not emitted as empty url", func(t *testing.T) {
		// A stray `--server ""` (e.g. an unset shell var) must not produce an
		// invalid `url: ""` Server Object; the valid entry survives.
		gen := NewWithConfig(&Config{
			Title: "T", Version: "1.0.0",
			Servers: []string{"  ", "https://api.example.com", ""},
		})
		spec, err := gen.Generate(&models.Project{})
		require.NoError(t, err)
		assert.Contains(t, spec, "url: https://api.example.com")
		assert.NotContains(t, spec, `url: ""`, "blank server must not be emitted")
	})

	t.Run("all-blank servers fall back to relative-root default", func(t *testing.T) {
		gen := NewWithConfig(&Config{
			Title: "T", Version: "1.0.0",
			Servers: []string{"", "   "},
		})
		spec, err := gen.Generate(&models.Project{})
		require.NoError(t, err)
		assert.Contains(t, spec, "url: /", "default server restored when none valid")
		assert.NotContains(t, spec, `url: ""`)
	})

	t.Run("generator is immutable after construction", func(t *testing.T) {
		// Mutating the caller's Config (or the slice/license it shared) after
		// construction must not change the generator's output.
		cfg := &Config{
			Title: "T", Version: "1.0.0",
			Servers: []string{"https://orig.example.com"},
			License: &License{Name: "MIT", URL: "https://mit"},
		}
		gen := NewWithConfig(cfg)
		cfg.Servers[0] = "https://mutated.example.com"
		cfg.License.Name = "MUTATED"

		spec, err := gen.Generate(&models.Project{})
		require.NoError(t, err)
		assert.Contains(t, spec, "url: https://orig.example.com", "server slice must be copied")
		assert.NotContains(t, spec, "mutated.example.com")
		assert.Contains(t, spec, "name: MIT", "license must be copied")
		assert.NotContains(t, spec, "MUTATED")
	})
}
