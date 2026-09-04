package inbox

import (
	"context"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	"github.com/gaborage/go-bricks/app"
)

// The ledger's dedup-hit counter. A redelivery that finds its id already
// processed is the ONLY observable of a replay campaign — the handler is skipped
// and the message ACKed — so the short-circuit counts and logs, never silently.
const (
	metricDedupHits = "inbox.dedup.hits"

	// attrTenantPresent says whether the hit was keyed under a tenant, as a
	// bool: the tenant id itself is request-derived and stays out of the label set.
	attrTenantPresent = "inbox.tenant.present"
	// attrSealed says whether the key came from a sealed envelope
	// (`<SignFamily>:<jti>`, ADR-097) rather than a header.
	attrSealed = "inbox.sealed"
)

// The four label sets a hit can carry, built once: the short-circuit is the path
// a replay campaign hammers, so it allocates nothing per hit. Indexed by
// [tenantPresent][sealed].
var dedupHitAttrs = [2][2]metric.AddOption{
	{dedupHitAttrsFor(false, false), dedupHitAttrsFor(false, true)},
	{dedupHitAttrsFor(true, false), dedupHitAttrsFor(true, true)},
}

func dedupHitAttrsFor(tenantPresent, sealed bool) metric.AddOption {
	return metric.WithAttributes(attribute.Bool(attrTenantPresent, tenantPresent), attribute.Bool(attrSealed, sealed))
}

func boolIndex(b bool) int {
	if b {
		return 1
	}
	return 0
}

// registerDedupCounter creates the dedup-hit counter under the inbox's meter.
// An instrument failure is reported, not fatal: a ledger that dedups without
// counting is worse observed, not broken.
func (m *Module) registerDedupCounter(deps *app.ModuleDeps) {
	if deps.MeterProvider == nil {
		return
	}
	counter, err := deps.MeterProvider.Meter(inboxMeterName).Int64Counter(metricDedupHits,
		metric.WithDescription("Redeliveries whose event id was already in the inbox ledger"))
	if err != nil {
		m.logger.Warn().Err(err).Msg("Inbox dedup-hit counter unavailable")
		return
	}
	m.dedupHits = counter
}

// recordDedupHit is the short-circuit's observability: one counter increment
// and one log line. Both carry the id's PRESENCE and LENGTH, never its value —
// the id is publisher-written, and a replayed one is exactly the value an
// attacker chose.
func (m *Module) recordDedupHit(ctx context.Context, tenantID, eventID string, sealed bool) {
	tenantPresent := tenantID != ""
	if m.dedupHits != nil {
		m.dedupHits.Add(ctx, 1, dedupHitAttrs[boolIndex(tenantPresent)][boolIndex(sealed)])
	}
	m.logger.Info().
		Bool("tenantPresent", tenantPresent).
		Int("eventIdLength", len(eventID)).
		Bool("sealed", sealed).
		Msg("Inbox dedup hit: event already processed, handler skipped")
}
