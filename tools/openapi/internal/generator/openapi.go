package generator

import (
	"bytes"
	"fmt"
	"sort"
	"strings"

	"github.com/gaborage/go-bricks/tools/openapi/internal/models"
	"gopkg.in/yaml.v3"
)

// OpenAPIGenerator creates OpenAPI specifications from project models
type OpenAPIGenerator struct {
	title       string
	version     string
	description string
}

// OpenAPIInfo represents the info section of an OpenAPI specification
type OpenAPIInfo struct {
	Title       string `yaml:"title"`
	Version     string `yaml:"version"`
	Description string `yaml:"description"`
}

// OpenAPISchema represents a schema definition
type OpenAPISchema struct {
	Type        string                      `yaml:"type"`
	Properties  map[string]*OpenAPIProperty `yaml:"properties,omitempty"`
	Required    []string                    `yaml:"required,omitempty"`
	Description string                      `yaml:"description,omitempty"`
}

// OpenAPIProperty represents a schema property
type OpenAPIProperty struct {
	Type        string `yaml:"type,omitempty"`
	Description string `yaml:"description,omitempty"`
	Ref         string `yaml:"$ref,omitempty"`
}

// New creates a new OpenAPI generator
func New(title, version, description string) *OpenAPIGenerator {
	return &OpenAPIGenerator{
		title:       title,
		version:     version,
		description: description,
	}
}

// Generate creates an OpenAPI YAML specification from a project
func (g *OpenAPIGenerator) Generate(project *models.Project) (string, error) {
	var sb strings.Builder

	if project == nil {
		project = &models.Project{}
	}

	// Header with proper YAML marshaling
	sb.WriteString("openapi: 3.0.1\n")

	// Marshal info section safely
	info := OpenAPIInfo{
		Title:       g.getTitle(project),
		Version:     g.getVersion(project),
		Description: g.getDescription(project),
	}

	infoYAML, err := g.marshalYAMLSection("info", info)
	if err != nil {
		return "", fmt.Errorf("failed to marshal info section: %w", err)
	}
	sb.WriteString(infoYAML)

	// Paths
	sb.WriteString("paths:\n")
	allRoutes := g.getAllRoutes(project)
	if len(allRoutes) == 0 {
		sb.WriteString("  {}\n")
	} else {
		g.writePaths(&sb, allRoutes)
	}

	// Components with proper YAML marshaling
	schemas := g.createStandardSchemas()
	components := map[string]any{
		"schemas": schemas,
	}

	componentsYAML, err := g.marshalYAMLSection("components", components)
	if err != nil {
		return "", fmt.Errorf("failed to marshal components section: %w", err)
	}
	sb.WriteString(componentsYAML)

	return sb.String(), nil
}

// getTitle returns the project title or default
func (g *OpenAPIGenerator) getTitle(project *models.Project) string {
	if project.Name != "" {
		return project.Name
	}
	return g.title
}

// getVersion returns the project version or default
func (g *OpenAPIGenerator) getVersion(project *models.Project) string {
	if project.Version != "" {
		return project.Version
	}
	return g.version
}

// getDescription returns the project description or default
func (g *OpenAPIGenerator) getDescription(project *models.Project) string {
	if project.Description != "" {
		return project.Description
	}
	return g.description
}

// getAllRoutes flattens routes from all modules
func (g *OpenAPIGenerator) getAllRoutes(project *models.Project) []models.Route {
	var routes []models.Route
	for _, module := range project.Modules {
		routes = append(routes, module.Routes...)
	}
	return routes
}

// groupRoutesByPath groups routes by their path to avoid duplicate path keys in OpenAPI spec
func (g *OpenAPIGenerator) groupRoutesByPath(routes []models.Route) map[string][]models.Route {
	pathGroups := make(map[string][]models.Route)
	for i := range routes {
		path := routes[i].Path
		pathGroups[path] = append(pathGroups[path], routes[i])
	}
	return pathGroups
}

// writePaths writes all paths with grouped routes to avoid duplicate path keys
func (g *OpenAPIGenerator) writePaths(sb *strings.Builder, routes []models.Route) {
	pathGroups := g.groupRoutesByPath(routes)

	// Sort paths for consistent output
	paths := make([]string, 0, len(pathGroups))
	for path := range pathGroups {
		paths = append(paths, path)
	}
	sort.Strings(paths)

	// Write each path with all its methods
	for _, path := range paths {
		fmt.Fprintf(sb, "  %s:\n", path)
		routesForPath := pathGroups[path]
		for i := range routesForPath {
			g.writeMethod(sb, &routesForPath[i])
		}
	}
}

// writeMethod writes a single HTTP method under a path
func (g *OpenAPIGenerator) writeMethod(sb *strings.Builder, route *models.Route) {
	method := strings.ToLower(route.Method)

	fmt.Fprintf(sb, "    %s:\n", method)
	fmt.Fprintf(sb, "      operationId: %s\n", g.getOperationID(route))
	fmt.Fprintf(sb, "      summary: %s\n", g.getSummary(route))

	if route.Description != "" {
		fmt.Fprintf(sb, "      description: %s\n", route.Description)
	}

	if len(route.Tags) > 0 {
		sb.WriteString("      tags:\n")
		for _, tag := range route.Tags {
			fmt.Fprintf(sb, "        - %s\n", tag)
		}
	}

	// Responses
	sb.WriteString("      responses:\n")
	sb.WriteString("        '200':\n")
	fmt.Fprintf(sb, "          description: %s\n", g.getResponseDescription(route.Method))
	sb.WriteString("          content:\n")
	sb.WriteString("            application/json:\n")
	sb.WriteString("              schema:\n")
	sb.WriteString("                $ref: '#/components/schemas/SuccessResponse'\n")
	sb.WriteString("        '400':\n")
	sb.WriteString("          description: Bad Request\n")
	sb.WriteString("          content:\n")
	sb.WriteString("            application/json:\n")
	sb.WriteString("              schema:\n")
	sb.WriteString("                $ref: '#/components/schemas/ErrorResponse'\n")
}

// getOperationID generates an operation ID for a route
func (g *OpenAPIGenerator) getOperationID(route *models.Route) string {
	if route.HandlerName != "" {
		return route.HandlerName
	}
	// Fallback: create from method and path
	cleanPath := strings.ReplaceAll(route.Path, "/", "_")
	cleanPath = strings.ReplaceAll(cleanPath, ":", "")
	return fmt.Sprintf("%s%s", strings.ToLower(route.Method), cleanPath)
}

// getSummary returns the route summary or generates one
func (g *OpenAPIGenerator) getSummary(route *models.Route) string {
	if route.Summary != "" {
		return route.Summary
	}
	return fmt.Sprintf("%s %s", route.Method, route.Path)
}

// getResponseDescription returns a description based on HTTP method
func (g *OpenAPIGenerator) getResponseDescription(method string) string {
	switch method {
	case "GET":
		return "Successful response"
	case "POST":
		return "Resource created successfully"
	case "PUT":
		return "Resource updated successfully"
	case "DELETE":
		return "Resource deleted successfully"
	case "PATCH":
		return "Resource partially updated"
	default:
		return "Successful response"
	}
}

// marshalYAMLSection marshals a section with proper indentation
func (g *OpenAPIGenerator) marshalYAMLSection(sectionName string, data any) (string, error) {
	var buf bytes.Buffer
	encoder := yaml.NewEncoder(&buf)
	encoder.SetIndent(2)

	// Create a map with the section name as key
	section := map[string]any{
		sectionName: data,
	}

	err := encoder.Encode(section)
	if err != nil {
		return "", err
	}

	if err := encoder.Close(); err != nil {
		return "", fmt.Errorf("failed to close YAML encoder: %w", err)
	}

	return buf.String(), nil
}

// createStandardSchemas creates common response schemas using proper structs
func (g *OpenAPIGenerator) createStandardSchemas() map[string]*OpenAPISchema {
	return map[string]*OpenAPISchema{
		"SuccessResponse": {
			Type: "object",
			Properties: map[string]*OpenAPIProperty{
				"data": {
					Type:        "object",
					Description: "Response data",
				},
				"meta": {
					Type:        "object",
					Description: "Response metadata",
				},
			},
		},
		"ErrorResponse": {
			Type: "object",
			Properties: map[string]*OpenAPIProperty{
				"error": {
					Type: "object",
					// Note: nested properties would need recursive handling for full OpenAPI spec
					Description: "Error details with code and message",
				},
				"meta": {
					Type:        "object",
					Description: "Response metadata",
				},
			},
			Required: []string{"error"},
		},
	}
}
