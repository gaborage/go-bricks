package app

import (
	"reflect"
	"sync"

	"github.com/gaborage/go-bricks/server"
)

// Describer is an optional interface that modules can implement to provide
// additional metadata for documentation generation and introspection.
type Describer interface {
	DescribeRoutes() []server.RouteDescriptor
	DescribeModule() ModuleDescriptor
}

// ModuleDescriptor captures module-level metadata
type ModuleDescriptor struct {
	Name        string   // Module name
	Version     string   // Module version
	Description string   // Module description
	Tags        []string // Module tags for grouping
	BasePath    string   // Base path for all module routes
}

// ModuleInfo contains both the module instance and its metadata
type ModuleInfo struct {
	Module     Module           // The actual module instance
	Descriptor ModuleDescriptor // Module metadata
	Package    string           // Go package path
}

// IsDescriber checks if a module implements the Describer interface
func IsDescriber(m Module) (Describer, bool) {
	d, ok := m.(Describer)
	return d, ok
}

// MetadataRegistry tracks discovered modules for introspection
type MetadataRegistry struct {
	mu      sync.RWMutex
	modules map[string]ModuleInfo
}

// DefaultModuleRegistry is the global module metadata registry
var DefaultModuleRegistry = &MetadataRegistry{
	modules: make(map[string]ModuleInfo),
}

// RegisterModule adds a module to the metadata registry
func (r *MetadataRegistry) RegisterModule(name string, module Module, pkg string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	info := ModuleInfo{
		Module:  module,
		Package: pkg,
	}

	// Get descriptor if module implements Describer
	if describer, ok := IsDescriber(module); ok {
		info.Descriptor = describer.DescribeModule()
	} else {
		// Default descriptor
		info.Descriptor = ModuleDescriptor{
			Name: name,
		}
	}

	r.modules[name] = info
}

// Modules returns a copy of all registered module information
func (r *MetadataRegistry) Modules() map[string]ModuleInfo {
	r.mu.RLock()
	defer r.mu.RUnlock()
	result := make(map[string]ModuleInfo, len(r.modules))
	for k, v := range r.modules {
		result[k] = v
	}
	return result
}

// Module returns information for a specific module
func (r *MetadataRegistry) Module(name string) (ModuleInfo, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	info, exists := r.modules[name]
	return info, exists
}

// Clear removes all registered modules (useful for testing)
func (r *MetadataRegistry) Clear() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.modules = make(map[string]ModuleInfo)
}

// Count returns the number of registered modules
func (r *MetadataRegistry) Count() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.modules)
}

// getModulePackage extracts the package path from a module instance
func getModulePackage(module Module) string {
	// Use reflection to get the package path
	moduleType := reflect.TypeOf(module)
	if moduleType.Kind() == reflect.Ptr {
		moduleType = moduleType.Elem()
	}
	return moduleType.PkgPath()
}
