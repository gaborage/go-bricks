package reflection

import (
	"reflect"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

type sampleType struct{}

func sampleFunction() {}

func (sampleType) method() {}

func captureCallerPackage() string {
	return GetCallerPackage(1)
}

func TestGetCallerPackage(t *testing.T) {
	expected := reflect.TypeOf(sampleType{}).PkgPath()
	assert.Equal(t, expected, captureCallerPackage())
	assert.Equal(t, "", GetCallerPackage(1000))
}

func TestGetCallerPackageNoCaller(t *testing.T) {
	originalCaller := callerFn
	callerFn = func(int) (uintptr, string, int, bool) {
		return 0, "", 0, false
	}
	t.Cleanup(func() { callerFn = originalCaller })

	assert.Equal(t, "", GetCallerPackage(0))
}

func TestGetCallerPackageNilFunc(t *testing.T) {
	originalCaller := callerFn
	originalFunc := funcForPCFn
	callerFn = func(int) (uintptr, string, int, bool) {
		return 1, "", 0, true
	}
	funcForPCFn = func(uintptr) *runtime.Func {
		return nil
	}
	t.Cleanup(func() {
		callerFn = originalCaller
		funcForPCFn = originalFunc
	})

	assert.Equal(t, "", GetCallerPackage(0))
}

func TestGetCallerPackageFallback(t *testing.T) {
	originalCaller := callerFn
	originalFunc := funcForPCFn
	callerFn = func(int) (uintptr, string, int, bool) {
		return reflect.ValueOf(strings.TrimSpace).Pointer(), "", 0, true
	}
	funcForPCFn = originalFunc
	t.Cleanup(func() {
		callerFn = originalCaller
		funcForPCFn = originalFunc
	})

	assert.Equal(t, "strings", GetCallerPackage(0))
}

func TestExtractPackageFromName(t *testing.T) {
	expected := reflect.TypeOf(sampleType{}).PkgPath()
	assert.Equal(t, expected, extractPackageFromName(expected+".sampleFunction"))
	assert.Equal(t, expected, extractPackageFromName(expected+".(*sampleType).method"))
	assert.Equal(t, "strings", extractPackageFromName("strings.TrimSpace"))
	assert.Equal(t, "", extractPackageFromName("no_delimiters"))
}

func TestExtractHandlerName(t *testing.T) {
	assert.Equal(t, "sampleFunction", ExtractHandlerName(sampleFunction))
	methodName := ExtractHandlerName(sampleType{}.method)
	assert.True(t, strings.HasPrefix(methodName, "method"))
	assert.Equal(t, "", ExtractHandlerName(nil))
	assert.Equal(t, "", ExtractHandlerName(42))
}

func TestExtractHandlerNameNilRuntimeFunc(t *testing.T) {
	originalFunc := funcForPCFn
	funcForPCFn = func(uintptr) *runtime.Func { return nil }
	t.Cleanup(func() { funcForPCFn = originalFunc })

	assert.Equal(t, "", ExtractHandlerName(sampleFunction))
}

func TestExtractHandlerNameFromName(t *testing.T) {
	assert.Equal(t, "handler", extractHandlerNameFromName("pkg.handler"))
	assert.Equal(t, "handler", extractHandlerNameFromName("handler"))
}

func TestGetTypeName(t *testing.T) {
	ptrType := reflect.TypeOf(&sampleType{})
	expected := reflect.TypeOf(sampleType{}).PkgPath() + ".sampleType"
	assert.Equal(t, expected, GetTypeName(ptrType))
	assert.Equal(t, "int", GetTypeName(reflect.TypeOf(42)))
	assert.Equal(t, "", GetTypeName(nil))
}

func TestGetTypeNameShort(t *testing.T) {
	ptrType := reflect.TypeOf(&sampleType{})
	assert.Equal(t, "sampleType", GetTypeNameShort(ptrType))
	assert.Equal(t, "int", GetTypeNameShort(reflect.TypeOf(42)))
	assert.Equal(t, "", GetTypeNameShort(nil))
}
