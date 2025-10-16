package generator

import (
	"bytes"
	"fmt"
	"maps"
	"sort"
	"strconv"
	"strings"

	"github.com/gaborage/go-bricks/tools/openapi/internal/analyzer"
	"github.com/gaborage/go-bricks/tools/openapi/internal/models"
	"gopkg.in/yaml.v3"
)

// OpenAPI type constants
const (
	typeInteger = "integer"
	typeObject  = "object"
	formatInt32 = "int32"
	formatInt64 = "int64"
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
	Type             string           `yaml:"type,omitempty"`
	Format           string           `yaml:"format,omitempty"`
	Description      string           `yaml:"description,omitempty"`
	Example          any              `yaml:"example,omitempty"`
	Ref              string           `yaml:"$ref,omitempty"`
	Items            *OpenAPIProperty `yaml:"items,omitempty"` // For arrays
	MinLength        *int             `yaml:"minLength,omitempty"`
	MaxLength        *int             `yaml:"maxLength,omitempty"`
	Minimum          *float64         `yaml:"minimum,omitempty"`
	Maximum          *float64         `yaml:"maximum,omitempty"`
	ExclusiveMinimum *bool            `yaml:"exclusiveMinimum,omitempty"`
	ExclusiveMaximum *bool            `yaml:"exclusiveMaximum,omitempty"`
	Pattern          string           `yaml:"pattern,omitempty"`
	Enum             []any            `yaml:"enum,omitempty"`
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
	standardSchemas := g.createStandardSchemas()
	generatedSchemas := g.generateSchemasFromTypes(allRoutes)

	// Merge schemas (generated schemas override standard if there's a conflict)
	schemas := make(map[string]*OpenAPISchema)
	maps.Copy(schemas, standardSchemas)
	maps.Copy(schemas, generatedSchemas)

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

// Parameter represents an OpenAPI parameter (path, query, or header)
type Parameter struct {
	Name        string
	In          string // "path", "query", "header"
	Required    bool
	Description string
	Schema      *OpenAPIProperty
	Example     any
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

	// Extract and write parameters (path, query, header)
	params, bodyFields := g.extractParameters(route)
	if len(params) > 0 {
		g.writeParameters(sb, params)
	}

	// Write request body if there are body fields
	if len(bodyFields) > 0 && route.Request != nil {
		g.writeRequestBody(sb, bodyFields, route.Request.Name)
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

// generateSchemasFromTypes creates OpenAPI schemas from discovered type information
func (g *OpenAPIGenerator) generateSchemasFromTypes(routes []models.Route) map[string]*OpenAPISchema {
	schemas := make(map[string]*OpenAPISchema)
	seen := make(map[string]bool)

	for i := range routes {
		// Generate schema for request type
		if routes[i].Request != nil && !seen[routes[i].Request.Name] {
			schema := g.typeInfoToSchema(routes[i].Request)
			if schema != nil {
				schemas[routes[i].Request.Name] = schema
				seen[routes[i].Request.Name] = true
			}
		}

		// Generate schema for response type
		if routes[i].Response != nil && !seen[routes[i].Response.Name] {
			schema := g.typeInfoToSchema(routes[i].Response)
			if schema != nil {
				schemas[routes[i].Response.Name] = schema
				seen[routes[i].Response.Name] = true
			}
		}
	}

	return schemas
}

// typeInfoToSchema converts a TypeInfo to an OpenAPI schema
func (g *OpenAPIGenerator) typeInfoToSchema(typeInfo *models.TypeInfo) *OpenAPISchema {
	if typeInfo == nil || len(typeInfo.Fields) == 0 {
		return nil
	}

	schema := &OpenAPISchema{
		Type:       "object",
		Properties: make(map[string]*OpenAPIProperty),
		Required:   []string{},
	}

	for i := range typeInfo.Fields {
		field := &typeInfo.Fields[i]

		// Skip fields explicitly marked with json:"-"
		if field.JSONName == "-" && field.ParamType == "" {
			continue
		}

		// Use field name as fallback if no json tag
		if field.JSONName == "" {
			field.JSONName = strings.ToLower(field.Name[:1]) + field.Name[1:]
		}

		// Use JSONName if set, otherwise use field name
		propName := field.JSONName
		if propName == "" {
			propName = strings.ToLower(field.Name[:1]) + field.Name[1:]
		}

		prop := g.fieldInfoToProperty(field)
		schema.Properties[propName] = prop

		// Add to required array if field is required
		if field.Required {
			schema.Required = append(schema.Required, propName)
		}
	}

	// Sort required fields for consistent output
	sort.Strings(schema.Required)

	return schema
}

// fieldInfoToProperty converts a FieldInfo to an OpenAPI property
func (g *OpenAPIGenerator) fieldInfoToProperty(field *models.FieldInfo) *OpenAPIProperty {
	prop := &OpenAPIProperty{
		Description: field.Description,
	}

	// Set example if present
	if field.Example != "" {
		prop.Example = field.Example
	}

	// Map Go type to OpenAPI type and format
	g.setTypeAndFormat(prop, field.Type)

	// Apply constraints from validation tags
	g.applyConstraints(prop, field)

	return prop
}

// setTypeAndFormat maps Go types to OpenAPI type and format
func (g *OpenAPIGenerator) setTypeAndFormat(prop *OpenAPIProperty, goType string) {
	// Strip pointer prefix
	goType = strings.TrimPrefix(goType, "*")

	// Handle arrays
	if strings.HasPrefix(goType, "[]") {
		prop.Type = "array"
		elementType := strings.TrimPrefix(goType, "[]")
		prop.Items = &OpenAPIProperty{}
		g.setTypeAndFormat(prop.Items, elementType)
		return
	}

	// Handle basic types
	switch goType {
	case "string":
		prop.Type = "string"
	case "int", "int8", "int16", "int32", "uint", "uint8", "uint16", "uint32":
		prop.Type = typeInteger
		prop.Format = formatInt32
	case "int64", "uint64":
		prop.Type = typeInteger
		prop.Format = formatInt64
	case "float32":
		prop.Type = "number"
		prop.Format = "float"
	case "float64":
		prop.Type = "number"
		prop.Format = "double"
	case "bool":
		prop.Type = "boolean"
	default:
		// Complex types (structs, maps, etc.) - use object or reference
		// Both maps and structs are represented as "object" in OpenAPI
		prop.Type = typeObject
	}
}

// applyConstraints applies validation constraints to an OpenAPI property
func (g *OpenAPIGenerator) applyConstraints(prop *OpenAPIProperty, field *models.FieldInfo) {
	if len(field.Constraints) == 0 {
		return
	}

	// Use the constraint mapper from analyzer package
	openAPIConstraints := analyzer.MapConstraintToOpenAPI(field.Type, field.Constraints)

	// Apply each constraint using specialized applicators
	for _, constraint := range openAPIConstraints {
		g.applyConstraint(prop, constraint)
	}
}

// applyConstraint routes a constraint to its specialized applicator
func (g *OpenAPIGenerator) applyConstraint(prop *OpenAPIProperty, constraint analyzer.OpenAPIConstraint) {
	// Map constraint names to applicator functions
	applicators := map[string]func(*OpenAPIProperty, any){
		"format":           applyFormatConstraint,
		"minLength":        applyMinLengthConstraint,
		"maxLength":        applyMaxLengthConstraint,
		"minimum":          applyMinimumConstraint,
		"maximum":          applyMaximumConstraint,
		"exclusiveMinimum": applyExclusiveMinimumConstraint,
		"exclusiveMaximum": applyExclusiveMaximumConstraint,
		"pattern":          applyPatternConstraint,
		"enum":             applyEnumConstraint,
	}

	if applicator, exists := applicators[constraint.Name]; exists {
		applicator(prop, constraint.Value)
	}
}

// applyFormatConstraint sets the format field
func applyFormatConstraint(prop *OpenAPIProperty, value any) {
	if str, ok := value.(string); ok {
		prop.Format = str
	}
}

// applyMinLengthConstraint sets the minLength field
func applyMinLengthConstraint(prop *OpenAPIProperty, value any) {
	if val, ok := value.(int); ok {
		prop.MinLength = &val
	}
}

// applyMaxLengthConstraint sets the maxLength field
func applyMaxLengthConstraint(prop *OpenAPIProperty, value any) {
	if val, ok := value.(int); ok {
		prop.MaxLength = &val
	}
}

// applyMinimumConstraint sets the minimum field with type conversion
func applyMinimumConstraint(prop *OpenAPIProperty, value any) {
	prop.Minimum = toFloat64Ptr(value)
}

// applyMaximumConstraint sets the maximum field with type conversion
func applyMaximumConstraint(prop *OpenAPIProperty, value any) {
	prop.Maximum = toFloat64Ptr(value)
}

// applyExclusiveMinimumConstraint sets the exclusiveMinimum field
func applyExclusiveMinimumConstraint(prop *OpenAPIProperty, value any) {
	if val, ok := value.(bool); ok {
		prop.ExclusiveMinimum = &val
	}
}

// applyExclusiveMaximumConstraint sets the exclusiveMaximum field
func applyExclusiveMaximumConstraint(prop *OpenAPIProperty, value any) {
	if val, ok := value.(bool); ok {
		prop.ExclusiveMaximum = &val
	}
}

// applyPatternConstraint sets the pattern field
func applyPatternConstraint(prop *OpenAPIProperty, value any) {
	if str, ok := value.(string); ok {
		prop.Pattern = str
	}
}

// applyEnumConstraint sets the enum field
func applyEnumConstraint(prop *OpenAPIProperty, value any) {
	if arr, ok := value.([]any); ok {
		prop.Enum = arr
	}
}

// toFloat64Ptr converts int, int64, float64, or string to *float64
func toFloat64Ptr(value any) *float64 {
	switch val := value.(type) {
	case int:
		f := float64(val)
		return &f
	case int64:
		f := float64(val)
		return &f
	case float64:
		return &val
	case string:
		if v, err := strconv.ParseFloat(val, 64); err == nil {
			return &v
		}
	default:
		return nil
	}
	return nil
}

// extractParameters separates parameters (path, query, header) from body fields
// Returns parameters array and body fields (non-parameter fields)
func (g *OpenAPIGenerator) extractParameters(route *models.Route) ([]Parameter, []models.FieldInfo) {
	var params []Parameter
	var bodyFields []models.FieldInfo

	if route.Request == nil || len(route.Request.Fields) == 0 {
		return params, bodyFields
	}

	for i := range route.Request.Fields {
		field := &route.Request.Fields[i]
		// Check if this field is a parameter (path, query, or header)
		if field.ParamType != "" {
			param := Parameter{
				Name:        field.ParamName,
				In:          field.ParamType,
				Required:    field.Required || field.ParamType == "path", // Path params always required
				Description: field.Description,
				Schema:      g.fieldInfoToProperty(field),
			}
			if field.Example != "" {
				param.Example = field.Example
			}
			params = append(params, param)
		} else {
			// Not a parameter, add to body fields
			bodyFields = append(bodyFields, *field)
		}
	}

	return params, bodyFields
}

// writeParameters writes the parameters array for an operation
func (g *OpenAPIGenerator) writeParameters(sb *strings.Builder, params []Parameter) {
	sb.WriteString("      parameters:\n")
	for i := range params {
		param := &params[i]
		sb.WriteString("        - name: ")
		sb.WriteString(param.Name)
		sb.WriteString("\n")

		fmt.Fprintf(sb, "          in: %s\n", param.In)
		fmt.Fprintf(sb, "          required: %t\n", param.Required)

		if param.Description != "" {
			fmt.Fprintf(sb, "          description: %s\n", param.Description)
		}

		// Write schema
		sb.WriteString("          schema:\n")
		g.writePropertySchema(sb, param.Schema, "            ")

		if param.Example != nil {
			// Marshal example value to YAML-compatible format
			fmt.Fprintf(sb, "          example: %v\n", param.Example)
		}
	}
}

// writeRequestBody writes the request body for an operation with only body fields
func (g *OpenAPIGenerator) writeRequestBody(sb *strings.Builder, _ []models.FieldInfo, schemaName string) {
	sb.WriteString("      requestBody:\n")
	sb.WriteString("        required: true\n")
	sb.WriteString("        content:\n")
	sb.WriteString("          application/json:\n")
	sb.WriteString("            schema:\n")

	// Reference the schema if it has a name, otherwise inline it
	if schemaName != "" {
		fmt.Fprintf(sb, "              $ref: '#/components/schemas/%s'\n", schemaName)
	} else {
		// Inline schema (fallback - shouldn't happen with proper type extraction)
		sb.WriteString("              type: object\n")
	}
}

// writePropertySchema writes an OpenAPI property schema with proper indentation
func (g *OpenAPIGenerator) writePropertySchema(sb *strings.Builder, prop *OpenAPIProperty, indent string) {
	if prop == nil {
		return
	}

	// Write all property components using focused writer functions
	writeBasicType(sb, prop, indent)
	writeStringConstraints(sb, prop, indent)
	writeNumericConstraints(sb, prop, indent)
	writeExclusiveBounds(sb, prop, indent)
	writePattern(sb, prop, indent)
	writeEnum(sb, prop, indent)
	g.writeArrayItems(sb, prop, indent)
}

// writeBasicType writes the type and format fields
func writeBasicType(sb *strings.Builder, prop *OpenAPIProperty, indent string) {
	if prop.Type != "" {
		fmt.Fprintf(sb, "%stype: %s\n", indent, prop.Type)
	}
	if prop.Format != "" {
		fmt.Fprintf(sb, "%sformat: %s\n", indent, prop.Format)
	}
}

// writeStringConstraints writes minLength and maxLength if present
func writeStringConstraints(sb *strings.Builder, prop *OpenAPIProperty, indent string) {
	if prop.MinLength != nil {
		fmt.Fprintf(sb, "%sminLength: %d\n", indent, *prop.MinLength)
	}
	if prop.MaxLength != nil {
		fmt.Fprintf(sb, "%smaxLength: %d\n", indent, *prop.MaxLength)
	}
}

// writeNumericConstraints writes minimum and maximum if present
func writeNumericConstraints(sb *strings.Builder, prop *OpenAPIProperty, indent string) {
	if prop.Minimum != nil {
		fmt.Fprintf(sb, "%sminimum: %v\n", indent, *prop.Minimum)
	}
	if prop.Maximum != nil {
		fmt.Fprintf(sb, "%smaximum: %v\n", indent, *prop.Maximum)
	}
}

// writeExclusiveBounds writes exclusive minimum/maximum if true
func writeExclusiveBounds(sb *strings.Builder, prop *OpenAPIProperty, indent string) {
	if prop.ExclusiveMinimum != nil && *prop.ExclusiveMinimum {
		fmt.Fprintf(sb, "%sexclusiveMinimum: true\n", indent)
	}
	if prop.ExclusiveMaximum != nil && *prop.ExclusiveMaximum {
		fmt.Fprintf(sb, "%sexclusiveMaximum: true\n", indent)
	}
}

// writePattern writes the pattern field if present
func writePattern(sb *strings.Builder, prop *OpenAPIProperty, indent string) {
	if prop.Pattern != "" {
		fmt.Fprintf(sb, "%spattern: %s\n", indent, prop.Pattern)
	}
}

// writeEnum writes the enum array if present
func writeEnum(sb *strings.Builder, prop *OpenAPIProperty, indent string) {
	if len(prop.Enum) == 0 {
		return
	}

	sb.WriteString(indent)
	sb.WriteString("enum:\n")
	for _, val := range prop.Enum {
		fmt.Fprintf(sb, "%s  - %v\n", indent, val)
	}
}

// writeArrayItems recursively writes array items if present
func (g *OpenAPIGenerator) writeArrayItems(sb *strings.Builder, prop *OpenAPIProperty, indent string) {
	if prop.Items == nil {
		return
	}

	sb.WriteString(indent)
	sb.WriteString("items:\n")
	g.writePropertySchema(sb, prop.Items, indent+"  ")
}
