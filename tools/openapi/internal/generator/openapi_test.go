package generator

import (
	"strings"
	"testing"

	"go-bricks/tools/openapi/internal/models"
)

func TestNew(t *testing.T) {
	gen := New("Test API", "1.0.0", "Test description")

	if gen == nil {
		t.Fatal("New() returned nil")
	}

	if gen.title != "Test API" {
		t.Errorf("Expected title 'Test API', got '%s'", gen.title)
	}
	if gen.version != "1.0.0" {
		t.Errorf("Expected version '1.0.0', got '%s'", gen.version)
	}
	if gen.description != "Test description" {
		t.Errorf("Expected description 'Test description', got '%s'", gen.description)
	}
}

func TestGenerate_EmptyProject(t *testing.T) {
	gen := New("Test API", "1.0.0", "Test description")
	project := &models.Project{}

	spec, err := gen.Generate(project)

	if err != nil {
		t.Fatalf("Generate() failed: %v", err)
	}

	// Check basic structure
	if !strings.Contains(spec, "openapi: 3.0.1") {
		t.Error("Missing OpenAPI version")
	}
	if !strings.Contains(spec, "title: Test API") {
		t.Error("Missing title")
	}
	if !strings.Contains(spec, "version: 1.0.0") {
		t.Error("Missing version")
	}
	if !strings.Contains(spec, "paths:\n  {}") {
		t.Error("Should have empty paths for no routes")
	}
	if !strings.Contains(spec, "components:") {
		t.Error("Missing components section")
	}
}

func TestGenerate_WithProjectMetadata(t *testing.T) {
	gen := New("Default API", "0.1.0", "Default description")
	project := &models.Project{
		Name:        "Custom API",
		Version:     "2.0.0",
		Description: "Custom description",
		Modules:     []models.Module{},
	}

	spec, err := gen.Generate(project)

	if err != nil {
		t.Fatalf("Generate() failed: %v", err)
	}

	// Should use project metadata over defaults
	if !strings.Contains(spec, "title: Custom API") {
		t.Error("Should use project title over default")
	}
	if !strings.Contains(spec, "version: 2.0.0") {
		t.Error("Should use project version over default")
	}
	if !strings.Contains(spec, "description: Custom description") {
		t.Error("Should use project description over default")
	}
}

func TestGenerate_WithRoutes(t *testing.T) {
	gen := New("Test API", "1.0.0", "Test description")
	project := &models.Project{
		Modules: []models.Module{
			{
				Name: "users",
				Routes: []models.Route{
					{
						Method:      "GET",
						Path:        "/users/:id",
						HandlerName: "getUser",
						Summary:     "Get user by ID",
						Description: "Retrieves a user by their unique identifier",
						Tags:        []string{"users"},
					},
					{
						Method: "POST",
						Path:   "/users",
						Tags:   []string{"users", "creation"},
					},
				},
			},
		},
	}

	spec, err := gen.Generate(project)

	if err != nil {
		t.Fatalf("Generate() failed: %v", err)
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
	gen := New("Test API", "1.0.0", "Test description")

	tests := []struct {
		name     string
		route    *models.Route
		expected string
	}{
		{
			name: "with handler name",
			route: &models.Route{
				Method:      "GET",
				Path:        "/users/:id",
				HandlerName: "getUser",
			},
			expected: "getUser",
		},
		{
			name: "without handler name",
			route: &models.Route{
				Method: "POST",
				Path:   "/users",
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
				t.Errorf("Expected %q, got %q", tt.expected, result)
			}
		})
	}
}

func TestGetSummary(t *testing.T) {
	gen := New("Test API", "1.0.0", "Test description")

	tests := []struct {
		name     string
		route    *models.Route
		expected string
	}{
		{
			name: "with summary",
			route: &models.Route{
				Method:  "GET",
				Path:    "/users",
				Summary: "List all users",
			},
			expected: "List all users",
		},
		{
			name: "without summary",
			route: &models.Route{
				Method: "POST",
				Path:   "/users",
			},
			expected: "POST /users",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := gen.getSummary(tt.route)
			if result != tt.expected {
				t.Errorf("Expected %q, got %q", tt.expected, result)
			}
		})
	}
}

func TestGetResponseDescription(t *testing.T) {
	gen := New("Test API", "1.0.0", "Test description")

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
				t.Errorf("Expected %q, got %q", tt.expected, result)
			}
		})
	}
}

func TestGetAllRoutes(t *testing.T) {
	gen := New("Test API", "1.0.0", "Test description")

	project := &models.Project{
		Modules: []models.Module{
			{
				Name: "users",
				Routes: []models.Route{
					{Method: "GET", Path: "/users"},
					{Method: "POST", Path: "/users"},
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

// Test that the generated YAML is valid and parseable
func TestGenerate_ValidYAML(t *testing.T) {
	gen := New("Test API", "1.0.0", "Test description")
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
		t.Fatalf("Generate() failed: %v", err)
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
