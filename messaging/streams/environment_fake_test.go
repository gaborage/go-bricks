package streams

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"

	"github.com/rabbitmq/rabbitmq-stream-go-client/pkg/amqp"
	"github.com/rabbitmq/rabbitmq-stream-go-client/pkg/ha"
	"github.com/rabbitmq/rabbitmq-stream-go-client/pkg/stream"
	"github.com/stretchr/testify/require"
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

// producerCall captures what the plain producer constructor received for one
// stream: the options a test asserts stream.NewProducerOptions() reached the
// port with, and the confirmation handler the publisher bound to correlate its
// own sends. The super lane captures the same two things in superProdOpts and
// superProdConfirmed instead, because it already had the options half before
// this struct existed.
type producerCall struct {
	opts      *stream.ProducerOptions
	confirmed ha.ConfirmMessageHandler
}

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
	producerCalls map[string]*producerCall
	superProdOpts map[string]*stream.SuperStreamProducerOptions
	// superProdConfirmed is superProdOpts' sibling for the confirmation handler:
	// kept as its own map, keyed the same way, rather than folded into a struct,
	// so superProdOpts and its existing accessor stay untouched.
	superProdConfirmed map[string]ha.PartitionConfirmMessageHandler
	preparedProds      map[string]*fakeProducer
}

var _ environment = (*fakeEnvironment)(nil)

func newFakeEnvironment() *fakeEnvironment {
	return &fakeEnvironment{
		errs:               map[string]error{},
		offsets:            map[string]int64{},
		consumers:          map[string]*fakeConsumer{},
		consumerOpts:       map[string]*stream.ConsumerOptions{},
		producers:          map[string]*fakeProducer{},
		producerCalls:      map[string]*producerCall{},
		superProdOpts:      map[string]*stream.SuperStreamProducerOptions{},
		superProdConfirmed: map[string]ha.PartitionConfirmMessageHandler{},
		preparedProds:      map[string]*fakeProducer{},
	}
}

// failOn makes one port call fail. key is a method name or "<method>:<target>".
func (f *fakeEnvironment) failOn(key string, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.errs[key] = err
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

// producerCall reads back what NewProducer received for one stream, or nil if
// the plain constructor was never reached for it.
func (f *fakeEnvironment) producerCall(streamName string) *producerCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.producerCalls[streamName]
}

// superProducerConfirmed is superProducerOptions' sibling for the confirmation
// handler NewSuperStreamProducer received for one super stream.
func (f *fakeEnvironment) superProducerConfirmed(superStream string) ha.PartitionConfirmMessageHandler {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.superProdConfirmed[superStream]
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

func (f *fakeEnvironment) NewProducer(streamName string, opts *stream.ProducerOptions,
	confirmed ha.ConfirmMessageHandler,
) (producerHandle, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.recordLocked(callNewProducer + ":" + streamName)
	if err := f.errForLocked(callNewProducer, streamName); err != nil {
		return nil, err
	}
	f.producerCalls[streamName] = &producerCall{opts: opts, confirmed: confirmed}
	return f.newProducerLocked(streamName), nil
}

func (f *fakeEnvironment) NewSuperStreamProducer(superStream string, opts *stream.SuperStreamProducerOptions,
	confirmed ha.PartitionConfirmMessageHandler,
) (producerHandle, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.recordLocked(callNewSuperProducer + ":" + superStream)
	if err := f.errForLocked(callNewSuperProducer, superStream); err != nil {
		return nil, err
	}
	f.superProdOpts[superStream] = opts
	f.superProdConfirmed[superStream] = confirmed
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
