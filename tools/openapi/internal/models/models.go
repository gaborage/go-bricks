package models

// Project represents a simplified project structure for OpenAPI generation
type Project struct {
	Name        string
	Description string
	Version     string
	Modules     []Module
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
}
