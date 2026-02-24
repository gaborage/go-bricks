// Package server provides enhanced HTTP handler functionality with type-safe request/response handling.
package server

import (
	"reflect"
	"sync"
)

// RouteDescriptor captures metadata about a registered route
type RouteDescriptor struct {
	Method       string       // HTTP method (GET, POST, etc.)
	Path         string       // Route path pattern (/users/:id)
	HandlerID    string       // Unique identifier for handler function
	HandlerName  string       // Function name (e.g., "getUser")
	ModuleName   string       // Module that registered this route
	Package      string       // Go package path
	RequestType  reflect.Type // Request type T from HandlerFunc[T, R]
	ResponseType reflect.Type // Response type R from HandlerFunc[T, R]
	Middleware   []string     // Applied middleware names
	Tags         []string     // Optional grouping tags
	Summary      string       // Optional summary from comments
	Description  string       // Optional description from comments
	RawResponse  bool         // If true, bypass APIResponse envelope (for Strangler Fig migration)
}

// RouteRegistry maintains discovered routes for introspection
type RouteRegistry struct {
	mu     sync.RWMutex
	routes []RouteDescriptor
}

// Global registry instance (package level)
var DefaultRouteRegistry = &RouteRegistry{}

// Register adds a route descriptor to the registry
func (r *RouteRegistry) Register(descriptor *RouteDescriptor) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.routes = append(r.routes, cloneDescriptor(descriptor))
}

// Routes returns a copy of all registered routes
func (r *RouteRegistry) Routes() []RouteDescriptor {
	r.mu.RLock()
	defer r.mu.RUnlock()
	result := make([]RouteDescriptor, len(r.routes))
	for i := range r.routes {
		result[i] = cloneDescriptor(&r.routes[i])
	}
	return result
}

// ByModule returns routes for a specific module
func (r *RouteRegistry) ByModule(moduleName string) []RouteDescriptor {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var result []RouteDescriptor
	for i := range r.routes {
		if r.routes[i].ModuleName == moduleName {
			result = append(result, cloneDescriptor(&r.routes[i]))
		}
	}
	return result
}

// ByPath returns routes for a specific path pattern
func (r *RouteRegistry) ByPath(path string) []RouteDescriptor {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var result []RouteDescriptor
	for i := range r.routes {
		if r.routes[i].Path == path {
			result = append(result, cloneDescriptor(&r.routes[i]))
		}
	}
	return result
}

// Clear removes all registered routes (useful for testing)
func (r *RouteRegistry) Clear() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.routes = nil
}

// Count returns the number of registered routes
func (r *RouteRegistry) Count() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.routes)
}

// RouteOption for configuring route descriptors during registration
type RouteOption func(*RouteDescriptor)

// WithModule sets the module name for a route
func WithModule(name string) RouteOption {
	return func(d *RouteDescriptor) {
		d.ModuleName = name
	}
}

// WithTags adds tags to a route for grouping and organization
func WithTags(tags ...string) RouteOption {
	return func(d *RouteDescriptor) {
		d.Tags = append(d.Tags, tags...)
	}
}

// WithSummary sets a summary description for the route
func WithSummary(summary string) RouteOption {
	return func(d *RouteDescriptor) {
		d.Summary = summary
	}
}

// WithDescription sets a detailed description for the route
func WithDescription(description string) RouteOption {
	return func(d *RouteDescriptor) {
		d.Description = description
	}
}

// WithMiddleware records middleware applied to this route
func WithMiddleware(middlewareNames ...string) RouteOption {
	return func(d *RouteDescriptor) {
		d.Middleware = append(d.Middleware, middlewareNames...)
	}
}

// WithHandlerName explicitly sets the handler function name
func WithHandlerName(name string) RouteOption {
	return func(d *RouteDescriptor) {
		d.HandlerName = name
	}
}

// WithRawResponse configures the route to bypass the standard APIResponse envelope,
// returning the handler's response directly as JSON. Useful for Strangler Fig migrations
// where legacy endpoints must return their original response format.
func WithRawResponse() RouteOption {
	return func(d *RouteDescriptor) {
		d.RawResponse = true
	}
}

// AddRoute is an alias for Register for consistency with test expectations
func (r *RouteRegistry) AddRoute(descriptor *RouteDescriptor) {
	r.Register(descriptor)
}

// RoutesByMethod filters routes by HTTP method
func (r *RouteRegistry) RoutesByMethod(method string) []RouteDescriptor {
	r.mu.RLock()
	defer r.mu.RUnlock()

	var result []RouteDescriptor
	for i := range r.routes {
		if r.routes[i].Method == method {
			result = append(result, cloneDescriptor(&r.routes[i]))
		}
	}
	return result
}

// RoutesByModule filters routes by module name (alias for ByModule)
func (r *RouteRegistry) RoutesByModule(moduleName string) []RouteDescriptor {
	return r.ByModule(moduleName)
}

// cloneDescriptor deep-copies slice fields to prevent external mutation
func cloneDescriptor(d *RouteDescriptor) RouteDescriptor {
	if d == nil {
		return RouteDescriptor{}
	}

	out := *d
	if d.Tags != nil {
		out.Tags = append([]string(nil), d.Tags...)
	}
	if d.Middleware != nil {
		out.Middleware = append([]string(nil), d.Middleware...)
	}
	return out
}
