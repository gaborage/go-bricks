package app

import (
	"context"
	"errors"
	"maps"
	"time"

	"github.com/gaborage/go-bricks/cache"
	"github.com/gaborage/go-bricks/config"
	"github.com/gaborage/go-bricks/database"
	"github.com/gaborage/go-bricks/messaging"
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
	// publicStats allowlists the statistics keys this kind may publish on the
	// unauthenticated /ready body; every other key stays on the access-controlled debug
	// view. nil means "status only".
	publicStats []string
}

// disabledProbe describes a kind whose manager does not exist.
func disabledProbe(name string) probeDescription {
	return probeDescription{name: name, disabled: true}
}

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

// cacheProbePingTimeout caps the warm-path PING so a hung Redis reports unhealthy instead
// of consuming the caller's whole readiness budget. See wiki/cache.md#readiness for the
// cold-poll caveat.
const cacheProbePingTimeout = 500 * time.Millisecond

// The statistics key names hoisted into constants where two or more sites must agree on the
// spelling: a manager's counters rendered into a map here (convertCacheStatsToMap) and the
// allowlist that admits it, an allowlist reused across kinds, or — despite a single use in
// this file — a string value goconst (min-occurrences 3) also finds recurring elsewhere in
// the package. A key with none of those reasons lives inline in its own allowlist instead.
// These constants say nothing about the managers themselves: database.DbManager,
// messaging.Manager, and streams.Manager hardcode their own map keys in their own packages,
// unreachable from here, so it is TestPublicStatsAllowlistsMatchManagerCounters
// (readiness_test.go) that pins spelling against them.
const (
	// Shared across kinds.
	statsErrorsKey         = "errors"
	statsEvictionsKey      = "evictions"
	statsIdleCleanupsKey   = "idle_cleanups"
	statsIdleTTLSecondsKey = "idle_ttl_seconds"
	// Database: each used once below, kept because "active_connections" and
	// "max_connections" also recur across this kind's test fixtures and assertions.
	statsActiveConnectionsKey = "active_connections"
	statsMaxConnectionsKey    = "max_connections"
	// Messaging: statsActivePublishersKey is used once below, like its neighbors
	// "max_publishers" and "active_consumers" (left inlined — neither appears anywhere else
	// in the package), but "active_publishers" also recurs across this kind's test fixtures
	// and assertions, so goconst requires the symbol.
	statsActivePublishersKey = "active_publishers"
	// Cache.
	statsActiveCachesKey = "active_caches"
	statsTotalCreatedKey = "total_created"
	statsMaxSizeKey      = "max_size"
	statsIdleTTLKey      = "idle_ttl"
	// Streams: each used once below, kept as constants because every value here also
	// recurs elsewhere in the package — a zerolog field name in ModuleRegistry's
	// declaration-summary log for "consumers", test fixtures and assertions for the rest.
	// "ready" is not among them: it reuses readyStatus (app.go) at its allowlist site
	// instead of a redundant twin constant.
	statsStartedKey             = "started"
	statsConsumersKey           = "consumers"
	statsPublishersKey          = "publishers"
	statsOffsetStoreCountKey    = "offset_store_count"
	statsOffsetFlushIntervalKey = "offset_flush_interval"
)

// The per-kind public-stats allowlists: the only statistics keys that may reach the
// unauthenticated /ready 200 body. An allowlist and not a denylist, so a counter added to a
// manager tomorrow stays off that body until someone reviews it.
//
// SECURITY: two manager keys are deliberately absent. DbManager.Stats()["connections"] holds
// one entry per live pooled connection, and each entry's "key" is the resourcepool key — the
// tenant ID in a multi-tenant deployment, the named-database key otherwise — alongside
// last_used and idle_duration, so polling /ready enumerated which tenants were active and
// when each was last served. streams.Manager.Stats()["stored_offsets"] is keyed
// "<stream>/<consumer>" — declared topology that usually names the domain — with live offsets
// as values, so differencing two polls yields the per-stream message rate. /ready carries no
// authentication and no IP allowlist, and its throttles are two IP-keyed rate limits
// (app.rate.limit, koanf default 100 rps; app.rate.ippreguard.threshold, koanf default
// 2000 rps/IP) that a Go-assembled config leaves at zero entirely (ADR-049) — no barrier to
// enumeration either way.
//
// The allowlists are declared here, beside the kinds they describe, and applied at the
// render seam (publicProjection, readiness_render.go) rather than in the managers or the
// probes: the access-controlled <debug.pathprefix>/health-debug renders the same details
// map unredacted, and operators need both withheld keys there.
var (
	databasePublicStats = []string{
		statsActiveConnectionsKey, statsMaxConnectionsKey, statsIdleTTLSecondsKey, statsErrorsKey,
	}
	messagingPublicStats = []string{
		statsActivePublishersKey, "max_publishers", "active_consumers", statsIdleTTLSecondsKey,
		statsEvictionsKey, statsIdleCleanupsKey, statsErrorsKey,
	}
	cachePublicStats = []string{
		statsActiveCachesKey, statsTotalCreatedKey, statsEvictionsKey, statsIdleCleanupsKey,
		statsErrorsKey, statsMaxSizeKey, statsIdleTTLKey,
	}
	streamsPublicStats = []string{
		statsStartedKey, statsConsumersKey, statsPublishersKey, readyStatus,
		statsOffsetStoreCountKey, statsOffsetFlushIntervalKey,
	}
)

// databaseProbe describes the database kind: critical, leased through the "" key, live when
// the leased connection's Health passes. perTenant only relabels a not-configured verdict —
// the lease is always attempted (see probeDescription.perTenant).
func databaseProbe(m *database.DbManager, perTenant bool) probeDescription {
	if m == nil {
		return disabledProbe(componentDatabase)
	}
	return probeDescription{
		name:        componentDatabase,
		critical:    true,
		perTenant:   perTenant,
		publicStats: databasePublicStats,
		acquire: func(ctx context.Context) (func(context.Context) error, func(), error) {
			conn, release, err := m.Get(ctx, "")
			if err != nil {
				return nil, nil, err
			}
			return conn.Health, release, nil
		},
		stats: m.Stats,
	}
}

// messagingProbe describes the messaging kind: never critical, leased through the ""
// key, live when the leased client reports ready.
func messagingProbe(m *messaging.Manager, perTenant bool) probeDescription {
	if m == nil {
		return disabledProbe(componentMessaging)
	}
	return probeDescription{
		name:        componentMessaging,
		perTenant:   perTenant,
		publicStats: messagingPublicStats,
		acquire: func(ctx context.Context) (func(context.Context) error, func(), error) {
			client, release, err := m.Publisher(ctx, "")
			if err != nil {
				return nil, nil, err
			}
			return func(context.Context) error {
				if !client.IsReady() {
					return errPublisherNotReady
				}
				return nil
			}, release, nil
		},
		stats: m.Stats,
	}
}

// cacheProbe describes the cache kind: critical per config (ADR-046), absent when the ""
// key can never resolve (rootCacheAbsent), live when a bounded PING of the leased instance
// passes — a pooled instance is returned without a round trip, so it is pinged explicitly.
func cacheProbe(m *cache.CacheManager, critical, absent, perTenant bool) probeDescription {
	if m == nil {
		return disabledProbe(componentCache)
	}
	return probeDescription{
		name:        componentCache,
		critical:    critical,
		absent:      absent,
		perTenant:   perTenant,
		publicStats: cachePublicStats,
		acquire: func(ctx context.Context) (func(context.Context) error, func(), error) {
			instance, release, err := m.Get(ctx, "")
			if err != nil {
				return nil, nil, err
			}
			return func(ctx context.Context) error {
				pingCtx, cancel := context.WithTimeout(ctx, cacheProbePingTimeout)
				defer cancel()
				return instance.Health(pingCtx)
			}, release, nil
		},
		stats: func() map[string]any { return convertCacheStatsToMap(m.Stats()) },
	}
}

// streamsProbe describes the native stream-protocol kind: NON-critical (the reliable
// consumers reconnect on their own, so a broker flap must not take the service out of the
// load balancer), lease-less, live when every consumer and publisher is open.
func streamsProbe(m streamHandle) probeDescription {
	if m == nil {
		return disabledProbe(componentStreams)
	}
	return probeDescription{
		name:        componentStreams,
		publicStats: streamsPublicStats,
		live: func(context.Context) error {
			if !m.Ready() {
				return errStreamsNotOpen
			}
			return nil
		},
		stats: m.Stats,
	}
}

// convertCacheStatsToMap renders cache.ManagerStats as the counters map every kind reports.
func convertCacheStatsToMap(stats cache.ManagerStats) map[string]any {
	return map[string]any{
		statsActiveCachesKey: stats.ActiveCaches,
		statsTotalCreatedKey: stats.TotalCreated,
		statsEvictionsKey:    stats.Evictions,
		statsIdleCleanupsKey: stats.IdleCleanups,
		statsErrorsKey:       stats.Errors,
		statsMaxSizeKey:      stats.MaxSize,
		statsIdleTTLKey:      stats.IdleTTL,
	}
}
