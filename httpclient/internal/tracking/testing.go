package tracking

import "sync"

// ResetMeterForTesting resets the package-level meter state so each test starts
// with a fresh meter. This is intended for use by httpclient package tests that
// cannot access the unexported meter state directly. Safe to call concurrently
// with InitHTTPMeter — the mutex serializes both paths.
func ResetMeterForTesting() {
	meterInitMu.Lock()
	defer meterInitMu.Unlock()

	meterOnce = sync.Once{}
	httpMeter = nil
	requestDuration = nil
	activeRequests = nil
	requestBodySize = nil
	responseBodySize = nil
	retriesTotal = nil
}
