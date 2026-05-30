package models

// Project represents a simplified project structure for OpenAPI generation
type Project struct {
	Name        string
	Description string
	Version     string
	Modules     []Module
	// Types is the registry of every named struct type reachable from a route's
	// request or response (including nested and recursively-referenced types),
	// keyed by schema name. The generator emits one component schema per entry.
	Types map[string]*TypeInfo
}

// Module represents a go-bricks module
type Module struct {
	Name        string
	Package     string
	Description string
	Routes      []Route
}

// Route represents a discovered HTTP route
type Route struct {
	Method      string
	Path        string
	HandlerName string
	Summary     string
	Description string
	Tags        []string
	Request     *TypeInfo
	Response    *TypeInfo
	// Module and Package identify the owning go-bricks module, stamped at
	// discovery time. They survive the module->route flattening in the generator
	// (getAllRoutes) so later passes can namespace operationIds and disambiguate
	// component names across modules.
	Module  string
	Package string
	// SuccessStatus is the HTTP status of the success response, derived from the
	// handler's result constructor (server.Created -> 201, Accepted -> 202,
	// NewResult(n) -> n). Zero means "use the default" (200).
	SuccessStatus int
	// RawResponse is true when the route is registered WithRawResponse(): the
	// handler bypasses the standard data/meta envelope and returns its payload
	// directly (Strangler-Fig migration).
	RawResponse bool
}

// TypeInfo represents type metadata for requests and responses
type TypeInfo struct {
	Name      string
	Package   string
	IsPointer bool
	Fields    []FieldInfo
	// JOSE is true when the struct carries a `jose:"..."` tag on any field — typically
	// a sentinel `_ struct{}` field. Routes whose request or response type is JOSE-tagged
	// emit Content-Type: application/jose in the OpenAPI spec while keeping the documented
	// plaintext schema as the source of truth (the on-the-wire compact JOSE serialization
	// wraps that plaintext after decrypt-and-verify).
	JOSE bool
	// NoContent is true when the response type is server.NoContentResult — the
	// route returns 204 with no body. Such a TypeInfo carries no Name/Fields, so
	// no component schema is generated; later passes use this flag to emit a 204
	// response instead of a 200 with a body.
	NoContent bool
}

// FieldInfo represents a struct field with validation metadata
type FieldInfo struct {
	Name          string
	Type          string
	JSONName      string            // Parsed from `json:"name"` tag
	ParamType     string            // "path", "query", "header", or "" for body fields
	ParamName     string            // Parsed from `param:"name"`, `query:"name"`, or `header:"name"` tags
	Required      bool              // Parsed from `validate:"required"` tag
	Description   string            // Parsed from `doc:"..."` tag
	Example       string            // Parsed from `example:"..."` tag
	RawValidation string            // Raw validation tag string (e.g., "required,email,min=5")
	Constraints   map[string]string // Parsed validation constraints for OpenAPI mapping
	// RefName is the schema name of the field's underlying named struct type when
	// the field (or its slice/pointer element) resolves to one in the registry.
	// Set, the property is emitted as a $ref (or items.$ref for a slice) rather
	// than an inline object.
	RefName string
	// MapValueRefName is the schema name of a map field's value struct type when
	// it resolves to one in the registry (e.g. map[string]Address). Set, the
	// property is an object whose additionalProperties is a $ref to that schema.
	// Distinct from RefName: a map field is never itself a $ref.
	MapValueRefName string
	// UnderlyingKind is the OpenAPI 3-way kind ("integer", "number", or "string")
	// a named, non-struct scalar type resolves to — e.g. `type Cents int64` ->
	// "integer", time.Duration -> "integer". Empty for builtin primitives (handled
	// directly), structs, and unresolved types. Consumed by the type/constraint
	// mappers so a named numeric is documented as its underlying kind.
	UnderlyingKind string
}
