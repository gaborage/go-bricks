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
	typeInteger  = "integer"
	typeObject   = "object"
	typeString   = "string"
	typeNumber   = "number"
	typeBoolean  = "boolean"
	typeArray    = "array"
	formatInt32  = "int32"
	formatInt64  = "int64"
	formatFloat  = "float"
	formatDouble = "double"
)

// Schema component names referenced in multiple emitter sites.
const (
	schemaErrorResponse = "ErrorResponse"
)

// HTTP method names used in switch discriminants and operation generation.
const (
	httpMethodPost = "POST"
)

// Go primitive type names matched against Go type identifiers when mapping to
// OpenAPI types/formats.
const (
	goTypeString  = "string"
	goTypeFloat32 = "float32"
	goTypeFloat64 = "float64"
	goTypeBool    = "bool"
	goTypeUint    = "uint"
	goTypeUint64  = "uint64"
)

// Response/parameter description text reused across operations.
const (
	respDescSuccess = "Successful response"
	paramTypePath   = "path"
	propNameError   = "error"
)

// Media type constants for the OpenAPI content map. Centralized so a future rename
// (e.g., to application/jose+json) is a one-line edit and so call sites in the spec
// emitter can be statically searched by const reference, not by string literal.
const (
	mediaJSON = "application/json"
	mediaJOSE = "application/jose"
)

// YAML indent depths reused across emitter call sites (S1192). Named by depth
// because the same depth occurs at multiple structural positions.
const (
	indent10 = "          "   // 10 spaces
	indent12 = "            " // 12 spaces
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
	totalRoutes := 0
	for _, module := range project.Modules {
		totalRoutes += len(module.Routes)
	}
	routes := make([]models.Route, 0, totalRoutes)
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

	// Write request body if there are body fields, OR if the route is JOSE-tagged
	// (a JOSE request type may have only the sentinel field, with all "plaintext"
	// fields filtered into header/path/query params or simply absent — but the route
	// still expects an application/jose payload on the wire).
	if route.Request != nil && (len(bodyFields) > 0 || route.Request.JOSE) {
		g.writeRequestBody(sb, route.Request)
	}

	// Responses
	g.writeResponses(sb, route)
}

// writeResponses emits the responses section. When the route's response carries a
// jose: tag the success response uses Content-Type: application/jose; the 4xx
// pre-trust failure path always uses application/json (peer is unauthenticated, so
// the framework returns a plaintext minimal envelope, never JOSE-wrapped — see the
// hybrid envelope contract in CLAUDE.md JOSE Middleware).
//
// 4xx schema selection is driven by EITHER side carrying jose tags. The runtime
// enforces bidirectional symmetry at registration, but the analyzer runs statically
// against source so we can encounter asymmetric setups; in any such case the
// pre-trust failure path is still routed through the JOSE plaintext-minimal
// envelope by the runtime, so the OpenAPI spec must reflect that.
func (g *OpenAPIGenerator) writeResponses(sb *strings.Builder, route *models.Route) {
	joseResponse := route.Response != nil && route.Response.JOSE
	joseRoute := joseResponse || (route.Request != nil && route.Request.JOSE)

	sb.WriteString("      responses:\n")
	sb.WriteString("        '200':\n")
	if joseResponse {
		// Description on the Response Object (spec-compliant) — per OpenAPI 3.0.1,
		// description is a fixed field on Response, but NOT on Media Type Object.
		// The Media Type schema describes the WIRE shape (a string token); the
		// plaintext component schema is referenced from the description text.
		writeJOSEDescription(sb, indent10, "SuccessResponse")
		writeContentLine(sb, indent10)
		writeMediaSchemaJOSE(sb, indent12)
	} else {
		fmt.Fprintf(sb, "%sdescription: %s\n", indent10, g.getResponseDescription(route.Method))
		writeContentLine(sb, indent10)
		writeMediaSchemaRef(sb, indent12, mediaJSON, "SuccessResponse")
	}
	sb.WriteString("        '400':\n")
	fmt.Fprintf(sb, "%sdescription: Bad Request\n", indent10)
	writeContentLine(sb, indent10)
	// Pre-trust failures on JOSE routes are plaintext minimal envelopes per the
	// security model: when decrypt/verify fails the peer is unauthenticated and the
	// server leaks nothing beyond {code,message}.
	errorSchema := schemaErrorResponse
	if joseRoute {
		errorSchema = "JOSEErrorEnvelope"
	}
	writeMediaSchemaRef(sb, indent12, mediaJSON, errorSchema)
}

// writeJOSEDescription emits the canonical "JOSE compact serialization" description
// block. Per OpenAPI 3.0.1, Media Type Objects (under content.<contentType>:) do NOT
// allow a description field — fixed fields are limited to schema, example, examples,
// encoding. RequestBody and Response objects DO allow description, so the helper is
// invoked at the parent level (under requestBody: or under '200':), not nested inside
// the content/media-type block.
//
// indent is the YAML depth of the description: line itself. plaintextSchema is the
// component schema name to reference from the description so consumers know what
// shape the decrypted payload conforms to.
func writeJOSEDescription(sb *strings.Builder, indent, plaintextSchema string) {
	fmt.Fprintf(sb, "%sdescription: |\n", indent)
	fmt.Fprintf(sb, "%s  JOSE compact serialization (signed-then-encrypted). The wire payload\n", indent)
	fmt.Fprintf(sb, "%s  is a base64url-encoded JWE compact form whose plaintext, after\n", indent)
	fmt.Fprintf(sb, "%s  decrypt+verify, conforms to the %s schema —\n", indent, plaintextSchema)
	fmt.Fprintf(sb, "%s  see #/components/schemas/%s.\n", indent, plaintextSchema)
}

// writeMediaSchemaRef emits a Media Type entry whose schema is a $ref to a component:
//
//	<indent><contentType>:
//	<indent>  schema:
//	<indent>    $ref: '#/components/schemas/<ref>'
//
// Used for application/json bodies where the wire shape IS the documented schema.
func writeMediaSchemaRef(sb *strings.Builder, indent, contentType, ref string) {
	writeMediaTypeKey(sb, indent, contentType)
	fmt.Fprintf(sb, "%s  schema:\n", indent)
	fmt.Fprintf(sb, "%s    $ref: '#/components/schemas/%s'\n", indent, ref)
}

// writeMediaSchemaJOSE emits a Media Type entry for application/jose where the schema
// describes the on-the-wire payload as a string token (per OpenAPI 3.0.1 — the JOSE
// compact serialization is a base64url-encoded string, NOT the decrypted plaintext
// shape). The plaintext schema is still emitted as a component and referenced from
// the parent RequestBody/Response description text via writeJOSEDescription.
func writeMediaSchemaJOSE(sb *strings.Builder, indent string) {
	writeMediaTypeKey(sb, indent, mediaJOSE)
	fmt.Fprintf(sb, "%s  schema:\n", indent)
	fmt.Fprintf(sb, "%s    type: string\n", indent)
	fmt.Fprintf(sb, "%s    format: jose\n", indent)
}

// writeContentLine emits "<indent>content:\n" — the YAML key that opens a Media Type
// map under either a Response Object or a RequestBody Object.
func writeContentLine(sb *strings.Builder, indent string) {
	fmt.Fprintf(sb, "%scontent:\n", indent)
}

// writeMediaTypeKey emits "<indent><mediaType>:\n" — the YAML key that opens a single
// Media Type Object inside a content map (e.g. "application/json:" or "application/jose:").
func writeMediaTypeKey(sb *strings.Builder, indent, mediaType string) {
	fmt.Fprintf(sb, "%s%s:\n", indent, mediaType)
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
		return respDescSuccess
	case httpMethodPost:
		return "Resource created successfully"
	case "PUT":
		return "Resource updated successfully"
	case "DELETE":
		return "Resource deleted successfully"
	case "PATCH":
		return "Resource partially updated"
	default:
		return respDescSuccess
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
			Type: typeObject,
			Properties: map[string]*OpenAPIProperty{
				"data": {
					Type:        typeObject,
					Description: "Response data",
				},
				"meta": {
					Type:        typeObject,
					Description: "Response metadata",
				},
			},
		},
		schemaErrorResponse: {
			Type: typeObject,
			Properties: map[string]*OpenAPIProperty{
				propNameError: {
					Type: typeObject,
					// Note: nested properties would need recursive handling for full OpenAPI spec
					Description: "Error details with code and message",
				},
				"meta": {
					Type:        typeObject,
					Description: "Response metadata",
				},
			},
			Required: []string{propNameError},
		},
		// JOSEErrorEnvelope is the minimal plaintext error envelope returned for
		// pre-trust JOSE failures (decrypt failed, signature invalid, kid unknown,
		// etc.). The framework intentionally omits traceId/timestamp/framework
		// metadata here — peer is unauthenticated and the envelope must leak
		// nothing beyond the canonical {code,message} pair.
		"JOSEErrorEnvelope": {
			Type: typeObject,
			Properties: map[string]*OpenAPIProperty{
				"code": {
					Type:        typeString,
					Description: "Machine-readable JOSE error code (e.g., JOSE_DECRYPT_FAILED, JOSE_SIGNATURE_INVALID, JOSE_KID_UNKNOWN)",
				},
				"message": {
					Type:        typeString,
					Description: "Constant-time generic message — never reveals which key was tried or which library detected the failure",
				},
			},
			Required: []string{"code", "message"},
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
		Type:       typeObject,
		Properties: make(map[string]*OpenAPIProperty),
		Required:   []string{},
	}

	for i := range typeInfo.Fields {
		field := &typeInfo.Fields[i]

		// Skip fields explicitly marked with json:"-"
		if field.JSONName == "-" && field.ParamType == "" {
			continue
		}

		// Use JSONName if set, otherwise use field name as fallback
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
		prop.Type = typeArray
		elementType := strings.TrimPrefix(goType, "[]")
		prop.Items = &OpenAPIProperty{}
		g.setTypeAndFormat(prop.Items, elementType)
		return
	}

	// Handle basic types
	switch goType {
	case goTypeString:
		prop.Type = typeString
	case "int", "int8", "int16", formatInt32, goTypeUint, "uint8", "uint16", "uint32":
		prop.Type = typeInteger
		prop.Format = formatInt32
	case formatInt64, goTypeUint64:
		prop.Type = typeInteger
		prop.Format = formatInt64
	case goTypeFloat32:
		prop.Type = typeNumber
		prop.Format = formatFloat
	case goTypeFloat64:
		prop.Type = typeNumber
		prop.Format = formatDouble
	case goTypeBool:
		prop.Type = typeBoolean
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
		//nolint:S8148 // NOSONAR: Parse error intentional - non-numeric strings return nil (no default value)
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
				Required:    field.Required || field.ParamType == paramTypePath, // Path params always required
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
		g.writePropertySchema(sb, param.Schema, indent12)

		if param.Example != nil {
			// Marshal example value to YAML-compatible format
			fmt.Fprintf(sb, "          example: %v\n", param.Example)
		}
	}
}

// writeRequestBody writes the request body for an operation. When the request type
// carries a jose: tag the Content-Type is application/jose and the schema $ref still
// points at the documented plaintext shape — the wire payload is the compact JOSE
// serialization that wraps that plaintext on decrypt+verify. Takes the full TypeInfo
// (rather than a positional bool) so future flags compose without signature churn.
func (g *OpenAPIGenerator) writeRequestBody(sb *strings.Builder, reqType *models.TypeInfo) {
	schemaName := ""
	isJOSE := false
	if reqType != nil {
		schemaName = reqType.Name
		isJOSE = reqType.JOSE
	}
	contentType := mediaJSON
	if isJOSE {
		contentType = mediaJOSE
	}

	sb.WriteString("      requestBody:\n")
	sb.WriteString("        required: true\n")
	if isJOSE {
		// Description on the RequestBody Object (spec-compliant) — sibling of content,
		// NOT nested inside content.<contentType> which would violate OpenAPI 3.0.1.
		writeJOSEDescription(sb, "        ", schemaName)
	}
	writeContentLine(sb, "        ")

	switch {
	case isJOSE:
		// JOSE wire payload is a string token, not the plaintext schema. The plaintext
		// component schema is referenced from the description text above.
		writeMediaSchemaJOSE(sb, indent10)
	case schemaName != "":
		writeMediaSchemaRef(sb, indent10, contentType, schemaName)
	default:
		// Inline schema (fallback — shouldn't happen with proper type extraction).
		writeMediaTypeKey(sb, indent10, contentType)
		sb.WriteString("            schema:\n")
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
