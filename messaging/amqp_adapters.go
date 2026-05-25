package messaging

import (
	"context"
	"sync"

	amqp "github.com/rabbitmq/amqp091-go"
)

// Internal interfaces and adapters to enable testing without a real broker
type amqpConnection interface {
	Channel() (*amqp.Channel, error)
	NotifyClose(c chan *amqp.Error) chan *amqp.Error
	Close() error
}

type amqpChannel interface {
	Confirm(noWait bool) error
	Qos(prefetchCount, prefetchSize int, global bool) error
	PublishWithContext(ctx context.Context, exchange, key string, mandatory, immediate bool, msg amqp.Publishing) error
	Consume(queue, consumer string, autoAck, exclusive, noLocal, noWait bool, args amqp.Table) (<-chan amqp.Delivery, error)
	QueueDeclare(name string, durable, autoDelete, exclusive, noWait bool, args amqp.Table) (amqp.Queue, error)
	ExchangeDeclare(name, kind string, durable, autoDelete, internal, noWait bool, args amqp.Table) error
	QueueBind(name, key, exchange string, noWait bool, args amqp.Table) error
	NotifyClose(c chan *amqp.Error) chan *amqp.Error
	NotifyPublish(confirm chan amqp.Confirmation) chan amqp.Confirmation
	// GetNextPublishSeqNo returns the DeliveryTag that the broker will assign
	// to the next call to PublishWithContext on this channel. Capturing this
	// BEFORE publish lets us correlate the confirmation that comes back later
	// to the specific publish that issued it, instead of consuming the first
	// confirmation off a shared channel (which cross-contaminates ACKs when
	// multiple publishes are in flight concurrently).
	GetNextPublishSeqNo() uint64
	Close() error
}

// Adapter to real amqp connection
type realConnection struct{ c *amqp.Connection }

func (r realConnection) Channel() (*amqp.Channel, error)                 { return r.c.Channel() }
func (r realConnection) NotifyClose(c chan *amqp.Error) chan *amqp.Error { return r.c.NotifyClose(c) }
func (r realConnection) Close() error                                    { return r.c.Close() }

// Pluggable dialer for tests
var (
	amqpDialMu   sync.RWMutex
	amqpDialFunc = func(url string) (amqpConnection, error) {
		conn, err := amqp.Dial(url)
		if err != nil {
			return nil, err
		}
		return realConnection{c: conn}, nil
	}
)

func setAmqpDialFunc(f func(string) (amqpConnection, error)) {
	amqpDialMu.Lock()
	amqpDialFunc = f
	amqpDialMu.Unlock()
}

func getAmqpDialFunc() func(string) (amqpConnection, error) {
	amqpDialMu.RLock()
	f := amqpDialFunc
	amqpDialMu.RUnlock()
	return f
}
