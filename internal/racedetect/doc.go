// Package racedetect reports whether the binary was built with -race, so the
// allocation-stability guards (testing.AllocsPerRun) can skip under a detector that
// inflates their counts.
package racedetect
