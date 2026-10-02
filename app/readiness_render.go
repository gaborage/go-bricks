package app

import (
	"context"
	"errors"
	"time"
)

// The readiness views are produced here from one probe run and one predicate, so they
// cannot disagree (ADR-066 rule 2). /ready's own body carries the verdict alone since
// ADR-120, so the report itself feeds two readers: the access-controlled debug detail, and
// record, which hands each judged kind's verdict to the store behind the readiness gauge.

const (
	// notReadyStatus and criticalStatus complete the status vocabulary app.go opens: the
	// former is the 503 body's verdict, the latter the debug summary's. notReadyStatus must
	// equal server's statusNotReady, the verdict /ready serves while the server is stopping.
	notReadyStatus = "not ready"
	criticalStatus = "critical"
)

// probeResult is one probe's outcome and the timing the debug view reports.
type probeResult struct {
	status    HealthStatus
	startedAt time.Time
	duration  time.Duration
}

// readinessReport is every judged kind's result, in slot order.
type readinessReport []probeResult

// readinessJudge is the one traversal behind both readiness views (ADR-066 rule 2,
// ADR-067). It holds the slot list and, at judgement time, asks each slot for the probe
// description that slot sealed after its start phase — no description is built or boxed per
// request.
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
			status:    description.Run(ctx),
			startedAt: startedAt,
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
func notStartedResult() HealthStatus {
	return readinessFailure(errors.New("the application has not started"))
}

// readinessFailure is a blocking result that names readiness itself, for a failure no kind
// can carry: the judge asked before start, or a request canceled while it waited on the
// shared judgment, before any kind was judged for it. Since ADR-120 that name reaches the
// `Readiness check failed` log line alone — the 503 body carries the verdict and nothing
// else — so it is what tells the two apart after the fact.
func readinessFailure(err error) HealthStatus {
	return HealthStatus{
		Name:     componentReadiness,
		Status:   unhealthyStatus,
		Critical: true,
		Err:      err,
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

// record is the second view of one report, beside debugComponents: every judged kind's
// verdict handed to the store the readiness gauge reads and the non-critical WARN is
// emitted from. Both judgment entry points call it — the shared /ready flight and the debug
// view's full() — so recording is a step at the entry point rather than a side effect inside
// the walk. The gate's short-circuit appends the blocking kind before it returns, so the
// recorded set is the judged set either way. It is also what carries a kind's verdict out of
// a /ready request at all, now that the body no longer does (ADR-120).
//
// Each verdict carries the startedAt of the probe that produced it, which is what the store
// orders two judgments' verdicts by: the entry points run concurrently, so a report's commit
// time says nothing about when it observed what it carries.
func (r readinessReport) record(s *verdictStore) {
	for i := range r {
		s.record(&r[i].status, r[i].startedAt)
	}
}

// readyBody and notReadyBody are the whole unauthenticated /ready contract (ADR-120): one
// verdict key, on either listener, for a caller that is only ever deciding whether to route
// traffic here.
//
// SECURITY: nothing derived from a probe reaches these bodies. /ready has no authentication
// and no IP allowlist by design, and its throttles are two IP-keyed rate limits a
// Go-assembled config leaves at zero entirely (ADR-049), so what it published was
// enumerable: which kinds a service wires, their live counters, and — through the blocking
// kind's name and ADR-048's "<kind> unavailable" text — which one is down. All of it now
// lives on the `Readiness check failed` log line (component=<kind>, full error), the
// access-controlled debug view, and the readiness gauge.
func readyBody() map[string]string {
	return map[string]string{statusKey: readyStatus}
}

func notReadyBody() map[string]string {
	return map[string]string{statusKey: notReadyStatus}
}

// debugComponents renders the access-controlled debug view: one entry per registered kind,
// carrying the full unredacted details, which is where every counter and every error text
// lives now that /ready answers its verdict alone (ADR-120).
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
		// Reachable only at zero probes now that probeDescription is the only probe the
		// judge sees (ADR-066 as amended): every status it can report is covered above, so
		// nothing else can land here.
		summary.OverallStatus = unknownStatus
	}
	return summary
}
