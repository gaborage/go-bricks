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
	typeInteger    = "integer"
	typeObject     = "object"
	typeString     = "string"
	typeNumber     = "number"
	typeBoolean    = "boolean"
	typeArray      = "array"
	formatInt32    = "int32"
	formatInt64    = "int64"
	formatFloat    = "float"
	formatDouble   = "double"
	formatDateTime = "date-time"
	formatBinary   = "binary"
	formatUUID     = "uuid"
)

// Schema component names referenced in multiple emitter sites.
const (
	schemaErrorResponse     = "ErrorResponse"
	schemaJOSEErrorEnvelope = "JOSEErrorEnvelope"
	schemaRawErrorResponse  = "RawErrorResponse"
	schemaSuccessResponse   = "SuccessResponse"
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

	// Qualified/composite Go types with a well-known OpenAPI representation.
	goTypeTimeTime     = "time.Time"
	goTypeTimeDuration = "time.Duration"
	goTypeByteSlice    = "[]byte"
	goTypeUint8Slice   = "[]uint8"
	goTypeUUID         = "uuid.UUID"
	goTypeRawMessage   = "json.RawMessage"
)

// Response/parameter description text reused across operations.
const (
	respDescSuccess = "Successful response"
	respDescCreated = "Resource created successfully"
	paramTypePath   = "path"
	propNameError   = "error"
	propNameData    = "data"
	propNameMeta    = "meta"
	propNameCode    = "code"
	propNameMessage = "message"
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
	Type                 string                      `yaml:"type,omitempty"`
	Properties           map[string]*OpenAPIProperty `yaml:"properties,omitempty"`           // For inline objects (e.g. the data/meta envelope)
	AdditionalProperties *OpenAPIProperty            `yaml:"additionalProperties,omitempty"` // For maps (the value schema)
	Format               string                      `yaml:"format,omitempty"`
	Description          string                      `yaml:"description,omitempty"`
	Example              any                         `yaml:"example,omitempty"`
	Ref                  string                      `yaml:"$ref,omitempty"`
	Items                *OpenAPIProperty            `yaml:"items,omitempty"` // For arrays
	MinLength            *int                        `yaml:"minLength,omitempty"`
	MaxLength            *int                        `yaml:"maxLength,omitempty"`
	Minimum              *float64                    `yaml:"minimum,omitempty"`
	Maximum              *float64                    `yaml:"maximum,omitempty"`
	ExclusiveMinimum     *bool                       `yaml:"exclusiveMinimum,omitempty"`
	ExclusiveMaximum     *bool                       `yaml:"exclusiveMaximum,omitempty"`
	Pattern              string                      `yaml:"pattern,omitempty"`
	Enum                 []any                       `yaml:"enum,omitempty"`
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

	// Components with proper YAML marshaling. Prefer the analyzer-built type
	// registry (which includes nested/recursive types); fall back to a flat
	// registry of route request/response types for projects assembled without it.
	types := project.Types
	if types == nil {
		types = routeTypeRegistry(project)
	}
	standardSchemas := g.createStandardSchemas()
	generatedSchemas := g.generateSchemasFromTypes(types)

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

// buildResponses builds the responses object for a route.
//
// Success response: the status code is the one the handler's result constructor
// implies (server.Created -> 201, Accepted -> 202, NewResult(n) -> n,
// NoContentResult -> 204), defaulting to 200. The body shape depends on the
// route flavour:
//   - NoContent       -> no body (204).
//   - JOSE response    -> a single application/jose string token; the Response
//     description names the plaintext component the decrypted payload conforms to.
//   - RawResponse      -> the bare payload schema ($ref to the response component),
//     bypassing the data/meta envelope (Strangler-Fig migration).
//   - default          -> the standard envelope {data: <$ref to response>, meta},
//     which is what finally references the response component the analyzer
//     discovered (closing the "orphan component" window).
//
// Error responses: 400 and 500 are always present; 422 is added when the request
// type carries validation tags (the framework returns 422 on validation
// failure). The error schema is JOSEErrorEnvelope for JOSE routes (pre-trust
// failures leak nothing beyond {code,message}), RawErrorResponse for raw routes,
// and ErrorResponse otherwise.
//
// JOSE 4xx schema selection is driven by EITHER side carrying jose tags. The
// runtime enforces bidirectional symmetry at registration, but the analyzer runs
// statically against source so we can encounter asymmetric setups; in any such
// case the pre-trust failure path is still routed through the JOSE
// plaintext-minimal envelope by the runtime, so the OpenAPI spec must reflect that.
func (g *OpenAPIGenerator) buildResponses(route *models.Route) map[string]*OpenAPIResponse {
	joseResponse := route.Response != nil && route.Response.JOSE
	joseRoute := joseResponse || (route.Request != nil && route.Request.JOSE)
	noContent := route.Response != nil && route.Response.NoContent

	successCode := successStatusCode(route, noContent)
	success := &OpenAPIResponse{Description: g.successDescription(route, successCode, noContent)}
	switch {
	case noContent:
		// 204: no response body.
	case joseResponse:
		// Description on the Response Object names the plaintext schema; the Media
		// Type schema describes the wire shape (a string token).
		success.Description = joseDescription(g.successPlaintextSchema(route))
		success.Content = map[string]*OpenAPIMediaType{mediaJOSE: {Schema: joseTokenSchema()}}
	case route.RawResponse:
		success.Content = map[string]*OpenAPIMediaType{mediaJSON: {Schema: responsePayloadSchema(route.Response)}}
	default:
		success.Content = map[string]*OpenAPIMediaType{mediaJSON: {Schema: successEnvelopeSchema(route.Response)}}
	}

	// Pre-trust failures on JOSE routes are plaintext minimal envelopes per the
	// security model: when decrypt/verify fails the peer is unauthenticated and
	// the server leaks nothing beyond {code,message}.
	errorSchema := schemaErrorResponse
	switch {
	case joseRoute:
		errorSchema = schemaJOSEErrorEnvelope
	case route.RawResponse:
		errorSchema = schemaRawErrorResponse
	}

	responses := map[string]*OpenAPIResponse{
		"400": {Description: "Bad Request", Content: jsonMediaRef(errorSchema)},
		"500": {Description: "Internal Server Error", Content: jsonMediaRef(errorSchema)},
	}
	if routeHasValidation(route.Request) {
		responses["422"] = &OpenAPIResponse{Description: "Unprocessable Entity", Content: jsonMediaRef(errorSchema)}
	}
	// Assign the success entry LAST so a success status that overlaps an error code
	// (e.g. a handler that returns NewResult(400, ...) as a non-error Result) keeps
	// the documented success response rather than being clobbered by the 400 entry.
	responses[successCode] = success
	return responses
}

// successStatusCode resolves the success response code as a string key. A 204
// (NoContent) wins outright; otherwise the constructor-derived SuccessStatus is
// used when set, defaulting to 200.
func successStatusCode(route *models.Route, noContent bool) string {
	if noContent {
		return "204"
	}
	if route.SuccessStatus != 0 {
		return strconv.Itoa(route.SuccessStatus)
	}
	return "200"
}

// successDescription returns a human description for the success response,
// preferring a status-specific phrase over the method-derived default.
func (g *OpenAPIGenerator) successDescription(route *models.Route, code string, noContent bool) string {
	switch {
	case noContent:
		return "No Content"
	case code == "201":
		return respDescCreated
	case code == "202":
		return "Request accepted for processing"
	default:
		return g.getResponseDescription(route.Method)
	}
}

// successPlaintextSchema names the plaintext component a JOSE response decrypts
// to, falling back to the generic SuccessResponse envelope when the handler
// declares no typed response.
func (g *OpenAPIGenerator) successPlaintextSchema(route *models.Route) string {
	if route.Response != nil && route.Response.Name != "" {
		return schemaName(route.Response)
	}
	return schemaSuccessResponse
}

// successEnvelopeSchema builds the inline {data, meta} success envelope. When the
// route has a typed response, data is a $ref to its component schema; otherwise
// data is a generic object (the handler returned an untyped/empty payload).
func successEnvelopeSchema(response *models.TypeInfo) *OpenAPIProperty {
	data := responsePayloadSchema(response)
	if data.Ref == "" {
		// Untyped fallback (generic object) — annotate it for readers.
		data.Description = "Response data"
	}
	return &OpenAPIProperty{
		Type: typeObject,
		Properties: map[string]*OpenAPIProperty{
			propNameData: data,
			propNameMeta: metaEnvelopeSchema(),
		},
	}
}

// metaEnvelopeSchema is the framework-authoritative envelope metadata block.
// timestamp and traceId are always populated by the response writer.
func metaEnvelopeSchema() *OpenAPIProperty {
	return &OpenAPIProperty{
		Type: typeObject,
		Properties: map[string]*OpenAPIProperty{
			"timestamp": {Type: typeString, Format: formatDateTime, Description: "RFC3339 response timestamp"},
			"traceId":   {Type: typeString, Description: "W3C trace identifier for the request"},
		},
	}
}

// responsePayloadSchema returns the bare schema for a response component: a $ref
// to the named type, or a generic object when the type is unnamed.
func responsePayloadSchema(response *models.TypeInfo) *OpenAPIProperty {
	if response == nil || response.Name == "" {
		return &OpenAPIProperty{Type: typeObject}
	}
	return &OpenAPIProperty{Ref: refPath(schemaName(response))}
}

// routeHasValidation reports whether the request type carries any validation
// constraints, which is what makes a 422 (Unprocessable Entity) reachable.
func routeHasValidation(request *models.TypeInfo) bool {
	if request == nil {
		return false
	}
	for i := range request.Fields {
		f := &request.Fields[i]
		if f.Required || f.RawValidation != "" {
			return true
		}
	}
	return false
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

// getResponseDescription returns a description based on HTTP method, used for the
// 200 success case. The method is normalized to upper-case (consistent with
// assignOperation) so a lowercase/mixed-case input still maps to the right
// description rather than silently falling through to the generic default.
//
// POST maps to the generic "Successful response", NOT "Resource created": a 201
// is described as created by successDescription's status branch, so a POST that
// returns 200 (e.g. a query-by-POST endpoint) is not mislabeled as a creation.
func (g *OpenAPIGenerator) getResponseDescription(method string) string {
	switch strings.ToUpper(method) {
	case httpMethodGet, httpMethodPost:
		return respDescSuccess
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
		// SuccessResponse is the generic envelope used as the JOSE plaintext-schema
		// fallback (a typed route emits an inline envelope instead). Its meta reuses
		// metaEnvelopeSchema so the documented metadata shape never diverges from the
		// inline envelopes.
		schemaSuccessResponse: {
			Type: typeObject,
			Properties: map[string]*OpenAPIProperty{
				propNameData: {
					Type:        typeObject,
					Description: "Response data",
				},
				propNameMeta: metaEnvelopeSchema(),
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
		schemaJOSEErrorEnvelope: {
			Type: typeObject,
			Properties: map[string]*OpenAPIProperty{
				propNameCode: {
					Type:        typeString,
					Description: "Machine-readable JOSE error code (e.g., JOSE_DECRYPT_FAILED, JOSE_SIGNATURE_INVALID, JOSE_KID_UNKNOWN)",
				},
				propNameMessage: {
					Type:        typeString,
					Description: "Constant-time generic message — never reveals which key was tried or which library detected the failure",
				},
			},
			Required: []string{propNameCode, propNameMessage},
		},
		// RawErrorResponse is the bare {code,message} error payload returned by
		// routes registered WithRawResponse(): no data/meta envelope, mirroring
		// the framework's formatRawErrorResponse (details is dev-only, omitted).
		schemaRawErrorResponse: {
			Type: typeObject,
			Properties: map[string]*OpenAPIProperty{
				propNameCode: {
					Type:        typeString,
					Description: "Machine-readable error code",
				},
				propNameMessage: {
					Type:        typeString,
					Description: "Human-readable error message",
				},
			},
			Required: []string{propNameCode, propNameMessage},
		},
	}
}

// generateSchemasFromTypes creates OpenAPI schemas from discovered type information
// generateSchemasFromTypes emits one component schema per registered type. The
// registry already contains every named struct reachable from a route's request
// or response (including nested, sliced, and recursive types), so iterating it
// produces a self-contained set of components with all $refs resolvable.
func (g *OpenAPIGenerator) generateSchemasFromTypes(types map[string]*models.TypeInfo) map[string]*OpenAPISchema {
	schemas := make(map[string]*OpenAPISchema, len(types))
	for _, ti := range types {
		if schema := g.typeInfoToSchema(ti); schema != nil {
			schemas[schemaName(ti)] = schema
		}
	}
	return schemas
}

// schemaName is the single source for a type's component-schema name (and the
// $ref that points at it). Centralized so name disambiguation across packages
// can be added in one place later.
func schemaName(ti *models.TypeInfo) string {
	return ti.Name
}

// routeTypeRegistry builds a flat type registry from the request/response types
// of a project's routes. It is the fallback used when project.Types is unset
// (e.g. a Project assembled directly rather than via the analyzer); it does not
// resolve nested types, which the analyzer's registry already includes.
func routeTypeRegistry(project *models.Project) map[string]*models.TypeInfo {
	types := make(map[string]*models.TypeInfo)
	add := func(ti *models.TypeInfo) {
		if ti == nil || ti.Name == "" {
			return
		}
		if _, ok := types[ti.Name]; !ok {
			types[ti.Name] = ti
		}
	}
	for mi := range project.Modules {
		for ri := range project.Modules[mi].Routes {
			route := &project.Modules[mi].Routes[ri]
			add(route.Request)
			add(route.Response)
		}
	}
	return types
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

// fieldInfoToProperty converts a FieldInfo to an OpenAPI property.
func (g *OpenAPIGenerator) fieldInfoToProperty(field *models.FieldInfo) *OpenAPIProperty {
	// A field whose underlying type is a registered struct is a $ref (or an array
	// of $ref). A $ref must stand alone — it carries no sibling type/format or
	// constraint keywords — so return early.
	if field.RefName != "" {
		ref := &OpenAPIProperty{Ref: refPath(field.RefName)}
		if isSliceType(field.Type) {
			// The inner $ref must stand alone, but the array wrapper carries the
			// field's documentation.
			arr := &OpenAPIProperty{Type: typeArray, Items: ref, Description: field.Description}
			if field.Example != "" {
				arr.Example = field.Example
			}
			return arr
		}
		return ref
	}

	prop := &OpenAPIProperty{
		Description: field.Description,
	}

	// Set example if present
	if field.Example != "" {
		prop.Example = field.Example
	}

	// A struct-valued map (map[string]Address) is an object whose
	// additionalProperties is a $ref to the value component. Handle it here, where
	// the analyzer-resolved MapValueRefName is available (setTypeAndFormat sees
	// only the type string and so can only type primitive-valued maps). A map of
	// slices (map[string][]Address) wraps the $ref in an array.
	if valueType, isMap := mapValueType(field.Type); isMap && field.MapValueRefName != "" {
		ref := &OpenAPIProperty{Ref: refPath(field.MapValueRefName)}
		prop.Type = typeObject
		if isSliceType(valueType) {
			prop.AdditionalProperties = &OpenAPIProperty{Type: typeArray, Items: ref}
		} else {
			prop.AdditionalProperties = ref
		}
		return prop
	}

	// Map Go type to OpenAPI type and format
	g.setTypeAndFormat(prop, field.Type)

	// Apply constraints from validation tags
	g.applyConstraints(prop, field)

	return prop
}

// isSliceType reports whether a Go type string denotes a slice (after an
// optional leading pointer), e.g. "[]Address" or "*[]Address".
func isSliceType(goType string) bool {
	return strings.HasPrefix(strings.TrimPrefix(goType, "*"), "[]")
}

// wellKnownType holds the OpenAPI type/format for a recognized stdlib/library type.
type wellKnownType struct {
	typ    string
	format string
}

// wellKnownFormats maps qualified Go types to their idiomatic OpenAPI schema.
// These types are NOT local structs (so the registry never refs them), and the
// default object fallback would lose their real wire shape:
//   - time.Time      -> RFC3339 string
//   - time.Duration  -> integer (int64): encoding/json (which go-bricks uses)
//     marshals a Duration as its underlying int64 nanosecond count — a JSON
//     number, NOT a string.
//   - []byte/[]uint8 -> base64 string (encoding/json marshals byte slices base64)
//   - uuid.UUID      -> uuid-formatted string
//   - json.RawMessage-> arbitrary JSON object
//
// NOTE: matching is by the analyzer's qualified type string (pkg-local alias +
// "." + name), so an aliased import (import t "time" -> "t.Time") is not yet
// recognized and falls through to the object default. Alias/import-path
// resolution lands with the cross-package resolver in PR9.
var wellKnownFormats = map[string]wellKnownType{
	goTypeTimeTime:     {typeString, formatDateTime},
	goTypeTimeDuration: {typeInteger, formatInt64},
	goTypeByteSlice:    {typeString, formatBinary},
	goTypeUint8Slice:   {typeString, formatBinary},
	goTypeUUID:         {typeString, formatUUID},
	goTypeRawMessage:   {typeObject, ""},
}

// mapValueType reports whether goType is a map (after an optional leading
// pointer) and returns its value type string. Keys are assumed simple (no nested
// brackets), which holds for JSON string-keyed maps. This is a deliberate twin of
// analyzer.mapValueType (kept private in each package rather than shared as an
// exported helper — see the note there); keep the two in sync.
func mapValueType(goType string) (string, bool) {
	goType = strings.TrimPrefix(goType, "*")
	if !strings.HasPrefix(goType, "map[") {
		return "", false
	}
	rest := goType[len("map["):]
	i := strings.IndexByte(rest, ']')
	if i < 0 {
		return "", false
	}
	return rest[i+1:], true
}

// setTypeAndFormat maps Go types to OpenAPI type and format
func (g *OpenAPIGenerator) setTypeAndFormat(prop *OpenAPIProperty, goType string) {
	// Strip pointer prefix
	goType = strings.TrimPrefix(goType, "*")

	// Well-known types first: []byte must win over the generic []T array branch,
	// and time.Time/uuid.UUID over the qualified-type object fallback.
	if wk, ok := wellKnownFormats[goType]; ok {
		prop.Type = wk.typ
		if wk.format != "" {
			prop.Format = wk.format
		}
		return
	}

	// Handle arrays
	if strings.HasPrefix(goType, "[]") {
		prop.Type = typeArray
		elementType := strings.TrimPrefix(goType, "[]")
		prop.Items = &OpenAPIProperty{}
		g.setTypeAndFormat(prop.Items, elementType)
		return
	}

	// Handle maps as objects with a typed additionalProperties (string-keyed).
	// Struct-valued maps emit a $ref via fieldInfoToProperty; this nested path
	// (maps inside slices/maps) recurses on the value type by string only.
	if valueType, ok := mapValueType(goType); ok {
		prop.Type = typeObject
		prop.AdditionalProperties = &OpenAPIProperty{}
		g.setTypeAndFormat(prop.AdditionalProperties, valueType)
		return
	}

	// Handle basic types
	switch goType {
	case goTypeString:
		prop.Type = typeString
	case "int", "int8", "int16", formatInt32:
		prop.Type = typeInteger
		prop.Format = formatInt32
	case goTypeUint, "uint8", "uint16", "uint32":
		prop.Type = typeInteger
		prop.Format = formatInt32
		prop.Minimum = floatPtr(0) // unsigned: never negative
	case formatInt64:
		prop.Type = typeInteger
		prop.Format = formatInt64
	case goTypeUint64:
		prop.Type = typeInteger
		prop.Format = formatInt64
		prop.Minimum = floatPtr(0) // unsigned: never negative (and may exceed int64 max)
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

// floatPtr returns a pointer to v, for the *float64 schema constraint fields.
func floatPtr(v float64) *float64 { return &v }

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
