package app

import (
	"context"
	"time"

	"github.com/gaborage/go-bricks/config"
)

// The two readiness views — /ready's verdict and body, and the access-controlled debug
// detail — are produced here from one probe run and one predicate, so they cannot disagree
// (ADR-066, rules 2 and 3).

// statsSuffix turns a component name into its statistics key on the /ready 200 body.
const statsSuffix = "_stats"

const (
	// notReadyStatus and criticalStatus complete the status vocabulary app.go opens: the
	// former is the 503 body's verdict, the latter the debug summary's.
	notReadyStatus = "not ready"
	criticalStatus = "critical"
	// timeKey and the app-envelope keys of the /ready 200 body.
	timeKey       = "time"
	appNameKey    = "name"
	appEnvKey     = "environment"
	appVersionKey = "version"
)

// publicProjection copies the allowlisted counters out of a kind's details and stamps the
// kind's own status, so <name>_stats mirrors <name> even for a Prober that reports no
// details at all. It copies rather than filtering in place because the debug view renders
// that same map unredacted.
func publicProjection(result *HealthStatus, allow []string) map[string]any {
	public := make(map[string]any, len(allow))
	for _, key := range allow {
		if value, ok := result.Details[key]; ok {
			public[key] = value
		}
	}
	public[statusKey] = result.Status
	return public
}

// probeResult is one probe's outcome, the allowlist of the description that produced it,
// and the timing the debug view reports.
type probeResult struct {
	status      HealthStatus
	publicStats []string
	startedAt   time.Time
	duration    time.Duration
}

// readinessReport is every registered probe's result, in registration order.
type readinessReport []probeResult

// runProbe runs one probe and records its outcome, its description's allowlist and its
// timing. Both traversals below go through it, so the two views cannot disagree about what
// a result is.
func runProbe(ctx context.Context, probe Prober) probeResult {
	startedAt := time.Now()
	result := probeResult{status: probe.Run(ctx), startedAt: startedAt}
	result.duration = time.Since(startedAt)
	// SECURITY: only the framework's own descriptions declare an allowlist. A Prober from
	// outside publishes its status and nothing else, because nothing here knows which of
	// its detail keys are safe on an unauthenticated body.
	if description, ok := probe.(probeDescription); ok {
		result.publicStats = description.publicStats
	}
	return result
}

// runReadinessProbes runs every registered probe once, in registration order. This is the
// debug view's traversal: it reports one entry per kind, so it cannot stop early.
func runReadinessProbes(ctx context.Context, probes []Prober) readinessReport {
	report := make(readinessReport, 0, len(probes))
	for _, probe := range probes {
		report = append(report, runProbe(ctx, probe))
	}
	return report
}

// runUntilBlocking is /ready's traversal: judge in registration order and stop at the first
// failing critical kind, which is the 503. Nothing after it runs — a database outage must
// not add a publisher lease and a Redis PING to every poll of an endpoint that carries no
// authentication and no IP allowlist. The returned report is complete whenever found is
// false, which is exactly when readyBody renders it.
func runUntilBlocking(ctx context.Context, probes []Prober) (report readinessReport, blocking HealthStatus, found bool) {
	report = make(readinessReport, 0, len(probes))
	for _, probe := range probes {
		result := runProbe(ctx, probe)
		report = append(report, result)
		if isFailing(result.status.Status) && result.status.Critical {
			return report, result.status, true
		}
	}
	return report, HealthStatus{}, false
}

// isFailing is the one predicate both views share: a kind is failing exactly when its
// status is unhealthy, and judge guarantees such a status carries an Err.
func isFailing(status string) bool {
	return status == unhealthyStatus
}

// isReadyEquivalent reports the statuses /ready answers 200 for. Absence by design —
// not_configured, disabled, per_tenant — is not failure, so the debug summary must agree
// with /ready; otherwise the same database-free service reads "ready" on one endpoint and
// "critical" on the other.
func isReadyEquivalent(status string) bool {
	switch status {
	case healthyStatus, notConfiguredStatus, disabledStatus, perTenantStatus:
		return true
	default:
		return false
	}
}

// readyBody renders the unauthenticated 200 body: the fixed envelope, then every registered
// kind's status under <name> and its public statistics under <name>_stats.
func (r readinessReport) readyBody(app *config.AppConfig, now time.Time) map[string]any {
	body := make(map[string]any)
	body[statusKey] = readyStatus
	body[timeKey] = now.Unix()
	body["app"] = map[string]any{
		appNameKey:    app.Name,
		appEnvKey:     app.Env,
		appVersionKey: app.Version,
	}
	for i := range r {
		result := &r[i]
		body[result.status.Name] = result.status.Status
		body[result.status.Name+statsSuffix] = publicProjection(&result.status, result.publicStats)
	}
	return body
}

// notReadyBody renders the unauthenticated 503 body: the blocking kind's status and
// ADR-048's sanitized error text, never its statistics and never any other kind's status.
func notReadyBody(result *HealthStatus) map[string]any {
	return map[string]any{
		statusKey:   notReadyStatus,
		result.Name: result.Status,
		errorKey:    publicProbeError(result),
	}
}

// publicProbeError picks the error text for the unauthenticated /ready body.
//
// SECURITY: probe errors carry connection identity — pgconn renders
// `user=<username> database=<dbname>` plus the resolved host:port, and the cache probe's
// connector names the Redis address, the dial IP and (on the cold path) the tenant key.
// /ready has no authentication and no IP allowlist by design, so this never renders
// result.Err: an empty PublicErr synthesizes "<name> unavailable", and PublicErr is only
// an override for a probe that wants different fixed wording. The full error still reaches
// the application log and, where debug is enabled and access-controlled, /_sys/health-debug
// through HealthStatus.Err.
// Err is deliberately not read here at all, so a nil one cannot panic this function
// regardless of what a future caller does.
func publicProbeError(result *HealthStatus) string {
	if result.PublicErr != "" {
		return result.PublicErr
	}
	return result.Name + " unavailable"
}

// debugComponents renders the access-controlled debug view: one entry per registered kind,
// carrying the full unredacted details the /ready projection withholds.
func (r readinessReport) debugComponents() map[string]ComponentHealth {
	components := make(map[string]ComponentHealth, len(r))
	for i := range r {
		result := &r[i]
		component := ComponentHealth{
			Status:   result.status.Status,
			Critical: result.status.Critical,
			Details:  result.status.Details,
			LastRun:  result.startedAt,
			Duration: result.duration.String(),
		}
		if result.status.Err != nil {
			component.Error = result.status.Err.Error()
		}
		if component.Details == nil {
			component.Details = make(map[string]any)
		}
		components[result.status.Name] = component
	}
	return components
}

// healthSummary aggregates the debug view from the predicate /ready gates on, so the two
// views cannot disagree about what counts as a failure. unknown survives for the two shapes
// the vocabulary does not cover: no probes at all, and a consumer Prober reporting a status
// of its own invention.
func healthSummary(components map[string]ComponentHealth) HealthSummary {
	summary := HealthSummary{TotalProbes: len(components)}
	for _, component := range components {
		switch {
		case isFailing(component.Status):
			summary.ErrorCount++
			if component.Critical {
				summary.CriticalCount++
			}
		case isReadyEquivalent(component.Status):
			summary.HealthyCount++
		}
	}

	switch {
	case summary.CriticalCount > 0:
		summary.OverallStatus = criticalStatus
	case summary.ErrorCount > 0:
		summary.OverallStatus = degradedStatus
	case summary.TotalProbes > 0 && summary.HealthyCount == summary.TotalProbes:
		summary.OverallStatus = healthyStatus
	default:
		summary.OverallStatus = unknownStatus
	}
	return summary
}
