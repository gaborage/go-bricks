//go:build integration

package containers

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeBoots stands in for Docker: each boot yields a container with no testcontainer
// behind it, whose Terminate only closes the admin pool, so admin() returning
// errAdminAfterTerminate witnesses that something terminated it. Boots block on gate
// when it is set, which lets a test hold them in flight.
type fakeBoots struct {
	gate chan struct{}

	mu       sync.Mutex
	inFlight int
	peak     int
	booted   []*PostgreSQLContainer
}

func (f *fakeBoots) boot(context.Context) (*PostgreSQLContainer, bool, error) {
	f.mu.Lock()
	f.inFlight++
	f.peak = max(f.peak, f.inFlight)
	f.mu.Unlock()

	if f.gate != nil {
		<-f.gate
	}

	c := &PostgreSQLContainer{}
	f.mu.Lock()
	f.inFlight--
	f.booted = append(f.booted, c)
	f.mu.Unlock()
	return c, true, nil
}

// snapshot returns the boots in flight, the most ever in flight at once, and the
// containers booted so far.
func (f *fakeBoots) snapshot() (inFlight, peak int, booted []*PostgreSQLContainer) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.inFlight, f.peak, append([]*PostgreSQLContainer(nil), f.booted...)
}

func terminated(c *PostgreSQLContainer) bool {
	_, err := c.admin()
	return errors.Is(err, errAdminAfterTerminate)
}

// takeIn takes one container from p inside its own subtest, so the container's
// cleanup has run by the time takeIn returns it.
func takeIn(t *testing.T, p *PostgreSQLPool) *PostgreSQLContainer {
	t.Helper()
	var c *PostgreSQLContainer
	t.Run("take", func(t *testing.T) {
		c = p.Take(context.Background(), t)
		assert.False(t, terminated(c), "a taken container is live until its test ends")
	})
	require.NotNil(t, c)
	return c
}

func TestPostgreSQLPoolHandsEachBootOutOnce(t *testing.T) {
	const depth, takes = 2, 5
	f := &fakeBoots{}
	p := newPostgreSQLPool(depth, f.boot)

	seen := make(map[*PostgreSQLContainer]bool, takes)
	for range takes {
		c := takeIn(t, p)
		require.False(t, seen[c], "a container was handed out twice")
		seen[c] = true
		assert.True(t, terminated(c), "a taken container is terminated when its test ends")
	}

	p.Close()

	_, _, booted := f.snapshot()
	require.Len(t, booted, takes+depth, "each take boots one replacement on top of the first depth")
	for _, c := range booted {
		assert.True(t, terminated(c), "Close terminates every container no test took")
	}
}

func TestPostgreSQLPoolKeepsDepthBootsOutstanding(t *testing.T) {
	const depth = 2
	f := &fakeBoots{gate: make(chan struct{}, 8)}
	p := newPostgreSQLPool(depth, f.boot)

	for i := range 3 {
		f.gate <- struct{}{} // lets exactly one boot finish
		takeIn(t, p)
		require.Eventuallyf(t, func() bool {
			inFlight, _, _ := f.snapshot()
			return inFlight == depth
		}, time.Second, time.Millisecond, "after take %d the replacement boot joins the one still in flight", i+1)
	}

	close(f.gate)
	p.Close()

	_, peak, booted := f.snapshot()
	assert.Equal(t, depth, peak, "no more than depth boots ever run at once")
	assert.Len(t, booted, 3+depth)
}

func TestPostgreSQLPoolSkipsWhenDockerIsUnavailable(t *testing.T) {
	var boots atomic.Int32
	p := newPostgreSQLPool(1, func(context.Context) (*PostgreSQLContainer, bool, error) {
		boots.Add(1)
		return nil, false, nil
	})

	var inner *testing.T
	t.Run("take", func(t *testing.T) {
		inner = t
		p.Take(context.Background(), t)
	})
	assert.True(t, inner.Skipped(), "Take skips the test when Docker is unavailable")
	require.Eventually(t, func() bool { return boots.Load() == 2 }, time.Second, time.Millisecond,
		"the skipped take still launched the next test's boot")

	p.Close()
}

func TestPostgreSQLPoolCloseWithoutTakeBootsNothing(t *testing.T) {
	f := &fakeBoots{}
	p := newPostgreSQLPool(2, f.boot)

	p.Close()
	p.Close() // idempotent

	_, _, booted := f.snapshot()
	assert.Empty(t, booted)
}

func TestNewPostgreSQLPoolRejectsZeroDepth(t *testing.T) {
	require.Panics(t, func() { NewPostgreSQLPool(nil, 0) })
}
