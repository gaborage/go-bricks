package streams

import (
	"context"
	"sync"
)

// fakeHoldLedger records what the runner parked and answers the held-set load.
// Safe for concurrent use: one runner serves every partition of a super stream,
// each from its own goroutine.
type fakeHoldLedger struct {
	mu sync.Mutex

	parks     []*HeldMessage
	heldCalls int
	// held is what HeldTenants answers, per consumer.
	held map[string][]string
	// parkErr is returned by the first failParkTimes Park calls, so a test can
	// drive the stall loop without waiting on a real ledger.
	parkErr       error
	failParkTimes int
	heldErr       error
}

func (f *fakeHoldLedger) Park(_ context.Context, msg *HeldMessage) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failParkTimes > 0 {
		f.failParkTimes--
		return f.parkErr
	}
	f.parks = append(f.parks, msg)
	return nil
}

func (f *fakeHoldLedger) HeldTenants(_ context.Context, consumer string) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.heldCalls++
	if f.heldErr != nil {
		return nil, f.heldErr
	}
	return f.held[consumer], nil
}

func (f *fakeHoldLedger) parked() []*HeldMessage {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]*HeldMessage(nil), f.parks...)
}

func (f *fakeHoldLedger) loads() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.heldCalls
}
