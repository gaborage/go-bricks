package app

import (
	"context"
)

// HealthStatus captures the outcome of a readiness probe.
type HealthStatus struct {
	// Name identifies the component on the `Readiness check failed` log line, as the debug
	// view's map key, and as the readiness gauge's readiness.kind attribute. Keep it a fixed
	// component identifier — never a tenant, host, or database name: the first two are read
	// by operators, and the third is a metric dimension whose cardinality must stay bounded.
	Name string
	// Status is one of "healthy", "unhealthy", "not_configured", "disabled", "per_tenant". A
	// component is failing iff Status == "unhealthy"; /ready answers 503 (and the debug
	// summary counts an error) on failing && Critical — the gate keys off Status, not Err;
	// framework probes always set Err alongside "unhealthy".
	Status   string
	Details  map[string]any
	Err      error
	Critical bool
}

// Prober is the probe description's own contract, implemented by the framework's own
// descriptions (probeDescription) and by nothing else — there is no registration door for a
// foreign Prober, and the judge only ever walks the slot list (ADR-066 as amended).
// SECURITY: no field of HealthStatus reaches the unauthenticated /ready body, which carries
// its verdict alone (ADR-120). Err and Name go to the application log; Err, Details and Name
// go to the access-controlled <debug.pathprefix>/health-debug. So a probe may put the whole
// diagnostic in Err — the connection identity a driver renders, the address a connector names
// — unredacted, but only ever behind one of those two.
type Prober interface {
	Run(ctx context.Context) HealthStatus
}
