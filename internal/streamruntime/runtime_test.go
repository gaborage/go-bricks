package streamruntime

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRegisterRejectsNil(t *testing.T) {
	assert.Panics(t, func() { Register(nil) })
}

func TestSwapRegisteredRestoresThePreviousRuntime(t *testing.T) {
	prev := SwapRegistered(nil)
	t.Cleanup(func() { SwapRegistered(prev) })

	require.Nil(t, Registered())
	SwapRegistered(prev)
	assert.Equal(t, prev, Registered())
}
