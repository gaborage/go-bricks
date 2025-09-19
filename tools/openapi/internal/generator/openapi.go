package generator

import (
	"fmt"
	"strings"

	"github.com/gaborage/go-bricks/tools/openapi/internal/models"
)

// OpenAPIGenerator creates OpenAPI specifications from project models
type OpenAPIGenerator struct {
	title       string
	version     string
	description string
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

	// Header
	sb.WriteString("openapi: 3.0.1\n")
	sb.WriteString("info:\n")
	sb.WriteString(fmt.Sprintf("  title: %s\n", g.getTitle(project)))
	sb.WriteString(fmt.Sprintf("  version: %s\n", g.getVersion(project)))
	sb.WriteString(fmt.Sprintf("  description: %s\n", g.getDescription(project)))

	// Paths
	sb.WriteString("paths:\n")
	if len(g.getAllRoutes(project)) == 0 {
		sb.WriteString("  {}\n")
	} else {
		for _, route := range g.getAllRoutes(project) {
			g.writeRoute(&sb, &route)
		}
	}

	// Components
	sb.WriteString("components:\n")
	sb.WriteString("  schemas:\n")
	g.writeStandardSchemas(&sb)

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

// writeRoute writes a single route to the string builder
func (g *OpenAPIGenerator) writeRoute(sb *strings.Builder, route *models.Route) {
	method := strings.ToLower(route.Method)

	fmt.Fprintf(sb, "  %s:\n", route.Path)
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

// writeStandardSchemas writes common response schemas
func (g *OpenAPIGenerator) writeStandardSchemas(sb *strings.Builder) {
	sb.WriteString("    SuccessResponse:\n")
	sb.WriteString("      type: object\n")
	sb.WriteString("      properties:\n")
	sb.WriteString("        data:\n")
	sb.WriteString("          type: object\n")
	sb.WriteString("          description: Response data\n")
	sb.WriteString("        meta:\n")
	sb.WriteString("          type: object\n")
	sb.WriteString("          description: Response metadata\n")
	sb.WriteString("    ErrorResponse:\n")
	sb.WriteString("      type: object\n")
	sb.WriteString("      properties:\n")
	sb.WriteString("        error:\n")
	sb.WriteString("          type: object\n")
	sb.WriteString("          properties:\n")
	sb.WriteString("            code:\n")
	sb.WriteString("              type: string\n")
	sb.WriteString("              description: Error code\n")
	sb.WriteString("            message:\n")
	sb.WriteString("              type: string\n")
	sb.WriteString("              description: Error message\n")
	sb.WriteString("          required:\n")
	sb.WriteString("            - code\n")
	sb.WriteString("            - message\n")
	sb.WriteString("        meta:\n")
	sb.WriteString("          type: object\n")
	sb.WriteString("          description: Response metadata\n")
	sb.WriteString("      required:\n")
	sb.WriteString("        - error\n")
}
