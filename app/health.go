package app

import (
	"context"
)

// HealthStatus captures the outcome of a readiness probe.
type HealthStatus struct {
	// Name is interpolated into the unauthenticated /ready body by publicProbeError.
	// Keep it a fixed component identifier — never a tenant, host, or database name.
	Name    string
	Status  string
	Details map[string]any
	Err     error
	// PublicErr overrides the error text on the unauthenticated /ready body. Empty
	// synthesizes "<Name> unavailable"; Err never reaches that body either way.
	PublicErr string
	Critical  bool
}

// Prober exposes a uniform interface for readiness probes. SECURITY: the /ready body is
// unauthenticated, so publicProbeError never renders HealthStatus.Err — an implementation
// that wants wording other than the synthesized "<name> unavailable" sets
// HealthStatus.PublicErr, which must be a fixed string and never derived from config. The
// same constraint binds Name, which the synthesized default interpolates.
type Prober interface {
	Run(ctx context.Context) HealthStatus
}
