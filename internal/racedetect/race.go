//go:build race

package racedetect

// Enabled reports whether the binary was built with -race.
//
// testing.AllocsPerRun is unreliable under the race detector: -race adds its own
// per-access bookkeeping allocations, so the counts the alloc-stability guards assert
// no longer hold. Those guards therefore skip when this is true. They are enforced
// WITHOUT -race via `make test-alloc` (wired into `make check`) and the Ubuntu "Enforce
// ADR-026 allocation guards (no -race)" CI step.
const Enabled bool = true
