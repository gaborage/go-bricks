// Package server provides enhanced HTTP handler functionality with type-safe request/response handling.
package server

import (
	"maps"
	"reflect"
	"sync"

	"github.com/gaborage/go-bricks/jose"
)

// RouteDescriptor captures metadata about a registered route.
//
// Routes registered through the raw RouteRegistrar.Add path (as opposed to the typed
// server.GET/POST helpers) carry only method/path/handler metadata: RequestType, ResponseType,
// InboundJOSE, and OutboundJOSE are nil, and ModuleName is empty, because a raw handler exposes
// no request/response models. Consumers iterating the registry must nil-check those fields.
type RouteDescriptor struct {
	Method       string       // HTTP method (GET, POST, etc.)
	Path         string       // Route path pattern (/users/:id)
	Listener     string       // Serving listener: empty for the application listener, ListenerProbes for the probe listener
	HandlerID    string       // Unique identifier for handler function
	HandlerName  string       // Function name (e.g., "getUser")
	ModuleName   string       // Module that registered this route (empty for raw routes)
	Package      string       // Go package path
	RequestType  reflect.Type // Request type T from HandlerFunc[T, R]; nil for raw routes
	ResponseType reflect.Type // Response type R from HandlerFunc[T, R]; nil for raw routes
	Middleware   []string     // Applied middleware names
	Tags         []string     // Optional grouping tags
	Summary      string       // Optional summary from comments
	Description  string       // Optional description from comments
	RawResponse  bool         // If true, bypass APIResponse envelope (for Strangler Fig migration)
	InboundJOSE  *jose.Policy // Resolved at registration time from request type's jose: tag; nil for raw routes
	OutboundJOSE *jose.Policy // Resolved at registration time from response type's jose: tag; nil for raw routes
}

// ListenerProbes is RouteDescriptor.Listener for a route served on the probe listener
// (server.probes.port, ADR-120).
const ListenerProbes = "probes"

// formatHandlerID builds the canonical HandlerID for a route ("METHOD:/full/path"). Both the
// typed registration path (RegisterHandler) and the raw path (RouteRegistrar.Add) use it so the
// identifiers stay identical across paths — consumers keying inventories by HandlerID depend on it.
// A probe-listener descriptor's ID is per-listener: it names the unprefixed probe path on that
// listener (GET:/ready) and intentionally differs from the application listener's conflict-tracker
// key for the reserved probe path (GET:/api/ready under base /api).
func formatHandlerID(method, fullPath string) string {
	return method + ":" + fullPath
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

// cloneDescriptor deep-copies slice fields and both JOSE policies so a registry
// reader cannot mutate a live route by writing through the returned pointers.
// Policy map values inside ProtectedHeaders are not copied further: they are
// consumer-supplied any and stay shared with the clone's map.
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
	out.InboundJOSE = clonePolicy(d.InboundJOSE)
	out.OutboundJOSE = clonePolicy(d.OutboundJOSE)
	return out
}

// clonePolicy returns a detached copy of p, including a shallow clone of
// ProtectedHeaders. A nil policy stays nil. Map values are not copied.
func clonePolicy(p *jose.Policy) *jose.Policy {
	if p == nil {
		return nil
	}
	out := *p
	if p.ProtectedHeaders != nil {
		out.ProtectedHeaders = maps.Clone(p.ProtectedHeaders)
	}
	return &out
}
