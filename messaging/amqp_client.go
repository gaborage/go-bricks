package messaging

import (
	"context"
	"errors"
	"sync"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"

	"github.com/gaborage/go-bricks/logger"
)

// AMQPClientImpl provides an AMQP implementation of the messaging client interface.
// It includes automatic reconnection, retry logic, and AMQP-specific features.
type AMQPClientImpl struct {
	m               *sync.RWMutex
	brokerURL       string
	log             logger.Logger
	connection      *amqp.Connection
	channel         *amqp.Channel
	done            chan bool
	notifyConnClose chan *amqp.Error
	notifyChanClose chan *amqp.Error
	notifyConfirm   chan amqp.Confirmation
	isReady         bool

	// Configuration
	reconnectDelay    time.Duration
	reInitDelay       time.Duration
	resendDelay       time.Duration
	connectionTimeout time.Duration
}

// Reconnection delays
const (
	defaultReconnectDelay    = 5 * time.Second
	defaultReInitDelay       = 2 * time.Second
	defaultResendDelay       = 5 * time.Second
	defaultConnectionTimeout = 30 * time.Second
)

var (
	errNotConnected  = errors.New("not connected to AMQP broker")
	errAlreadyClosed = errors.New("AMQP client already closed")
	errShutdown      = errors.New("AMQP client is shutting down")
)

// NewAMQPClient creates a new AMQP client instance.
// It automatically attempts to connect to the broker and handles reconnections.
func NewAMQPClient(brokerURL string, log logger.Logger) *AMQPClientImpl {
	client := &AMQPClientImpl{
		m:                 &sync.RWMutex{},
		brokerURL:         brokerURL,
		log:               log,
		done:              make(chan bool),
		reconnectDelay:    defaultReconnectDelay,
		reInitDelay:       defaultReInitDelay,
		resendDelay:       defaultResendDelay,
		connectionTimeout: defaultConnectionTimeout,
	}

	// Start connection management in background
	go client.handleReconnect()
	return client
}

// IsReady returns true if the client is connected and ready to send/receive messages.
func (c *AMQPClientImpl) IsReady() bool {
	c.m.RLock()
	defer c.m.RUnlock()
	return c.isReady
}

// Publish sends a message to the specified destination (queue name).
// Uses default exchange ("") and destination as routing key.
func (c *AMQPClientImpl) Publish(ctx context.Context, destination string, data []byte) error {
	return c.PublishToExchange(ctx, PublishOptions{
		Exchange:   "",
		RoutingKey: destination,
	}, data)
}

// PublishToExchange publishes a message to a specific exchange with routing key.
func (c *AMQPClientImpl) PublishToExchange(ctx context.Context, options PublishOptions, data []byte) error {
	startTime := time.Now()

	c.m.RLock()
	if !c.isReady {
		c.m.RUnlock()
		c.log.Warn().
			Str("exchange", options.Exchange).
			Str("routing_key", options.RoutingKey).
			Msg("AMQP client not ready, message not published")
		return nil // Return nil to avoid failing the business operation
	}
	c.m.RUnlock()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-c.done:
			return errShutdown
		default:
		}

		err := c.unsafePublish(ctx, options, data)
		if err != nil {
			c.log.Warn().Err(err).Msg("Publish failed, retrying...")
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-c.done:
				return errShutdown
			case <-time.After(c.resendDelay):
			}
			continue
		}

		// Wait for confirmation
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-c.done:
			return errShutdown
		case confirm := <-c.notifyConfirm:
			if confirm.Ack {
				// Track elapsed time and increment AMQP counter in context for request tracking
				elapsed := time.Since(startTime)
				logger.IncrementAMQPCounter(ctx)
				logger.AddAMQPElapsed(ctx, elapsed.Nanoseconds())
				c.log.Debug().
					Str("exchange", options.Exchange).
					Str("routing_key", options.RoutingKey).
					Uint64("delivery_tag", confirm.DeliveryTag).
					Msg("Message published successfully")
				return nil
			}
			c.log.Warn().
				Uint64("delivery_tag", confirm.DeliveryTag).
				Msg("Message publish not acknowledged")
		case <-time.After(c.connectionTimeout):
			c.log.Warn().Msg("Publish confirmation timeout")
		}
	}
}

// Consume starts consuming messages from the specified destination (queue name).
func (c *AMQPClientImpl) Consume(ctx context.Context, destination string) (<-chan amqp.Delivery, error) {
	return c.ConsumeFromQueue(ctx, ConsumeOptions{
		Queue:     destination,
		Consumer:  "",
		AutoAck:   false,
		Exclusive: false,
		NoLocal:   false,
		NoWait:    false,
	})
}

// ConsumeFromQueue consumes messages from a queue with specific options.
func (c *AMQPClientImpl) ConsumeFromQueue(_ context.Context, options ConsumeOptions) (<-chan amqp.Delivery, error) {
	c.m.RLock()
	if !c.isReady {
		c.m.RUnlock()
		return nil, errNotConnected
	}
	channel := c.channel
	c.m.RUnlock()

	// Set QoS for fair dispatch
	if err := channel.Qos(1, 0, false); err != nil {
		return nil, err
	}

	return channel.Consume(
		options.Queue,
		options.Consumer,
		options.AutoAck,
		options.Exclusive,
		options.NoLocal,
		options.NoWait,
		nil, // args
	)
}

// DeclareQueue declares a queue with the given parameters.
func (c *AMQPClientImpl) DeclareQueue(name string, durable, autoDelete, exclusive, noWait bool) error {
	c.m.RLock()
	if !c.isReady {
		c.m.RUnlock()
		return errNotConnected
	}
	channel := c.channel
	c.m.RUnlock()

	_, err := channel.QueueDeclare(name, durable, autoDelete, exclusive, noWait, nil)
	return err
}

// DeclareExchange declares an exchange with the given parameters.
func (c *AMQPClientImpl) DeclareExchange(name, kind string, durable, autoDelete, internal, noWait bool) error {
	c.m.RLock()
	if !c.isReady {
		c.m.RUnlock()
		return errNotConnected
	}
	channel := c.channel
	c.m.RUnlock()

	return channel.ExchangeDeclare(name, kind, durable, autoDelete, internal, noWait, nil)
}

// BindQueue binds a queue to an exchange with a routing key.
func (c *AMQPClientImpl) BindQueue(queue, exchange, routingKey string, noWait bool) error {
	c.m.RLock()
	if !c.isReady {
		c.m.RUnlock()
		return errNotConnected
	}
	channel := c.channel
	c.m.RUnlock()

	return channel.QueueBind(queue, routingKey, exchange, noWait, nil)
}

// Close gracefully shuts down the AMQP client.
func (c *AMQPClientImpl) Close() error {
	c.m.Lock()
	defer c.m.Unlock()

	if !c.isReady {
		return errAlreadyClosed
	}

	close(c.done)
	c.isReady = false

	var err error
	if c.channel != nil {
		if closeErr := c.channel.Close(); closeErr != nil {
			err = closeErr
		}
	}
	if c.connection != nil {
		if closeErr := c.connection.Close(); closeErr != nil && err == nil {
			err = closeErr
		}
	}

	c.log.Info().Msg("AMQP client closed")
	return err
}

// handleReconnect manages connection lifecycle and reconnection logic.
func (c *AMQPClientImpl) handleReconnect() {
	for {
		c.m.Lock()
		c.isReady = false
		c.m.Unlock()

		c.log.Info().Str("broker_url", c.brokerURL).Msg("Attempting to connect to AMQP broker")

		conn, err := c.connect()
		if err != nil {
			c.log.Error().Err(err).Msg("Failed to connect to AMQP broker, retrying...")

			select {
			case <-c.done:
				return
			case <-time.After(c.reconnectDelay):
			}
			continue
		}

		if done := c.handleReInit(conn); done {
			break
		}
	}
}

// connect creates a new AMQP connection.
func (c *AMQPClientImpl) connect() (*amqp.Connection, error) {
	conn, err := amqp.Dial(c.brokerURL)
	if err != nil {
		return nil, err
	}

	c.changeConnection(conn)
	c.log.Info().Msg("Connected to AMQP broker")
	return conn, nil
}

// handleReInit manages channel initialization and reinitialization.
func (c *AMQPClientImpl) handleReInit(conn *amqp.Connection) bool {
	for {
		c.m.Lock()
		c.isReady = false
		c.m.Unlock()

		err := c.init(conn)
		if err != nil {
			c.log.Error().Err(err).Msg("Failed to initialize AMQP channel, retrying...")

			select {
			case <-c.done:
				return true
			case <-c.notifyConnClose:
				c.log.Info().Msg("AMQP connection closed, reconnecting...")
				return false
			case <-time.After(c.reInitDelay):
			}
			continue
		}

		select {
		case <-c.done:
			return true
		case <-c.notifyConnClose:
			c.log.Info().Msg("AMQP connection closed, reconnecting...")
			return false
		case <-c.notifyChanClose:
			c.log.Info().Msg("AMQP channel closed, reinitializing...")
		}
	}
}

// init initializes the AMQP channel and sets up confirmation mode.
func (c *AMQPClientImpl) init(conn *amqp.Connection) error {
	ch, err := conn.Channel()
	if err != nil {
		return err
	}

	if err := ch.Confirm(false); err != nil {
		err := ch.Close()
		if err != nil {
			return err
		}
		return err
	}

	c.changeChannel(ch)
	c.m.Lock()
	c.isReady = true
	c.m.Unlock()

	c.log.Info().Msg("AMQP client initialized and ready")
	return nil
}

// changeConnection updates the connection and sets up close notifications.
func (c *AMQPClientImpl) changeConnection(connection *amqp.Connection) {
	c.connection = connection
	c.notifyConnClose = make(chan *amqp.Error, 1)
	c.connection.NotifyClose(c.notifyConnClose)
}

// changeChannel updates the channel and sets up notifications.
func (c *AMQPClientImpl) changeChannel(channel *amqp.Channel) {
	c.channel = channel
	c.notifyChanClose = make(chan *amqp.Error, 1)
	c.notifyConfirm = make(chan amqp.Confirmation, 1)
	c.channel.NotifyClose(c.notifyChanClose)
	c.channel.NotifyPublish(c.notifyConfirm)
}

// unsafePublish publishes a message without confirmation handling.
func (c *AMQPClientImpl) unsafePublish(ctx context.Context, options PublishOptions, data []byte) error {
	c.m.RLock()
	if !c.isReady {
		c.m.RUnlock()
		return errNotConnected
	}
	channel := c.channel
	c.m.RUnlock()

	publishing := amqp.Publishing{
		ContentType: "application/octet-stream",
		Body:        data,
	}

	if options.Headers != nil {
		publishing.Headers = options.Headers
	}

	return channel.PublishWithContext(
		ctx,
		options.Exchange,
		options.RoutingKey,
		options.Mandatory,
		options.Immediate,
		publishing,
	)
}
