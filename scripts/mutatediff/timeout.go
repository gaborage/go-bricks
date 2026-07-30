package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// gremlins derives each mutant's wall-clock ceiling as
// coverage_elapsed × unleash.timeout-coefficient and enforces it with a hard
// context deadline that also has to cover compiling and linking the mutated
// test binary. Scaling the coefficient per package holds that ceiling above
// the build cost, without which the engine reports TIMED OUT for every mutant
// and the gate has nothing to judge. See wiki/testing.md#timeout-ceiling.
const (
	// minTimeoutCoefficient is gremlins' own default: never go below it.
	minTimeoutCoefficient = 3
	// maxTimeoutCoefficient bounds how long a genuinely hanging mutant runs
	// before it is cut off, and caps the damage when elapsed is unmeasurable.
	maxTimeoutCoefficient = 600
	// defaultCeilingFloor is the minimum ceiling, which matters for suites too
	// fast for buildBudget alone to dominate. ~7x the largest ceiling that still
	// produced mass timeouts in the first nightly baseline (database, 1.07s).
	defaultCeilingFloor = 30 * time.Second
	// buildBudget is headroom added to the measured suite time for compiling and
	// linking the mutated test binary, which gremlins' ceiling must also cover.
	buildBudget = 20 * time.Second
	// ceilingFloorEnv lets an operator retune the floor without a code change.
	ceilingFloorEnv = "MUTATE_CEILING_FLOOR"
	// measureTimeout bounds the forced Uncached pass, above go test's 10m default
	// so a genuinely slow (not hung) suite is measured rather than truncated. It is
	// applied ONLY to that pass: -timeout participates in go test's cache key, so
	// adding it to the cached passes would warm an entry gremlins never reads,
	// leaving its own coverage run a cache miss whose Elapsed is the real suite.
	// The coefficient is then multiplied by the suite instead of the replay —
	// 148 x 24.7s is a 60-minute ceiling on ./observability, and `vacuous` cannot
	// see it because it only catches ceilings that are too tight. -count=1 already
	// disables caching, so the flag is free exactly where it is needed.
	measureTimeout = "20m"
	// coefficientFlag is the lever. gremlins also documents
	// GREMLINS_UNLEASH_TIMEOUT_COEFFICIENT, but that is silently ignored: its
	// configuration.Get[T] does an unchecked `viper.Get(k).(T)`, and viper hands
	// back environment values as strings, so the assertion for an int fails and
	// yields 0 — which the engine reads as "unset" and replaces with its default
	// 3. Values in .gremlins.yaml are parsed as ints and do work.
	coefficientFlag = "--timeout-coefficient"
	// workersFlag caps concurrent `go test` processes; each also keeps its own copy
	// of the module tree.
	workersFlag = "--workers"
)

// ceilingFloor is the minimum per-mutant ceiling the gate will accept,
// overridable via MUTATE_CEILING_FLOOR (any time.ParseDuration string).
func ceilingFloor() time.Duration {
	raw := os.Getenv(ceilingFloorEnv)
	if raw == "" {
		return defaultCeilingFloor
	}
	d, err := time.ParseDuration(raw)
	if err != nil || d <= 0 {
		return defaultCeilingFloor
	}
	return d
}

// suiteTiming holds the two different durations this calculation needs. They can
// differ by two orders of magnitude and conflating them is the whole trap.
type suiteTiming struct {
	// Uncached is what one mutant run actually costs: a mutant edits the source,
	// so its test run is always a cache miss and pays the real suite.
	Uncached time.Duration
	// Baseline is what gremlins measures for itself and multiplies the
	// coefficient by. Its command omits -count=1, so this is a cache-served
	// replay — ~0.3s of process overhead for a suite that really takes 25s.
	Baseline time.Duration
}

// timeoutCoefficient returns the smallest coefficient that holds gremlins'
// derived ceiling (Baseline × coefficient) above what a mutant run actually
// costs. Because the two inputs measure different things, targeting a fixed
// wall-clock floor is not enough: a package with a 25s suite needs a 25s+
// ceiling however fast its cached baseline replays.
//
// An unmeasurable Baseline fails generous rather than reinstating the vacuous
// pass this whole mechanism exists to prevent.
func timeoutCoefficient(t suiteTiming, floor time.Duration) int {
	target := t.Uncached + buildBudget
	if target < floor {
		target = floor
	}
	if t.Baseline <= 0 {
		return maxTimeoutCoefficient
	}
	// Integer ceiling division, so the product never lands just under target.
	coefficient := int((target + t.Baseline - 1) / t.Baseline)
	if coefficient < minTimeoutCoefficient {
		return minTimeoutCoefficient
	}
	if coefficient > maxTimeoutCoefficient {
		return maxTimeoutCoefficient
	}
	return coefficient
}

// suiteMeasurer times pkg's suite both ways; see suiteTiming.
type suiteMeasurer func(ctx context.Context, pkg string) (suiteTiming, error)

// coefficientFor measures pkg and converts the result into a coefficient,
// reporting both timings so a surprising ceiling is visible in the log rather
// than buried in the engine's behavior.
func coefficientFor(ctx context.Context, pkg string, measure suiteMeasurer, floor time.Duration, out io.Writer) int {
	t, err := measure(ctx, pkg)
	if err != nil {
		fmt.Fprintf(out, "WARN: could not time %s's tests (%v) — using the maximum timeout coefficient %d\n",
			pkg, err, maxTimeoutCoefficient)
		return maxTimeoutCoefficient
	}
	coefficient := timeoutCoefficient(t, floor)
	fmt.Fprintf(out, "mutatediff: %s suite %s, engine baseline %s — timeout coefficient %d (ceiling ~%s)\n",
		pkg, t.Uncached.Round(time.Millisecond), t.Baseline.Round(time.Millisecond),
		coefficient, (t.Baseline * time.Duration(coefficient)).Round(time.Second))
	return coefficient
}

// coverageArgs builds the `go test` argv for a measurement pass. With forceRun
// false it MUST stay byte-identical to gremlins' own coverage command
// (internal/coverage/coverage.go), because that is the cache entry gremlins will
// read and the coefficient divides by. Any extra flag here splits the cache key
// and silently un-measures the thing being measured — see measureTimeout.
func coverageArgs(profile, pkg string, forceRun bool) []string {
	args := []string{"test"}
	if forceRun {
		args = append(args, "-count=1", "-timeout", measureTimeout)
	}
	return append(args, "-cover", "-coverprofile", profile, strings.TrimSuffix(pkg, "/")+"/...")
}

// measureSuite times pkg's tests using gremlins' own baseline command
// (internal/coverage/coverage.go: `go test -cover -coverprofile <file>
// ./pkg/...`, recursive) in three passes:
//
//  1. with -count=1, which forces a real run and writes nothing to the test
//     cache — this is Uncached, the cost a mutant actually pays;
//  2. without it, discarded, purely to populate the test cache;
//  3. without it again, which is the cache hit gremlins will get — Baseline.
//
// The third pass is why the result is exact rather than a guess. Measuring only
// the suite and dividing the floor by it collapses the coefficient to the
// engine's default for exactly the packages that need it most: ./observability
// measures 25s but replays in 0.34s, which yields 3 and reproduces the bug.
//
// A failing suite still yields usable durations — a failed test is never cached,
// so passes 2 and 3 simply cost the same as 1 — and the exit status is
// deliberately ignored. This errors only when the toolchain cannot start at all,
// a state in which gremlins could not run either.
func measureSuite(ctx context.Context, pkg string) (suiteTiming, error) {
	profile, err := os.CreateTemp("", "mutatediff-cover-*")
	if err != nil {
		return suiteTiming{}, fmt.Errorf("coverage scratch file for %s: %w", pkg, err)
	}
	_ = profile.Close()
	defer func() { _ = os.Remove(profile.Name()) }()

	var exitErr *exec.ExitError
	run := func(stage string, forceRun bool) (time.Duration, error) {
		args := coverageArgs(profile.Name(), pkg, forceRun)
		start := time.Now()
		// #nosec G204 -- pkg comes from `git diff` paths resolved to package dirs, not user input
		runErr := exec.CommandContext(ctx, "go", args...).Run()
		if runErr != nil && !errors.As(runErr, &exitErr) {
			return 0, fmt.Errorf("%s %s: %w", stage, pkg, runErr)
		}
		return time.Since(start), nil
	}
	uncached, err := run("timing", true)
	if err != nil {
		return suiteTiming{}, err
	}
	if _, warmErr := run("warming", false); warmErr != nil {
		return suiteTiming{}, warmErr
	}
	baseline, err := run("re-timing", false)
	if err != nil {
		return suiteTiming{}, err
	}
	return suiteTiming{Uncached: uncached, Baseline: baseline}, nil
}

// printCoefficient backs the -coefficient mode, which exists so the Makefile's
// baseline loop scales the ceiling through this same code path instead of
// reimplementing the arithmetic in shell. The number goes to stdout alone;
// diagnostics go to stderr.
func printCoefficient(ctx context.Context, pkg string, measure suiteMeasurer, floor time.Duration, stdout, stderr io.Writer) int {
	fmt.Fprintln(stdout, coefficientFor(ctx, pkg, measure, floor, stderr))
	return 0
}

// gremlinsTimeoutArgs returns the engine flag that pins the computed
// coefficient. See coefficientFlag for why this is a flag and not an env var.
func gremlinsTimeoutArgs(coefficient int) []string {
	return []string{coefficientFlag, strconv.Itoa(coefficient)}
}

// workerArgs returns the engine's worker flag, or nothing when workers is 0 so
// .gremlins.yaml stays in charge. Each worker is a concurrent `go test`, which is
// the knob that matters on a laptop now that mutants run to completion.
func workerArgs(workers int) []string {
	if workers <= 0 {
		return nil
	}
	return []string{workersFlag, strconv.Itoa(workers)}
}
