package app

import (
	"context"
	"errors"
	"maps"

	"github.com/gaborage/go-bricks/config"
)

// Readiness is one module: every kind is judged by the same machine from a probe
// description (CONTEXT.md), so the status vocabulary, the lease→liveness order and the
// criticality decision have one home. Prober and HealthStatus (health.go) stay the exported
// seam; this file is what sits behind it.

var (
	// errPublisherNotReady is the liveness error for a leased AMQP client that is not ready:
	// unhealthy always carries an Err (see judge), so /ready's gate and the debug summary can
	// share one predicate.
	errPublisherNotReady = errors.New("publisher not ready")
	// errStreamsNotOpen is the liveness error for a streams manager whose consumers or
	// publishers are not all open.
	errStreamsNotOpen = errors.New("stream consumers not open")
)

// probeDescription is what a slot hands readiness so its kind can be judged: a fixed
// component name, whether the kind is critical, how to lease it, how to check it is live,
// and its statistics. Zero-value fields mean "this kind has no such step".
//
// SECURITY: name is interpolated into the unauthenticated /ready body ("<name> unavailable",
// ADR-048) — keep it a fixed component identifier, never a tenant, host or database name.
type probeDescription struct {
	name string
	// critical is decided once, when the description is built (config verdict × absence);
	// judge never re-derives it.
	critical bool
	// disabled marks a kind with no manager at all: reported as disabled, nothing is leased.
	disabled bool
	// absent marks a kind whose fixed "" key can never resolve (see rootCacheAbsent):
	// reported as not_configured (or per_tenant) without attempting a lease.
	absent bool
	// perTenant relabels a not-configured verdict as per_tenant: a multi-tenant deployment
	// has the resource, just not under the fixed "" key. It never short-circuits the lease —
	// a shared-ledger control-plane database (ADR-041) resolves through exactly that key.
	perTenant bool
	// acquire leases the kind's fixed-key resource and returns how to check it is live and
	// how to release it. nil for kinds probed without a lease, which set live directly.
	acquire func(ctx context.Context) (live func(context.Context) error, release func(), err error)
	// live checks a lease-less kind (only read when acquire is nil).
	live func(ctx context.Context) error
	// stats snapshots the kind's counters; called while the lease is held so the entry the
	// probe itself pooled is counted (the messaging manager publishes active_publishers: 0
	// beside a healthy verdict otherwise).
	stats func() map[string]any
}

// disabledProbe describes a kind whose manager does not exist.
func disabledProbe(name string) probeDescription {
	return probeDescription{name: name, disabled: true}
}

// Run implements Prober: judge the kind, then carry its statistics under Details with
// details.status mirroring the verdict.
func (d probeDescription) Run(ctx context.Context) HealthStatus {
	status, stats, err := d.judge(ctx)
	details := make(map[string]any, len(stats)+1)
	maps.Copy(details, stats)
	details[statusKey] = status
	return HealthStatus{
		Name:     d.name,
		Status:   status,
		Details:  details,
		Err:      err,
		Critical: d.critical,
	}
}

// judge is the one lease→liveness→status machine. Every arm that returns unhealthy also
// returns a non-nil error, so "failing" is one predicate (status == unhealthy) for both
// the /ready gate and the debug summary.
func (d probeDescription) judge(ctx context.Context) (status string, stats map[string]any, err error) {
	if d.disabled {
		return disabledStatus, nil, nil
	}
	if d.absent {
		return d.notConfigured(), d.snapshot(), nil
	}
	live := d.live
	if d.acquire != nil {
		leasedLive, release, acquireErr := d.acquire(ctx)
		if acquireErr != nil {
			if config.IsNotConfigured(acquireErr) {
				return d.notConfigured(), d.snapshot(), nil
			}
			return unhealthyStatus, d.snapshot(), acquireErr
		}
		defer release() // the probe holds no scope; the snapshot below is taken before this runs
		live = leasedLive
	}
	if liveErr := live(ctx); liveErr != nil {
		return unhealthyStatus, d.snapshot(), liveErr
	}
	return healthyStatus, d.snapshot(), nil
}

// notConfigured is the verdict for a kind that has nothing under the fixed "" key.
func (d probeDescription) notConfigured() string {
	if d.perTenant {
		return perTenantStatus
	}
	return notConfiguredStatus
}

func (d probeDescription) snapshot() map[string]any {
	if d.stats == nil {
		return nil
	}
	return d.stats()
}
