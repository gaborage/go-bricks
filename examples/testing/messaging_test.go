package testing_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"
	"github.com/stretchr/testify/assert"

	"github.com/gaborage/go-bricks/messaging"
	"github.com/gaborage/go-bricks/testing/fixtures"
	"github.com/gaborage/go-bricks/testing/mocks"
)

// Example event types for demonstration
type UserCreatedEvent struct {
	UserID int64  `json:"user_id"`
	Name   string `json:"name"`
	Email  string `json:"email"`
}

type UserUpdatedEvent struct {
	UserID int64  `json:"user_id"`
	Name   string `json:"name"`
	Email  string `json:"email"`
}

// Example EventService for demonstration
type EventService struct {
	client messaging.Client
}

func NewEventService(client messaging.Client) *EventService {
	return &EventService{client: client}
}

func (s *EventService) PublishUserCreated(ctx context.Context, userID int64, name, email string) error {
	event := UserCreatedEvent{
		UserID: userID,
		Name:   name,
		Email:  email,
	}

	data, err := json.Marshal(event)
	if err != nil {
		return err
	}

	return s.client.Publish(ctx, "user.events", data)
}

func (s *EventService) StartConsuming(ctx context.Context, queue string) (<-chan amqp.Delivery, error) {
	return s.client.Consume(ctx, queue)
}

func (s *EventService) IsReady() bool {
	return s.client.IsReady()
}

// Example MessageHandler for demonstration
type UserMessageHandler struct {
	processedEvents []string
}

func NewUserMessageHandler() *UserMessageHandler {
	return &UserMessageHandler{
		processedEvents: make([]string, 0),
	}
}

func (h *UserMessageHandler) Handle(_ context.Context, delivery *amqp.Delivery) error {
	// Simple handler that just tracks processed events
	var event map[string]interface{}
	if err := json.Unmarshal(delivery.Body, &event); err != nil {
		return err
	}

	eventType, ok := event["event_type"].(string)
	if !ok {
		eventType = "unknown"
	}

	h.processedEvents = append(h.processedEvents, eventType)
	return nil
}

func (h *UserMessageHandler) GetProcessedEvents() []string {
	return h.processedEvents
}

// Test Examples

// TestEventService_PublishUserCreated_Success demonstrates basic messaging mock usage
func TestEventService_PublishUserCreated_Success(t *testing.T) {
	mockClient := &mocks.MockMessagingClient{}

	// Set up expectations
	expectedData, _ := json.Marshal(UserCreatedEvent{
		UserID: 1,
		Name:   "John Doe",
		Email:  "john@example.com",
	})

	mockClient.ExpectPublish("user.events", expectedData, nil)
	mockClient.ExpectIsReady(true)

	// Test the service
	service := NewEventService(mockClient)
	err := service.PublishUserCreated(context.Background(), 1, "John Doe", "john@example.com")

	assert.NoError(t, err)
	mockClient.AssertExpectations(t)
}

// TestEventService_PublishUserCreated_Failure demonstrates failure scenario testing
func TestEventService_PublishUserCreated_Failure(t *testing.T) {
	mockClient := &mocks.MockMessagingClient{}

	// Set up failure expectation
	publishErr := amqp.ErrClosed
	mockClient.ExpectPublishAny(publishErr)

	service := NewEventService(mockClient)
	err := service.PublishUserCreated(context.Background(), 1, "John Doe", "john@example.com")

	assert.Error(t, err)
	assert.Equal(t, publishErr, err)
	mockClient.AssertExpectations(t)
}

// TestEventService_ConsumeMessages demonstrates message simulation
func TestEventService_ConsumeMessages(t *testing.T) {
	// Create a mock client with pre-loaded messages
	messages := [][]byte{
		[]byte(`{"event_type": "user.created", "user_id": 1}`),
		[]byte(`{"event_type": "user.updated", "user_id": 1}`),
	}
	mockClient := fixtures.NewMessageSimulator(messages...)

	service := NewEventService(mockClient)

	// Start consuming
	deliveries, err := service.StartConsuming(context.Background(), "test.queue")
	assert.NoError(t, err)

	// Collect messages with timeout
	var receivedMessages [][]byte
	timeout := time.After(100 * time.Millisecond)

messageLoop:
	for {
		select {
		case delivery := <-deliveries:
			receivedMessages = append(receivedMessages, delivery.Body)
			if len(receivedMessages) >= 2 {
				break messageLoop
			}
		case <-timeout:
			break messageLoop
		}
	}

	assert.Len(t, receivedMessages, 2)
	assert.Contains(t, string(receivedMessages[0]), "user.created")
	assert.Contains(t, string(receivedMessages[1]), "user.updated")
}

// TestEventService_MessageSimulation demonstrates real-time message simulation
func TestEventService_MessageSimulation(t *testing.T) {
	mockClient := fixtures.NewWorkingMessagingClient()

	service := NewEventService(mockClient)

	// Start consuming
	deliveries, err := service.StartConsuming(context.Background(), "user.events")
	assert.NoError(t, err)

	// Simulate messages after consumption starts
	go func() {
		time.Sleep(10 * time.Millisecond)
		mockClient.SimulateMessage("user.events", []byte(`{"event_type": "user.created", "user_id": 1}`))
		mockClient.SimulateMessage("user.events", []byte(`{"event_type": "user.deleted", "user_id": 2}`))
	}()

	// Collect messages
	var receivedMessages [][]byte
	timeout := time.After(100 * time.Millisecond)

	for len(receivedMessages) < 2 {
		select {
		case delivery := <-deliveries:
			receivedMessages = append(receivedMessages, delivery.Body)
		case <-timeout:
			t.Fatal("Timeout waiting for messages")
		}
	}

	assert.Len(t, receivedMessages, 2)
	assert.Contains(t, string(receivedMessages[0]), "user.created")
	assert.Contains(t, string(receivedMessages[1]), "user.deleted")
}

// TestUserMessageHandler_Handle demonstrates message handler testing
func TestUserMessageHandler_Handle(t *testing.T) {
	handler := NewUserMessageHandler()

	// Create mock deliveries using fixtures
	deliveries := []amqp.Delivery{
		fixtures.NewJSONDelivery([]byte(`{"event_type": "user.created", "user_id": 1}`)),
		fixtures.NewJSONDelivery([]byte(`{"event_type": "user.updated", "user_id": 1}`)),
		fixtures.NewJSONDelivery([]byte(`{"event_type": "user.deleted", "user_id": 1}`)),
	}

	// Process each delivery
	for _, delivery := range deliveries {
		err := handler.Handle(context.Background(), &delivery)
		assert.NoError(t, err)
	}

	// Verify processing
	processedEvents := handler.GetProcessedEvents()
	assert.Len(t, processedEvents, 3)
	assert.Equal(t, "user.created", processedEvents[0])
	assert.Equal(t, "user.updated", processedEvents[1])
	assert.Equal(t, "user.deleted", processedEvents[2])
}

// TestUserMessageHandler_HandleInvalidJSON demonstrates error handling
func TestUserMessageHandler_HandleInvalidJSON(t *testing.T) {
	handler := NewUserMessageHandler()

	// Create delivery with invalid JSON
	delivery := fixtures.NewJSONDelivery([]byte(`{invalid json}`))

	err := handler.Handle(context.Background(), &delivery)
	assert.Error(t, err)

	// No events should be processed
	processedEvents := handler.GetProcessedEvents()
	assert.Empty(t, processedEvents)
}

// TestEventService_ConnectionFailures demonstrates connection failure testing
func TestEventService_ConnectionFailures(t *testing.T) {
	t.Run("client_not_ready", func(t *testing.T) {
		mockClient := fixtures.NewDisconnectedMessagingClient()

		service := NewEventService(mockClient)
		ready := service.IsReady()

		assert.False(t, ready)
	})

	t.Run("immediate_failure", func(t *testing.T) {
		mockClient := fixtures.NewFailingMessagingClient(0) // Fail immediately

		service := NewEventService(mockClient)
		err := service.PublishUserCreated(context.Background(), 1, "John", "john@example.com")

		assert.Error(t, err)
	})

	t.Run("failure_after_success", func(t *testing.T) {
		mockClient := fixtures.NewFailingMessagingClient(2) // Succeed twice, then fail

		service := NewEventService(mockClient)

		// First two calls should succeed
		err1 := service.PublishUserCreated(context.Background(), 1, "John", "john@example.com")
		err2 := service.PublishUserCreated(context.Background(), 2, "Jane", "jane@example.com")

		assert.NoError(t, err1)
		assert.NoError(t, err2)

		// Third call should fail
		err3 := service.PublishUserCreated(context.Background(), 3, "Bob", "bob@example.com")
		assert.Error(t, err3)
	})
}

// TestAMQPClient_Infrastructure demonstrates AMQP infrastructure testing
func TestAMQPClient_Infrastructure(t *testing.T) {
	mockAMQP := fixtures.NewWorkingAMQPClient()

	// Test exchange declaration
	err := mockAMQP.DeclareExchange("user.events", "topic", true, false, false, false)
	assert.NoError(t, err)

	// Test queue declaration
	err = mockAMQP.DeclareQueue("user.notifications", true, false, false, false)
	assert.NoError(t, err)

	// Test binding
	err = mockAMQP.BindQueue("user.notifications", "user.events", "user.*")
	assert.NoError(t, err)

	// Verify infrastructure state
	assert.True(t, mockAMQP.IsExchangeDeclared("user.events"))
	assert.True(t, mockAMQP.IsQueueDeclared("user.notifications"))
	assert.True(t, mockAMQP.IsQueueBound("user.notifications", "user.events", "user.*"))
}

// TestAMQPClient_PublishToExchange demonstrates exchange publishing
func TestAMQPClient_PublishToExchange(t *testing.T) {
	mockAMQP := mocks.NewMockAMQPClient()

	// Set up expectation for publishing to exchange
	options := messaging.PublishOptions{
		Exchange:   "user.events",
		RoutingKey: "user.created",
		Headers:    map[string]any{"content-type": "application/json"},
	}
	data := []byte(`{"user_id": 1, "name": "John"}`)

	mockAMQP.ExpectPublishToExchange(options, data, nil)

	// Test the publish
	err := mockAMQP.PublishToExchange(context.Background(), options, data)
	assert.NoError(t, err)

	mockAMQP.AssertExpectations(t)
}

// TestRegistry_Infrastructure demonstrates registry testing
func TestRegistry_Infrastructure(t *testing.T) {
	mockRegistry := fixtures.NewWorkingRegistry()

	// Register infrastructure
	exchange := fixtures.NewExchangeDeclaration("user.events", "topic")
	queue := fixtures.NewQueueDeclaration("user.notifications")
	binding := fixtures.NewBindingDeclaration("user.notifications", "user.events", "user.*")

	mockRegistry.RegisterExchange(exchange)
	mockRegistry.RegisterQueue(queue)
	mockRegistry.RegisterBinding(binding)

	// Test infrastructure declaration
	err := mockRegistry.DeclareInfrastructure(context.Background())
	assert.NoError(t, err)

	// Test consumer lifecycle
	err = mockRegistry.StartConsumers(context.Background())
	assert.NoError(t, err)

	mockRegistry.StopConsumers()

	// Verify registrations
	exchanges := mockRegistry.GetExchanges()
	assert.Contains(t, exchanges, "user.events")

	queues := mockRegistry.GetQueues()
	assert.Contains(t, queues, "user.notifications")

	bindings := mockRegistry.GetBindings()
	assert.Len(t, bindings, 1)
	assert.Equal(t, "user.*", bindings[0].RoutingKey)

	mockRegistry.AssertExpectations(t)
}
