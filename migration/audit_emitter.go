package migration

import (
	"context"
	"fmt"
	"sync"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"

	"github.com/gaborage/go-bricks/logger"
)

const (
	auditTracerName = "github.com/gaborage/go-bricks/migration"
	auditMeterName  = "github.com/gaborage/go-bricks/migration"

	// auditSinkQueueCap bounds the in-memory queue feeding the optional
	// AuditSink. When full the framework drops newest events and increments
	// migration.audit.sink_drops — this is the deliberate trade-off from
	// ADR-019: audit must never block business work.
	auditSinkQueueCap = 256
)

// Audit attribute keys are centralised here so the span path and the
// structured-log path emit identical names — schema drift between the two
// paths would silently break downstream alerting that pivots across them.
const (
	attrKeyType          = "audit.type"
	attrKeyTarget        = "audit.target"
	attrKeyPrincipal     = "audit.principal"
	attrKeyOutcome       = "audit.outcome"
	attrKeyStartedAt     = "audit.started_at"
	attrKeyCompletedAt   = "audit.completed_at"
	attrKeyVersion       = "audit.version"
	attrKeyFromState     = "audit.from_state"
	attrKeyToState       = "audit.to_state"
	attrKeyErrorClass    = "audit.error_class"
	attrKeyGitCommitSHA  = "audit.git_commit_sha"
	attrKeyPipelineRunID = "audit.pipeline_run_id"
	attrPrefixCustom     = "audit.attr."
	attrKeyCodeNamespace = "code.namespace"

	codeNamespaceMigration = "migration"
)

// auditKV is the canonical string-pair view of an AuditEvent field shared
// by emitLog and eventAttrs.
type auditKV struct {
	Key   string
	Value string
}

// eventKVs flattens an AuditEvent into the (key, value) pairs that both
// emission paths agree on. The two paths add their own extras on top:
// emitLog appends RFC3339 timestamps; eventAttrs appends code.namespace.
func eventKVs(ev *AuditEvent) []auditKV {
	pairs := make([]auditKV, 0, 10+len(ev.Attributes))
	pairs = append(pairs,
		auditKV{attrKeyType, string(ev.Type)},
		auditKV{attrKeyTarget, ev.Target},
		auditKV{attrKeyPrincipal, ev.AppliedByPrincipal},
		auditKV{attrKeyOutcome, string(ev.Outcome)},
	)
	if ev.Version != "" {
		pairs = append(pairs, auditKV{attrKeyVersion, ev.Version})
	}
	if ev.FromState != "" {
		pairs = append(pairs, auditKV{attrKeyFromState, ev.FromState})
	}
	if ev.ToState != "" {
		pairs = append(pairs, auditKV{attrKeyToState, ev.ToState})
	}
	if ev.ErrorClass != "" {
		pairs = append(pairs, auditKV{attrKeyErrorClass, string(ev.ErrorClass)})
	}
	if ev.GitCommitSHA != "" {
		pairs = append(pairs, auditKV{attrKeyGitCommitSHA, ev.GitCommitSHA})
	}
	if ev.PipelineRunID != "" {
		pairs = append(pairs, auditKV{attrKeyPipelineRunID, ev.PipelineRunID})
	}
	for k, v := range ev.Attributes {
		pairs = append(pairs, auditKV{attrPrefixCustom + k, v})
	}
	return pairs
}

// auditEmitter delivers AuditEvents via two paths: always-on OpenTelemetry
// (one span + one structured log record per event) and an optional AuditSink
// fan-out on a separate goroutine with a bounded send queue. Zero value is
// not usable; construct via newAuditEmitter.
type auditEmitter struct {
	logger logger.Logger
	tracer trace.Tracer
	sink   AuditSink

	sinkQueue    chan *AuditEvent
	sinkDrops    metric.Int64Counter
	sinkFailures metric.Int64Counter

	// closeMu serializes the non-blocking enqueue in enqueueForSink with
	// the channel-close in Close so a concurrent emit cannot panic with
	// "send on closed channel". Held only for the microsecond-scale span
	// of a non-blocking select, so contention is negligible at the
	// audit-event frequency this package expects.
	closeMu sync.Mutex
	closed  bool

	consumerCtx    context.Context
	consumerCancel context.CancelFunc
	consumerWG     sync.WaitGroup
}

// newAuditEmitter wires the always-on OTel path and, if sink is non-nil,
// spawns the bounded-queue consumer that fans out to the sink. log must be
// non-nil; sink may be nil.
func newAuditEmitter(log logger.Logger, sink AuditSink) *auditEmitter {
	meter := otel.Meter(auditMeterName)

	// Counter creation can fail when the meter is misconfigured. The
	// framework's policy elsewhere (cache/database trackers) is to keep
	// going with a nil counter on init failure — same shape here.
	drops, _ := meter.Int64Counter(
		"migration.audit.sink_drops",
		metric.WithDescription("Audit events dropped because the AuditSink queue was full."),
	)
	failures, _ := meter.Int64Counter(
		"migration.audit.sink_failures",
		metric.WithDescription("AuditSink.Record calls that returned an error."),
	)

	e := &auditEmitter{
		logger:       log,
		tracer:       otel.Tracer(auditTracerName),
		sink:         sink,
		sinkDrops:    drops,
		sinkFailures: failures,
	}

	if sink != nil {
		e.sinkQueue = make(chan *AuditEvent, auditSinkQueueCap)
		e.consumerCtx, e.consumerCancel = context.WithCancel(context.Background())
		e.consumerWG.Add(1)
		go e.consumeSink()
	}

	return e
}

// emit dispatches a single AuditEvent through both delivery paths. ev must
// be non-nil. Safe to call from any goroutine. Does not block on the sink
// path. Callers should supply StartedAt/CompletedAt; the emitter normalizes
// missing principals to PrincipalUnspecified and logs a warning so the gap
// is itself auditable.
func (e *auditEmitter) emit(ctx context.Context, ev *AuditEvent) {
	if e == nil || ev == nil {
		return
	}

	if ev.AppliedByPrincipal == "" {
		e.logger.Warn().
			Str("audit_type", string(ev.Type)).
			Str("target", ev.Target).
			Msg("audit event missing AppliedByPrincipal — operators MUST pass an explicit principal")
		ev.AppliedByPrincipal = PrincipalUnspecified
	}

	e.emitSpan(ctx, ev)
	e.emitLog(ev)
	e.enqueueForSink(ctx, ev)
}

func (e *auditEmitter) emitSpan(ctx context.Context, ev *AuditEvent) {
	spanName := fmt.Sprintf("migration.audit.%s", ev.Type)
	_, span := e.tracer.Start(ctx, spanName,
		trace.WithTimestamp(ev.StartedAt),
		trace.WithAttributes(eventAttrs(ev)...),
	)
	if ev.Outcome == AuditOutcomeFailed {
		span.SetStatus(codes.Error, string(ev.ErrorClass))
	}
	span.End(trace.WithTimestamp(ev.CompletedAt))
}

func (e *auditEmitter) emitLog(ev *AuditEvent) {
	var entry logger.LogEvent
	if ev.Outcome == AuditOutcomeFailed {
		entry = e.logger.Error()
	} else {
		entry = e.logger.Info()
	}

	for _, kv := range eventKVs(ev) {
		entry = entry.Str(kv.Key, kv.Value)
	}
	entry.
		Str(attrKeyStartedAt, ev.StartedAt.UTC().Format(rfc3339Nano)).
		Str(attrKeyCompletedAt, ev.CompletedAt.UTC().Format(rfc3339Nano)).
		Msg("migration audit event")
}

func (e *auditEmitter) enqueueForSink(ctx context.Context, ev *AuditEvent) {
	if e.sink == nil {
		return
	}
	e.closeMu.Lock()
	defer e.closeMu.Unlock()
	if e.closed {
		// OTel emission already happened; dropping the sink delivery on
		// shutdown is the deliberate trade-off from ADR-019.
		return
	}
	select {
	case e.sinkQueue <- ev:
	default:
		if e.sinkDrops != nil {
			e.sinkDrops.Add(ctx, 1,
				metric.WithAttributes(attribute.String(attrKeyType, string(ev.Type))),
			)
		}
		e.logger.Warn().
			Str("audit_type", string(ev.Type)).
			Str("target", ev.Target).
			Int("queue_cap", auditSinkQueueCap).
			Msg("audit sink queue full — event dropped")
	}
}

func (e *auditEmitter) consumeSink() {
	defer e.consumerWG.Done()
	for ev := range e.sinkQueue {
		err := e.sink.Record(e.consumerCtx, ev)
		if err == nil {
			continue
		}
		if e.sinkFailures != nil {
			e.sinkFailures.Add(e.consumerCtx, 1,
				metric.WithAttributes(attribute.String(attrKeyType, string(ev.Type))),
			)
		}
		e.logger.Warn().
			Err(err).
			Str("audit_type", string(ev.Type)).
			Str("target", ev.Target).
			Msg("audit sink delivery failed")
	}
}

// Close drains the AuditSink queue and tears down the consumer goroutine.
// Safe to call when no sink is configured. Idempotent — calling twice is a
// no-op. Honors ctx for shutdown deadline; events still in the queue when
// ctx expires are silently dropped (their OTel emission already succeeded).
//
// The defer here matters: on the timeout branch, cancelling the consumer
// context propagates a Done() signal into any in-flight sink.Record call so
// a well-behaved sink bails immediately instead of running past Close's
// deadline. On the drain-completed branch the cancel is a cleanup no-op.
func (e *auditEmitter) Close(ctx context.Context) error {
	if e == nil || e.sink == nil {
		return nil
	}
	e.closeMu.Lock()
	if e.closed {
		e.closeMu.Unlock()
		return nil
	}
	e.closed = true
	close(e.sinkQueue)
	e.closeMu.Unlock()

	defer e.consumerCancel()

	done := make(chan struct{})
	go func() {
		e.consumerWG.Wait()
		close(done)
	}()

	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// eventAttrs flattens an AuditEvent into OTel span attributes. Optional
// fields are only set when non-empty so spans aren't littered with empty
// keys.
func eventAttrs(ev *AuditEvent) []attribute.KeyValue {
	pairs := eventKVs(ev)
	attrs := make([]attribute.KeyValue, 0, len(pairs)+1)
	for _, kv := range pairs {
		attrs = append(attrs, attribute.String(kv.Key, kv.Value))
	}
	attrs = append(attrs, attribute.String(attrKeyCodeNamespace, codeNamespaceMigration))
	return attrs
}

// rfc3339Nano avoids importing time directly into the format string at each
// callsite while keeping serialization stable for log aggregation.
const rfc3339Nano = "2006-01-02T15:04:05.000000000Z07:00"
