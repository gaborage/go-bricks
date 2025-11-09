package cache

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestSentinelErrors(t *testing.T) {
	tests := []struct {
		name string
		err  error
	}{
		{"ErrNotFound", ErrNotFound},
		{"ErrCASFailed", ErrCASFailed},
		{"ErrClosed", ErrClosed},
		{"ErrInvalidTTL", ErrInvalidTTL},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.NotNil(t, tt.err)
			assert.Error(t, tt.err)
			assert.True(t, errors.Is(tt.err, tt.err))
		})
	}
}

func TestConfigError(t *testing.T) {
	t.Run("WithoutUnderlyingError", func(t *testing.T) {
		err := NewConfigError("redis.host", "host is required", nil)

		assert.NotNil(t, err)
		assert.Equal(t, "redis.host", err.Field)
		assert.Equal(t, "host is required", err.Message)
		assert.Nil(t, err.Err)
		assert.Contains(t, err.Error(), "cache configuration error")
		assert.Contains(t, err.Error(), "redis.host")
		assert.Contains(t, err.Error(), "host is required")
	})

	t.Run("WithUnderlyingError", func(t *testing.T) {
		underlying := errors.New("invalid port number")
		err := NewConfigError("redis.port", "port must be between 1 and 65535", underlying)

		assert.NotNil(t, err)
		assert.Equal(t, "redis.port", err.Field)
		assert.Equal(t, "port must be between 1 and 65535", err.Message)
		assert.Equal(t, underlying, err.Err)
		assert.Contains(t, err.Error(), "cache configuration error")
		assert.Contains(t, err.Error(), "redis.port")
		assert.Contains(t, err.Error(), "invalid port number")
	})

	t.Run("ErrorUnwrap", func(t *testing.T) {
		underlying := errors.New("validation failed")
		err := NewConfigError("cache.type", "unsupported type", underlying)

		assert.True(t, errors.Is(err, underlying))
		assert.Equal(t, underlying, errors.Unwrap(err))
	})
}

func TestConnectionError(t *testing.T) {
	t.Run("Basic", func(t *testing.T) {
		underlying := errors.New("connection refused")
		err := NewConnectionError("dial", "localhost:6379", underlying)

		assert.NotNil(t, err)
		assert.Equal(t, "dial", err.Op)
		assert.Equal(t, "localhost:6379", err.Address)
		assert.Equal(t, underlying, err.Err)
		assert.Contains(t, err.Error(), "cache connection error")
		assert.Contains(t, err.Error(), "dial")
		assert.Contains(t, err.Error(), "localhost:6379")
		assert.Contains(t, err.Error(), "connection refused")
	})

	t.Run("PingOperation", func(t *testing.T) {
		underlying := errors.New("timeout")
		err := NewConnectionError("ping", "redis.example.com:6379", underlying)

		assert.Equal(t, "ping", err.Op)
		assert.Equal(t, "redis.example.com:6379", err.Address)
		assert.Contains(t, err.Error(), "ping")
		assert.Contains(t, err.Error(), "timeout")
	})

	t.Run("ErrorUnwrap", func(t *testing.T) {
		underlying := errors.New("network unreachable")
		err := NewConnectionError("dial", "192.168.1.1:6379", underlying)

		assert.True(t, errors.Is(err, underlying))
		assert.Equal(t, underlying, errors.Unwrap(err))
	})
}

func TestOperationError(t *testing.T) {
	t.Run("GetOperation", func(t *testing.T) {
		underlying := errors.New("timeout")
		err := NewOperationError("get", "user:123", underlying)

		assert.NotNil(t, err)
		assert.Equal(t, "get", err.Op)
		assert.Equal(t, "user:123", err.Key)
		assert.Equal(t, underlying, err.Err)
		assert.Contains(t, err.Error(), "cache operation error")
		assert.Contains(t, err.Error(), "get")
		assert.Contains(t, err.Error(), "user:123")
		assert.Contains(t, err.Error(), "timeout")
	})

	t.Run("SetOperation", func(t *testing.T) {
		underlying := errors.New("out of memory")
		err := NewOperationError("set", "session:abc-def-123", underlying)

		assert.Equal(t, "set", err.Op)
		assert.Equal(t, "session:abc-def-123", err.Key)
		assert.Contains(t, err.Error(), "set")
		assert.Contains(t, err.Error(), "session:abc-def-123")
	})

	t.Run("CASOperation", func(t *testing.T) {
		err := NewOperationError("cas", "lock:job:456", ErrCASFailed)

		assert.Equal(t, "cas", err.Op)
		assert.Equal(t, "lock:job:456", err.Key)
		assert.True(t, errors.Is(err, ErrCASFailed))
	})

	t.Run("ErrorUnwrap", func(t *testing.T) {
		underlying := ErrNotFound
		err := NewOperationError("get", "missing:key", underlying)

		assert.True(t, errors.Is(err, ErrNotFound))
		assert.Equal(t, underlying, errors.Unwrap(err))
	})

	t.Run("NestedWrapping", func(t *testing.T) {
		// Test that we can wrap errors multiple times and still use errors.Is
		baseErr := errors.New("base error")
		opErr := NewOperationError("get", "key:123", baseErr)

		assert.True(t, errors.Is(opErr, baseErr))
		assert.Contains(t, opErr.Error(), "base error")
	})
}

func TestErrorWrapping(t *testing.T) {
	t.Run("NotFoundWrappedInOperationError", func(t *testing.T) {
		err := NewOperationError("get", "user:999", ErrNotFound)

		assert.True(t, errors.Is(err, ErrNotFound))
		assert.Contains(t, err.Error(), "get")
		assert.Contains(t, err.Error(), "user:999")
	})

	t.Run("CASFailedWrappedInOperationError", func(t *testing.T) {
		err := NewOperationError("cas", "lock:123", ErrCASFailed)

		assert.True(t, errors.Is(err, ErrCASFailed))
		assert.Contains(t, err.Error(), "cas")
		assert.Contains(t, err.Error(), "lock:123")
	})

	t.Run("MultipleWrappingLevels", func(t *testing.T) {
		baseErr := errors.New("network error")
		connErr := NewConnectionError("dial", "localhost:6379", baseErr)
		opErr := NewOperationError("get", "key:abc", connErr)

		// Should be able to unwrap to the base error
		assert.True(t, errors.Is(opErr, baseErr))
		assert.True(t, errors.Is(opErr, connErr))
	})
}

func TestErrorMessages(t *testing.T) {
	t.Run("ConfigErrorMessage", func(t *testing.T) {
		err := NewConfigError("redis.host", "host is required", nil)
		expected := "cache configuration error: redis.host: host is required"
		assert.Equal(t, expected, err.Error())
	})

	t.Run("ConnectionErrorMessage", func(t *testing.T) {
		underlying := errors.New("timeout")
		err := NewConnectionError("ping", "localhost:6379", underlying)
		assert.Contains(t, err.Error(), "cache connection error: ping failed for localhost:6379: timeout")
	})

	t.Run("OperationErrorMessage", func(t *testing.T) {
		underlying := errors.New("timeout")
		err := NewOperationError("get", "user:123", underlying)
		assert.Contains(t, err.Error(), "cache operation error: get failed for key \"user:123\": timeout")
	})
}
