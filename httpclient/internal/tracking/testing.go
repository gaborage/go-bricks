package tracking

import "sync"

// ResetMeterForTesting resets the package-level meter state so each test starts
// with a fresh meter. This is intended for use by httpclient package tests that
// cannot access the unexported resetMeterForTesting helper directly.
// It is safe to call from any test goroutine, but must not be called concurrently
// with InitHTTPMeter or any metric-recording function.
func ResetMeterForTesting() {
	meterOnce = sync.Once{}
	httpMeter = nil
	requestDuration = nil
	activeRequests = nil
	requestBodySize = nil
	responseBodySize = nil
	retriesTotal = nil
}
