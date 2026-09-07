//go:build !race

package racedetect

// Enabled is false in non-race builds, so the allocation-stability guards
// (testing.AllocsPerRun) run and enforce their invariant. They are exercised by
// `make test-alloc` (wired into `make check`) and the Ubuntu non-race CI step; the
// default -race test run skips them (see the //go:build race counterpart for why).
const Enabled bool = false
