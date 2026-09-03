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
	// attrSealed says whether the key came from a sealed envelope. Always false
	// until the sealed typed door lands; present now so dashboards do not move.
	attrSealed = "inbox.sealed"
)

// The two label sets a hit can carry, built once: the short-circuit is the path
// a replay campaign hammers, so it allocates nothing per hit.
var (
	dedupHitAttrsTenant = metric.WithAttributes(
		attribute.Bool(attrTenantPresent, true), attribute.Bool(attrSealed, false))
	dedupHitAttrsNoTenant = metric.WithAttributes(
		attribute.Bool(attrTenantPresent, false), attribute.Bool(attrSealed, false))
)

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
func (m *Module) recordDedupHit(ctx context.Context, tenantID, eventID string) {
	tenantPresent := tenantID != ""
	if m.dedupHits != nil {
		attrs := dedupHitAttrsNoTenant
		if tenantPresent {
			attrs = dedupHitAttrsTenant
		}
		m.dedupHits.Add(ctx, 1, attrs)
	}
	m.logger.Info().
		Bool("tenantPresent", tenantPresent).
		Int("eventIdLength", len(eventID)).
		Bool("sealed", false).
		Msg("Inbox dedup hit: event already processed, handler skipped")
}
