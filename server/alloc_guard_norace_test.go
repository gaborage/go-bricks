//go:build !race

package server

// raceDetectorEnabled is false in non-race builds, so the allocation-stability guards
// (testing.AllocsPerRun) run and enforce the ADR-026 baselines. They are exercised by
// `make test-alloc` (wired into `make check`) and the Ubuntu non-race CI step; the default
// -race test run skips them (see the //go:build race counterpart for why).
const raceDetectorEnabled = false
