//go:build integration

package containers

import (
	"context"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"
)

// poolBootTimeout bounds one background boot: the 3-minute ceiling the per-test
// helpers put on MustStartPostgreSQLContainer.
const poolBootTimeout = 3 * time.Minute

// pgBoot is one finished background boot: a container, or why there is none.
type pgBoot struct {
	container       *PostgreSQLContainer
	dockerAvailable bool
	err             error
}

// PostgreSQLPool hands each test its own never-used PostgreSQL container, as
// MustStartPostgreSQLContainer does, but boots the next ones while the current
// test runs, so serial tests stop paying every boot back to back. A container is
// handed out once and terminated when its test ends, so per-test isolation is
// unchanged (ADR-020).
//
// Boots run through the *testing.T-free start path, so nothing is logged on, or
// skipped against, a test that did not ask for the container. The pool starts on
// the first Take, so a run that matches no integration test boots nothing.
//
// The zero value is not usable; construct with NewPostgreSQLPool.
type PostgreSQLPool struct {
	boot func(context.Context) (container *PostgreSQLContainer, dockerAvailable bool, err error)

	once  sync.Once
	ready chan pgBoot // capacity is the pool's depth
	wg    sync.WaitGroup
}

// NewPostgreSQLPool returns a pool that keeps depth boots outstanding — in flight
// or booted and unclaimed — so at most depth containers boot at once and Close
// discards at most depth. A nil cfg means DefaultPostgreSQLConfig.
func NewPostgreSQLPool(cfg *PostgreSQLContainerConfig, depth int) *PostgreSQLPool {
	return newPostgreSQLPool(depth, func(ctx context.Context) (*PostgreSQLContainer, bool, error) {
		return StartPostgreSQLContainerForTestMain(ctx, cfg)
	})
}

func newPostgreSQLPool(depth int, boot func(context.Context) (*PostgreSQLContainer, bool, error)) *PostgreSQLPool {
	if depth < 1 {
		panic(fmt.Sprintf("containers: PostgreSQLPool depth must be at least 1, got %d", depth))
	}
	return &PostgreSQLPool{boot: boot, ready: make(chan pgBoot, depth)}
}

// Take returns a container no other test has used, terminated when t ends, and
// starts booting its replacement. Docker being unavailable skips t and a failed
// boot fails it, as MustStartPostgreSQLContainer does. ctx bounds only the wait
// for a boot already under way.
func (p *PostgreSQLPool) Take(ctx context.Context, t *testing.T) *PostgreSQLContainer {
	t.Helper()

	p.once.Do(func() {
		for range cap(p.ready) {
			p.launch()
		}
	})

	var b pgBoot
	select {
	case b = <-p.ready:
	case <-ctx.Done():
		t.Fatalf("Failed to start PostgreSQL container: %v", ctx.Err())
	}
	// Before Skip/Fatalf, which end t's goroutine: the next test still needs it.
	p.launch()

	requireStarted(t, "PostgreSQL", b.dockerAvailable, b.err)

	t.Logf("PostgreSQL container started successfully at %s", maskConnectionString(b.container.connStr))
	return b.container.WithCleanup(t)
}

// launch starts one background boot. Every launch follows the receive it
// replaces, so the ready buffer of depth never blocks a sender.
func (p *PostgreSQLPool) launch() {
	p.wg.Go(func() {
		ctx, cancel := context.WithTimeout(context.Background(), poolBootTimeout)
		defer cancel()

		c, dockerAvailable, err := p.boot(ctx)
		p.ready <- pgBoot{container: c, dockerAvailable: dockerAvailable, err: err}
	})
}

// Close waits for the boots still in flight, then terminates every container no
// test took, all at once. Call it from TestMain after m.Run, which has joined
// every test, so no Take races it. In-flight boots are waited for, not
// canceled: a canceled postgres.Run can leave a created container behind. A
// binary killed before it reaches Close — a -timeout kill, say — strands them,
// leaving testcontainers' Ryuk sidecar to reap them.
func (p *PostgreSQLPool) Close() {
	p.wg.Wait()

	ctx, cancel := context.WithTimeout(context.Background(), sharedStopTimeout)
	defer cancel()
	var terminating sync.WaitGroup
	for range len(p.ready) {
		b := <-p.ready
		if b.container == nil {
			continue
		}
		terminating.Go(func() {
			if err := b.container.Terminate(ctx); err != nil {
				fmt.Fprintf(os.Stderr, "warning: failed to terminate pooled PostgreSQL container: %v\n", err)
			}
		})
	}
	terminating.Wait()
}
