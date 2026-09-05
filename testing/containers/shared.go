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

// DockerUnavailableSkipMessage is the single reason string every integration test
// reports when Docker is missing. It is one constant so a CI log grep for a skipped
// integration run matches whichever container the package needed.
const DockerUnavailableSkipMessage = "Docker is not available - skipping integration test. Install Docker Desktop or ensure Docker daemon is running."

// sharedStopTimeout bounds the post-suite Terminate of a shared container.
const sharedStopTimeout = 60 * time.Second

// Terminator is all Shared needs of a container: the ability to stop it.
type Terminator interface {
	Terminate(context.Context) error
}

// Shared owns one container for a whole test binary (ADR-020). It starts lazily on
// the first Get, so a run that matches no integration test — a unit-test-only -run
// filter, say — never pays for the container, and it terminates once from TestMain
// instead of once per test.
//
// The zero value is not usable; construct with NewShared.
type Shared[T Terminator] struct {
	name         string
	startTimeout time.Duration
	start        func(context.Context) (container T, dockerAvailable bool, err error)

	once     sync.Once
	c        T
	dockerOK bool
	started  bool
	err      error
}

// NewShared returns a Shared that boots its container through start, bounded by
// startTimeout. name appears in the skip/failure messages ("PostgreSQL", "RabbitMQ").
// start has the signature of the package's Start<X>ContainerForTestMain helpers.
func NewShared[T Terminator](name string, startTimeout time.Duration, start func(context.Context) (T, bool, error)) *Shared[T] {
	return &Shared[T]{name: name, startTimeout: startTimeout, start: start}
}

// Get returns the shared container, booting it on first call. Docker being absent
// skips the requesting test — the package's unit tests still run — and a startup
// failure fails it. Every later caller sees the same outcome without re-dialing.
func (s *Shared[T]) Get(t *testing.T) T {
	t.Helper()

	s.once.Do(func() {
		ctx, cancel := context.WithTimeout(context.Background(), s.startTimeout)
		defer cancel()

		s.c, s.dockerOK, s.err = s.start(ctx)
		// Tracked as a bool rather than a nil check so T need not be comparable.
		s.started = s.dockerOK && s.err == nil
	})

	if !s.dockerOK {
		t.Skip(DockerUnavailableSkipMessage)
	}
	if s.err != nil {
		t.Fatalf("Failed to start %s container: %v", s.name, s.err)
	}
	return s.c
}

// Close terminates the container if Get ever started one. Call it from TestMain
// after m.Run, which has joined every test goroutine, so the once has settled.
// A test binary killed before it reaches Close — a -timeout kill, say — strands
// the container, leaving testcontainers' Ryuk sidecar to reap it.
func (s *Shared[T]) Close() {
	if !s.started {
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), sharedStopTimeout)
	defer cancel()
	if err := s.c.Terminate(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "warning: failed to terminate shared %s container: %v\n", s.name, err)
	}
}
