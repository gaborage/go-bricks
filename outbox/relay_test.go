package outbox

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDecodeHeadersEmpty(t *testing.T) {
	headers, err := decodeHeaders(nil)
	assert.NoError(t, err)
	assert.Nil(t, headers)
}

func TestDecodeHeadersValid(t *testing.T) {
	data := []byte(`{"x-priority":"high","x-source":"test"}`)
	headers, err := decodeHeaders(data)
	require.NoError(t, err)
	assert.Equal(t, "high", headers["x-priority"])
	assert.Equal(t, "test", headers["x-source"])
}

func TestDecodeHeadersInvalidJSON(t *testing.T) {
	data := []byte(`{invalid json}`)
	headers, err := decodeHeaders(data)
	assert.Error(t, err)
	assert.Nil(t, headers)
	assert.Contains(t, err.Error(), "invalid headers JSON")
}
