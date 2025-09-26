package testing_test

import (
	"context"
	"encoding/json"
	"fmt"
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

type noopMessageHandler struct {
	event string
}

func (h noopMessageHandler) Handle(context.Context, *amqp.Delivery) error { return nil }

func (h noopMessageHandler) EventType() string { return h.event }

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

	return s.client.Publish(ctx, userEventsExchangeName, data)
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
	var event map[string]any
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

// TestEventServicePublishUserCreatedSuccess demonstrates basic messaging mock usage
func TestEventServicePublishUserCreatedSuccess(t *testing.T) {
	mockClient := &mocks.MockMessagingClient{}

	// Set up expectations
	expectedData, _ := json.Marshal(UserCreatedEvent{
		UserID: 1,
		Name:   testUser,
		Email:  testEmail,
	})

	mockClient.ExpectPublish(userEventsExchangeName, expectedData, nil)

	// Test the service
	service := NewEventService(mockClient)
	err := service.PublishUserCreated(context.Background(), 1, testUser, testEmail)

	assert.NoError(t, err)
	mockClient.AssertExpectations(t)
}

// TestEventServicePublishUserCreatedFailure demonstrates failure scenario testing
func TestEventServicePublishUserCreatedFailure(t *testing.T) {
	mockClient := &mocks.MockMessagingClient{}

	// Set up failure expectation
	publishErr := amqp.ErrClosed
	mockClient.ExpectPublishAny(publishErr)

	service := NewEventService(mockClient)
	err := service.PublishUserCreated(context.Background(), 1, testUser, testEmail)

	assert.Error(t, err)
	assert.Equal(t, publishErr, err)
	mockClient.AssertExpectations(t)
}

// TestEventServiceConsumeMessages demonstrates message simulation
func TestEventServiceConsumeMessages(t *testing.T) {
	// Create a mock client with pre-loaded messages
	messages := [][]byte{
		[]byte(fmt.Sprintf(`{"event_type": %q, "user_id": 1}`, userCreated)),
		[]byte(fmt.Sprintf(`{"event_type": %q, "user_id": 1}`, userUpdated)),
	}
	mockClient := fixtures.NewWorkingMessagingClient()
	service := NewEventService(mockClient)

	// Start consuming
	deliveries, err := service.StartConsuming(context.Background(), "test.queue")
	assert.NoError(t, err)
	for _, msg := range messages {
		mockClient.SimulateMessage("test.queue", msg)
	}

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
	assert.Contains(t, string(receivedMessages[0]), userCreated)
	assert.Contains(t, string(receivedMessages[1]), userUpdated)
}

// TestEventServiceMessageSimulation demonstrates real-time message simulation
func TestEventServiceMessageSimulation(t *testing.T) {
	mockClient := fixtures.NewWorkingMessagingClient()

	service := NewEventService(mockClient)

	// Start consuming
	deliveries, err := service.StartConsuming(context.Background(), userEventsExchangeName)
	assert.NoError(t, err)

	// Simulate messages after consumption starts
	go func() {
		time.Sleep(10 * time.Millisecond)
		mockClient.SimulateMessage(userEventsExchangeName, []byte(`{"event_type": "user.created", "user_id": 1}`))
		mockClient.SimulateMessage(userEventsExchangeName, []byte(`{"event_type": "user.deleted", "user_id": 2}`))
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
	assert.Contains(t, string(receivedMessages[0]), userCreated)
	assert.Contains(t, string(receivedMessages[1]), userDeleted)
}

// TestUserMessageHandlerHandle demonstrates message handler testing
func TestUserMessageHandlerHandle(t *testing.T) {
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
	assert.Equal(t, userCreated, processedEvents[0])
	assert.Equal(t, userUpdated, processedEvents[1])
	assert.Equal(t, userDeleted, processedEvents[2])
}

// TestUserMessageHandlerHandleInvalidJSON demonstrates error handling
func TestUserMessageHandlerHandleInvalidJSON(t *testing.T) {
	handler := NewUserMessageHandler()

	// Create delivery with invalid JSON
	delivery := fixtures.NewJSONDelivery([]byte(`{invalid json}`))

	err := handler.Handle(context.Background(), &delivery)
	assert.Error(t, err)

	// No events should be processed
	processedEvents := handler.GetProcessedEvents()
	assert.Empty(t, processedEvents)
}

// TestEventServiceConnectionFailures demonstrates connection failure testing
func TestEventServiceConnectionFailures(t *testing.T) {
	t.Run("client_not_ready", func(t *testing.T) {
		mockClient := fixtures.NewDisconnectedMessagingClient()

		service := NewEventService(mockClient)
		ready := service.IsReady()

		assert.False(t, ready)
	})

	t.Run("immediate_failure", func(t *testing.T) {
		mockClient := fixtures.NewFailingMessagingClient(0) // Fail immediately

		service := NewEventService(mockClient)
		err := service.PublishUserCreated(context.Background(), 1, "John", testEmail)

		assert.Error(t, err)
	})

	t.Run("failure_after_success", func(t *testing.T) {
		mockClient := fixtures.NewFailingMessagingClient(2) // Succeed twice, then fail

		service := NewEventService(mockClient)

		// First two calls should succeed
		err1 := service.PublishUserCreated(context.Background(), 1, "John", testEmail)
		err2 := service.PublishUserCreated(context.Background(), 2, "Jane", "jane@example.com")

		assert.NoError(t, err1)
		assert.NoError(t, err2)

		// Third call should fail
		err3 := service.PublishUserCreated(context.Background(), 3, "Bob", "bob@example.com")
		assert.Error(t, err3)
	})
}

// TestAMQPClientInfrastructure demonstrates AMQP infrastructure testing
func TestAMQPClientInfrastructure(t *testing.T) {
	mockAMQP := fixtures.NewWorkingAMQPClient()

	// Test exchange declaration
	err := mockAMQP.DeclareExchange(userEventsExchangeName, "topic", true, false, false, false)
	assert.NoError(t, err)

	// Test queue declaration
	err = mockAMQP.DeclareQueue(userNotificationsQueue, true, false, false, false)
	assert.NoError(t, err)

	// Test binding
	err = mockAMQP.BindQueue(userNotificationsQueue, userEventsExchangeName, usersWildcardRoutingKey, false)
	assert.NoError(t, err)

	// Verify infrastructure state
	assert.True(t, mockAMQP.IsExchangeDeclared(userEventsExchangeName))
	assert.True(t, mockAMQP.IsQueueDeclared(userNotificationsQueue))
	assert.True(t, mockAMQP.IsQueueBound(userNotificationsQueue, userEventsExchangeName, usersWildcardRoutingKey))
}

// TestAMQPClientPublishToExchange demonstrates exchange publishing
func TestAMQPClientPublishToExchange(t *testing.T) {
	mockAMQP := mocks.NewMockAMQPClient()

	// Set up expectation for publishing to exchange
	options := messaging.PublishOptions{
		Exchange:   userEventsExchangeName,
		RoutingKey: userCreated,
		Headers:    map[string]any{"content-type": "application/json"},
	}
	data := []byte(`{"user_id": 1, "name": "John"}`)

	mockAMQP.ExpectPublishToExchange(options, data, nil)

	// Test the publish
	err := mockAMQP.PublishToExchange(context.Background(), options, data)
	assert.NoError(t, err)

	mockAMQP.AssertExpectations(t)
}

// TestRegistryInfrastructure demonstrates registry testing
func TestRegistryInfrastructure(t *testing.T) {
	mockRegistry := fixtures.NewWorkingRegistry()

	// Register infrastructure
	exchange := fixtures.NewExchangeDeclaration(userEventsExchangeName, "topic")
	queue := fixtures.NewQueueDeclaration(userNotificationsQueue)
	binding := fixtures.NewBindingDeclaration(userNotificationsQueue, userEventsExchangeName, usersWildcardRoutingKey)
	publisher := fixtures.NewPublisherDeclaration(userEventsExchangeName, userCreated, "user.created")
	consumer := fixtures.NewConsumerDeclaration(
		userNotificationsQueue,
		"user.created",
		noopMessageHandler{event: "user.created"},
	)

	mockRegistry.RegisterExchange(exchange)
	mockRegistry.RegisterQueue(queue)
	mockRegistry.RegisterBinding(binding)
	mockRegistry.RegisterPublisher(publisher)
	mockRegistry.RegisterConsumer(consumer)

	// Test infrastructure declaration
	err := mockRegistry.DeclareInfrastructure(context.Background())
	assert.NoError(t, err)

	// Test consumer lifecycle
	err = mockRegistry.StartConsumers(context.Background())
	assert.NoError(t, err)

	mockRegistry.StopConsumers()

	// Verify registrations
	exchanges := mockRegistry.GetExchanges()
	assert.Contains(t, exchanges, userEventsExchangeName)

	queues := mockRegistry.GetQueues()
	assert.Contains(t, queues, userNotificationsQueue)

	bindings := mockRegistry.GetBindings()
	assert.Len(t, bindings, 1)
	assert.Equal(t, usersWildcardRoutingKey, bindings[0].RoutingKey)

	mockRegistry.AssertExpectations(t)
}
