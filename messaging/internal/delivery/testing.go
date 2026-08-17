package delivery

import "sync"

// ResetTracerForTesting drops the cached tracer so the next delivery binds to the
// currently installed global TracerProvider. Mirrors tracking.ResetMeterForTesting,
// and is exported for the same reason: the lane suites that swap in a test provider
// live in other packages. Not safe against a concurrent Run.
func ResetTracerForTesting() {
	sharedTracerOnce = sync.Once{}
	sharedTracer = nil
}
