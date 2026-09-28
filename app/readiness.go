package app

import (
	"context"
	"errors"
	"maps"
	"time"

	"github.com/gaborage/go-bricks/cache"
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
	// errProbeHasNoCheck is the liveness error for a description that carries neither a
	// lease-independent live check nor an acquire step: nothing to judge, so nothing may be
	// reported ready. Fixed text, and unreachable from any kind the framework wires.
	errProbeHasNoCheck = errors.New("probe has no liveness check")
	// errConsumerResubscribeExhausted is the liveness error for a messaging kind with a
	// declared consumer whose supervisor has given up re-subscribing. The text names no
	// queue and no consumer tag, so the log line and the debug view carry the condition
	// rather than the topology.
	errConsumerResubscribeExhausted = errors.New("consumer re-subscribe exhausted")
)

// probeDescription is what a slot hands readiness so its kind can be judged: a fixed
// component name, whether the kind is critical, how to lease it, how to check it is live,
// and its statistics. Zero-value fields mean "this kind has no such step".
//
// name reaches no unauthenticated body since ADR-120; keep it a fixed component identifier
// all the same, for the `component=` log field, the debug view's map key and the readiness
// gauge's readiness.kind attribute — never a tenant, host or database name.
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
	// how to release it. nil for kinds probed without a lease, which set live alone.
	acquire func(ctx context.Context) (live func(context.Context) error, release func(), err error)
	// live is the kind's LEASE-INDEPENDENT liveness check, judged before the lease is taken
	// and, when it fails, instead of taking one. A kind may set it beside acquire: the two
	// then run in that order, so a condition that does not need the fixed "" key is still
	// judged when that key resolves to nothing (a per-tenant deployment, where judge
	// short-circuits the lease to per_tenant).
	live func(ctx context.Context) error
	// stats snapshots the kind's counters for its two readers since ADR-120 trimmed the
	// /ready body: the access-controlled debug view, and observeSlotStats
	// (readiness_metrics.go), which feeds the messaging.consumer.* and messaging.streams.*
	// gauges. On every path that takes a lease it is called while that lease is held, so the
	// entry the probe itself pooled is counted (the messaging manager publishes
	// active_publishers: 0 beside a healthy verdict otherwise). A failing lease-independent
	// live check returns before any lease exists, so its snapshot counts no probe-held entry.
	stats func() map[string]any
}

// disabledProbe describes a kind whose manager does not exist.
func disabledProbe(name string) probeDescription {
	return probeDescription{name: name, disabled: true}
}

// probeDescription is the framework's one Prober implementation; nothing foreign reaches
// the judge (ADR-066 as amended).
var _ Prober = probeDescription{}

// Run implements Prober: judge the kind, then carry its statistics under Details with
// details.status mirroring the verdict.
func (d probeDescription) Run(ctx context.Context) HealthStatus {
	status, stats, err := d.judge(ctx)
	details := maps.Clone(stats) // never hand the caller the kind's own map
	if details == nil {
		details = make(map[string]any, 1)
	}
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
	// The lease-independent check first: it is the one arm that still has an answer when the
	// fixed "" key resolves to nothing, and a kind already known to be failing need not lease.
	if d.live != nil {
		if liveErr := d.live(ctx); liveErr != nil {
			return unhealthyStatus, d.snapshot(), liveErr
		}
	}
	if d.acquire == nil {
		if d.live == nil {
			// A kind with no arm at all is a wiring bug, not a healthy kind: disabled and
			// absent are their own fields, handled above. Fail closed — a probe that checks
			// nothing must never report ready (root CLAUDE.md: Fail Fast, no silent failures).
			return unhealthyStatus, d.snapshot(), errProbeHasNoCheck
		}
		return healthyStatus, d.snapshot(), nil
	}

	leasedLive, release, acquireErr := d.acquire(ctx)
	if acquireErr != nil {
		if config.IsNotConfigured(acquireErr) {
			return d.notConfigured(), d.snapshot(), nil
		}
		return unhealthyStatus, d.snapshot(), acquireErr
	}
	defer release() // the probe holds no scope; the snapshot below is taken before this runs

	if liveErr := leasedLive(ctx); liveErr != nil {
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

// cacheProbePingTimeout caps the warm-path PING so a hung Redis reports unhealthy instead
// of consuming the caller's whole readiness budget. See wiki/cache.md#readiness for the
// cold-poll caveat.
const cacheProbePingTimeout = 500 * time.Millisecond

// The cache counter names, hoisted into constants because convertCacheStatsToMap below and
// the tests that read its output must agree on the spelling. Every other kind's counters are
// the manager's own map keys, built in database, messaging and streams. The messaging and
// streams ones are respelled once more in readiness_metrics.go, where the gauges read them by
// name; the debug view renders whatever the manager published, without naming any of them.
const (
	statsActiveCachesKey = "active_caches"
	statsTotalCreatedKey = "total_created"
	statsEvictionsKey    = "evictions"
	statsRemovalsKey     = "removals"
	statsIdleCleanupsKey = "idle_cleanups"
	statsErrorsKey       = "errors"
	statsMaxSizeKey      = "max_size"
	statsIdleTTLKey      = "idle_ttl"
)

// convertCacheStatsToMap renders cache.ManagerStats as the counters map every kind reports.
func convertCacheStatsToMap(stats cache.ManagerStats) map[string]any {
	return map[string]any{
		statsActiveCachesKey: stats.ActiveCaches,
		statsTotalCreatedKey: stats.TotalCreated,
		statsEvictionsKey:    stats.Evictions,
		statsRemovalsKey:     stats.Removals,
		statsIdleCleanupsKey: stats.IdleCleanups,
		statsErrorsKey:       stats.Errors,
		statsMaxSizeKey:      stats.MaxSize,
		statsIdleTTLKey:      stats.IdleTTL,
	}
}
