package publishdoor

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPublishWithoutADispatcherFailsClosed(t *testing.T) {
	prev := Swap(nil)
	t.Cleanup(func() { Swap(prev) })

	err := Publish(t.Context(), nil, Options{}, nil)
	assert.ErrorIs(t, err, ErrNotRegistered)
}

func TestRegisterRejectsNil(t *testing.T) {
	assert.Panics(t, func() { Register(nil) })
}

// TestRegisterRefusesASecondDispatcher pins the guard the streams seam has too:
// only Swap may replace a registered dispatcher.
func TestRegisterRefusesASecondDispatcher(t *testing.T) {
	prev := Swap(nil)
	t.Cleanup(func() { Swap(prev) })

	first := func(context.Context, any, Options, []byte) error { return nil }
	Register(first)
	assert.Panics(t, func() { Register(first) })
	assert.NotNil(t, Swap(nil), "the first registration is still the installed one")
}

// TestPublishDispatchesEveryArgumentUnchanged pins that the seam forwards the
// client, the destination and the payload as given: the relay's bytes must reach
// messaging byte-for-byte.
func TestPublishDispatchesEveryArgumentUnchanged(t *testing.T) {
	type gotCall struct {
		client any
		opts   Options
		data   []byte
	}
	var got gotCall
	sentinel := errors.New("dispatched")
	prev := Swap(func(_ context.Context, client any, opts Options, data []byte) error {
		got = gotCall{client: client, opts: opts, data: data}
		return sentinel
	})
	t.Cleanup(func() { Swap(prev) })

	client := &struct{ name string }{name: "c"}
	opts := Options{Exchange: "ex", RoutingKey: "rk", Headers: map[string]any{"h": 1}, Mandatory: true}
	err := Publish(t.Context(), client, opts, []byte("payload"))

	require.ErrorIs(t, err, sentinel)
	assert.Same(t, client, got.client)
	assert.Equal(t, opts, got.opts)
	assert.Equal(t, []byte("payload"), got.data)
}

// TestSwapReturnsThePreviousDispatcher pins the restore contract a test relies on.
func TestSwapReturnsThePreviousDispatcher(t *testing.T) {
	first := func(context.Context, any, Options, []byte) error { return errors.New("first") }
	prev := Swap(first)
	t.Cleanup(func() { Swap(prev) })

	returned := Swap(nil)
	require.NotNil(t, returned)
	assert.EqualError(t, returned(t.Context(), nil, Options{}, nil), "first")
	assert.Nil(t, Swap(first), "after a nil swap nothing is registered")
}
