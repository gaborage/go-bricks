package messaging

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestAmqpDialFuncRoundTrip verifies set/get round-trip and the substitute
// is invoked by callers that read through getAmqpDialFunc.
func TestAmqpDialFuncRoundTrip(t *testing.T) {
	original := getAmqpDialFunc()
	t.Cleanup(func() { setAmqpDialFunc(original) })

	called := 0
	var gotURL string
	replacement := func(url string) (amqpConnection, error) {
		called++
		gotURL = url
		return stubConn{}, nil
	}
	setAmqpDialFunc(replacement)

	dial := getAmqpDialFunc()
	conn, err := dial(amqpURLIdle)
	require.NoError(t, err)
	require.NotNil(t, conn)
	assert.Equal(t, 1, called)
	assert.Equal(t, amqpURLIdle, gotURL)
}

// TestAmqpDialFuncPropagatesError verifies that errors from the substituted
// dialer surface through getAmqpDialFunc unchanged.
func TestAmqpDialFuncPropagatesError(t *testing.T) {
	original := getAmqpDialFunc()
	t.Cleanup(func() { setAmqpDialFunc(original) })

	wantErr := errors.New("connection refused")
	setAmqpDialFunc(func(string) (amqpConnection, error) {
		return nil, wantErr
	})

	conn, err := getAmqpDialFunc()(amqpURLIdle)
	assert.Nil(t, conn)
	assert.ErrorIs(t, err, wantErr)
}
