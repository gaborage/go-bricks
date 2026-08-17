package delivery

// ResetTracerForTesting drops the cached tracer so the next delivery binds to the
// currently installed global TracerProvider. Mirrors tracking.ResetMeterForTesting,
// and is exported for the same reason: the lane suites that swap in a test provider
// live in other packages. Safe against a concurrent Run: a delivery in flight keeps
// the tracer it already resolved.
func ResetTracerForTesting() {
	sharedTracer.Store(nil)
}
