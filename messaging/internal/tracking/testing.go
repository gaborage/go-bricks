package tracking

import "sync"

// ResetMeterForTesting resets the package-level meter state so a test starts
// with instruments bound to the currently installed global MeterProvider.
// Intended for messaging-package tests, which cannot reach the unexported
// state. Not safe against a concurrent getAMQPMeter: that path runs
// meterOnce.Do without meterInitMu, so callers must ensure no goroutine is
// recording metrics when this runs.
func ResetMeterForTesting() {
	meterInitMu.Lock()
	defer meterInitMu.Unlock()

	meterOnce = sync.Once{}
	amqpMeter = nil
	amqpOperationDuration = nil
	amqpMessagesSent = nil
	amqpMessagesConsumed = nil
	amqpPublishRetries = nil
	amqpConnectionCreate = nil
	amqpConnectionClose = nil
	amqpChannelCreate = nil
	amqpChannelClose = nil
}
