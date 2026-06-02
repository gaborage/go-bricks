package migration

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/gaborage/go-bricks/logger"
)

// stubGate is a minimal QuiesceGate for exercising quiesceBlocks directly.
type stubGate struct {
	set bool
	err error
}

func (g stubGate) IsSet(context.Context) (bool, error) { return g.set, g.err }
func (g stubGate) Query(context.Context) (*QuiesceStatus, error) {
	return &QuiesceStatus{Active: g.set}, nil
}

func TestQuiesceBlocksNilGateNeverBlocks(t *testing.T) {
	assert.False(t, quiesceBlocks(context.Background(), nil, nil))
}

func TestQuiesceBlocksReportsGateState(t *testing.T) {
	assert.True(t, quiesceBlocks(context.Background(), stubGate{set: true}, nil))
	assert.False(t, quiesceBlocks(context.Background(), stubGate{set: false}, nil))
}

func TestQuiesceBlocksFailsOpenOnCheckError(t *testing.T) {
	boom := errors.New("control-plane down")
	// With a logger (WARN path) and without (nil-logger branch): both fail open.
	assert.False(t, quiesceBlocks(context.Background(), stubGate{err: boom}, logger.New("disabled", true)))
	assert.False(t, quiesceBlocks(context.Background(), stubGate{err: boom}, nil))
}
