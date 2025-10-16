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
}

// TypeInfo represents type metadata for requests and responses
type TypeInfo struct {
	Name      string
	Package   string
	IsPointer bool
	Fields    []FieldInfo
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
