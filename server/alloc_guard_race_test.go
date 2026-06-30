//go:build race

package server

// raceDetectorEnabled reports whether the test binary was built with -race.
//
// testing.AllocsPerRun is unreliable under the race detector: -race adds its own
// per-access bookkeeping allocations, so the absolute allocs/op the alloc-stability
// guards assert against no longer hold. Those guards therefore skip when this is true.
// They are enforced WITHOUT -race via `make test-alloc` (wired into `make check`) and the
// Ubuntu "Enforce ADR-026 allocation guards (no -race)" CI step, so the ADR-026 invariant
// is still gated — just not in the default -race test run.
const raceDetectorEnabled = true
