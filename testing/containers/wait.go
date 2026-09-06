//go:build integration

package containers

import (
	"fmt"
	"time"

	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

// bindStartupTimeout binds timeout to every leaf strategy, and is the reason the helpers
// in this package get the bound they configure. Call sites pass freshly constructed,
// unbound leaves, so the known types are set unconditionally.
//
// In testcontainers-go v0.44.0 a composite wait strategy binds nothing on its own:
//   - MultiStrategy.WithStartupTimeout is a deprecated alias for WithDeadline — it sets
//     only ms.deadline and leaves ms.timeout (Timeout()) nil;
//   - MultiStrategy.WaitUntilReady pushes a per-leaf timeout only when Timeout() is
//     non-nil, so under that alias every leaf falls back to its own
//     defaultStartupTimeout() of 60s no matter what was configured;
//   - WithStartupTimeoutDefault does not lift that cap either: it only widens the
//     context handed to the leaf, which then re-caps itself at 60s;
//   - testcontainers.WithWaitStrategy is WithWaitStrategyAndDeadline(60*time.Second, ...),
//     a second hardcoded ceiling, which is why waitOptionWithin calls the AndDeadline form.
//
// The vendor setters mutate in place and return the receiver, but only on the concrete
// types — there is no common setter interface — hence the type switch. A leaf this
// function cannot bind is a programmer error in test-support code, not a runtime
// condition, so it panics at construction (naming the offending TYPE, never a value)
// rather than silently reintroducing the 60s cap it exists to prevent.
func bindStartupTimeout(timeout time.Duration, strategies ...wait.Strategy) []wait.Strategy {
	for _, s := range strategies {
		switch st := s.(type) {
		case *wait.LogStrategy:
			st.WithStartupTimeout(timeout)
		case *wait.HostPortStrategy:
			st.WithStartupTimeout(timeout)
		default:
			// A leaf outside the switch is only safe if it already carries its own
			// bound; anything else would fall back to the 60s default unnoticed.
			if t, ok := s.(wait.StrategyTimeout); !ok || t.Timeout() == nil {
				panic(fmt.Sprintf("containers: wait strategy %T has no startup timeout and no known setter", s))
			}
		}
	}
	return strategies
}

// waitAllWithin composes strategies for a helper that builds its own ContainerRequest,
// binding timeout to every leaf and to the composite's deadline.
func waitAllWithin(timeout time.Duration, strategies ...wait.Strategy) *wait.MultiStrategy {
	return wait.ForAll(bindStartupTimeout(timeout, strategies...)...).WithDeadline(timeout)
}

// waitOptionWithin is the same for a helper that goes through a testcontainers module's
// Run: WithWaitStrategyAndDeadline already composes the strategies into a MultiStrategy
// carrying deadline, so wrapping them again here would only add an identical second layer.
func waitOptionWithin(timeout time.Duration, strategies ...wait.Strategy) testcontainers.ContainerCustomizer {
	return testcontainers.WithWaitStrategyAndDeadline(timeout, bindStartupTimeout(timeout, strategies...)...)
}
