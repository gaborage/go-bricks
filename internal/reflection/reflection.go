// Package reflection provides internal utility functions for reflection operations in the go-bricks framework.
package reflection

import (
	"reflect"
	"runtime"
	"strings"
)

var (
	callerFn    = runtime.Caller
	funcForPCFn = runtime.FuncForPC
)

// GetCallerPackage extracts the package path of the calling function.
// The skip parameter is relative to the caller of GetCallerPackage.
func GetCallerPackage(skip int) string {
	pc, _, _, ok := callerFn(skip + 1)
	if !ok {
		return ""
	}

	fn := funcForPCFn(pc)
	if fn == nil {
		return ""
	}

	return extractPackageFromName(fn.Name())
}

// ExtractHandlerName gets the function name from a handler function
func ExtractHandlerName(handler any) string {
	if handler == nil {
		return ""
	}

	v := reflect.ValueOf(handler)
	if v.Kind() != reflect.Func {
		return ""
	}

	fn := funcForPCFn(v.Pointer())
	if fn == nil {
		return ""
	}

	return extractHandlerNameFromName(fn.Name())
}

// GetTypeName returns the fully qualified type name
func GetTypeName(t reflect.Type) string {
	if t == nil {
		return ""
	}

	// Handle pointer types
	if t.Kind() == reflect.Pointer {
		t = t.Elem()
	}

	if t.PkgPath() == "" {
		return t.Name()
	}

	return t.PkgPath() + "." + t.Name()
}

// GetTypeNameShort returns just the type name without package path
func GetTypeNameShort(t reflect.Type) string {
	if t == nil {
		return ""
	}

	// Handle pointer types
	if t.Kind() == reflect.Pointer {
		t = t.Elem()
	}

	return t.Name()
}

func extractPackageFromName(name string) string {
	lastSlash := strings.LastIndex(name, "/")
	if lastSlash >= 0 {
		remaining := name[lastSlash+1:]
		if dot := strings.Index(remaining, "."); dot >= 0 {
			return name[:lastSlash+1+dot]
		}
	}

	if dot := strings.LastIndex(name, "."); dot >= 0 {
		packagePart := name[:dot]
		if parenIndex := strings.LastIndex(packagePart, "("); parenIndex >= 0 {
			if dotIndex := strings.LastIndex(packagePart[:parenIndex], "."); dotIndex >= 0 {
				return packagePart[:dotIndex]
			}
		}
		return packagePart
	}

	return ""
}

func extractHandlerNameFromName(name string) string {
	if lastDot := strings.LastIndex(name, "."); lastDot >= 0 {
		return name[lastDot+1:]
	}
	return name
}
