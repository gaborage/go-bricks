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
	httpMethodGet     = "GET"
	httpMethodPut     = "PUT"
	httpMethodPost    = "POST"
	httpMethodDelete  = "DELETE"
	httpMethodPatch   = "PATCH"
	httpMethodHead    = "HEAD"
	httpMethodOptions = "OPTIONS"
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

// The types below model the paths/operations half of an OpenAPI document as a
// struct graph. The whole document — info, paths, and components — is emitted
// through a single yaml.Marshal path (see marshalYAMLSection), so $ref, items,
// and future schema fields serialize correctly whether inline (in an operation)
// or under components, with no hand-rolled text writers to keep in sync.

// OpenAPIPathItem holds the operations registered under one path. Method fields
// are declared in canonical order so yaml.Marshal emits them deterministically;
// omitempty drops the methods a path does not use.
type OpenAPIPathItem struct {
	Get     *OpenAPIOperation `yaml:"get,omitempty"`
	Put     *OpenAPIOperation `yaml:"put,omitempty"`
	Post    *OpenAPIOperation `yaml:"post,omitempty"`
	Delete  *OpenAPIOperation `yaml:"delete,omitempty"`
	Patch   *OpenAPIOperation `yaml:"patch,omitempty"`
	Head    *OpenAPIOperation `yaml:"head,omitempty"`
	Options *OpenAPIOperation `yaml:"options,omitempty"`
}

// OpenAPIOperation is a single HTTP operation. Field order matches the emitted
// document: operationId, summary, description, tags, parameters, requestBody,
// responses.
type OpenAPIOperation struct {
	OperationID string                      `yaml:"operationId"`
	Summary     string                      `yaml:"summary"`
	Description string                      `yaml:"description,omitempty"`
	Tags        []string                    `yaml:"tags,omitempty"`
	Parameters  []Parameter                 `yaml:"parameters,omitempty"`
	RequestBody *OpenAPIRequestBody         `yaml:"requestBody,omitempty"`
	Responses   map[string]*OpenAPIResponse `yaml:"responses"`
}

// OpenAPIRequestBody is a Request Body Object. Description carries the JOSE
// compact-serialization note when the request type is jose-tagged.
type OpenAPIRequestBody struct {
	Required    bool                         `yaml:"required"`
	Description string                       `yaml:"description,omitempty"`
	Content     map[string]*OpenAPIMediaType `yaml:"content"`
}

// OpenAPIResponse is a Response Object.
type OpenAPIResponse struct {
	Description string                       `yaml:"description"`
	Content     map[string]*OpenAPIMediaType `yaml:"content,omitempty"`
}

// OpenAPIMediaType is a Media Type Object (the value under a content-type key).
type OpenAPIMediaType struct {
	Schema *OpenAPIProperty `yaml:"schema"`
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

	// Paths — built as a struct graph and emitted through the same yaml.Marshal
	// path as info/components (an empty project marshals to "paths: {}").
	allRoutes := g.getAllRoutes(project)
	pathsYAML, err := g.marshalYAMLSection("paths", g.buildPaths(allRoutes))
	if err != nil {
		return "", fmt.Errorf("failed to marshal paths section: %w", err)
	}
	sb.WriteString(pathsYAML)

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

// getAllRoutes flattens routes from all modules, preserving each route's owning
// module identity (stamping it when the analyzer did not — e.g. hand-built
// projects in tests) so later passes can namespace by module.
func (g *OpenAPIGenerator) getAllRoutes(project *models.Project) []models.Route {
	totalRoutes := 0
	for i := range project.Modules {
		totalRoutes += len(project.Modules[i].Routes)
	}
	routes := make([]models.Route, 0, totalRoutes)
	for mi := range project.Modules {
		module := &project.Modules[mi]
		for ri := range module.Routes {
			route := module.Routes[ri]
			if route.Module == "" {
				route.Module = module.Name
			}
			if route.Package == "" {
				route.Package = module.Package
			}
			routes = append(routes, route)
		}
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

// buildPaths builds the paths object as a struct graph keyed by path. yaml.Marshal
// sorts map keys, giving the same deterministic path ordering the previous
// hand-rolled writer produced via sort.Strings.
func (g *OpenAPIGenerator) buildPaths(routes []models.Route) map[string]*OpenAPIPathItem {
	pathGroups := g.groupRoutesByPath(routes)
	paths := make(map[string]*OpenAPIPathItem, len(pathGroups))
	for path := range pathGroups {
		// Defensive guard: a valid OpenAPI path template must start with "/".
		// The analyzer already drops unresolvable paths; this rejects anything
		// that still slipped through rather than emitting an invalid document.
		if !strings.HasPrefix(path, "/") {
			continue
		}
		group := pathGroups[path]
		item := &OpenAPIPathItem{}
		for i := range group {
			g.assignOperation(item, &group[i])
		}
		paths[path] = item
	}
	return paths
}

// assignOperation builds the operation for a route and attaches it to the path
// item under the matching HTTP method. The analyzer only emits the standard
// methods (analyzer.isHTTPMethod), so an unrecognized method is a no-op. A
// (method, path) already populated is left untouched (first-wins de-dup).
func (g *OpenAPIGenerator) assignOperation(item *OpenAPIPathItem, route *models.Route) {
	slot := item.methodSlot(strings.ToUpper(route.Method))
	if slot == nil || *slot != nil {
		return
	}
	*slot = g.buildOperation(route)
}

// methodSlot returns a pointer to the operation field for an HTTP method, or nil
// for an unrecognized method.
func (item *OpenAPIPathItem) methodSlot(method string) **OpenAPIOperation {
	switch method {
	case httpMethodGet:
		return &item.Get
	case httpMethodPut:
		return &item.Put
	case httpMethodPost:
		return &item.Post
	case httpMethodDelete:
		return &item.Delete
	case httpMethodPatch:
		return &item.Patch
	case httpMethodHead:
		return &item.Head
	case httpMethodOptions:
		return &item.Options
	}
	return nil
}

// Parameter represents an OpenAPI parameter (path, query, or header). Field
// order matches the emitted document; Schema is always present, description and
// example are omitted when empty.
type Parameter struct {
	Name        string           `yaml:"name"`
	In          string           `yaml:"in"` // "path", "query", "header"
	Required    bool             `yaml:"required"`
	Description string           `yaml:"description,omitempty"`
	Schema      *OpenAPIProperty `yaml:"schema"`
	Example     any              `yaml:"example,omitempty"`
}

// buildOperation builds the Operation Object for a route. Empty tags/parameters
// and an absent request body are dropped by the struct's omitempty tags, matching
// the previous conditional text emission.
func (g *OpenAPIGenerator) buildOperation(route *models.Route) *OpenAPIOperation {
	op := &OpenAPIOperation{
		OperationID: g.getOperationID(route),
		Summary:     g.getSummary(route),
		Description: route.Description,
		Tags:        route.Tags,
		Responses:   g.buildResponses(route),
	}

	// Parameters (path, query, header) plus the remaining body fields.
	params, bodyFields := g.extractParameters(route)
	op.Parameters = params

	// Emit a request body when there are body fields, OR when the route is
	// JOSE-tagged (a JOSE request type may have only the sentinel field, with all
	// "plaintext" fields filtered into header/path/query params or absent — but
	// the route still expects an application/jose payload on the wire).
	if route.Request != nil && (len(bodyFields) > 0 || route.Request.JOSE) {
		op.RequestBody = g.buildRequestBody(route.Request)
	}

	return op
}

// refComponentPrefix is the JSON-pointer prefix for component-schema $refs.
const refComponentPrefix = "#/components/schemas/"

// refPath returns the $ref pointer for a named component schema.
func refPath(name string) string {
	return refComponentPrefix + name
}

// joseTokenSchema is the Media Type schema for an application/jose payload. Per
// OpenAPI 3.0.1 the Media Type schema describes the on-the-wire shape — a
// base64url JOSE compact-serialization string token — NOT the decrypted
// plaintext shape. The plaintext component schema is referenced from the parent
// RequestBody/Response description instead.
func joseTokenSchema() *OpenAPIProperty {
	return &OpenAPIProperty{Type: typeString, Format: "jose"}
}

// joseDescription is the canonical RequestBody/Response description for a JOSE
// route. RequestBody and Response objects allow a description field (Media Type
// objects do not), so this is attached at the parent level. It names the
// plaintext component schema the decrypted payload conforms to.
func joseDescription(plaintextSchema string) string {
	return fmt.Sprintf(
		"JOSE compact serialization (signed-then-encrypted). The wire payload\n"+
			"is a base64url-encoded JWE compact form whose plaintext, after\n"+
			"decrypt+verify, conforms to the %s schema —\n"+
			"see %s.\n",
		plaintextSchema, refPath(plaintextSchema))
}

// jsonMediaRef builds a single-entry application/json content map whose schema is
// a $ref to the named component.
func jsonMediaRef(name string) map[string]*OpenAPIMediaType {
	return map[string]*OpenAPIMediaType{mediaJSON: {Schema: &OpenAPIProperty{Ref: refPath(name)}}}
}

// buildResponses builds the responses object for a route. When the route's
// response carries a jose: tag the 200 success response uses Content-Type
// application/jose; the 4xx pre-trust failure path always uses application/json
// (peer is unauthenticated, so the framework returns a plaintext minimal
// envelope, never JOSE-wrapped — see the hybrid envelope contract in CLAUDE.md
// JOSE Middleware).
//
// 4xx schema selection is driven by EITHER side carrying jose tags. The runtime
// enforces bidirectional symmetry at registration, but the analyzer runs
// statically against source so we can encounter asymmetric setups; in any such
// case the pre-trust failure path is still routed through the JOSE
// plaintext-minimal envelope by the runtime, so the OpenAPI spec must reflect that.
func (g *OpenAPIGenerator) buildResponses(route *models.Route) map[string]*OpenAPIResponse {
	joseResponse := route.Response != nil && route.Response.JOSE
	joseRoute := joseResponse || (route.Request != nil && route.Request.JOSE)

	resp200 := &OpenAPIResponse{}
	if joseResponse {
		// Description on the Response Object names the plaintext schema; the Media
		// Type schema describes the wire shape (a string token).
		resp200.Description = joseDescription("SuccessResponse")
		resp200.Content = map[string]*OpenAPIMediaType{mediaJOSE: {Schema: joseTokenSchema()}}
	} else {
		resp200.Description = g.getResponseDescription(route.Method)
		resp200.Content = jsonMediaRef("SuccessResponse")
	}

	// Pre-trust failures on JOSE routes are plaintext minimal envelopes per the
	// security model: when decrypt/verify fails the peer is unauthenticated and
	// the server leaks nothing beyond {code,message}.
	errorSchema := schemaErrorResponse
	if joseRoute {
		errorSchema = "JOSEErrorEnvelope"
	}

	return map[string]*OpenAPIResponse{
		"200": resp200,
		"400": {Description: "Bad Request", Content: jsonMediaRef(errorSchema)},
	}
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

// getResponseDescription returns a description based on HTTP method. The method
// is normalized to upper-case (consistent with assignOperation) so a
// lowercase/mixed-case input still maps to the right description rather than
// silently falling through to the generic default.
func (g *OpenAPIGenerator) getResponseDescription(method string) string {
	switch strings.ToUpper(method) {
	case httpMethodGet:
		return respDescSuccess
	case httpMethodPost:
		return "Resource created successfully"
	case httpMethodPut:
		return "Resource updated successfully"
	case httpMethodDelete:
		return "Resource deleted successfully"
	case httpMethodPatch:
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
		// NOSONAR: Parse error intentional - non-numeric strings return nil (no default value).
		// (S8148 is a SonarCloud rule; NOSONAR is the suppressor it reads — a //nolint
		// directive would name a golangci-lint linter, which S8148 is not.)
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

// buildRequestBody builds the Request Body Object for a request type. When the
// request carries a jose: tag the Content-Type is application/jose with a
// string-token wire schema and the plaintext shape is named in the description;
// otherwise the schema is a $ref to the documented plaintext component. Takes the
// full TypeInfo (rather than a positional bool) so future flags compose without
// signature churn.
func (g *OpenAPIGenerator) buildRequestBody(reqType *models.TypeInfo) *OpenAPIRequestBody {
	schemaName := ""
	isJOSE := false
	if reqType != nil {
		schemaName = reqType.Name
		isJOSE = reqType.JOSE
	}

	rb := &OpenAPIRequestBody{Required: true}
	switch {
	case isJOSE:
		// Description on the RequestBody Object (spec-compliant) names the plaintext
		// schema; the Media Type schema describes the JOSE string-token wire shape.
		rb.Description = joseDescription(schemaName)
		rb.Content = map[string]*OpenAPIMediaType{mediaJOSE: {Schema: joseTokenSchema()}}
	case schemaName != "":
		rb.Content = jsonMediaRef(schemaName)
	default:
		// Inline fallback — shouldn't happen with proper type extraction.
		rb.Content = map[string]*OpenAPIMediaType{mediaJSON: {Schema: &OpenAPIProperty{Type: typeObject}}}
	}
	return rb
}
