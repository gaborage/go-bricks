# Streams Environment Port Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Put an *Environment port* between `messaging/streams`' `Manager` and the RabbitMQ stream client so `Manager.Start` — declare, bind, start, unwind — runs end to end in a unit test against an in-memory fake.

**Architecture:** An unexported `environment` interface with one method per vendor call the manager makes, a `vendorEnvironment` adapter wrapping `*stream.Environment`, and an unexported `dialEnvironment` field on `Manager` that tests overwrite (the same seam shape as the AMQP lane's `amqpDialFunc`, minus the process-global). The vendor callback `stream.MessagesHandler` — whose `stream.ConsumerContext` no test can construct — is unwrapped inside the adapter, so the port hands the runner a go-bricks-shaped `func(streamName string, offset int64, msg *amqp.Message, store offsetStorer)`; the `store` argument is the delivering consumer, exactly what `runner.messagesHandler` passes today. `producerFactory`/`superProducerFactory` fold into the port. Nothing exported changes, and **no commit path moves**.

**Tech Stack:** Go 1.26 · `github.com/rabbitmq/rabbitmq-stream-go-client@v1.8.3` (`pkg/stream`, `pkg/ha`, `pkg/amqp`, `pkg/message`) · testify (`assert`/`require`) · `go test -race`.

**Spec:** [docs/superpowers/specs/2026-08-16-messaging-environment-port-and-delivery-pipeline-design.md](../specs/2026-08-16-messaging-environment-port-and-delivery-pipeline-design.md) — **decisions 1–6 only** ("Environment port (card 8)", Stack B PR1). Decisions 7–13 (the delivery pipeline) are PR2/PR3 and are **out of scope**: do not create `messaging/internal/delivery`, do not touch `StartConsumeSpan`, `RecordAMQPConsumeMetrics`, `registry.go`, or trace extraction in `runner.deliver`.

**Vocabulary:** [CONTEXT.md](../../../CONTEXT.md) — the seam is the **Environment port**. Use those words in comments; avoid "env", "client", "connection" as its name.

## Global Constraints

- Test function names are **camelCase** (`TestManagerStartDeclaresBeforeItBinds`); table-driven case names are **snake_case** (`{name: "stored_offset_wins"}`). 100% compliance across >800 test functions — no exceptions.
- Commit with `git commit -F <file>`; the repo's commit hook rejects heredoc `-m`. **Never** pass `--no-gpg-sign` — if signing fails, stop and report it.
- Implementers do **not** run `make check`, `make mutate`, or `git push`. The controller runs every gate (Task 5). Implementers run only the targeted `go test` commands each step names.
- **No exported API change.** `NewManager`, `ManagerOptions`, `Manager`'s exported methods, `Publisher`, `Declarations`, `Handler`, `Message` keep their current signatures. `app/` consumes only those (`app/streams_setup.go:48`, `app/readiness.go:208`, `app/lifecycle.go:583`).
- **No commit path moves.** This is a seam extraction, not a behavior change: an in-flight commit still goes through the consumer that delivered the message, and the shutdown flush still goes through `runningConsumer.storerFor`. Any diff that changes which object a `StoreCustomOffset` lands on is a plan violation.
- `messaging/streams` must **not** import `github.com/gaborage/go-bricks/messaging`. It imports `messaging/internal/tracking` today (`runner.go:20`, `publisher.go:22`) and nothing else from the parent — keep it that way.
- No ADR, no `wiki/migrations.md` atom: nothing exported moves.
- Every new production comment earns its place — non-obvious intent only (`CLAUDE.md` → "Keep comments bare-minimum").

## File Structure

| File | Responsibility |
| --- | --- |
| `messaging/streams/environment.go` **(new)** | The Environment port: the `environment` interface, the `messageHandler` shape, the `vendorEnvironment` adapter, the vendor `stream.MessagesHandler` unwrapping, and the production dial. |
| `messaging/streams/manager.go` **(modify)** | `env` typed as the port; new `dialEnvironment` field; producer factories deleted and folded into `constructProducer`; every `*stream.Environment` parameter becomes `environment`. |
| `messaging/streams/runner.go` **(modify)** | `messagesHandler` deleted — the vendor unwrapping moves into the adapter. `deliver` and the offset bookkeeping are untouched. |
| `messaging/streams/environment_fake_test.go` **(new, test-only)** | `fakeEnvironment` — call-order recording, per-method error injection, in-memory offsets, drivable consumer handles — plus the `dialFake` / `startOnFake` helpers. |
| `messaging/streams/manager_test.go` **(modify)** | The in-process `Start` suite; the `attach*` helpers deleted and their tests rebuilt on `Start`. |
| `messaging/streams/streams_integration_test.go` **(modify)** | One container test shrunk (Task 4); the other nine unchanged. |

`messaging/streams/runner_test.go` is **not** modified by any task: `deliver` keeps its signature, so all twelve of its call sites and the `storerByStream` / `bookOf` helpers stay exactly as they are.

## Design decisions this plan locks in

**How the plain lane's `offsetStorer` is reached through the port.** `consumerHandle` stays exactly `{Close() error; GetStatus() int}` (`manager.go:70-73`) and `startStreamConsumer` type-asserts the returned handle to the existing `offsetStorer` (`runner.go:39-41`) when it builds the shutdown-flush resolver: `*ha.ReliableSuperStreamConsumer` genuinely has no `StoreCustomOffset` — it exposes only `GetStreamName`, `GetStatus`, `GetStatusAsString`, `Close` (`$(go env GOMODCACHE)/github.com/rabbitmq/rabbitmq-stream-go-client@v1.8.3/pkg/ha/ha_super_stream_consumer.go:130-144`) — so adding the method to `consumerHandle` would force the super adapter to fake a method the vendor type does not have, whereas the assertion keeps the asymmetry exactly where the vendor put it and adds no new interface. A failed assertion yields a nil `offsetStorer`, which `offsetTracker.storeLocked` already answers with `errNoOffsetStorer` (`runner.go:118-121`).

**The in-flight commit target still arrives with the delivery.** `runner.messagesHandler` passes the delivering `*stream.Consumer` straight into `deliver` today (`runner.go:233-236`), and `*stream.Consumer` implements `StoreCustomOffset` (`pkg/stream/consumer.go:521`). The port's handler therefore carries it: `type messageHandler func(streamName string, offset int64, msg *amqp.Message, store offsetStorer)`. The vendor adapter unwraps `consumerContext.Consumer` and passes it as `store`; `deliver`'s signature does not change; nothing in `runner.go` resolves a storer. An in-flight commit lands on the same object it lands on today, on the same connection.

**The shutdown flush is the only thing `storerFor` resolves — unchanged.** `trackConsumer` keeps storing the resolver on `runningConsumer` exactly as it does now (`manager.go:415-430`), and `runningConsumer.storerFor`'s existing doc comment ("Only the shutdown flush needs it; every in-flight commit goes through the consumer the client hands to the delivery callback", `manager.go:84-87`) stays true and stays put. Plain: the handle, via the type assertion. Super: `envOffsetStorer` per partition, through the port — which is already what it does (`manager.go:396-398`).

---

### Task 1: The Environment port, the vendor adapter, and the manager on top of it

**Files:**

- Create: `messaging/streams/environment.go`
- Create: `messaging/streams/environment_fake_test.go`
- Modify: `messaging/streams/manager.go` (struct `94-119`, `NewManager` `129-146`, `Start` `165-241`, `declareStreams` `273-283`, `declareSuperStreams` `290-300`, `startConsumer` `325-330`, `startStreamConsumer` `333-365`, `startSuperStreamConsumer` `369-400`, `bindPublisher` `465-479`, `constructProducer` `483-488`, factories `517-551`, `envOffsetStorer` `553-563`, `resolveOffset` `582-587`)
- Modify: `messaging/streams/runner.go` (delete `messagesHandler` `225-236`, move its concurrency prose onto `deliver`)
- Modify: `messaging/streams/manager_test.go` (the `*stream.Environment` literals and the four producer-factory tests)

**Interfaces:**

- Produces, consumed by Tasks 2–4:
  - `type messageHandler func(streamName string, offset int64, msg *amqp.Message, store offsetStorer)`
  - `type environment interface { DeclareStream(name string, opts *stream.StreamOptions) error; DeclareSuperStream(name string, opts *stream.PartitionsOptions) error; QueryOffset(consumer, streamName string) (int64, error); StoreOffset(consumer, streamName string, offset int64) error; NewConsumer(streamName string, opts *stream.ConsumerOptions, handler messageHandler) (consumerHandle, error); NewSuperStreamConsumer(superStream string, opts *stream.SuperStreamConsumerOptions, handler messageHandler) (consumerHandle, error); NewProducer(streamName string, opts *stream.ProducerOptions, confirmed ha.ConfirmMessageHandler) (producerHandle, error); NewSuperStreamProducer(superStream string, opts *stream.SuperStreamProducerOptions, confirmed ha.PartitionConfirmMessageHandler) (producerHandle, error); Close() error }`
  - `Manager.dialEnvironment func(*stream.EnvironmentOptions) (environment, error)`
  - test helpers: `newFakeEnvironment() *fakeEnvironment`, `dialFake(m *Manager, fake *fakeEnvironment)`, `fakeEnvironment.failOn(key string, err error)`, `fakeEnvironment.setOffset` / `storedOffset` / `recorded` / `consumer` / `consumerOptions` / `producer` / `superProducerOptions` / `useProducer(p *fakeProducer)` (as delivered — Task 3 dropped the redundant stream argument), `fakeConsumer.deliver(streamName string, offset int64, msg *amqp.Message)`, `fakeConsumer.promote(streamName string) stream.OffsetSpecification`
- Consumes, **unchanged**: `consumerHandle` (`manager.go:70-73`), `producerHandle` (`publisher.go:41-45`), `offsetStorer` (`runner.go:39-41`), `consumerRunner.deliver(streamName string, offset int64, message *amqp.Message, store offsetStorer)` (`runner.go:239`), `fakeHandle` / `fakeProducer` (existing test doubles).

> **As delivered:** `useProducer` takes only the producer (`useProducer(p *fakeProducer)`) and the fake keeps one prepared producer (`preparedProd`) rather than a per-stream map — every test starts one publisher, so `unparam` rejected the stream argument in Task 3. The code blocks below are the plan as written; the tree is the authority.

- [ ] **Step 1: Write the failing test — the manager must dial through a swappable seam**

Create `messaging/streams/environment_fake_test.go` with the first cut of the fake. Everything here is used by this task; Task 2 grows it.

```go
package streams

import (
	"errors"
	"fmt"
	"sync"

	"github.com/rabbitmq/rabbitmq-stream-go-client/pkg/amqp"
	"github.com/rabbitmq/rabbitmq-stream-go-client/pkg/ha"
	"github.com/rabbitmq/rabbitmq-stream-go-client/pkg/stream"
)

// The calls a fakeEnvironment records, in the order it made them. A target is
// appended after a colon so a test asserts WHICH stream a phase reached, not only
// that the phase ran. consumer_store and store_offset are deliberately separate:
// an in-flight commit goes through the consumer that delivered the message, a
// shutdown flush of a super stream through the Environment port, and a test that
// could not tell them apart could not catch one turning into the other.
const (
	callDeclareStream      = "declare_stream"
	callDeclareSuperStream = "declare_super_stream"
	callNewProducer        = "new_producer"
	callNewSuperProducer   = "new_super_producer"
	callNewConsumer        = "new_consumer"
	callNewSuperConsumer   = "new_super_consumer"
	callQueryOffset        = "query_offset"
	callStoreOffset        = "store_offset"
	callConsumerStore      = "consumer_store"
	callClose              = "close"
)

// errDeclareFailed, errConsumerStart and errProducerConstruction are the broker
// failures a test injects at the port.
var (
	errDeclareFailed        = errors.New("declaration refused")
	errConsumerStart        = errors.New("consumer start refused")
	errProducerConstruction = errors.New("producer construction failed")
)

// deliveryStorer is the commit target the fake hands each delivery, standing in
// for the *stream.Consumer the client passes its own callback: an in-flight
// commit goes through the consumer that delivered the message, on ITS connection,
// never through the Environment port or the reliable handle above it. It writes
// where QueryOffset answers from, so a committed position is observable.
type deliveryStorer struct {
	env      *fakeEnvironment
	consumer string
	stream   string
}

func (s deliveryStorer) StoreCustomOffset(offset int64) error {
	return s.env.storeFromConsumer(s.consumer, s.stream, offset)
}

// fakeConsumer is one consumer the fake environment handed back: the handle the
// manager tracks, plus the callbacks a test drives it through.
type fakeConsumer struct {
	env *fakeEnvironment
	// name is the consumer name the manager asked for, which is the key the
	// broker stores offsets under.
	name string
	// handle is what the port returned: a *fakeHandle for a plain stream, because
	// *ha.ReliableConsumer is an offsetStorer, and a *fakeSuperHandle for a super
	// stream, because *ha.ReliableSuperStreamConsumer is not.
	handle consumerHandle
	// events is that handle's shared event log, so a test asserts store and close
	// without caring which handle kind it got.
	events  *fakeHandle
	handler messageHandler
	// sac is the promotion callback the manager installed, nil when the
	// declaration did not ask for single active consumer.
	sac stream.ConsumerUpdate
}

// deliver pushes one message through the runner exactly as the client's read
// loop would, including the delivering consumer it commits through.
func (c *fakeConsumer) deliver(streamName string, offset int64, msg *amqp.Message) {
	c.handler(streamName, offset, msg,
		deliveryStorer{env: c.env, consumer: c.name, stream: streamName})
}

// promote fires the single-active-consumer promotion callback, which is where a
// per-partition stored offset is restored.
func (c *fakeConsumer) promote(streamName string) stream.OffsetSpecification {
	return c.sac(streamName, true)
}

// fakeSuperHandle stands in for *ha.ReliableSuperStreamConsumer. It delegates to
// a fakeHandle rather than embedding one: embedding would promote
// StoreCustomOffset, and the vendor type has none — that absence is exactly what
// routes a super stream's shutdown flush through the Environment port.
type fakeSuperHandle struct{ h *fakeHandle }

func (f *fakeSuperHandle) Close() error   { return f.h.Close() }
func (f *fakeSuperHandle) GetStatus() int { return f.h.GetStatus() }

// fakeEnvironment is the in-memory adapter behind the Environment port.
type fakeEnvironment struct {
	mu sync.Mutex

	calls []string
	// errs injects one failure per port call, keyed either by method
	// ("new_producer") or by method and target ("new_producer:orders").
	errs map[string]error
	// offsets is the broker's server-side offset store, keyed "<consumer>/<stream>".
	offsets map[string]int64

	consumers     map[string]*fakeConsumer
	consumerOpts  map[string]*stream.ConsumerOptions
	producers     map[string]*fakeProducer
	superProdOpts map[string]*stream.SuperStreamProducerOptions
	preparedProds map[string]*fakeProducer
}

var _ environment = (*fakeEnvironment)(nil)

func newFakeEnvironment() *fakeEnvironment {
	return &fakeEnvironment{
		errs:          map[string]error{},
		offsets:       map[string]int64{},
		consumers:     map[string]*fakeConsumer{},
		consumerOpts:  map[string]*stream.ConsumerOptions{},
		producers:     map[string]*fakeProducer{},
		superProdOpts: map[string]*stream.SuperStreamProducerOptions{},
		preparedProds: map[string]*fakeProducer{},
	}
}

// failOn makes one port call fail. key is a method name or "<method>:<target>".
func (f *fakeEnvironment) failOn(key string, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.errs[key] = err
}

// useProducer makes the fake hand back p for streamName instead of a default
// open producer, so a test can start a manager on a producer that blocks, that
// refuses to close, or that reports a status of its choosing.
func (f *fakeEnvironment) useProducer(streamName string, p *fakeProducer) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.preparedProds[streamName] = p
}

// setOffset seeds the broker's stored offset for one consumer and stream.
func (f *fakeEnvironment) setOffset(consumer, streamName string, offset int64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.offsets[consumer+"/"+streamName] = offset
}

// storedOffset reports what the broker holds, whichever path committed it.
func (f *fakeEnvironment) storedOffset(consumer, streamName string) (int64, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	offset, ok := f.offsets[consumer+"/"+streamName]
	return offset, ok
}

func (f *fakeEnvironment) recorded() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.calls...)
}

func (f *fakeEnvironment) consumer(streamName string) *fakeConsumer {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.consumers[streamName]
}

func (f *fakeEnvironment) consumerOptions(streamName string) *stream.ConsumerOptions {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.consumerOpts[streamName]
}

func (f *fakeEnvironment) producer(streamName string) *fakeProducer {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.producers[streamName]
}

func (f *fakeEnvironment) superProducerOptions(superStream string) *stream.SuperStreamProducerOptions {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.superProdOpts[superStream]
}

// recordLocked and errForLocked are called with f.mu held.
func (f *fakeEnvironment) recordLocked(entry string) { f.calls = append(f.calls, entry) }

func (f *fakeEnvironment) errForLocked(method, target string) error {
	if err, ok := f.errs[method+":"+target]; ok {
		return err
	}
	return f.errs[method]
}

// storeFromConsumer records a commit made through a delivering consumer rather
// than through the port, and writes it where QueryOffset answers from.
func (f *fakeEnvironment) storeFromConsumer(consumer, streamName string, offset int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	key := consumer + "/" + streamName
	f.recordLocked(fmt.Sprintf("%s:%s=%d", callConsumerStore, key, offset))
	if err := f.errForLocked(callConsumerStore, key); err != nil {
		return err
	}
	f.offsets[key] = offset
	return nil
}

func (f *fakeEnvironment) DeclareStream(name string, _ *stream.StreamOptions) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.recordLocked(callDeclareStream + ":" + name)
	return f.errForLocked(callDeclareStream, name)
}

func (f *fakeEnvironment) DeclareSuperStream(name string, _ *stream.PartitionsOptions) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.recordLocked(callDeclareSuperStream + ":" + name)
	return f.errForLocked(callDeclareSuperStream, name)
}

func (f *fakeEnvironment) QueryOffset(consumer, streamName string) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	key := consumer + "/" + streamName
	f.recordLocked(callQueryOffset + ":" + key)
	if err := f.errForLocked(callQueryOffset, key); err != nil {
		return 0, err
	}
	offset, ok := f.offsets[key]
	if !ok {
		// The client answers a name it has never stored with this sentinel, and
		// offsetSpecFor is the only place that tells it apart from a real failure.
		return 0, stream.OffsetNotFoundError
	}
	return offset, nil
}

func (f *fakeEnvironment) StoreOffset(consumer, streamName string, offset int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	key := consumer + "/" + streamName
	f.recordLocked(fmt.Sprintf("%s:%s=%d", callStoreOffset, key, offset))
	if err := f.errForLocked(callStoreOffset, key); err != nil {
		return err
	}
	f.offsets[key] = offset
	return nil
}

func (f *fakeEnvironment) NewConsumer(streamName string, opts *stream.ConsumerOptions,
	handler messageHandler,
) (consumerHandle, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.recordLocked(callNewConsumer + ":" + streamName)
	if err := f.errForLocked(callNewConsumer, streamName); err != nil {
		return nil, err
	}
	events := &fakeHandle{status: ha.StatusOpen}
	f.consumerOpts[streamName] = opts
	f.consumers[streamName] = &fakeConsumer{
		env:     f,
		name:    opts.ConsumerName,
		handle:  events,
		events:  events,
		handler: handler,
		sac:     consumerUpdateOf(opts.SingleActiveConsumer),
	}
	return events, nil
}

func (f *fakeEnvironment) NewSuperStreamConsumer(superStream string, opts *stream.SuperStreamConsumerOptions,
	handler messageHandler,
) (consumerHandle, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.recordLocked(callNewSuperConsumer + ":" + superStream)
	if err := f.errForLocked(callNewSuperConsumer, superStream); err != nil {
		return nil, err
	}
	events := &fakeHandle{status: ha.StatusOpen}
	handle := &fakeSuperHandle{h: events}
	f.consumers[superStream] = &fakeConsumer{
		env:     f,
		name:    opts.ConsumerName,
		handle:  handle,
		events:  events,
		handler: handler,
		sac:     consumerUpdateOf(opts.SingleActiveConsumer),
	}
	return handle, nil
}

// consumerUpdateOf reads the promotion callback out of either options type's
// single-active-consumer block, which the client leaves nil when SAC is off.
func consumerUpdateOf(sac *stream.SingleActiveConsumer) stream.ConsumerUpdate {
	if sac == nil {
		return nil
	}
	return sac.ConsumerUpdate
}

func (f *fakeEnvironment) NewProducer(streamName string, _ *stream.ProducerOptions,
	_ ha.ConfirmMessageHandler,
) (producerHandle, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.recordLocked(callNewProducer + ":" + streamName)
	if err := f.errForLocked(callNewProducer, streamName); err != nil {
		return nil, err
	}
	return f.newProducerLocked(streamName), nil
}

func (f *fakeEnvironment) NewSuperStreamProducer(superStream string, opts *stream.SuperStreamProducerOptions,
	_ ha.PartitionConfirmMessageHandler,
) (producerHandle, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.recordLocked(callNewSuperProducer + ":" + superStream)
	if err := f.errForLocked(callNewSuperProducer, superStream); err != nil {
		return nil, err
	}
	f.superProdOpts[superStream] = opts
	return f.newProducerLocked(superStream), nil
}

func (f *fakeEnvironment) newProducerLocked(streamName string) *fakeProducer {
	p, ok := f.preparedProds[streamName]
	if !ok {
		p = openProducer()
	}
	f.producers[streamName] = p
	return p
}

func (f *fakeEnvironment) Close() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.recordLocked(callClose)
	return f.errForLocked(callClose, "")
}

// dialFake makes m.Start open fake instead of a broker.
func dialFake(m *Manager, fake *fakeEnvironment) {
	m.dialEnvironment = func(*stream.EnvironmentOptions) (environment, error) { return fake, nil }
}
```

Now append the RED test to `messaging/streams/manager_test.go`:

```go
// TestManagerStartDeclaresThroughTheEnvironmentPort is the whole point of the
// port: Start's declare phase reaches the broker seam in a unit test. Before the
// port there was no way in — the phase was only ever reached with a nil
// environment, by a test that asserted through a panic.
func TestManagerStartDeclaresThroughTheEnvironmentPort(t *testing.T) {
	m := testManager(t)
	fake := newFakeEnvironment()
	dialFake(m, fake)

	decls := NewDeclarations()
	decls.DeclareStream(testStream, nil)

	require.NoError(t, m.Start(context.Background(), decls))

	assert.Equal(t, []string{callDeclareStream + ":" + testStream}, fake.recorded())
	assert.True(t, m.started)
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./messaging/streams/ -run TestManagerStartDeclaresThroughTheEnvironmentPort`

Expected: FAIL — a build failure, which is Go's red for a seam that does not exist yet:

```text
# github.com/gaborage/go-bricks/messaging/streams [github.com/gaborage/go-bricks/messaging/streams.test]
messaging/streams/environment_fake_test.go:...: undefined: environment
messaging/streams/environment_fake_test.go:...: undefined: messageHandler
messaging/streams/manager_test.go:...: m.dialEnvironment undefined (type *Manager has no field or method dialEnvironment)
FAIL	github.com/gaborage/go-bricks/messaging/streams [build failed]
```

- [ ] **Step 3: Create the Environment port and its vendor adapter**

Create `messaging/streams/environment.go`:

```go
package streams

import (
	"github.com/rabbitmq/rabbitmq-stream-go-client/pkg/amqp"
	"github.com/rabbitmq/rabbitmq-stream-go-client/pkg/ha"
	"github.com/rabbitmq/rabbitmq-stream-go-client/pkg/stream"
)

// messageHandler receives one delivery in the framework's own vocabulary. store
// is the consumer that delivered the message, which is what an in-flight commit
// goes through: the client's callback carries a stream.ConsumerContext whose
// Consumer is a vendor struct of unexported fields that no test can populate, so
// the unwrapping stays in the adapter and everything above this line is reachable
// without a broker.
type messageHandler func(streamName string, offset int64, msg *amqp.Message, store offsetStorer)

// environment is the Environment port: the seam through which the streams lane
// reaches the broker. One method per vendor call the manager makes, with the
// vendor environment as its production adapter and an in-memory fake for tests.
//
// Unexported on purpose: nothing outside this package varies across the seam —
// one production adapter, one test fake — so there is no consumer-facing
// interface to export and no second implementation to abstract over.
type environment interface {
	DeclareStream(name string, opts *stream.StreamOptions) error
	DeclareSuperStream(name string, opts *stream.PartitionsOptions) error
	QueryOffset(consumer, streamName string) (int64, error)
	StoreOffset(consumer, streamName string, offset int64) error
	NewConsumer(streamName string, opts *stream.ConsumerOptions, handler messageHandler) (consumerHandle, error)
	NewSuperStreamConsumer(superStream string, opts *stream.SuperStreamConsumerOptions, handler messageHandler) (consumerHandle, error)
	NewProducer(streamName string, opts *stream.ProducerOptions, confirmed ha.ConfirmMessageHandler) (producerHandle, error)
	NewSuperStreamProducer(superStream string, opts *stream.SuperStreamProducerOptions, confirmed ha.PartitionConfirmMessageHandler) (producerHandle, error)
	Close() error
}

// vendorEnvironment is the production adapter over the stream client.
type vendorEnvironment struct{ env *stream.Environment }

var _ environment = vendorEnvironment{}

// dialVendorEnvironment opens the port against a real broker.
//
// NewEnvironment returns a non-nil Environment BESIDE a non-nil error, so the
// failure path has something to dispose and `env != nil` is not a success test.
// v1.8.3 does tear the locator socket down itself, but only through an internal
// `defer client.Close()` it documents nowhere, and Client.connect opens the
// socket and starts its read goroutine before authentication — so disposing it
// here is what keeps a rejected credential from depending on that detail.
func dialVendorEnvironment(opts *stream.EnvironmentOptions) (environment, error) {
	env, err := stream.NewEnvironment(opts)
	if err != nil {
		if env != nil {
			_ = env.Close()
		}
		return nil, err
	}
	return vendorEnvironment{env: env}, nil
}

func (e vendorEnvironment) DeclareStream(name string, opts *stream.StreamOptions) error {
	return e.env.DeclareStream(name, opts)
}

func (e vendorEnvironment) DeclareSuperStream(name string, opts *stream.PartitionsOptions) error {
	return e.env.DeclareSuperStream(name, opts)
}

func (e vendorEnvironment) QueryOffset(consumer, streamName string) (int64, error) {
	return e.env.QueryOffset(consumer, streamName)
}

func (e vendorEnvironment) StoreOffset(consumer, streamName string, offset int64) error {
	return e.env.StoreOffset(consumer, streamName, offset)
}

// NewConsumer returns the reliable consumer itself rather than a wrapper: it
// satisfies consumerHandle AND offsetStorer, which is what lets a plain stream's
// SHUTDOWN flush commit through its own handle. The nil-on-error return matters —
// the client hands back a non-nil consumer beside a non-nil error, and a typed
// nil in a consumerHandle would read as a live handle.
func (e vendorEnvironment) NewConsumer(streamName string, opts *stream.ConsumerOptions,
	handler messageHandler,
) (consumerHandle, error) {
	consumer, err := ha.NewReliableConsumer(e.env, streamName, opts, vendorMessagesHandler(handler))
	if err != nil {
		return nil, err
	}
	return consumer, nil
}

// NewSuperStreamConsumer's handle is deliberately NOT an offsetStorer:
// *ha.ReliableSuperStreamConsumer has no StoreCustomOffset, so a super stream's
// shutdown flush goes through this port's StoreOffset, per partition.
func (e vendorEnvironment) NewSuperStreamConsumer(superStream string, opts *stream.SuperStreamConsumerOptions,
	handler messageHandler,
) (consumerHandle, error) {
	consumer, err := ha.NewReliableSuperStreamConsumer(e.env, superStream, vendorMessagesHandler(handler), opts)
	if err != nil {
		return nil, err
	}
	return consumer, nil
}

func (e vendorEnvironment) NewProducer(streamName string, opts *stream.ProducerOptions,
	confirmed ha.ConfirmMessageHandler,
) (producerHandle, error) {
	producer, err := ha.NewReliableProducer(e.env, streamName, opts, confirmed)
	if err != nil {
		return nil, err
	}
	return producer, nil
}

func (e vendorEnvironment) NewSuperStreamProducer(superStream string, opts *stream.SuperStreamProducerOptions,
	confirmed ha.PartitionConfirmMessageHandler,
) (producerHandle, error) {
	producer, err := ha.NewReliableSuperStreamProducer(e.env, superStream, opts, confirmed)
	if err != nil {
		return nil, err
	}
	return producer, nil
}

func (e vendorEnvironment) Close() error { return e.env.Close() }

// vendorMessagesHandler adapts the port's handler to the client's callback. The
// consumer it unwraps is both the source of the position and the target of the
// in-flight commit, exactly as the client hands it over.
func vendorMessagesHandler(handler messageHandler) stream.MessagesHandler {
	return func(consumerContext stream.ConsumerContext, msg *amqp.Message) {
		consumer := consumerContext.Consumer
		handler(consumer.GetStreamName(), consumer.GetOffset(), msg, consumer)
	}
}
```

- [ ] **Step 4: Rewire `Manager` onto the port**

In `messaging/streams/manager.go`, replace the producer-factory fields and the environment field (lines `104-119`) with:

```go
	// dialEnvironment opens the Environment port. A field rather than a direct
	// call so an in-package test can hand the manager a fake: the dial is the only
	// thing between Start and every phase it drives. Same seam as the AMQP lane's
	// amqpDialFunc (messaging/amqp_adapters.go:47), minus the process-global — a
	// Manager owns its own dialer, so tests need no save-and-restore.
	dialEnvironment func(*stream.EnvironmentOptions) (environment, error)

	mu         sync.Mutex
	env        environment
	consumers  []*runningConsumer
	publishers []*Publisher
	started    bool
	cancel     context.CancelFunc
```

In `NewManager` (`139-145`), replace the two factory assignments:

```go
	return &Manager{
		opts:            opts,
		log:             opts.Logger,
		flushBudget:     shutdownFlushBudget,
		dialEnvironment: dialVendorEnvironment,
	}
```

In `Start`, replace the dial block (`180-193`) with:

```go
	env, err := m.dialEnvironment(m.environmentOptions())
	if err != nil {
		return fmt.Errorf("failed to connect to stream endpoint %s: %w", redactStreamURI(m.opts.URI), safeEnvError(err))
	}
	m.env = env
```

Retype every environment parameter — `declareStreams`, `declareSuperStreams`, `startConsumer`, `startStreamConsumer`, `startSuperStreamConsumer`, `bindPublisher`, `constructProducer`, `resolveOffset` — from `env *stream.Environment` to `env environment`, and `envOffsetStorer.env` likewise:

```go
// envOffsetStorer commits one stream's offset through the Environment port
// instead of through a consumer.
type envOffsetStorer struct {
	env      environment
	consumer string
	stream   string
}
```

Replace the two consumer constructions. `startStreamConsumer` (`357-364`) — `runner.deliver` is a `messageHandler` as it stands, so the handler argument is a plain method value:

```go
	handle, err := env.NewConsumer(decl.Stream, opts, runner.deliver)
	if err != nil {
		return fmt.Errorf("failed to start consumer %q on stream %q: %w", decl.Name, decl.Stream, err)
	}

	// One stream, so one flush target: the reliable consumer itself. The assertion
	// is what keeps consumerHandle narrower than offset storage — the super-stream
	// handle has no StoreCustomOffset at all — and a handle that is not a storer
	// commits nothing rather than panicking (errNoOffsetStorer).
	storer, _ := handle.(offsetStorer)
	m.trackConsumer(decl, handle, runner, func(string) offsetStorer { return storer })
	return nil
```

`startSuperStreamConsumer` (`387-399`) — the comment is today's, unchanged but for naming the port:

```go
	handle, err := env.NewSuperStreamConsumer(decl.Stream, opts, runner.deliver)
	if err != nil {
		return fmt.Errorf("failed to start consumer %q on super stream %q: %w", decl.Name, decl.Stream, err)
	}

	// The shutdown flush goes through the Environment port, per partition:
	// *ha.ReliableSuperStreamConsumer has no StoreCustomOffset, and the partition
	// consumer that delivered the last message may already have been replaced by a
	// reconnect.
	m.trackConsumer(decl, handle, runner, func(partition string) offsetStorer {
		return envOffsetStorer{env: env, consumer: decl.Name, stream: partition}
	})
	return nil
```

`trackConsumer` (`415-430`) and `runningConsumer` (`78-88`) are **not** modified: the resolver they carry is still the shutdown flush's alone, and the existing doc comments already say so.

Replace `constructProducer` (`483-488`) and delete `producerFactory`, `superProducerFactory`, `newReliableProducer` and `newReliableSuperProducer` (`517-551`) entirely:

```go
// constructProducer builds one declaration's producer through the Environment
// port, on the API its kind requires.
//
// The plain producer options are the client's defaults on purpose: deduplication,
// sub-entry batching and compression are all deferred, and the default
// SubEntrySize of 1 is what makes the confirmation's message pointer a valid
// correlation key. Hash routing is the only strategy offered for a super stream:
// it is murmur3 with RabbitMQ's shared seed, so a partition assignment made here
// matches what the Java, .NET and Python clients compute for the same key. Key
// routing — which asks the broker to resolve a key to partitions — is deferred.
func (m *Manager) constructProducer(env environment, decl *publisherDeclaration) (producerHandle, error) {
	if decl.Super {
		opts := stream.NewSuperStreamProducerOptions(
			stream.NewHashRoutingStrategy(m.routingKeyExtractor(decl.Publisher)))
		return env.NewSuperStreamProducer(decl.Stream, opts, decl.Publisher.partitionsConfirmed)
	}
	return env.NewProducer(decl.Stream, stream.NewProducerOptions(), decl.Publisher.confirmed)
}
```

Keep `manager.go`'s `message` import (`routingKeyExtractor` still returns `func(message.StreamMessage) string`) and its `ha` import (`readyLocked` uses `ha.StatusOpen`).

- [ ] **Step 5: Move the vendor callback out of the runner**

In `messaging/streams/runner.go`, delete `messagesHandler` (`225-236`) and move its concurrency prose onto `deliver`, whose signature and body are otherwise untouched:

```go
// deliver runs the handler for one message, then applies the commit policy.
// store is the consumer that delivered it, which is what the in-flight commit
// goes through.
//
// The client invokes this sequentially per STREAM — which for a super stream
// means per partition, where one runner serves every partition and the client
// calls it from one goroutine each, concurrently. The framework keeps that shape:
// handlers run inline with no worker pool, because a stream is an ordered log and
// parallelism *within* one would break that order and make a committed offset
// claim messages behind it were handled. Anything reachable from here that is not
// per-stream state must therefore be safe for concurrent use — the offset book
// is, precisely because it hands each stream its own tracker.
func (r *consumerRunner) deliver(streamName string, offset int64, message *amqp.Message, store offsetStorer) {
```

`consumerRunner`'s fields, `invoke`, `offsetTracker` and `offsetBook` are unchanged. The `stream` import may now be unused in `runner.go` — remove it only if the compiler says so.

- [ ] **Step 6: Run the new test to verify it passes**

Run: `go test ./messaging/streams/ -run TestManagerStartDeclaresThroughTheEnvironmentPort -v`
Expected: `--- PASS: TestManagerStartDeclaresThroughTheEnvironmentPort` then `ok  github.com/gaborage/go-bricks/messaging/streams`

- [ ] **Step 7: Move the producer-construction tests onto the port**

In `messaging/streams/manager_test.go`: delete `errProducerConstruction` (line `39` — it now lives in `environment_fake_test.go`), `unreachableProducer` (`1340-1342`), `unreachableSuperProducer` (`1345-1349`), and the `"github.com/rabbitmq/rabbitmq-stream-go-client/pkg/message"` import (line `14`), which no test needs after this step.

Replace the two `m.env = &stream.Environment{}` literals — `TestManagerStartRejectsSecondStart` (line `465`) and `TestManagerStartRefusesRestartAfterStopConsumers` (line `1137-1138`) — with the fake, which is now what the field's type admits:

```go
	m.env = newFakeEnvironment()
```

```go
	env := newFakeEnvironment()
	m.env = env
```

Replace the four producer-factory tests (`1268-1410`) with these five, which drive the same paths through the port:

```go
// TestManagerBindPublisherWrapsAConstructionFailure exercises the construction
// failure through the Environment port: against a real broker it dials, so this
// is the only way a broker-free test can reach it.
func TestManagerBindPublisherWrapsAConstructionFailure(t *testing.T) {
	m := testManager(t)
	fake := newFakeEnvironment()
	fake.failOn(callNewProducer, errProducerConstruction)
	decl := onePublisherDeclaration(t)

	err := m.bindPublisher(fake, decl)

	require.ErrorIs(t, err, errProducerConstruction, "the client's own cause reaches the caller")
	assert.Contains(t, err.Error(), `failed to start the publisher on stream "`+testStream+`"`)
	assert.Empty(t, m.publishers, "a publisher that could not be constructed is not tracked")
	assert.ErrorIs(t, decl.Publisher.Publish(context.Background(), &PublishMessage{Data: []byte(testBody)}),
		ErrPublisherNotStarted, "and its handle stays unbound")
}

// TestManagerBindPublisherTracksAConstructedProducer is the success half: the
// producer is built for the declared stream, bound, and tracked.
func TestManagerBindPublisherTracksAConstructedProducer(t *testing.T) {
	m := testManager(t)
	fake := newFakeEnvironment()
	decl := onePublisherDeclaration(t)

	require.NoError(t, m.bindPublisher(fake, decl))

	assert.Equal(t, []string{callNewProducer + ":" + testStream}, fake.recorded(),
		"the producer is built for the declared stream, through the plain constructor")
	assert.Equal(t, []*Publisher{decl.Publisher}, m.publishers)
	assert.Equal(t, ha.StatusOpen, decl.Publisher.status(), "the handle is bound to the new producer")
}

// TestManagerBindPublisherWrapsASuperConstructionFailure is the super-stream half
// of the construction-failure path, and pins that the failure names the kind of
// target the declaration asked for rather than calling every target a stream.
func TestManagerBindPublisherWrapsASuperConstructionFailure(t *testing.T) {
	m := testManager(t)
	fake := newFakeEnvironment()
	fake.failOn(callNewSuperProducer, errProducerConstruction)
	decl := oneSuperPublisherDeclaration(t)

	err := m.bindPublisher(fake, decl)

	require.ErrorIs(t, err, errProducerConstruction)
	assert.Contains(t, err.Error(), `failed to start the publisher on super stream "`+testSuperStream+`"`)
	assert.Empty(t, m.publishers)
}

// TestManagerBindPublisherBuildsASuperProducerForASuperTarget pins the dispatch: a
// super declaration must reach the port's super-stream constructor, with the hash
// routing strategy that constructor requires and an extractor that answers the key
// the caller registered. Binding it through the plain constructor would compile and
// then fail at the broker, because a super stream is not a stream.
func TestManagerBindPublisherBuildsASuperProducerForASuperTarget(t *testing.T) {
	m := testManager(t)
	fake := newFakeEnvironment()
	// A plain bind here is the defect under test: it must never be reached.
	fake.failOn(callNewProducer, errProducerConstruction)
	decl := oneSuperPublisherDeclaration(t)

	require.NoError(t, m.bindPublisher(fake, decl))

	assert.Equal(t, []string{callNewSuperProducer + ":" + testSuperStream}, fake.recorded())
	opts := fake.superProducerOptions(testSuperStream)
	require.NotNil(t, opts)
	strategy, ok := opts.RoutingStrategy.(*stream.HashRoutingStrategy)
	require.True(t, ok, "hash routing is the only strategy offered")

	registered := amqp.NewMessage([]byte(testBody))
	decl.Publisher.pending.add(registered, testRoutingKey)
	assert.Equal(t, testRoutingKey, strategy.RoutingKeyExtractor(registered),
		"the extractor answers the key the caller registered with that exact message")

	assert.Equal(t, []*Publisher{decl.Publisher}, m.publishers)
	assert.Equal(t, ha.StatusOpen, decl.Publisher.status())
}

// TestManagerBindPublisherBuildsAPlainProducerForAPlainTarget is the other half of
// that dispatch, and would fail if the two constructors were ever swapped.
func TestManagerBindPublisherBuildsAPlainProducerForAPlainTarget(t *testing.T) {
	m := testManager(t)
	fake := newFakeEnvironment()
	// A super bind here is the defect under test: it must never be reached.
	fake.failOn(callNewSuperProducer, errProducerConstruction)

	require.NoError(t, m.bindPublisher(fake, onePublisherDeclaration(t)))

	assert.Equal(t, []string{callNewProducer + ":" + testStream}, fake.recorded())
	require.Len(t, m.publishers, 1)
	assert.Equal(t, ha.StatusOpen, m.publishers[0].status())
}
```

- [ ] **Step 8: Run the whole package to verify it is green**

Run: `go test -race ./messaging/streams/`
Expected: `ok  github.com/gaborage/go-bricks/messaging/streams` with no `DATA RACE` and no build errors. `runner_test.go` must compile untouched — if it does not, `deliver`'s signature was changed and the change must be reverted. If `TestManagerDeclareProceedsOnALiveContext` fails, leave it — Task 3 replaces it; if it *passes*, it still asserts through a panic on a nil interface, which Task 3 removes.

Run: `go vet ./messaging/streams/`
Expected: no output. (`go build` alone will not compile the test doubles — a new interface method is only caught by `vet`/`test`.)

- [ ] **Step 9: Commit**

```bash
cat > /tmp/streams-port-msg.txt <<'EOF'
refactor(streams): route the manager through an Environment port

Manager threaded the concrete *stream.Environment through ten methods, so
Start never reached declareStreams in a unit test and startStreamConsumer,
startSuperStreamConsumer, resolveOffset, trackConsumer and newRunner had
zero unit hits.

Introduce an unexported environment interface -- one method per vendor call
the manager makes -- with vendorEnvironment as its production adapter and a
dialEnvironment field tests overwrite, mirroring the AMQP lane's
amqpDialFunc minus the process-global. producerFactory and
superProducerFactory fold into the port.

The vendor stream.MessagesHandler unwrapping moves into the adapter, which
passes the delivering consumer through as the port handler's store argument,
so consumerRunner.deliver keeps its signature and every commit path is
byte-for-byte what it was: in flight through the consumer that delivered the
message, at shutdown through trackConsumer's storerFor -- the handle on a
plain stream, the port per partition on a super stream.

No exported API change.
EOF
git add messaging/streams/environment.go messaging/streams/environment_fake_test.go \
        messaging/streams/manager.go messaging/streams/runner.go \
        messaging/streams/manager_test.go
git commit -F /tmp/streams-port-msg.txt
```

---

### Task 2: The full fake and the in-process `Start` suite

**Files:**

- Modify: `messaging/streams/environment_fake_test.go` (add the blocking-store hook and the shared declaration/start helpers)
- Modify: `messaging/streams/manager_test.go` (add the `Start` suite)

**Interfaces:**

- Consumes from Task 1: `environment`, `messageHandler`, `fakeEnvironment`, `fakeConsumer`, `dialFake`, `Manager.dialEnvironment`, `consumerRunner.deliver`.
- Produces, consumed by Task 3:
  - `func startOnFake(t *testing.T, m *Manager, fake *fakeEnvironment, decls *Declarations)`
  - `func oneConsumerDecls() *Declarations` · `func superConsumerDecls() *Declarations`
  - `func (f *fakeEnvironment) blockStoreOn(key string) (entered <-chan struct{}, release chan<- struct{})`
  - `func ptrTo[T any](v T) *T`

- [ ] **Step 1: Write the failing tests — the `Start` suite**

First add the helpers to `messaging/streams/environment_fake_test.go`. The blocking hook needs new fields on `fakeEnvironment`:

```go
	// blockedStore parks one StoreOffset key until release closes, standing in for
	// the client's locator reconnect loop against a broker that is down: it has no
	// attempt cap and no deadline, so such a commit never returns on its own.
	blockedStore string
	storeEntered chan struct{}
	storeRelease chan struct{}
	storeOnce    sync.Once
```

```go
func (f *fakeEnvironment) blockStoreOn(key string) (entered <-chan struct{}, release chan<- struct{}) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.blockedStore = key
	f.storeEntered = make(chan struct{})
	f.storeRelease = make(chan struct{})
	return f.storeEntered, f.storeRelease
}
```

and `StoreOffset` becomes (replacing the Task 1 body):

```go
func (f *fakeEnvironment) StoreOffset(consumer, streamName string, offset int64) error {
	key := consumer + "/" + streamName

	f.mu.Lock()
	f.recordLocked(fmt.Sprintf("%s:%s=%d", callStoreOffset, key, offset))
	injected := f.errForLocked(callStoreOffset, key)
	blocked := f.blockedStore == key
	entered, release := f.storeEntered, f.storeRelease
	f.mu.Unlock()

	// Parked OUTSIDE the lock on purpose: the shutdown flush budget exists to walk
	// away from a commit that cannot finish, and holding this mutex would stall the
	// consumer behind it on the mutex instead of letting the budget skip it.
	if blocked {
		f.storeOnce.Do(func() { close(entered) })
		<-release
	}

	if injected != nil {
		return injected
	}

	f.mu.Lock()
	defer f.mu.Unlock()
	f.offsets[key] = offset
	return nil
}
```

Then the shared start helpers, at the end of the same file:

```go
// startOnFake runs a real Start against fake, so a test asserts on state Start
// actually produced instead of state a helper fabricated.
func startOnFake(t *testing.T, m *Manager, fake *fakeEnvironment, decls *Declarations) {
	t.Helper()
	dialFake(m, fake)
	require.NoError(t, m.Start(context.Background(), decls))
}

// oneConsumerDecls is the plain lane's minimum: one stream, one consumer on it.
func oneConsumerDecls() *Declarations {
	decls := NewDeclarations()
	decls.DeclareStream(testStream, nil)
	decls.DeclareConsumer(&ConsumerOptions{Stream: testStream, Name: testConsumerName, Handler: noopHandler})
	return decls
}

// superConsumerDecls is the partitioned lane's minimum: one super stream, one
// consumer across every partition of it.
func superConsumerDecls() *Declarations {
	decls := NewDeclarations()
	decls.DeclareSuperStream(testSuperStream, testPartitions, nil)
	decls.DeclareSuperStreamConsumer(&SuperStreamConsumerOptions{
		SuperStream: testSuperStream, Name: testConsumerName, Handler: noopHandler,
	})
	return decls
}

// ptrTo is how a table tells "no stored offset" from "stored offset 0".
func ptrTo[T any](v T) *T { return &v }
```

Add `"context"`, `"testing"` and `"github.com/stretchr/testify/require"` to that file's imports.

Now append the suite to `messaging/streams/manager_test.go`:

```go
// TestManagerStartDeclaresBindsThenStarts pins Start's phase order against the
// port. Publishers bind BEFORE consumers start because a handler may publish from
// its very first delivery, and an unbound publisher would reject it with
// ErrPublisherNotStarted; both declare phases come first because neither a
// producer nor a consumer can attach to a stream that does not exist.
func TestManagerStartDeclaresBindsThenStarts(t *testing.T) {
	m := testManager(t)
	fake := newFakeEnvironment()

	decls := NewDeclarations()
	decls.DeclareStream(testStream, nil)
	decls.DeclareSuperStream(testSuperStream, testPartitions, nil)
	decls.DeclarePublisher(&PublisherOptions{Stream: testStream})
	decls.DeclareConsumer(&ConsumerOptions{Stream: testStream, Name: testConsumerName, Handler: noopHandler})

	startOnFake(t, m, fake, decls)

	assert.Equal(t, []string{
		callDeclareStream + ":" + testStream,
		callDeclareSuperStream + ":" + testSuperStream,
		callNewProducer + ":" + testStream,
		callQueryOffset + ":" + testConsumerName + "/" + testStream,
		callNewConsumer + ":" + testStream,
	}, fake.recorded())
	assert.True(t, m.started)
	assert.Len(t, m.consumers, 1)
	assert.Len(t, m.publishers, 1)
}

// TestManagerStartUnwindsAFailedDeclaration is abortStartLocked in process: the
// environment was already dialed, so a caller that treats the error as fatal
// without calling Close must leak nothing, and a retried Start must not orphan
// the previous connection pool.
func TestManagerStartUnwindsAFailedDeclaration(t *testing.T) {
	m := testManager(t)
	failing := newFakeEnvironment()
	failing.failOn(callDeclareStream+":"+testStream, errDeclareFailed)
	dialFake(m, failing)

	err := m.Start(context.Background(), oneConsumerDecls())

	require.ErrorIs(t, err, errDeclareFailed)
	assert.Contains(t, err.Error(), `failed to declare stream "`+testStream+`"`)
	assert.Contains(t, failing.recorded(), callClose, "the dialed environment is disposed by the failed Start itself")
	assert.Nil(t, m.env)
	assert.Empty(t, m.consumers)
	assert.Empty(t, m.publishers)
	assert.False(t, m.started)

	// The unwind has to leave the manager startable, or a retry would be refused
	// by the already-started guard for the rest of the process's life.
	healthy := newFakeEnvironment()
	startOnFake(t, m, healthy, oneConsumerDecls())
	assert.True(t, m.started)
}

// TestManagerStartUnwindsAFailedConsumerStart is the unwind's other entry point,
// and the one with something to undo: a publisher was already bound, so the
// unwind has to close it rather than leave a producer attached to an environment
// it is about to dispose.
func TestManagerStartUnwindsAFailedConsumerStart(t *testing.T) {
	m := testManager(t)
	fake := newFakeEnvironment()
	fake.failOn(callNewConsumer+":"+testStream, errConsumerStart)
	dialFake(m, fake)

	decls := NewDeclarations()
	decls.DeclareStream(testStream, nil)
	publisher := decls.DeclarePublisher(&PublisherOptions{Stream: testStream})
	decls.DeclareConsumer(&ConsumerOptions{Stream: testStream, Name: testConsumerName, Handler: noopHandler})

	err := m.Start(context.Background(), decls)

	require.ErrorIs(t, err, errConsumerStart)
	assert.Contains(t, err.Error(), `failed to start consumer "`+testConsumerName+`" on stream "`+testStream+`"`)
	assert.True(t, fake.producer(testStream).isClosed(), "the publisher bound before the failure is closed")
	assert.ErrorIs(t, publisher.Publish(context.Background(), &PublishMessage{Data: []byte(testBody)}),
		ErrPublisherClosed)
	assert.Contains(t, fake.recorded(), callClose)
	assert.Nil(t, m.env)
	assert.False(t, m.started)
}

// TestManagerStartResolvesTheAttachPosition drives resolveOffset through Start.
// Only a MISSING offset may fall back to the declared start: any other query
// failure answered with a start position would SKIP, fatally so for the
// zero-value OffsetNext, and streams have no redelivery to get it back.
func TestManagerStartResolvesTheAttachPosition(t *testing.T) {
	tests := []struct {
		name     string
		stored   *int64
		queryErr error
		start    OffsetStart
		want     stream.OffsetSpecification
	}{
		{
			name:   "stored_offset_resumes_one_past_it",
			stored: ptrTo(int64(17)),
			start:  OffsetFirst(),
			want:   stream.OffsetSpecification{}.Offset(18),
		},
		{
			name:  "no_stored_offset_uses_the_declared_start",
			start: OffsetFirst(),
			want:  stream.OffsetSpecification{}.First(),
		},
		{
			name:     "query_failure_without_a_local_commit_replays_from_first",
			queryErr: errors.New("boom"),
			start:    OffsetNext(),
			want:     stream.OffsetSpecification{}.First(),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := testManager(t)
			fake := newFakeEnvironment()
			if tt.stored != nil {
				fake.setOffset(testConsumerName, testStream, *tt.stored)
			}
			if tt.queryErr != nil {
				fake.failOn(callQueryOffset, tt.queryErr)
			}

			decls := NewDeclarations()
			decls.DeclareStream(testStream, nil)
			decls.DeclareConsumer(&ConsumerOptions{
				Stream: testStream, Name: testConsumerName, Start: tt.start, Handler: noopHandler,
			})
			startOnFake(t, m, fake, decls)

			opts := fake.consumerOptions(testStream)
			require.NotNil(t, opts)
			assert.Equal(t, tt.want, opts.Offset)
		})
	}
}

// TestManagerStartPromotionResolvesTheOffsetAgain exercises the single active
// consumer promotion callback the client fires from its own goroutine: another
// group member may have advanced the offset while this one was passive, so the
// position is resolved again rather than reused from attach time.
func TestManagerStartPromotionResolvesTheOffsetAgain(t *testing.T) {
	m := testManager(t)
	fake := newFakeEnvironment()

	decls := NewDeclarations()
	decls.DeclareStream(testStream, nil)
	decls.DeclareConsumer(&ConsumerOptions{
		Stream: testStream, Name: testConsumerName, SAC: true, Start: OffsetFirst(), Handler: noopHandler,
	})
	startOnFake(t, m, fake, decls)

	require.Equal(t, stream.OffsetSpecification{}.First(), fake.consumerOptions(testStream).Offset,
		"the premise: nothing was stored when the consumer attached")

	// Another member committed while this one was passive.
	fake.setOffset(testConsumerName, testStream, 41)

	assert.Equal(t, stream.OffsetSpecification{}.Offset(42), fake.consumer(testStream).promote(testStream))
}

// TestManagerStartPromotionFallsBackToTheLocalCommit is resolveOffset's third
// branch, which only a promotion can reach: the broker cannot be asked, but this
// process committed a position for the stream itself, so that is the closest
// truth available and is no older than the broker's.
func TestManagerStartPromotionFallsBackToTheLocalCommit(t *testing.T) {
	m := NewManager(ManagerOptions{
		URI: unreachableTestURI, OffsetStoreCount: 1, Logger: logger.New("error", false),
	})
	fake := newFakeEnvironment()

	decls := NewDeclarations()
	decls.DeclareStream(testStream, nil)
	decls.DeclareConsumer(&ConsumerOptions{
		Stream: testStream, Name: testConsumerName, SAC: true, Start: OffsetNext(), Handler: noopHandler,
	})
	startOnFake(t, m, fake, decls)

	consumer := fake.consumer(testStream)
	consumer.deliver(testStream, 41, amqpMessage("payload"))
	require.Equal(t, map[string]int64{testStream: 41}, m.consumers[0].offsets.stored(),
		"the premise: this process committed 41")

	fake.failOn(callQueryOffset, errors.New("boom"))

	assert.Equal(t, stream.OffsetSpecification{}.Offset(42), consumer.promote(testStream),
		"a broker that cannot be asked resumes from the local commit, never from the declared start")
}

// TestManagerStartCommitsInFlightThroughTheDeliveringConsumer pins the commit
// path the Environment port must NOT have moved: an in-flight commit goes through
// the consumer that delivered the message, on its connection — neither through
// the reliable handle above it nor through the port's locator. Both kinds behave
// identically here; the asymmetry is the shutdown flush's alone.
func TestManagerStartCommitsInFlightThroughTheDeliveringConsumer(t *testing.T) {
	tests := []struct {
		name         string
		decls        func() *Declarations
		trackedName  string
		deliverOn    string
	}{
		{name: "plain_stream", decls: oneConsumerDecls, trackedName: testStream, deliverOn: testStream},
		{name: "super_stream_partition", decls: superConsumerDecls, trackedName: testSuperStream, deliverOn: testPartition0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := NewManager(ManagerOptions{
				URI: unreachableTestURI, OffsetStoreCount: 1, Logger: logger.New("error", false),
			})
			fake := newFakeEnvironment()
			startOnFake(t, m, fake, tt.decls())

			consumer := fake.consumer(tt.trackedName)
			consumer.deliver(tt.deliverOn, 7, amqpMessage("payload"))

			key := testConsumerName + "/" + tt.deliverOn
			assert.Contains(t, fake.recorded(), callConsumerStore+":"+key+"=7")
			assert.NotContains(t, fake.recorded(), callStoreOffset+":"+key+"=7",
				"an in-flight commit never goes through the Environment port")
			assert.Empty(t, consumer.events.recorded(),
				"nor through the reliable handle the manager tracks")

			offset, ok := fake.storedOffset(testConsumerName, tt.deliverOn)
			require.True(t, ok, "the commit reaches the broker's offset store")
			assert.Equal(t, int64(7), offset)
			assert.Equal(t, map[string]int64{tt.deliverOn: 7}, m.consumers[0].offsets.stored())
		})
	}
}

// TestManagerStartFlushesPlainThroughTheHandleAndSuperThroughThePort is the
// offset-storer asymmetry, which lives entirely in the SHUTDOWN flush: a plain
// consumer's handle is an offsetStorer, while *ha.ReliableSuperStreamConsumer has
// no StoreCustomOffset at all, so its partitions commit through the port.
func TestManagerStartFlushesPlainThroughTheHandleAndSuperThroughThePort(t *testing.T) {
	tests := []struct {
		name        string
		decls       func() *Declarations
		trackedName string
		deliverOn   string
		wantHandle  []string
		wantPort    bool
	}{
		{
			name: "plain_stream_flushes_through_its_own_handle",
			decls: oneConsumerDecls, trackedName: testStream, deliverOn: testStream,
			wantHandle: []string{"store:7", "close"}, wantPort: false,
		},
		{
			name: "super_stream_flushes_through_the_port",
			decls: superConsumerDecls, trackedName: testSuperStream, deliverOn: testPartition0,
			wantHandle: []string{"close"}, wantPort: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// A high threshold leaves the delivery pending, so the flush has work.
			m := NewManager(ManagerOptions{
				URI: unreachableTestURI, OffsetStoreCount: 1000, Logger: logger.New("error", false),
			})
			fake := newFakeEnvironment()
			startOnFake(t, m, fake, tt.decls())

			consumer := fake.consumer(tt.trackedName)
			consumer.deliver(tt.deliverOn, 7, amqpMessage("payload"))
			require.Empty(t, consumer.events.recorded(), "the premise: the offset is still pending")

			m.StopConsumers()

			assert.Equal(t, tt.wantHandle, consumer.events.recorded())
			portEntry := callStoreOffset + ":" + testConsumerName + "/" + tt.deliverOn + "=7"
			if tt.wantPort {
				assert.Contains(t, fake.recorded(), portEntry)
			} else {
				assert.NotContains(t, fake.recorded(), portEntry)
			}
		})
	}
}

// TestManagerStartRunsTheHandlerForADeliveredMessage is the delivery path end to
// end through Start: the port hands the runner a message, the module's handler
// sees the framework's view of it, and the offset is committed after success.
func TestManagerStartRunsTheHandlerForADeliveredMessage(t *testing.T) {
	var got *Message
	m := NewManager(ManagerOptions{
		URI: unreachableTestURI, OffsetStoreCount: 1, Logger: logger.New("error", false),
	})
	fake := newFakeEnvironment()

	decls := NewDeclarations()
	decls.DeclareStream(testStream, nil)
	decls.DeclareConsumer(&ConsumerOptions{
		Stream: testStream, Name: testConsumerName,
		Handler: func(_ context.Context, msg *Message) error {
			got = msg
			return nil
		},
	})
	startOnFake(t, m, fake, decls)

	fake.consumer(testStream).deliver(testStream, 55, amqpMessage("payload"))

	require.NotNil(t, got)
	assert.Equal(t, []byte("payload"), got.Data)
	assert.Equal(t, int64(55), got.Offset)
	assert.Equal(t, testStream, got.Stream)
	assert.Equal(t, map[string]any{"kind": "test"}, got.Properties)
	assert.Contains(t, fake.recorded(), callConsumerStore+":"+testConsumerName+"/"+testStream+"=55")
}
```

- [ ] **Step 2: Run the suite to verify it fails**

Run: `go test ./messaging/streams/ -run 'TestManagerStart(DeclaresBindsThenStarts|Unwinds|Resolves|Promotion|Commits|Flushes|RunsTheHandler)'`

Expected: FAIL — a build failure naming the helpers that do not exist yet:

```text
messaging/streams/manager_test.go:...: undefined: startOnFake
messaging/streams/manager_test.go:...: undefined: oneConsumerDecls
messaging/streams/manager_test.go:...: undefined: superConsumerDecls
messaging/streams/manager_test.go:...: undefined: ptrTo
FAIL	github.com/gaborage/go-bricks/messaging/streams [build failed]
```

If you added the helpers in Step 1 as written, run the suite anyway before proceeding and confirm every one of the nine tests passes; if any fails, the failure is real and belongs to `manager.go` — fix it there, never by weakening the assertion.

- [ ] **Step 3: Make the suite pass**

The suite is written against the port Task 1 already built, so no production change should be needed. If a test fails, the likely causes and their fixes:

- `TestManagerStartDeclaresBindsThenStarts` sees `new_consumer` before `new_producer` → `Start` is calling `startConsumers` before `bindPublishers`; restore the order at `manager.go:221-237`.
- `TestManagerStartUnwindsAFailedConsumerStart` reports the publisher still open → `abortStartLocked` is not reaching `stopPublishersLocked`; check `stopLocked` (`manager.go:713-738`).
- `TestManagerStartCommitsInFlightThroughTheDeliveringConsumer` records a `store_offset:` or a `store:` → a commit path moved; the adapter must pass `consumerContext.Consumer` as `store` and `deliver` must use its `store` argument untouched.
- `TestManagerStartFlushesPlainThroughTheHandleAndSuperThroughThePort` fails on the super case → `startSuperStreamConsumer`'s `storerFor` is returning the handle instead of `envOffsetStorer`.
- `TestManagerStartPromotionFallsBackToTheLocalCommit` returns `First()` → `resolveOffset` is not consulting `committed.stored()` (`manager.go:583-586`).

- [ ] **Step 4: Run the suite to verify it passes**

Run: `go test -race ./messaging/streams/ -run 'TestManagerStart' -v`
Expected: every `TestManagerStart*` reports `--- PASS`, then `ok  github.com/gaborage/go-bricks/messaging/streams`.

- [ ] **Step 5: Run the whole package**

Run: `go test -race ./messaging/streams/`
Expected: `ok  github.com/gaborage/go-bricks/messaging/streams`, no `DATA RACE`.

- [ ] **Step 6: Commit**

```bash
cat > /tmp/streams-start-suite-msg.txt <<'EOF'
test(streams): cover Manager.Start in process against the fake environment

The fake environment records port calls in order, injects a failure per call,
keeps offsets in memory and hands back consumer handles a test can push
messages through, so the whole of Start is now reachable without a broker. A
delivery driven through the fake commits through the consumer that delivered
it, recorded under a label distinct from the port's own StoreOffset so a test
can tell the two paths apart.

New coverage: the declare/bind/start phase order, abortStartLocked's unwind
from a failed declaration and from a failed consumer start (including the
publisher bound before it), resolveOffset's fallbacks including the
local-commit branch only a single-active-consumer promotion can reach, the
in-flight commit target for both kinds, the shutdown-flush asymmetry, and one
delivery running the module handler end to end.
EOF
git add messaging/streams/environment_fake_test.go messaging/streams/manager_test.go
git commit -F /tmp/streams-start-suite-msg.txt
```

---

### Task 3: Retire the `attach*` helpers — build every test's state through `Start`

**Files:**

- Modify: `messaging/streams/manager_test.go` (delete `attach` `240-253`, `attachPartitioned` `255-279`, `attachPublisher` `1163-1166`, `rebindPublisher` `1168-1175`; rewrite every caller)

**Interfaces:**

- Consumes from Task 2: `startOnFake`, `oneConsumerDecls`, `superConsumerDecls`, `fakeEnvironment.useProducer`, `fakeEnvironment.blockStoreOn`, `fakeConsumer.deliver`.

This task ships as **two commits** so each diff stays reviewable: the consumer-side helpers first, the publisher-side helpers second.

#### 3a — the consumer-side helpers

- [ ] **Step 1: Write the failing test — the declare phase asserted by a call, not by a panic**

Replace `TestManagerDeclareProceedsOnALiveContext` (`manager_test.go:539-547`) with:

```go
// A live context must not short-circuit the fan-out. Before the Environment port
// this was asserted by panicking on a nil environment, which proved only that
// SOMETHING was dereferenced; now it names the call that reached the broker.
func TestManagerDeclareProceedsOnALiveContext(t *testing.T) {
	decls := NewDeclarations()
	decls.DeclareStream(testStream, nil)
	m := testManager(t)
	fake := newFakeEnvironment()

	require.NoError(t, m.declareStreams(context.Background(), fake, decls))

	assert.Equal(t, []string{callDeclareStream + ":" + testStream}, fake.recorded())
}
```

- [ ] **Step 2: Run it to verify it fails**

Run: `go test ./messaging/streams/ -run TestManagerDeclareProceedsOnALiveContext -v`
Expected: PASS. This one is a *straight replacement* of an assertion, not a new behavior — the red here is the old test's `assert.Panics`, which you have just deleted. If instead it FAILS, `declareStreams` is not forwarding to the port; fix `manager.go:273-283`.

- [ ] **Step 3: Rewrite the `attach`-based tests**

Delete `attach` (`240-253`) and `attachPartitioned` (`255-279`), and rewrite `recordingManager` (`220-230`):

```go
// recordingManager starts a manager on fake with one plain consumer that has a
// pending, uncommitted offset, so both shutdown guards are reachable. The count
// threshold is deliberately high: the delivery must leave work for the shutdown
// flush to do rather than commit it inline.
func recordingManager(t *testing.T, fake *fakeEnvironment) (*Manager, *recordingLogger) {
	t.Helper()
	log := &recordingLogger{}
	m := NewManager(ManagerOptions{URI: unreachableTestURI, OffsetStoreCount: 1000, Logger: log})
	startOnFake(t, m, fake, oneConsumerDecls())
	fake.consumer(testStream).deliver(testStream, 4, amqpMessage("payload"))
	return m, log
}
```

Now each caller, in file order:

`TestManagerStartRejectsSecondStart` (`462-474`) — drop the `attach` line; the fake environment installed in Task 1 Step 7 is the whole premise:

```go
func TestManagerStartRejectsSecondStart(t *testing.T) {
	m := NewManager(ManagerOptions{URI: unreachableTestURI, Logger: logger.New("error", false)})
	fake := newFakeEnvironment()
	startOnFake(t, m, fake, oneConsumerDecls())

	err := m.Start(context.Background(), oneConsumerDecls())

	require.Error(t, err)
	assert.Contains(t, err.Error(), "already started")
}
```

`TestManagerStopConsumersFlushesEveryTrackedStream` (`285-296`):

```go
func TestManagerStopConsumersFlushesEveryTrackedStream(t *testing.T) {
	m := NewManager(ManagerOptions{
		URI: unreachableTestURI, OffsetStoreCount: 1000, Logger: logger.New("error", false),
	})
	fake := newFakeEnvironment()
	startOnFake(t, m, fake, superConsumerDecls())

	consumer := fake.consumer(testSuperStream)
	consumer.deliver(testPartition0, 11, amqpMessage("a"))
	consumer.deliver(testPartition1, 501, amqpMessage("b"))
	require.NotContains(t, fake.recorded(), callStoreOffset+":"+testConsumerName+"/"+testPartition0+"=11",
		"the premise: both offsets are still pending")

	m.StopConsumers()

	assert.Contains(t, fake.recorded(), callStoreOffset+":"+testConsumerName+"/"+testPartition0+"=11")
	assert.Contains(t, fake.recorded(), callStoreOffset+":"+testConsumerName+"/"+testPartition1+"=501")
	assert.Equal(t, []string{"close"}, consumer.events.recorded(), "the handle itself stores nothing")
	assert.Empty(t, m.consumers)
}
```

`TestManagerStopConsumersAbandonsFlushWhenBudgetSpent` (`328-385`) — the super stream is declared first so it is the consumer whose commit hangs, and the plain stream behind it is the one that must be skipped rather than attempted:

```go
func TestManagerStopConsumersAbandonsFlushWhenBudgetSpent(t *testing.T) {
	log := &recordingLogger{}
	m := NewManager(ManagerOptions{URI: unreachableTestURI, OffsetStoreCount: 1000, Logger: log})
	m.flushBudget = 50 * time.Millisecond

	fake := newFakeEnvironment()
	entered, release := fake.blockStoreOn(testConsumerName + "/" + testPartition0)
	defer close(release)

	decls := NewDeclarations()
	decls.DeclareSuperStream(testSuperStream, testPartitions, nil)
	decls.DeclareStream(testStream, nil)
	decls.DeclareSuperStreamConsumer(&SuperStreamConsumerOptions{
		SuperStream: testSuperStream, Name: testConsumerName, Handler: noopHandler,
	})
	decls.DeclareConsumer(&ConsumerOptions{Stream: testStream, Name: secondConsumerName, Handler: noopHandler})
	startOnFake(t, m, fake, decls)

	fake.consumer(testSuperStream).deliver(testPartition0, 11, amqpMessage("a"))
	behind := fake.consumer(testStream)
	behind.deliver(testStream, 501, amqpMessage("b"))
	require.Empty(t, behind.events.recorded(), "the premise: the second consumer's offset is still pending")

	returned := make(chan struct{})
	go func() {
		m.StopConsumers()
		close(returned)
	}()

	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("the blocked commit was never attempted, so this test is not exercising the hang it claims to")
	}

	select {
	case <-returned:
	case <-time.After(10 * time.Second):
		t.Fatal("StopConsumers never returned: the flush budget did not bound a commit that cannot finish")
	}

	assert.Equal(t, []string{"close"}, behind.events.recorded(),
		"a budget already spent must skip the remaining flush outright, not attempt one more")
	assert.Equal(t, []string{testSuperStream, testStream}, log.warnStreams(msgFlushSkipped),
		"every skipped flush is reported at WARN, naming its stream")
	assert.Equal(t, []string{"close"}, fake.consumer(testSuperStream).events.recorded(),
		"an abandoned flush must still let its consumer be closed")
	assert.Empty(t, m.consumers)
	assert.False(t, m.started)
}
```

`TestManagerStopConsumersFlushesWithinBudget` (`391-402`):

```go
func TestManagerStopConsumersFlushesWithinBudget(t *testing.T) {
	log := &recordingLogger{}
	m := NewManager(ManagerOptions{URI: unreachableTestURI, OffsetStoreCount: 1000, Logger: log})
	fake := newFakeEnvironment()
	startOnFake(t, m, fake, superConsumerDecls())

	consumer := fake.consumer(testSuperStream)
	consumer.deliver(testPartition0, 11, amqpMessage("a"))
	consumer.deliver(testPartition1, 501, amqpMessage("b"))

	m.StopConsumers()

	assert.Contains(t, fake.recorded(), callStoreOffset+":"+testConsumerName+"/"+testPartition0+"=11")
	assert.Contains(t, fake.recorded(), callStoreOffset+":"+testConsumerName+"/"+testPartition1+"=501")
	assert.Empty(t, log.warnStreams(msgFlushSkipped), "a flush that lands well inside the budget skips nothing")
	assert.Equal(t, []string{"close"}, consumer.events.recorded())
}
```

`TestManagerStatsKeysOffsetsByTrackedStream` (`407-418`):

```go
func TestManagerStatsKeysOffsetsByTrackedStream(t *testing.T) {
	m := NewManager(ManagerOptions{
		URI: unreachableTestURI, OffsetStoreCount: 1, Logger: logger.New("error", false),
	})
	fake := newFakeEnvironment()
	startOnFake(t, m, fake, superConsumerDecls())

	consumer := fake.consumer(testSuperStream)
	consumer.deliver(testPartition0, 11, amqpMessage("a"))
	consumer.deliver(testPartition1, 501, amqpMessage("b"))

	stats := m.Stats()

	assert.Equal(t, map[string]int64{
		testPartition0 + "/" + testConsumerName: 11,
		testPartition1 + "/" + testConsumerName: 501,
	}, stats["stored_offsets"])
	assert.Equal(t, 1, stats["consumers"], "the two partitions are one consumer")
}
```

`TestManagerStopConsumersFlushesBeforeClosing` (`591-604`):

```go
func TestManagerStopConsumersFlushesBeforeClosing(t *testing.T) {
	m := NewManager(ManagerOptions{
		URI: unreachableTestURI, OffsetStoreCount: 1000, Logger: logger.New("error", false),
	})
	fake := newFakeEnvironment()
	startOnFake(t, m, fake, oneConsumerDecls())
	fake.consumer(testStream).deliver(testStream, 88, amqpMessage("payload"))

	m.StopConsumers()

	assert.Equal(t, []string{"store:88", "close"}, fake.consumer(testStream).events.recorded(),
		"a clean shutdown commits the last handled offset before the consumer goes away")
	assert.False(t, m.started)
	assert.Empty(t, m.consumers)
}
```

`TestManagerStopConsumersIsIdempotent` (`606-615`):

```go
func TestManagerStopConsumersIsIdempotent(t *testing.T) {
	m := testManager(t)
	fake := newFakeEnvironment()
	startOnFake(t, m, fake, oneConsumerDecls())

	m.StopConsumers()
	m.StopConsumers()

	assert.Equal(t, []string{"close"}, fake.consumer(testStream).events.recorded(),
		"nothing pending, and no second close")
}
```

`TestManagerStopConsumersCancelsConsumeContext` (`652-662`) — `Start` now installs `m.cancel` itself, so the test overwrites it with a context it owns and can observe:

```go
func TestManagerStopConsumersCancelsConsumeContext(t *testing.T) {
	m := testManager(t)
	fake := newFakeEnvironment()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	startOnFake(t, m, fake, oneConsumerDecls())
	consumeCtx, consumeCancel := consumeContext(ctx)
	m.cancel = consumeCancel

	m.StopConsumers()

	require.ErrorIs(t, consumeCtx.Err(), context.Canceled)
	assert.Nil(t, m.cancel)
}
```

`TestManagerStopConsumersStopsDetachedConsumeContext` (`683-698`):

```go
func TestManagerStopConsumersStopsDetachedConsumeContext(t *testing.T) {
	m := testManager(t)
	fake := newFakeEnvironment()
	parent, cancelParent := context.WithCancel(context.Background())
	startOnFake(t, m, fake, oneConsumerDecls())
	consumeCtx, cancel := consumeContext(parent)
	m.cancel = cancel

	cancelParent()
	require.NoError(t, consumeCtx.Err(), "the premise: the caller's context is canceled first")

	m.StopConsumers()

	require.ErrorIs(t, consumeCtx.Err(), context.Canceled)
	assert.Nil(t, m.cancel)
	assert.False(t, m.started)
}
```

`TestManagerStats` (`707-726`):

```go
func TestManagerStats(t *testing.T) {
	m := NewManager(ManagerOptions{
		URI:                 unreachableTestURI,
		OffsetStoreCount:    1,
		OffsetStoreInterval: 2 * time.Second,
		Logger:              logger.New("error", false),
	})
	fake := newFakeEnvironment()
	startOnFake(t, m, fake, oneConsumerDecls())
	fake.consumer(testStream).deliver(testStream, 31, amqpMessage("payload"))

	stats := m.Stats()

	assert.Equal(t, true, stats["started"])
	assert.Equal(t, 1, stats["consumers"])
	assert.Equal(t, true, stats["ready"])
	assert.Equal(t, map[string]int64{testStream + "/" + testConsumerName: 31}, stats["stored_offsets"])
	assert.Equal(t, 1, stats["offset_store_count"])
	assert.Equal(t, "2s", stats["offset_flush_interval"])
}
```

`TestManagerStatsOmitsUncommittedOffsets` (`728-735`):

```go
func TestManagerStatsOmitsUncommittedOffsets(t *testing.T) {
	m := NewManager(ManagerOptions{
		URI: unreachableTestURI, OffsetStoreCount: 1000, Logger: logger.New("error", false),
	})
	fake := newFakeEnvironment()
	startOnFake(t, m, fake, oneConsumerDecls())
	fake.consumer(testStream).deliver(testStream, 31, amqpMessage("payload"))

	stats := m.Stats()

	assert.Empty(t, stats["stored_offsets"])
}
```

`TestManagerReady` (`737-760`):

```go
func TestManagerReady(t *testing.T) {
	tests := []struct {
		name    string
		started bool
		status  int
		want    bool
	}{
		{name: "open_consumer_is_ready", started: true, status: ha.StatusOpen, want: true},
		{name: "reconnecting_consumer_is_not_ready", started: true, status: ha.StatusReconnecting, want: false},
		{name: "closed_consumer_is_not_ready", started: true, status: ha.StatusClosed, want: false},
		{name: "never_started_is_not_ready", started: false, status: ha.StatusOpen, want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := testManager(t)
			fake := newFakeEnvironment()
			startOnFake(t, m, fake, oneConsumerDecls())
			fake.consumer(testStream).events.status = tt.status
			m.started = tt.started

			assert.Equal(t, tt.want, m.Ready())
			assert.Equal(t, tt.want, m.Stats()["ready"])
		})
	}
}
```

`TestManagerStopConsumersToleratesFlushAndCloseErrors` (`621-638`):

```go
func TestManagerStopConsumersToleratesFlushAndCloseErrors(t *testing.T) {
	fake := newFakeEnvironment()
	m, log := recordingManager(t, fake)
	handle := fake.consumer(testStream).events
	handle.closeErr = errors.New("close failed")
	handle.storeErr = errors.New("store failed")

	assert.NotPanics(t, m.StopConsumers)
	assert.False(t, m.started)

	assert.ElementsMatch(t, []string{msgFlushFailed, msgCloseConsumerFailed}, log.warnMessages())
	flushErr, ok := log.warnError(msgFlushFailed)
	require.True(t, ok)
	assert.Equal(t, "store failed", flushErr)
	closeErr, ok := log.warnError(msgCloseConsumerFailed)
	require.True(t, ok)
	assert.Equal(t, "close failed", closeErr)
}
```

`TestManagerStopConsumersIsSilentOnCleanShutdown` (`642-650`):

```go
func TestManagerStopConsumersIsSilentOnCleanShutdown(t *testing.T) {
	fake := newFakeEnvironment()
	m, log := recordingManager(t, fake)

	m.StopConsumers()

	assert.Equal(t, []string{"store:4", "close"}, fake.consumer(testStream).events.recorded())
	assert.Empty(t, log.warnMessages(), "a clean shutdown reports nothing to the operator")
}
```

`TestManagerAbortStartLockedDisposesEnvironment` (`1095-1108`):

```go
func TestManagerAbortStartLockedDisposesEnvironment(t *testing.T) {
	m := testManager(t)
	fake := newFakeEnvironment()
	startOnFake(t, m, fake, oneConsumerDecls())

	m.abortStartLocked()

	assert.Nil(t, m.env, "the environment is disposed, not just the consumers")
	assert.Empty(t, m.consumers)
	assert.False(t, m.started)
	assert.Equal(t, []string{"close"}, fake.consumer(testStream).events.recorded())
	assert.Contains(t, fake.recorded(), callClose)

	require.NoError(t, m.Close(), "the follow-up Close short-circuits instead of closing twice")
}
```

`TestManagerAbortStartLockedReportsOnlyRealDisposalFailures` (`1114-1123`) — the premise changes with the port: there IS an environment to dispose now, so the guard is exercised from both sides:

```go
// TestManagerAbortStartLockedReportsOnlyRealDisposalFailures pins the unwind's
// own guard from both sides: a disposal that succeeded must not be reported as a
// failure on every successful unwind, and one that failed must reach the operator
// with its cause attached.
func TestManagerAbortStartLockedReportsOnlyRealDisposalFailures(t *testing.T) {
	t.Run("successful_disposal_is_silent", func(t *testing.T) {
		fake := newFakeEnvironment()
		m, log := recordingManager(t, fake)

		m.abortStartLocked()

		require.Contains(t, fake.recorded(), callClose, "the premise: there was an environment to dispose")
		assert.Empty(t, log.warnMessages(), "the whole unwind is silent when every step succeeds")
	})

	t.Run("failed_disposal_is_reported", func(t *testing.T) {
		fake := newFakeEnvironment()
		m, log := recordingManager(t, fake)
		fake.failOn(callClose, errors.New("close failed"))

		m.abortStartLocked()

		assert.Contains(t, log.warnMessages(), msgCloseEnvFailed)
		closeErr, ok := log.warnError(msgCloseEnvFailed)
		require.True(t, ok)
		assert.Equal(t, "close failed", closeErr)
	})
}
```

`TestManagerStartRefusesRestartAfterStopConsumers` (`1133-1153`):

```go
func TestManagerStartRefusesRestartAfterStopConsumers(t *testing.T) {
	m := NewManager(ManagerOptions{URI: unreachableTestURI, Logger: logger.New("error", false)})
	fake := newFakeEnvironment()
	startOnFake(t, m, fake, oneConsumerDecls())

	m.StopConsumers()
	require.False(t, m.started, "the premise: StopConsumers clears started")
	require.Same(t, fake, m.env, "the premise: StopConsumers leaves the environment for Close")

	err := m.Start(context.Background(), oneConsumerDecls())

	require.Error(t, err)
	assert.Contains(t, err.Error(), "already started",
		"the restart is refused by the guard, not by a failed dial")
	assert.Same(t, fake, m.env, "the first environment is still the one Close disposes, not an orphan")
	assert.False(t, m.started)
	assert.NotContains(t, fake.recorded(), callClose, "a refused restart disposes nothing")
}
```

- [ ] **Step 4: Run the rewritten tests**

Run: `go test -race ./messaging/streams/ -run 'TestManager(Stop|Stats|Ready|AbortStart|Start|Declare)' -v`
Expected: every listed test reports `--- PASS`. Then `go test -race ./messaging/streams/` → `ok`.

- [ ] **Step 5: Commit 3a**

```bash
cat > /tmp/streams-attach-consumers-msg.txt <<'EOF'
test(streams): build consumer shutdown state through Start, not attach

attach and attachPartitioned fabricated the runningConsumer state Start is
supposed to produce, so every shutdown, stats and readiness test asserted
against a hand-built shape rather than the one the manager builds. They now
run a real Start against the fake environment and push deliveries through the
handle it returns.

TestManagerDeclareProceedsOnALiveContext stops asserting through a panic on a
nil environment and names the port call the phase made instead.
TestManagerAbortStartLockedReportsOnlyRealDisposalFailures gains the failing
half, which was unreachable while there was never an environment to dispose.
EOF
git add messaging/streams/manager_test.go
git commit -F /tmp/streams-attach-consumers-msg.txt
```

#### 3b — the publisher-side helpers

- [ ] **Step 6: Rewrite the `attachPublisher`/`rebindPublisher` tests**

Delete `attachPublisher` (`1163-1166`) and `rebindPublisher` (`1168-1175`), then rewrite their seven callers.

`TestManagerStopClosesPublishersAfterConsumers` (`1448-1463`):

```go
func TestManagerStopClosesPublishersAfterConsumers(t *testing.T) {
	m := testManager(t)
	fake := newFakeEnvironment()

	var order []string
	producer := openProducer()
	producer.onClose = func() { order = append(order, "publisher") }
	fake.useProducer(producer) // as delivered: the fake hands p back for the one publisher a test starts

	decls := oneConsumerDecls()
	decls.DeclarePublisher(&PublisherOptions{Stream: testStream})
	startOnFake(t, m, fake, decls)
	fake.consumer(testStream).events.onClose = func() { order = append(order, "consumer") }

	m.StopConsumers()

	assert.Equal(t, []string{"consumer", "publisher"}, order)
	assert.Empty(t, m.publishers)
	assert.False(t, m.started)
}
```

`TestManagerStopFailsAnInFlightPublish` (`1468-1481`):

```go
func TestManagerStopFailsAnInFlightPublish(t *testing.T) {
	m := testManager(t)
	fake := newFakeEnvironment()
	producer := blockingProducer(t)
	fake.useProducer(testStream, producer)

	decls := onePublisher(t)
	startOnFake(t, m, fake, decls)
	p := decls.publishers[0].Publisher

	done := publishAsync(context.Background(), p, &PublishMessage{Data: []byte(testBody)})
	waitForSend(t, producer)

	m.StopConsumers()

	require.ErrorIs(t, <-done, ErrPublisherClosed)
	assert.ErrorIs(t, p.Publish(context.Background(), &PublishMessage{Data: []byte(testBody)}), ErrPublisherClosed,
		"the publisher stays closed for late callers")
}
```

`TestManagerStopReportsAPublisherCloseFailure` (`1483-1496`):

```go
func TestManagerStopReportsAPublisherCloseFailure(t *testing.T) {
	log := &recordingLogger{}
	m := NewManager(ManagerOptions{URI: unreachableTestURI, Logger: log})
	fake := newFakeEnvironment()
	producer := openProducer()
	producer.closeErr = errors.New("close failed")
	fake.useProducer(testStream, producer)
	startOnFake(t, m, fake, onePublisher(t))

	assert.NotPanics(t, m.StopConsumers)

	assert.Equal(t, []string{msgClosePublisherFailed}, log.warnMessages())
	closeErr, ok := log.warnError(msgClosePublisherFailed)
	require.True(t, ok)
	assert.Equal(t, "close failed", closeErr)
}
```

`TestManagerStopIsSilentOnACleanPublisherClose` (`1498-1506`):

```go
func TestManagerStopIsSilentOnACleanPublisherClose(t *testing.T) {
	log := &recordingLogger{}
	m := NewManager(ManagerOptions{URI: unreachableTestURI, Logger: log})
	fake := newFakeEnvironment()
	startOnFake(t, m, fake, onePublisher(t))

	m.StopConsumers()

	assert.Empty(t, log.warnMessages(), "a clean publisher close reports nothing to the operator")
}
```

`TestManagerReadyRequiresEveryPublisher` (`1510-1529`):

```go
func TestManagerReadyRequiresEveryPublisher(t *testing.T) {
	tests := []struct {
		name   string
		status int
		want   bool
	}{
		{name: "open_publisher_is_ready", status: ha.StatusOpen, want: true},
		{name: "reconnecting_publisher_is_not_ready", status: ha.StatusReconnecting, want: false},
		{name: "closed_publisher_is_not_ready", status: ha.StatusClosed, want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := testManager(t)
			fake := newFakeEnvironment()
			fake.useProducer(testStream, &fakeProducer{status: tt.status})
			startOnFake(t, m, fake, onePublisher(t))

			assert.Equal(t, tt.want, m.Ready())
		})
	}
}
```

`TestManagerStatsCountsPublishers` (`1531-1540`):

```go
func TestManagerStatsCountsPublishers(t *testing.T) {
	m := testManager(t)
	fake := newFakeEnvironment()
	startOnFake(t, m, fake, onePublisher(t))

	stats := m.Stats()

	assert.Equal(t, 1, stats["publishers"])
	assert.Equal(t, 0, stats["consumers"])
	assert.Equal(t, true, stats["ready"])
}
```

`TestManagerRebindRevivesAPublisherAfterAStopCycle` (`1547-1563`) — this one no longer *simulates* the Start → Close → Start cycle, it runs it:

```go
// TestManagerRebindRevivesAPublisherAfterAStopCycle covers the Start → Close →
// Start cycle Manager.Start allows: its guard is the environment, which Close
// nils, and consumers survive it because each Start rebuilds them. A publisher
// cannot be rebuilt — the module holds the same handle from declaration onwards —
// so the rebind has to reopen it or the second Start comes up publishing nothing.
func TestManagerRebindRevivesAPublisherAfterAStopCycle(t *testing.T) {
	m := testManager(t)
	decls := onePublisher(t)
	p := decls.publishers[0].Publisher

	firstEnv := newFakeEnvironment()
	first := openProducer()
	firstEnv.useProducer(testStream, first)
	startOnFake(t, m, firstEnv, decls)

	m.StopConsumers()
	require.NoError(t, m.Close())

	require.ErrorIs(t, p.Publish(context.Background(), &PublishMessage{Data: []byte(testBody)}), ErrPublisherClosed)
	assert.False(t, m.Ready(), "a stopped manager is not ready")

	secondEnv := newFakeEnvironment()
	second := openProducer()
	secondEnv.useProducer(testStream, second)
	startOnFake(t, m, secondEnv, decls)

	assert.True(t, m.Ready(), "the second start comes up ready")
	publishConfirmed(t, p, second, &PublishMessage{Data: []byte(testBody)})
	assert.Zero(t, first.sentCount(), "the revived publisher sends through the new producer, not the disposed one")
}
```

- [ ] **Step 7: Run the rewritten tests**

Run: `go test -race ./messaging/streams/ -run 'TestManager(Stop|Ready|Stats|Rebind)' -v`
Expected: every listed test reports `--- PASS`.

- [ ] **Step 8: Confirm the helpers are gone**

Run: `git grep -n -E '(attach|attachPartitioned|attachPublisher|rebindPublisher)\(' -- messaging/streams`
Expected: no output (exit status 1). `git grep -E` is used without PCRE escapes on purpose — `\b`, `\s`, `\d` and `\w` are silently ignored by it.

- [ ] **Step 9: Run the whole package**

Run: `go test -race ./messaging/streams/`
Expected: `ok  github.com/gaborage/go-bricks/messaging/streams`.

- [ ] **Step 10: Commit 3b**

```bash
cat > /tmp/streams-attach-publishers-msg.txt <<'EOF'
test(streams): bind publishers through Start, not attachPublisher

attachPublisher and rebindPublisher hand-built the binding Manager.Start
installs, which meant the shutdown-order, in-flight-publish, close-failure and
readiness tests never exercised bindPublisher at all. They now start a real
manager on the fake environment, which hands back the producer the test
prepared.

TestManagerRebindRevivesAPublisherAfterAStopCycle stops simulating the
Start-Close-Start cycle and runs it.
EOF
git add messaging/streams/manager_test.go
git commit -F /tmp/streams-attach-publishers-msg.txt
```

---

### Task 4: Container-test triage

**Files:**

- Modify: `messaging/streams/streams_integration_test.go` (one test)
- Modify: `messaging/streams/manager_test.go` (one comment that points at it)

Spec decision 6: a container assertion is deleted only when an in-process assertion replaced it. Verdict per test, all ten:

| # | Integration test (line) | Verdict | Why |
| --- | --- | --- | --- |
| 1 | `TestStreamsManagerConsumesAndRestoresOffsetIntegration` (`:183`) | **Keep** | Proves the BROKER stores and returns the offset across a restart. The fake's offset map is our own code; it cannot stand in for that. |
| 2 | `TestStreamsManagerSkipsFailedMessageIntegration` (`:227`) | **Keep** | Commit-only-after-success against a real broker, and the only proof that a skipped message is never redelivered. |
| 3 | `TestStreamsManagerConsumesSuperStreamPartitionsIntegration` (`:415`) | **Keep** | Per-partition commit and per-partition resume are broker-side; the in-process asymmetry test covers which object the flush targets, not what the broker does with it. |
| 4 | `TestStreamsManagerSuperStreamDistributesPartitionsIntegration` (`:468`) | **Keep** | Group distribution and hand-over are broker-side; nothing in-process replaces them. |
| 5 | `TestStreamsManagerSuperStreamPartitionMismatchIsSilentIntegration` (`:535`) | **Keep** | Pins a VENDOR behavior (the client swallows `StreamAlreadyExists` for super streams). The fake implements our port, not the client's quirk. |
| 6 | `TestStreamsManagerSingleActiveConsumerIntegration` (`:586`) | **Keep** | The in-process test proves the promotion callback re-resolves the position; only this one proves the broker fires it and attaches there. Its `-race` rationale for the environment snapshot is unchanged. |
| 7 | `TestStreamsManagerDisposesEnvironmentOnDeclareFailureIntegration` (`:628`) | **Shrink** | `TestManagerStartUnwindsAFailedDeclaration` now covers the unwind in process — env disposed, `started` false, follow-up `Close` a no-op — with a dialed environment. What remains broker-only is that a retention mismatch really is rejected. |
| 8 | `TestStreamsPublisherRoundTripIntegration` (`:662`) | **Keep** | The pointer-identity correlation the publisher is built on is a vendor contract. |
| 9 | `TestStreamsSuperStreamPublisherPartitionsIntegration` (`:842`) | **Keep** | murmur3 partition agreement with the broker. |
| 10 | `TestStreamsPublisherRejectedAfterStopIntegration` (`:882`) | **Keep** | End-to-end shutdown contract; cheap, and no in-process assertion covers publishing into a disposed environment. |

- [ ] **Step 1: Shrink the one test an in-process assertion replaced**

Replace `TestStreamsManagerDisposesEnvironmentOnDeclareFailureIntegration` (`streams_integration_test.go:623-651`) with:

```go
// TestStreamsManagerRejectsAConflictingRetentionIntegration is what only a broker
// can answer: re-declaring an existing stream with different retention really is
// answered with precondition-failed, so Start fails after the dial.
//
// The unwind that failure triggers — environment disposed, started false, the
// caller's follow-up Close a no-op — is asserted in process against the
// Environment port by TestManagerStartUnwindsAFailedDeclaration, so it is not
// repeated here.
func TestStreamsManagerRejectsAConflictingRetentionIntegration(t *testing.T) {
	ctx := context.Background()
	opts := streamsTestEnv(ctx, t)

	first := NewManager(opts)
	firstDecls := NewDeclarations()
	firstDecls.DeclareStream(itStream, &StreamSpec{MaxAge: time.Hour})
	require.NoError(t, first.Start(ctx, firstDecls))
	first.StopConsumers()
	require.NoError(t, first.Close())

	// Same stream, different retention: the broker rejects the declaration.
	second := NewManager(opts)
	conflicting := NewDeclarations()
	conflicting.DeclareStream(itStream, &StreamSpec{MaxAge: 48 * time.Hour})

	err := second.Start(ctx, conflicting)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to declare stream")
	require.NoError(t, second.Close())
}
```

- [ ] **Step 2: Repoint the comment that named it**

In `messaging/streams/manager_test.go`, `TestManagerStartFailedDialLeavesNothingToDispose`'s comment (`1078-1082`) claims the post-dial half needs a broker. That stopped being true. Replace the comment with:

```go
// TestManagerStartFailedDialLeavesNothingToDispose is the pre-dial half: the
// environment was never stored, so Close is a no-op. The post-dial half — where
// the environment exists and must be disposed — is
// TestManagerStartUnwindsAFailedDeclaration, in process against the Environment
// port.
```

- [ ] **Step 3: Verify the integration file still builds**

Run: `go vet -tags=integration ./messaging/streams/`
Expected: no output. (Do **not** run `make test-integration`; the controller decides whether a Docker run is warranted — Task 5.)

- [ ] **Step 4: Verify the unit suite is untouched by the edit**

Run: `go test -race ./messaging/streams/`
Expected: `ok  github.com/gaborage/go-bricks/messaging/streams`.

- [ ] **Step 5: Commit**

```bash
cat > /tmp/streams-container-triage-msg.txt <<'EOF'
test(streams): shrink the declare-failure container test to its broker half

The Environment port makes the post-dial unwind reachable in process, so
TestStreamsManagerDisposesEnvironmentOnDeclareFailureIntegration no longer has
to spend a container on asserting that the environment was disposed, that
started is false and that the follow-up Close short-circuits.

What is left is the half only a broker can answer -- a retention mismatch on an
existing stream really is precondition-failed -- renamed for what it now
asserts. The other nine container tests are unchanged: each still covers a
broker-side or vendor-side property no in-process assertion replaced.
EOF
git add messaging/streams/streams_integration_test.go messaging/streams/manager_test.go
git commit -F /tmp/streams-container-triage-msg.txt
```

---

### Task 5: Gates (controller only)

Implementers stop after Task 4. The controller runs everything below, in this order, and never delegates it.

- [ ] **Step 1: `make check`, backgrounded**

```bash
make check
```

Run with `run_in_background: true` (CLAUDE.md workflow rule). Expected: exits 0. Watch for:

- `gofmt`/`gci` import ordering in the new `environment.go` and `environment_fake_test.go` — `gofmt -l` is silent on gofumpt/gci rules, so after `make fmt` always check `git status --porcelain`.
- `gocritic`/`staticcheck` on the type assertion `storer, _ := handle.(offsetStorer)` — the blank is deliberate; the nil result is the documented `errNoOffsetStorer` path.

- [ ] **Step 2: `/simplify`**

Runs first because it mutates the diff. Likely targets: the nine one-line delegations in `vendorEnvironment` (they are irreducible — resist a reflective or generic "simplification"), and duplication between `oneConsumerDecls`/`superConsumerDecls` and the ad-hoc declaration builders in the `Start` suite. If it changes code, re-run `make check`.

- [ ] **Step 3: `/security-audit`**

Focus areas for this diff: `safeEnvError` and `redactStreamURI` still wrap every dial failure (the URI-credential path moved into `dialVendorEnvironment` — confirm the error still reaches `Start`'s `fmt.Errorf` with `safeEnvError` applied, and that `TestManagerStartDoesNotLeakURIOnParseFailure` still passes); the fake's `StoreOffset` parks outside its own mutex, so confirm no shutdown path can deadlock. If it changes code, re-run `make check`.

- [ ] **Step 4: `/code-review` (CodeRabbit)**

Must see the final diff. If findings are applied afterwards, re-run it. Expect it to ask for an ADR — answer that nothing exported changes (`environment`, `messageHandler`, `vendorEnvironment`, `dialEnvironment` are all unexported; `NewManager`/`ManagerOptions`/`Manager`'s method set are byte-identical) and no behavior changes (no commit path moves), so ADR-and-atom does not apply.

- [ ] **Step 5: `make mutate`, backgrounded, after committing**

```bash
make mutate
```

`run_in_background: true`. The scope is `merge-base..HEAD`, so **commit first** — uncommitted work yields `no mutatable changes` and a misleading exit 0. Proof it ran is a `(N mutants on changed lines)` line with N > 0. Surviving mutants on changed lines block the push. The lines most likely to survive:

- `vendorEnvironment`'s nil-on-error returns — nothing unit-tests the vendor adapter, since constructing one needs a broker. If they survive, note it rather than inventing a test that cannot attribute its failure to those lines.
- `startStreamConsumer`'s `storer, _ := handle.(offsetStorer)` — killed by the plain case of `TestManagerStartFlushesPlainThroughTheHandleAndSuperThroughThePort`.
- `vendorMessagesHandler`'s `store` argument — not reachable by unit tests either; the fake supplies its own. `TestStreamsManagerSkipsFailedMessageIntegration` is the container-side guard.

- [ ] **Step 6: Integration suite (optional, Docker required)**

```bash
make test-integration
```

Worth one run because Task 4 renamed a test. No commit path changed, so no behavioral surprise is expected.

- [ ] **Step 7: Push and open the PR**

Confirm the branch is not `main`, push, then open the PR with the three-heading body (`## What` / `## Impact` / `## Verification`, ≤3 sentences each, whole body under 150 words). `## Impact` is `None.` — no exported API change, no config key, no behavior a consumer configures. `## Verification` names only what CI cannot show: that `make mutate` ran clean on the changed lines and, if it was run, that the container suite passed against a real broker.

---

## Self-review

**Spec coverage (decisions 1–6).**

| Spec decision | Where |
| --- | --- |
| 1 — seam mirrors `amqp_adapters.go`; unexported interface + `dialEnvironment` field; tests swap the field | Task 1 Steps 3–4; `dialFake` in Step 1 |
| 2 — nine port methods, one per vendor call; constructors return the existing handles; factories fold in | Task 1 Step 3 (interface), Step 4 (`constructProducer`, factories deleted) |
| 3 — offset-storer asymmetry preserved; `storerFor` stays and is exercised in process | Task 1 Step 4 (both `storerFor` closures, `trackConsumer` untouched); Task 2 `TestManagerStartFlushesPlainThroughTheHandleAndSuperThroughThePort` |
| 4 — go-bricks-shaped handler; adapter does the `ConsumerContext` unwrapping | Task 1 Step 3 (`messageHandler`, `vendorMessagesHandler`), Step 5 (`messagesHandler` deleted from the runner). The handler carries the delivering consumer as `store`, so the in-flight commit path is unchanged. |
| 5 — fake records call order, injects errors, drives deliveries, stores offsets in memory | Task 1 Step 1 + Task 2 Step 1 |
| 6 — container tests shrink only where an in-process assertion replaced them | Task 4, all ten listed |

Spec items *not* covered, by design: decisions 7–13 (delivery pipeline, `messaging/internal/delivery`, `StartConsumeSpan` removal, ADR-068) — PR2/PR3.

**Placeholder scan.** No `TBD`, no "similar to Task N", no "add error handling". Every code step carries the code; every run step carries the command and the expected output. The one judgement call left open is Task 5 Step 5's mutant triage, which is explicitly a controller decision with named candidates.

**Type consistency.**

- `messageHandler` is `func(streamName string, offset int64, msg *amqp.Message, store offsetStorer)` in the interface (Task 1 Step 3), in the fake's field and both constructors (Step 1), and matches `consumerRunner.deliver`'s **unchanged** signature (`runner.go:239`) so `runner.deliver` is assignable as a method value in `startStreamConsumer` and `startSuperStreamConsumer` (Step 4).
- `vendorMessagesHandler` passes `consumerContext.Consumer` (a `*stream.Consumer`, which has `StoreCustomOffset(int64) error`, `pkg/stream/consumer.go:521`) as `store`; `fakeConsumer.deliver` passes `deliveryStorer`, which has the same method. Both satisfy `offsetStorer`.
- `consumerHandle` stays `{Close() error; GetStatus() int}` everywhere; `*fakeHandle` satisfies it plus `offsetStorer`, `*fakeSuperHandle` satisfies only it — mirroring `*ha.ReliableConsumer` vs `*ha.ReliableSuperStreamConsumer`.
- `environment.Close() error` is what `closeEnvLocked` calls and what `fakeEnvironment.Close` records as `callClose`.
- `storerFor func(streamName string) offsetStorer` keeps the same signature on `runningConsumer`, on `trackConsumer`'s parameter, and as `offsetBook.flush`'s argument — none of them modified.
- Fake method names used by tests: `recorded`, `consumer`, `consumerOptions`, `producer`, `superProducerOptions`, `failOn`, `useProducer`, `setOffset`, `storedOffset`, `blockStoreOn`; consumer-level: `deliver`, `promote`, `events`. Every call site in Tasks 1–4 uses exactly these.
- `runner_test.go` is untouched by every task, and `deliverWith` / `bindStorers` / `runner.storerFor` appear nowhere in this plan.
