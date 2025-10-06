package observability

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"go.opentelemetry.io/otel/trace"
	"go.opentelemetry.io/otel/trace/noop"
)

// mockProvider is a test provider for testing shutdown behavior.
type mockProvider struct {
	shutdownErr    error
	shutdownCalled bool
	flushCalled    bool
}

func (m *mockProvider) TracerProvider() trace.TracerProvider {
	return noop.NewTracerProvider()
}

func (m *mockProvider) Shutdown(_ context.Context) error {
	m.shutdownCalled = true
	return m.shutdownErr
}

func (m *mockProvider) ForceFlush(_ context.Context) error {
	m.flushCalled = true
	return nil
}

func TestShutdownSuccess(t *testing.T) {
	mock := &mockProvider{}

	err := Shutdown(mock, 1*time.Second)
	assert.NoError(t, err)
	assert.True(t, mock.shutdownCalled)
}

func TestShutdownError(t *testing.T) {
	expectedErr := errors.New("shutdown failed")
	mock := &mockProvider{shutdownErr: expectedErr}

	err := Shutdown(mock, 1*time.Second)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "observability shutdown failed")
	assert.True(t, mock.shutdownCalled)
}

func TestShutdownDefaultTimeout(t *testing.T) {
	mock := &mockProvider{}

	err := Shutdown(mock, 0) // Should use DefaultShutdownTimeout
	assert.NoError(t, err)
	assert.True(t, mock.shutdownCalled)
}

func TestShutdownNegativeTimeout(t *testing.T) {
	mock := &mockProvider{}

	err := Shutdown(mock, -1*time.Second) // Should use DefaultShutdownTimeout
	assert.NoError(t, err)
	assert.True(t, mock.shutdownCalled)
}

func TestMustShutdownSuccess(t *testing.T) {
	mock := &mockProvider{}

	assert.NotPanics(t, func() {
		MustShutdown(mock, 1*time.Second)
	})
	assert.True(t, mock.shutdownCalled)
}

func TestMustShutdownPanic(t *testing.T) {
	expectedErr := errors.New("shutdown failed")
	mock := &mockProvider{shutdownErr: expectedErr}

	assert.Panics(t, func() {
		MustShutdown(mock, 1*time.Second)
	})
	assert.True(t, mock.shutdownCalled)
}
