package streams

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"sync"
	"sync/atomic"
	"time"

	"github.com/rabbitmq/rabbitmq-stream-go-client/pkg/amqp"
	"github.com/rabbitmq/rabbitmq-stream-go-client/pkg/ha"
	"github.com/rabbitmq/rabbitmq-stream-go-client/pkg/message"
	"github.com/rabbitmq/rabbitmq-stream-go-client/pkg/stream"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	semconv "go.opentelemetry.io/otel/semconv/v1.32.0"
	"go.opentelemetry.io/otel/trace"

	"github.com/gaborage/go-bricks/messaging/internal/tenantstamp"
	"github.com/gaborage/go-bricks/messaging/internal/tracking"
	"github.com/gaborage/go-bricks/observability"
	gobrickstrace "github.com/gaborage/go-bricks/trace"
)

const spanOperationPublish = "publish"

var (
	// ErrPublisherNotStarted reports a publish on a publisher Manager.Start has
	// not bound to a client producer yet.
	ErrPublisherNotStarted = errors.New("streams: publisher not started; Manager.Start binds it")
	// ErrPublisherClosed reports a publish on a publisher whose manager has shut
	// down, and is what an in-flight publish is resolved with when that happens.
	ErrPublisherClosed = errors.New("streams: publisher closed")
)

// producerHandle is the seam the client's reliable producers satisfy
// (*ha.ReliableProducer and *ha.ReliableSuperStreamProducer both have this
// shape). It keeps the confirmation-correlation policy testable without a broker,
// the same way consumerHandle does for the consume side.
type producerHandle interface {
	Send(msg message.StreamMessage) error
	Close() error
	GetStatus() int
}

// boundProducer is the live client producer a started Publisher sends through.
// It exists so the binding can be swapped atomically: Publish reads it on every
// call from any goroutine, while Manager.Start installs it and shutdown clears it.
type boundProducer struct {
	handle producerHandle
}

// waiter is one in-flight send waiting for its confirmation. ch is buffered so a
// resolve never blocks the client goroutine that delivers the confirmation, and
// done makes resolving idempotent.
type waiter struct {
	ch         chan error
	routingKey string
	done       bool
}

// resolve delivers err to a waiter that has not been resolved yet. Idempotent: a
// confirmation for an already-resolved or tombstoned send is a no-op rather than
// a second send on a cap-1 channel. Callers hold waiters.mu.
func (w *waiter) resolve(err error) {
	if w.done {
		return
	}
	w.done = true
	w.ch <- err
}

// waiters correlates in-flight sends with their confirmations by POINTER
// IDENTITY of the client message: ConfirmationStatus.GetMessage returns the exact
// message that was passed to Send (v1.8.3, producer.go:386), which holds at the
// default SubEntrySize of 1. Each entry also carries the routing key, because the
// hash routing extractor is called with that same pointer.
//
// ENTRY LIFECYCLE (load-bearing — the super-stream router depends on it): an
// entry is REMOVED only when its send resolves, that is on a confirmation or on
// a non-nil synchronous Send error, which the client never confirms. A ctx expiry
// does NOT remove the entry, it tombstones it: the waiter stops accepting a
// result so the eventual confirmation is a no-op, but the entry stays. The client
// can park inside Send during a reconnect BEFORE the routing extractor runs, so
// an abandoned send may look its routing key up long after the caller's ctx died
// — removing on ctx expiry would silently hash "" onto one partition. Eventual
// removal is still guaranteed: every enqueued message gets exactly one
// confirmation (broker, ConfirmationTimeOut ticker, or entityClosed), every
// never-enqueued send returns a synchronous error, and the close sweep resolves
// whatever is left.
type waiters struct {
	mu sync.Mutex
	m  map[message.StreamMessage]*waiter
}

func newWaiters() *waiters {
	return &waiters{m: make(map[message.StreamMessage]*waiter)}
}

// add registers one in-flight send.
func (ws *waiters) add(msg message.StreamMessage, routingKey string) *waiter {
	w := &waiter{ch: make(chan error, 1), routingKey: routingKey}

	ws.mu.Lock()
	defer ws.mu.Unlock()
	ws.m[msg] = w
	return w
}

// resolve settles one send and removes its entry: the send is over, so nothing
// can need its routing key again.
func (ws *waiters) resolve(msg message.StreamMessage, err error) {
	ws.mu.Lock()
	defer ws.mu.Unlock()

	w, ok := ws.m[msg]
	if !ok {
		return
	}
	delete(ws.m, msg)
	w.resolve(err)
}

// abandon tombstones one send whose caller's ctx expired, keeping the entry — and
// with it the routing key — alive for a send that may not have routed yet.
func (ws *waiters) abandon(msg message.StreamMessage) {
	ws.mu.Lock()
	defer ws.mu.Unlock()

	if w, ok := ws.m[msg]; ok {
		w.done = true
	}
}

// routingKeyFor reports the partition key registered for a message. It answers
// from a tombstoned entry too, which is the whole point of the tombstone.
func (ws *waiters) routingKeyFor(msg message.StreamMessage) (routingKey string, ok bool) {
	ws.mu.Lock()
	defer ws.mu.Unlock()

	w, ok := ws.m[msg]
	if !ok {
		return "", false
	}
	return w.routingKey, true
}

// sweep resolves and removes every outstanding send.
func (ws *waiters) sweep(err error) {
	ws.mu.Lock()
	defer ws.mu.Unlock()

	for _, w := range ws.m {
		w.resolve(err)
	}
	clear(ws.m)
}

// Publisher publishes to one declared stream or super stream. It is obtained from
// Declarations.DeclarePublisher or DeclareSuperStreamPublisher at declaration time
// and is inert until Manager.Start binds it to a client producer. Safe for
// concurrent use.
type Publisher struct {
	stream string
	// super marks a publisher whose target is a partitioned super stream, which
	// inverts the RoutingKey requirement.
	super  bool
	tracer trace.Tracer
	// spanName and spanStartOpts are precomputed because a Publisher targets
	// exactly one stream for its lifetime, so the name and every attribute but the
	// body size are constant. The consume-side twin in runner.go deliberately does
	// NOT do this: one runner serves every partition of a super stream, so its
	// stream name is only known per delivery.
	spanName      string
	spanStartOpts []trace.SpanStartOption

	bound   atomic.Pointer[boundProducer]
	closed  atomic.Bool
	pending *waiters
}

// newPublisher builds the handle a declare method hands back to the module.
func newPublisher(streamName string, super bool) *Publisher {
	return &Publisher{
		stream:   streamName,
		super:    super,
		tracer:   otel.Tracer(tracerName),
		spanName: streamName + " " + spanOperationPublish,
		spanStartOpts: []trace.SpanStartOption{
			trace.WithSpanKind(trace.SpanKindProducer),
			trace.WithAttributes(
				attribute.String(string(semconv.MessagingSystemKey), messagingSystem),
				semconv.MessagingOperationName(spanOperationPublish),
				semconv.MessagingDestinationName(streamName),
			),
		},
		pending: newWaiters(),
	}
}

// status reports the bound producer's connection status, or ha.StatusClosed when
// unbound, so readiness asks the one owner of the binding.
func (p *Publisher) status() int {
	if b := p.bound.Load(); b != nil {
		return b.handle.GetStatus()
	}
	return ha.StatusClosed
}

// binding resolves the producer to send through, naming the right reason when
// there is none.
//
// The two sentinels are not interchangeable, and a missing binding does not by
// itself say which applies: a close that lands between send's own closed check
// and this load drops the binding, and reporting that as ErrPublisherNotStarted
// would tell a caller its startup wiring is broken during a normal shutdown.
// closeBound stores closed BEFORE it swaps the binding away, so a binding that
// went missing because of a close always has the flag set to distinguish it.
func (p *Publisher) binding() (*boundProducer, error) {
	if bound := p.bound.Load(); bound != nil {
		return bound, nil
	}
	if p.closed.Load() {
		return nil, ErrPublisherClosed
	}
	return nil, ErrPublisherNotStarted
}

// Publish sends one message and blocks until the broker confirms it, ctx expires,
// or the publisher closes.
//
// A ctx expiry is NOT proof of failure: the send may still be in flight and may
// still land. Delivery is at-least-once, so consumers must be idempotent. A nil
// return from the client's own Send proves nothing either — it swallows write
// errors — which is why the confirmation is what this waits for.
func (p *Publisher) Publish(ctx context.Context, msg *PublishMessage) error {
	start := time.Now()

	var data []byte
	if msg != nil {
		data = msg.Data
	}

	ctx, span := p.tracer.Start(ctx, p.spanName, p.spanStartOpts...)
	defer span.End()

	span.SetAttributes(semconv.MessagingMessageBodySize(len(data)))

	err := p.send(ctx, msg)
	// SECURITY: type only on the off-platform span sinks (ADR-083).
	observability.RecordErrorByType(span, err)
	tracking.RecordStreamPublish(ctx, p.stream, time.Since(start), err)
	return err
}

// send runs the publish itself: validate, enqueue, wait for the confirmation.
func (p *Publisher) send(ctx context.Context, msg *PublishMessage) error {
	if msg == nil {
		return fmt.Errorf("streams: nil message published to stream %q", p.stream)
	}
	if p.closed.Load() {
		return ErrPublisherClosed
	}
	bound, bindErr := p.binding()
	if bindErr != nil {
		return bindErr
	}
	if err := p.routingError(msg.RoutingKey); err != nil {
		return err
	}
	// The stream lane has no per-tenant client to fall back on: it runs single-tenant
	// or on the control-plane key, so the publishing context is the only source and
	// the replay key is always empty. The conflict that key could raise belongs to
	// the classic lane; what this call refuses here is a caller-supplied stamp.
	stamp, stampErr := tenantstamp.ResolveForPublish(ctx, msg.Properties, "")
	if stampErr != nil {
		return stampErr
	}

	clientMsg := p.buildMessage(ctx, msg, stamp)
	w, live := p.registerSend(clientMsg, msg.RoutingKey)

	// The send runs on a goroutine this call is willing to abandon: the client's
	// send path waits on a bare sync.Cond while the producer reconnects, so a
	// broker outage can park it indefinitely and only the caller's ctx can cut it
	// short. Same trade as Manager.flushOffsetsLocked. A non-nil error never
	// reaches the confirmation handler, so it resolves the waiter here instead.
	if live {
		go func() {
			if sendErr := bound.handle.Send(clientMsg); sendErr != nil {
				p.pending.resolve(clientMsg, fmt.Errorf("failed to publish to stream %q: %w", p.stream, sendErr))
			}
		}()
	}

	select {
	case err := <-w.ch:
		return err
	case <-ctx.Done():
		p.pending.abandon(clientMsg)
		return fmt.Errorf("publish to stream %q abandoned before confirmation: %w", p.stream, ctx.Err())
	}
}

// registerSend registers one in-flight send and reports whether it may still be
// dispatched to the client.
//
// The second check of closed is not redundant with the one send() opens with: a
// close can land between them, and closeBound sweeps the map at a fixed point.
// An entry added after that sweep would sit outside every removal path — and
// because the client swallows write errors, a send against the closed producer
// may return nothing at all, hanging a caller with no deadline across the whole
// shutdown. closeBound stores closed strictly BEFORE it sweeps, so any entry
// that lands after the sweep is guaranteed to observe the flag here and resolve
// itself. An entry that landed before the sweep is resolved by the sweep, and
// this resolve is then the no-op that idempotency exists for.
func (p *Publisher) registerSend(clientMsg message.StreamMessage, routingKey string) (w *waiter, live bool) {
	w = p.pending.add(clientMsg, routingKey)
	if p.closed.Load() {
		p.pending.resolve(clientMsg, ErrPublisherClosed)
		return w, false
	}
	return w, true
}

// routingError rejects a routing key that does not match the publisher's target
// kind. An empty key on a super stream is rejected rather than defaulted because
// hashing "" would pile every message onto a single partition.
func (p *Publisher) routingError(routingKey string) error {
	switch {
	case p.super && routingKey == "":
		return fmt.Errorf("streams: a RoutingKey is required to publish to super stream %q; "+
			"hashing an empty key would send every message to one partition", p.stream)
	case !p.super && routingKey != "":
		return fmt.Errorf("streams: routing keys select super-stream partitions, "+
			"but this publisher targets plain stream %q", p.stream)
	default:
		return nil
	}
}

// buildMessage renders the framework message as the client's. The trace context
// goes through the framework's own trace package rather than the OTel propagator,
// so both messaging lanes write the same header names and the consume side can
// extract them symmetrically.
func (p *Publisher) buildMessage(ctx context.Context, msg *PublishMessage, stamp string) message.StreamMessage {
	clientMsg := amqp.NewMessage(msg.Data)

	// Sized to the caller's properties only: the map grows itself for the two or
	// three trace headers, and over-sizing the hint would be an unobservable
	// micro-optimisation nobody can test.
	properties := make(map[string]any, len(msg.Properties))
	maps.Copy(properties, msg.Properties)
	accessor := propertyAccessor(properties)
	gobrickstrace.InjectIntoHeaders(ctx, accessor)
	tenantstamp.Write(stamp, accessor.Set)
	clientMsg.ApplicationProperties = properties

	return clientMsg
}

// confirmed resolves the waiters of one batch of broker confirmations. The client
// calls it from its own goroutine, so it only ever resolves: it never blocks and
// never reaches back into the manager.
func (p *Publisher) confirmed(statuses []*stream.ConfirmationStatus) {
	for _, cs := range statuses {
		if cs == nil {
			continue
		}
		p.resolveConfirmation(cs.GetMessage(), cs.IsConfirmed(), cs.GetError())
	}
}

// partitionsConfirmed is the super-stream half of confirmed: the client delivers
// confirmations grouped by the partition each message was routed to, wrapping the
// same status type the plain lane resolves.
func (p *Publisher) partitionsConfirmed(confirms []*stream.PartitionPublishConfirm) {
	p.confirmed(partitionStatuses(confirms))
}

// partitionStatuses flattens one batch of per-partition confirmations. Which
// partition a message landed on is the routing strategy's business, not the
// correlation's: a waiter is found by the message pointer alone.
func partitionStatuses(confirms []*stream.PartitionPublishConfirm) []*stream.ConfirmationStatus {
	var statuses []*stream.ConfirmationStatus
	for _, confirm := range confirms {
		if confirm == nil {
			continue
		}
		statuses = append(statuses, confirm.ConfirmationStatus...)
	}
	return statuses
}

// resolveConfirmation settles one send from one broker confirmation. It is the
// client-free half of confirmed, because a *stream.ConfirmationStatus carries
// only unexported fields and no test can populate one.
func (p *Publisher) resolveConfirmation(msg message.StreamMessage, confirmed bool, cause error) {
	var err error
	if !confirmed {
		err = confirmationError(p.stream, cause)
	}
	p.pending.resolve(msg, err)
}

// confirmationError renders a rejected confirmation. The client maps unknown
// response codes to a nil cause, so the failure still has to name itself then.
func confirmationError(streamName string, cause error) error {
	if cause == nil {
		return fmt.Errorf("publish to stream %q was not confirmed by the broker", streamName)
	}
	return fmt.Errorf("publish to stream %q not confirmed: %w", streamName, cause)
}

// bind installs the client producer Manager.Start created for this publisher and
// reopens it.
//
// Clearing closed is what makes a Close-then-Start cycle work: Manager.Start
// guards on the environment, not on started, so it may run again once Close
// disposed the previous one — and unlike consumers, which are rebuilt from
// scratch each time, the module keeps the same *Publisher it was handed at
// declaration time. Without this it would stay permanently closed. The binding
// is installed BEFORE the flag clears, so no caller can find an open publisher
// with nothing to send through.
func (p *Publisher) bind(handle producerHandle) {
	p.bound.Store(&boundProducer{handle: handle})
	p.closed.Store(false)
}

// closeBound shuts the publisher down and reports the client producer's own close
// error. Late callers get ErrPublisherClosed, the binding is dropped so nothing
// can publish into an environment that is about to be disposed, and every send
// still outstanding is resolved.
//
// The sweep is mandatory rather than belt-and-braces: the client's entityClosed
// confirmations cover only messages that reached its queue, so a send parked in a
// reconnect was never enqueued and gets no confirmation at all. Without the sweep
// a Publish caller with no deadline would hang across the whole shutdown.
func (p *Publisher) closeBound() error {
	p.closed.Store(true)

	var err error
	if bound := p.bound.Swap(nil); bound != nil {
		err = bound.handle.Close()
	}
	p.pending.sweep(ErrPublisherClosed)
	return err
}

// propertyAccessor adapts AMQP 1.0 application properties to
// trace.HeaderAccessor. A map type rather than a struct wrapper: a map is already
// pointer-shaped, so satisfying the interface costs no allocation per publish.
type propertyAccessor map[string]any

func (a propertyAccessor) Get(key string) any { return a[key] }

// Lookup reports the property AND whether it was there, which Get cannot: a
// property written as nil is not the same as one that was never written.
func (a propertyAccessor) Lookup(key string) (value any, present bool) {
	v, ok := a[key]
	return v, ok
}

func (a propertyAccessor) Set(key string, value any) { a[key] = value }
