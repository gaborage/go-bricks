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

// Consumer log-field names. Shared by the consumer session, worker, and
// re-subscribe log contexts so the structured field keys stay consistent
// (and to satisfy goconst now that they appear in 3+ field maps).
const (
	genericQueue     = "queue"
	genericConsumer  = "consumer"
	genericEventType = "event_type"
)

// Registry readiness polling. Previously inline literals in registry.go's
// "wait for AMQP client ready" loop.
const (
	// readyTimeoutDuration is how long DeclareInfrastructure waits for the
	// AMQP client to enter the isReady state before failing startup.
	// Long enough to cover broker handshake + channel init; short enough
	// that misconfigurations surface during deploy rather than hanging.
	readyTimeoutDuration = 30 * time.Second

	// readinessCheckInterval is the poll interval inside the readiness
	// wait loop. Tight enough to react quickly to broker connect; loose
	// enough not to spin the CPU.
	readinessCheckInterval = 100 * time.Millisecond

	// infraSetupTimeout is the best-effort budget for one consumer-infrastructure
	// setup pass (client create + readiness wait + declare loop), independent of
	// any caller deadline so lazy setup triggered by a ~5s HTTP request can't
	// abort mid-declare-loop and roll back otherwise-successful setup. It is a
	// SOFT bound, not a hard wall-clock cap: it cancels the readiness wait and
	// fail-fasts between declares, but amqp091 declare RPCs are not context-aware
	// on the wire, so a single already-issued declare that blocks can outlast it
	// (and hold consMu past expiry — the F17 lock-hold tradeoff). Must exceed
	// readyTimeoutDuration (the wait phase) with headroom for the declares.
	infraSetupTimeout = 45 * time.Second
)
