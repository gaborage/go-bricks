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
