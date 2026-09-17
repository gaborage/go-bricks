package testutil

import (
	"sync"
	"testing"
)

// BlockedCreate parks a create callback after it has started, then releases it.
// Arrive signals Started once and blocks until Release (or test cleanup). Tests
// use it to order Remove against an in-flight Get / GetOrCreate.
type BlockedCreate struct {
	Started <-chan struct{}

	started chan struct{}
	block   chan struct{}
	start   sync.Once
	release sync.Once
}

// NewBlockedCreate returns a gate whose Release runs on cleanup so a failed test
// cannot leave a create goroutine blocked on Arrive.
func NewBlockedCreate(t *testing.T) *BlockedCreate {
	t.Helper()
	started := make(chan struct{})
	b := &BlockedCreate{
		Started: started,
		started: started,
		block:   make(chan struct{}),
	}
	t.Cleanup(b.Release)
	return b
}

// Arrive signals that the create has started, then waits for Release.
func (b *BlockedCreate) Arrive() {
	b.start.Do(func() { close(b.started) })
	<-b.block
}

// Release unblocks every Arrive. Idempotent.
func (b *BlockedCreate) Release() {
	b.release.Do(func() { close(b.block) })
}
