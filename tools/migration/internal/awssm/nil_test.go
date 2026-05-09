package awssm

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFetcherFromClientNilReturnsErrorFetcher(t *testing.T) {
	fetcher := FetcherFromClient(nil)
	require.NotNil(t, fetcher)
	_, err := fetcher(context.Background(), "any")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "nil SecretsManagerAPI client")
}
