package tracking

import "sync"

// ResetMeterForTesting resets the package-level meter state so a test starts
// with instruments bound to the currently installed global MeterProvider.
// Intended for messaging-package tests, which cannot reach the unexported
// state. Safe to call concurrently with initAMQPMeter — the mutex serializes
// both paths.
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
