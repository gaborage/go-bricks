package app

import (
	"context"
	"errors"
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
// kind's own status, so <name>_stats mirrors <name> even for a kind that reports no details
// at all. It copies rather than filtering in place because the debug view renders that same
// map unredacted.
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

// readinessReport is every rendered kind's result, in slot order.
type readinessReport []probeResult

// readinessJudge is the one traversal behind both readiness views (ADR-066 rules 2 and 3,
// ADR-067). It holds the slot list and, at judgement time, asks each slot for the probe
// description that slot sealed after its start phase — no description is built and no
// Prober is boxed per request.
type readinessJudge struct {
	slots []resourceSlot
	// started records that the startSlots walk completed, so a judge asked before it can
	// fail closed instead of reading an empty report as "nothing to gate on". It is written
	// once at the end of that walk, before serve() starts the listener goroutine, and the
	// goroutine start orders that write before every request's read of it.
	started bool
}

// gate is /ready's judgement: the walk ends at the first failing critical kind, which is the
// 503 and the reason a database outage costs the probes ahead of it and no more.
func (j readinessJudge) gate(ctx context.Context) (report readinessReport, blocking HealthStatus, found bool) {
	if !j.started {
		return nil, notStartedResult(), true
	}
	return j.walk(ctx, true)
}

// full is the debug view's judgement: every kind is judged, whatever the gate would have
// decided. It carries no fail-closed guard of its own — the debug handlers are registered
// after the startSlots walk, so it is never reached before the seal, and an honest empty
// report is the right answer if it ever were. Failing closed is the gate's job alone.
func (j readinessJudge) full(ctx context.Context) readinessReport {
	report, _, _ := j.walk(ctx, false)
	return report
}

// walk judges every slot's sealed description in slot order, optionally stopping at the
// first failing critical kind. A kind whose readiness is nil renders nothing at all.
func (j readinessJudge) walk(ctx context.Context, stopAtCritical bool) (report readinessReport, blocking HealthStatus, found bool) {
	report = make(readinessReport, 0, len(j.slots))
	for _, slot := range j.slots {
		description := slot.readiness()
		if description == nil {
			continue
		}
		startedAt := time.Now()
		result := probeResult{
			status:      description.Run(ctx),
			publicStats: description.publicStats,
			startedAt:   startedAt,
		}
		result.duration = time.Since(startedAt)
		report = append(report, result)
		if stopAtCritical && isFailing(result.status.Status) && result.status.Critical {
			return report, result.status, true
		}
	}
	return report, HealthStatus{}, false
}

// notStartedResult is the blocking result a judge asked before the start walk completed
// synthesizes: nothing started, so nothing may take traffic.
//
// SECURITY: componentReadiness is a fixed component identifier, like every other name that
// reaches the unauthenticated /ready body (ADR-048).
func notStartedResult() HealthStatus {
	return HealthStatus{
		Name:     componentReadiness,
		Status:   unhealthyStatus,
		Critical: true,
		Err:      errors.New("the application has not started"),
	}
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
func (r readinessReport) debugComponents() map[string]componentHealth {
	components := make(map[string]componentHealth, len(r))
	for i := range r {
		result := &r[i]
		component := componentHealth{
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

// summarizeHealth aggregates the debug view from the predicate /ready gates on, so the two
// views cannot disagree about what counts as a failure.
func summarizeHealth(components map[string]componentHealth) healthSummary {
	summary := healthSummary{TotalProbes: len(components)}
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
		// Reachable only at zero probes now that probeDescription is the one Prober the
		// judge sees (ADR-066 as amended): every status it can report is covered above, so
		// nothing else can land here.
		summary.OverallStatus = unknownStatus
	}
	return summary
}
