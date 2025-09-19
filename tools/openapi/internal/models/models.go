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
	Name        string
	Type        string
	JSONName    string
	Required    bool
	Description string
	Example     string
	Constraints map[string]string
}
