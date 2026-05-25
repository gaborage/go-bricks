package messaging

import "time"

// Consumer concurrency + prefetch tuning. Previously inline literals in
// helpers.go's worker auto-scaling logic.
const (
	// maxWorkersPerConsumer is the safeguard cap on a single consumer's
	// concurrent worker pool. Set high enough that production workloads
	// rarely hit it, low enough to prevent runaway resource use from a
	// typo in DeclareConsumer.Workers.
	maxWorkersPerConsumer = 200

	// defaultPrefetchMultiplier is the auto-scale factor applied when
	// PrefetchCount is unset: prefetch = workers * defaultPrefetchMultiplier
	// (then capped at maxDefaultPrefetch below). 10 keeps the pipeline
	// full without overloading any single consumer.
	defaultPrefetchMultiplier = 10

	// maxDefaultPrefetch caps the auto-scaled prefetch (workers *
	// defaultPrefetchMultiplier) to avoid pulling more unacked messages
	// from the broker than a worker pool can chew through.
	maxDefaultPrefetch = 500

	// maxPrefetchCount is the hard upper bound applied AFTER the operator's
	// PrefetchCount value (whether explicit or auto-scaled), to prevent a
	// misconfiguration from causing memory exhaustion on consumer hosts.
	maxPrefetchCount = 1000
)

// Registry readiness polling. Previously inline literals in registry.go's
// "wait for AMQP client ready" loop.
const (
	// readyTimeoutDuration is how long DeclareConsumers waits for the
	// AMQP client to enter the isReady state before failing startup.
	// Long enough to cover broker handshake + channel init; short enough
	// that misconfigurations surface during deploy rather than hanging.
	readyTimeoutDuration = 30 * time.Second

	// readinessCheckInterval is the poll interval inside the readiness
	// wait loop. Tight enough to react quickly to broker connect; loose
	// enough not to spin the CPU.
	readinessCheckInterval = 100 * time.Millisecond
)
