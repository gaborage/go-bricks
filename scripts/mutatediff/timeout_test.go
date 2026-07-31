package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"slices"
	"strings"
	"testing"
	"time"
)

// TestTimeoutCoefficientCoversTheRealMutantCost is the regression this whole
// file exists for. ./observability's suite takes ~25s but its engine baseline is
// a cached replay of ~0.34s. Scaling a fixed 30s floor by the baseline alone
// yields a ceiling that does not cover one real run of the suite, which is how
// the first nightly baseline produced 241 timeouts and zero verdicts there.
func TestTimeoutCoefficientCoversTheRealMutantCost(t *testing.T) {
	obs := suiteTiming{Uncached: 25 * time.Second, Baseline: 340 * time.Millisecond}
	got := timeoutCoefficient(obs, 30*time.Second)
	ceiling := obs.Baseline * time.Duration(got)
	if ceiling < obs.Uncached {
		t.Fatalf("coefficient %d gives a %s ceiling, under the %s the suite really needs",
			got, ceiling, obs.Uncached)
	}
	// Naively dividing the floor by the cached baseline would have been enough;
	// dividing it by the SUITE is what collapsed to the engine default of 3.
	if naive := timeoutCoefficient(
		suiteTiming{Uncached: 25 * time.Second, Baseline: 25 * time.Second},
		30*time.Second); naive > minTimeoutCoefficient+1 {
		t.Errorf("sanity: conflating the two timings should collapse the coefficient, got %d", naive)
	}
}

func TestTimeoutCoefficientFloorsFastSuites(t *testing.T) {
	// A trivial suite: the floor, not the suite time, sets the ceiling.
	fast := suiteTiming{Uncached: 50 * time.Millisecond, Baseline: 100 * time.Millisecond}
	if got := timeoutCoefficient(fast, 30*time.Second); got != 300 {
		t.Errorf("timeoutCoefficient(fast, 30s floor) = %d, want 300 (ceil(30/0.1))", got)
	}
}

func TestTimeoutCoefficientRoundsUpToClearTheTarget(t *testing.T) {
	// target 30s, baseline 7s: 30/7 = 4.28, and truncating to 4 leaves 28s.
	tim := suiteTiming{Uncached: time.Second, Baseline: 7 * time.Second}
	got := timeoutCoefficient(tim, 30*time.Second)
	if got != 5 {
		t.Fatalf("timeoutCoefficient = %d, want 5", got)
	}
	if ceiling := tim.Baseline * time.Duration(got); ceiling < 30*time.Second {
		t.Errorf("ceiling %s is below the 30s target", ceiling)
	}
}

func TestTimeoutCoefficientKeepsEngineDefaultWhenBaselineIsAlreadyAmple(t *testing.T) {
	// A failing suite is never cached, so baseline == uncached. 3x the real cost
	// is ample, and inflating it only lengthens a genuinely hanging mutant.
	tim := suiteTiming{Uncached: 40 * time.Second, Baseline: 40 * time.Second}
	if got := timeoutCoefficient(tim, 30*time.Second); got != minTimeoutCoefficient {
		t.Errorf("timeoutCoefficient = %d, want %d", got, minTimeoutCoefficient)
	}
}

func TestTimeoutCoefficientClampsPathologicalValues(t *testing.T) {
	tiny := suiteTiming{Uncached: time.Second, Baseline: time.Microsecond}
	if got := timeoutCoefficient(tiny, 30*time.Second); got != maxTimeoutCoefficient {
		t.Errorf("timeoutCoefficient(1µs baseline) = %d, want %d", got, maxTimeoutCoefficient)
	}
	for _, baseline := range []time.Duration{0, -time.Second} {
		tim := suiteTiming{Uncached: time.Second, Baseline: baseline}
		if got := timeoutCoefficient(tim, 30*time.Second); got != maxTimeoutCoefficient {
			t.Errorf("timeoutCoefficient(baseline %s) = %d, want %d (unmeasurable must fail generous)",
				baseline, got, maxTimeoutCoefficient)
		}
	}
}

func TestCoefficientForFallsBackGenerouslyWhenMeasurementFails(t *testing.T) {
	var buf bytes.Buffer
	measure := func(context.Context, string) (suiteTiming, error) {
		return suiteTiming{}, errors.New("go test unavailable")
	}
	got := coefficientFor(t.Context(), "./observability", measure, 30*time.Second, &buf)
	if got != maxTimeoutCoefficient {
		t.Errorf("coefficientFor with failed measurement = %d, want %d", got, maxTimeoutCoefficient)
	}
	if !strings.Contains(buf.String(), "WARN") {
		t.Errorf("a failed measurement must be visible, got: %q", buf.String())
	}
}

func TestCoefficientForReportsBothTimings(t *testing.T) {
	var buf bytes.Buffer
	measure := func(context.Context, string) (suiteTiming, error) {
		return suiteTiming{Uncached: 25 * time.Second, Baseline: 100 * time.Millisecond}, nil
	}
	// target = 25s + 20s budget = 45s; 45/0.1 = 450.
	if got := coefficientFor(t.Context(), "./observability", measure, 30*time.Second, &buf); got != 450 {
		t.Errorf("coefficientFor = %d, want 450", got)
	}
	for _, want := range []string{"suite 25s", "engine baseline 100ms", "coefficient 450"} {
		if !strings.Contains(buf.String(), want) {
			t.Errorf("log %q missing %q", buf.String(), want)
		}
	}
}

// TestMeasurementFailureSeparatesCancellationFromRedTests pins a distinction the
// error type cannot make on its own: CommandContext kills the child on cancel,
// and a killed process surfaces as *exec.ExitError — exactly what a red suite
// produces, which measureSuite deliberately tolerates. Tolerating the kill too
// would time a process shot partway through and pass that off as the suite.
func TestMeasurementFailureSeparatesCancellationFromRedTests(t *testing.T) {
	killed := &exec.ExitError{ProcessState: &os.ProcessState{}}
	if err := measurementFailure(nil, killed); err != nil {
		t.Errorf("a red suite must still yield a timing, got %v", err)
	}
	if err := measurementFailure(context.Canceled, killed); !errors.Is(err, context.Canceled) {
		t.Errorf("measurementFailure(canceled, ExitError) = %v, want context.Canceled", err)
	}
	// Cancellation racing a clean exit is still cancellation.
	if err := measurementFailure(context.Canceled, nil); !errors.Is(err, context.Canceled) {
		t.Errorf("measurementFailure(canceled, nil) = %v, want context.Canceled", err)
	}
	if err := measurementFailure(nil, nil); err != nil {
		t.Errorf("measurementFailure(nil, nil) = %v, want nil", err)
	}
	notStarted := errors.New(`exec: "go": executable file not found in $PATH`)
	if err := measurementFailure(nil, notStarted); !errors.Is(err, notStarted) {
		t.Errorf("a toolchain that cannot start must propagate, got %v", err)
	}
}

// TestPrintCoefficientEmitsNothingWhenCanceled pins the other half. coefficientFor
// falls back to the maximum on any measurement failure, which is right for a
// genuine one and wrong for Ctrl-C: 600 is numeric, so the Makefile's guard would
// accept it and an interrupted measurement would be indistinguishable from a
// package that legitimately needs a 600x ceiling.
func TestPrintCoefficientEmitsNothingWhenCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var stdout, stderr bytes.Buffer
	measure := func(ctx context.Context, _ string) (suiteTiming, error) {
		return suiteTiming{}, ctx.Err()
	}
	if code := printCoefficient(ctx, "./observability", measure, 30*time.Second, &stdout, &stderr); code == 0 {
		t.Errorf("printCoefficient = 0 on a canceled run, want nonzero")
	}
	if got := strings.TrimSpace(stdout.String()); got != "" {
		t.Errorf("stdout = %q, want empty — the Makefile's numeric guard would accept that as a real coefficient", got)
	}
	// Not just "canceled": coefficientFor's fallback WARN already carries that
	// word, so matching it would pass even with the guard removed.
	if !strings.Contains(stderr.String(), "no coefficient emitted") {
		t.Errorf("stderr = %q, want the cancellation diagnostic", stderr.String())
	}
}

// TestPrintCoefficientKeepsStdoutParseable pins the contract the Makefile's
// baseline loop relies on: the number alone on stdout, diagnostics on stderr,
// so `coeff=$(mutatediff -coefficient ./pkg)` captures an integer.
func TestPrintCoefficientKeepsStdoutParseable(t *testing.T) {
	var stdout, stderr bytes.Buffer
	measure := func(context.Context, string) (suiteTiming, error) {
		return suiteTiming{Uncached: 10 * time.Second, Baseline: 100 * time.Millisecond}, nil
	}
	if code := printCoefficient(t.Context(), "./observability", measure, 30*time.Second, &stdout, &stderr); code != 0 {
		t.Fatalf("printCoefficient = %d, want 0", code)
	}
	// target = max(30s, 10s+20s) = 30s; 30/0.1 = 300.
	if got := strings.TrimSpace(stdout.String()); got != "300" {
		t.Errorf("stdout = %q, want %q", got, "300")
	}
	if !strings.Contains(stderr.String(), "timeout coefficient") {
		t.Errorf("stderr = %q, want the diagnostic line", stderr.String())
	}
}

// TestGremlinsTimeoutArgsUsesTheFlag pins the lever; see coefficientFlag for why
// the env var gremlins documents cannot be used. Verified on v0.5.1 against
// ./internal/sqlid: env var gave 6 TIMED OUT and no verdicts, the flag gave
// 5 KILLED and 1 LIVED.
func TestGremlinsTimeoutArgsUsesTheFlag(t *testing.T) {
	got := gremlinsTimeoutArgs(212)
	want := []string{"--timeout-coefficient", "212"}
	if !slices.Equal(got, want) {
		t.Errorf("gremlinsTimeoutArgs(212) = %v, want %v", got, want)
	}
}

func TestCeilingFloorReadsOverrideFromEnv(t *testing.T) {
	t.Setenv(ceilingFloorEnv, "45s")
	if got := ceilingFloor(); got != 45*time.Second {
		t.Errorf("ceilingFloor() = %s, want 45s", got)
	}
	t.Setenv(ceilingFloorEnv, "not-a-duration")
	if got := ceilingFloor(); got != defaultCeilingFloor {
		t.Errorf("ceilingFloor() on unparsable override = %s, want the %s default", got, defaultCeilingFloor)
	}
	t.Setenv(ceilingFloorEnv, "")
	if got := ceilingFloor(); got != defaultCeilingFloor {
		t.Errorf("ceilingFloor() unset = %s, want %s", got, defaultCeilingFloor)
	}
}

func TestWorkerArgsOnlyOverridesWhenAsked(t *testing.T) {
	if got := workerArgs(2); !slices.Equal(got, []string{workersFlag, "2"}) {
		t.Errorf("workerArgs(2) = %v, want [--workers 2]", got)
	}
	for _, n := range []int{0, -1} {
		if got := workerArgs(n); got != nil {
			t.Errorf("workerArgs(%d) = %v, want nil so .gremlins.yaml stays in charge", n, got)
		}
	}
}

// TestCoverageArgsMatchTheEngineWhenCached pins the invariant a regression already
// broke once: the cached passes must be argv-identical to gremlins' own coverage
// command, because -timeout participates in go test's cache key. Warming a
// different argv leaves gremlins' run a cache miss, so its Elapsed becomes the
// real suite and the coefficient multiplies the suite instead of the replay —
// verified on go1.26.5: after `go clean -testcache` and warming only the
// -timeout variant, the engine's command took the full 24.5s.
func TestCoverageArgsMatchTheEngineWhenCached(t *testing.T) {
	// gremlins: `go test -cover -coverprofile <file> ./pkg/...` and nothing else.
	want := []string{"test", "-cover", "-coverprofile", "/tmp/p", "./observability/..."}
	if got := coverageArgs("/tmp/p", "./observability", false); !slices.Equal(got, want) {
		t.Errorf("coverageArgs(cached) = %v, want exactly %v", got, want)
	}
	forced := coverageArgs("/tmp/p", "./observability", true)
	if !slices.Contains(forced, "-count=1") || !slices.Contains(forced, measureTimeout) {
		t.Errorf("coverageArgs(forceRun) = %v, want -count=1 and -timeout %s", forced, measureTimeout)
	}
	// -count=1 disables caching, so the extra flag is free only on that pass.
	if slices.Contains(coverageArgs("/tmp/p", "./x", false), "-timeout") {
		t.Error("the cached pass must not carry -timeout — it splits the cache key")
	}
}
