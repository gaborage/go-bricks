//go:build !integration

package sealed_test

// closeSharedBroker has no broker to close in a build without the integration tag.
func closeSharedBroker() {}
