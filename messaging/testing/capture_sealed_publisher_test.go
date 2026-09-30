package testing

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type sealedOrder struct{ ID string }

func TestCaptureSealedPublisherMintsDistinctPlaceholders(t *testing.T) {
	c := NewCaptureSealedPublisher[sealedOrder]()
	d1, j1, err := c.Seal(context.Background(), sealedOrder{ID: "a"})
	require.NoError(t, err)
	d2, j2, err := c.Seal(context.Background(), sealedOrder{ID: "b"})
	require.NoError(t, err)
	assert.Equal(t, "sealed-1", string(d1))
	assert.Equal(t, "jti-1", j1)
	assert.Equal(t, "sealed-2", string(d2))
	assert.Equal(t, "jti-2", j2)
	assert.Equal(t, []sealedOrder{{ID: "a"}, {ID: "b"}}, c.Sealed())
}

func TestCaptureSealedPublisherRecordsACopyOfEachBody(t *testing.T) {
	c := NewCaptureSealedPublisher[sealedOrder]()
	body := []byte("sealed-1")
	require.NoError(t, c.PublishSealed(context.Background(), nil, body))
	body[0] = 'X'
	assert.Equal(t, [][]byte{[]byte("sealed-1")}, c.Published())
	got := c.Published()
	got[0][0] = 'Y'
	assert.Equal(t, "sealed-1", string(c.Published()[0]), "Published hands out copies")
}

func TestCaptureSealedPublisherReturnsConfiguredErrors(t *testing.T) {
	c := NewCaptureSealedPublisher[sealedOrder]()
	sealErr, pubErr := errors.New("seal down"), errors.New("broker down")
	c.FailSeal(sealErr)
	c.FailPublish(pubErr)
	data, jti, err := c.Seal(context.Background(), sealedOrder{ID: "a"})
	require.ErrorIs(t, err, sealErr)
	assert.Nil(t, data)
	assert.Empty(t, jti)
	require.ErrorIs(t, c.PublishSealed(context.Background(), nil, []byte("x")), pubErr)
	assert.Len(t, c.Sealed(), 1, "the attempt is still recorded")
	assert.Len(t, c.Published(), 1)
}

func TestCaptureSealedPublisherResetKeepsErrors(t *testing.T) {
	c := NewCaptureSealedPublisher[sealedOrder]()
	pubErr := errors.New("broker down")
	c.FailPublish(pubErr)
	_, _, _ = c.Seal(context.Background(), sealedOrder{ID: "a"})
	_ = c.PublishSealed(context.Background(), nil, []byte("x"))
	c.Reset()
	assert.Empty(t, c.Sealed())
	assert.Empty(t, c.Published())
	data, _, err := c.Seal(context.Background(), sealedOrder{ID: "b"})
	require.NoError(t, err)
	assert.Equal(t, "sealed-1", string(data), "the counter restarts")
	require.ErrorIs(t, c.PublishSealed(context.Background(), nil, []byte("y")), pubErr)
}

func TestCaptureSealedPublisherIsSafeForConcurrentUse(t *testing.T) {
	c := NewCaptureSealedPublisher[sealedOrder]()
	var wg sync.WaitGroup
	for range 16 {
		wg.Go(func() {
			data, _, err := c.Seal(context.Background(), sealedOrder{ID: "x"})
			assert.NoError(t, err)
			assert.NoError(t, c.PublishSealed(context.Background(), nil, data))
		})
	}
	wg.Wait()
	assert.Len(t, c.Sealed(), 16)
	assert.Len(t, c.Published(), 16)
}
