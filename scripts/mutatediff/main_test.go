package main

import (
	"bytes"
	"os"
	"strings"
	"testing"
)

func TestRunNoOpDiffExitsCleanWithoutEngine(t *testing.T) {
	var buf bytes.Buffer
	if got := run(t.Context(), "false", "HEAD", throttle{}, false, &buf); got != 0 {
		t.Fatalf("run = %d, want 0; output: %s", got, buf.String())
	}
	if !strings.Contains(buf.String(), "no mutatable changes") {
		t.Fatalf("missing no-op message, got: %s", buf.String())
	}
}

func TestReportWarningsDistinguishesTimeoutsFromUncovered(t *testing.T) {
	var buf bytes.Buffer
	got := reportWarnings([]mutantVerdict{
		{File: "observability/logs.go", Line: 27, Operator: "CONDITIONALS_NEGATION", Status: statusTimedOut},
		{File: "observability/logs.go", Line: 33, Operator: "ARITHMETIC_BASE", Status: statusNotCovered},
		{File: "messaging/client.go", Line: 91, Operator: "CONDITIONALS_BOUNDARY", Status: statusTimedOut},
	}, &buf)
	if got != 2 {
		t.Errorf("timedOut = %d, want 2", got)
	}
	out := buf.String()
	if !strings.Contains(out, "WARN timed out") || !strings.Contains(out, "indeterminate") {
		t.Errorf("timeouts must read as indeterminate, got: %s", out)
	}
	if !strings.Contains(out, "WARN not covered: observability/logs.go:33") {
		t.Errorf("uncovered mutant lost its own message, got: %s", out)
	}
}

// TestReportVerdictFailsVacuousBeforeSurvivors pins the ordering: with nothing
// judged, an empty failure list carries no information, so the vacuous packages
// must be what the operator is told about.
func TestReportVerdictFailsVacuousBeforeSurvivors(t *testing.T) {
	var buf bytes.Buffer
	code := reportVerdict(nil,
		[]mutantVerdict{{File: "observability/logs.go", Line: 27, Status: statusTimedOut}},
		[]mutantVerdict{{File: "./observability"}}, &buf)
	if code != 1 {
		t.Errorf("reportVerdict = %d, want 1 for a vacuous package", code)
	}
	if !strings.Contains(buf.String(), "no verdict for these packages") {
		t.Errorf("output must name the vacuous packages, got: %s", buf.String())
	}
	if strings.Contains(buf.String(), "all mutants on changed lines killed") {
		t.Error("must not claim a clean sweep")
	}
}

func TestReportVerdictCleanSweepOnlyWhenFullyJudged(t *testing.T) {
	var clean, withTimeout bytes.Buffer
	if code := reportVerdict(nil, nil, nil, &clean); code != 0 {
		t.Errorf("reportVerdict = %d, want 0", code)
	}
	if !strings.Contains(clean.String(), "all mutants on changed lines killed") {
		t.Errorf("want the clean-sweep line, got: %s", clean.String())
	}
	// A timeout without a vacuous package still passes, but not as a clean sweep.
	code := reportVerdict(nil, []mutantVerdict{{File: "a.go", Line: 1, Status: statusTimedOut}}, nil, &withTimeout)
	if code != 0 {
		t.Errorf("reportVerdict = %d, want 0 (a timeout alone must not block)", code)
	}
	if strings.Contains(withTimeout.String(), "all mutants on changed lines killed") {
		t.Errorf("must not claim a clean sweep with a timeout outstanding, got: %s", withTimeout.String())
	}
}

func TestRunBlankEngineFailsFast(t *testing.T) {
	var buf bytes.Buffer
	if got := run(t.Context(), "   ", "HEAD", throttle{}, false, &buf); got != 2 {
		t.Fatalf("run = %d, want 2 for whitespace-only engine", got)
	}
}

// TestRunNoOpDiffLeavesEnvironmentAlone pins the ordering: the budget is applied
// only once there is work, so a no-op gate does not mutate the caller's GOFLAGS.
func TestRunNoOpDiffLeavesEnvironmentAlone(t *testing.T) {
	t.Setenv("GOFLAGS", "-mod=mod")
	t.Setenv("GOMAXPROCS", "sentinel")
	var buf bytes.Buffer
	if got := run(t.Context(), "false", "HEAD", throttle{cpu: 4, workers: 2}, false, &buf); got != 0 {
		t.Fatalf("run = %d, want 0; output: %s", got, buf.String())
	}
	if got := os.Getenv("GOFLAGS"); got != "-mod=mod" {
		t.Errorf("GOFLAGS = %q, want it untouched by a no-op run", got)
	}
	// Both, not just GOFLAGS: applyBudget writes two variables, so asserting one
	// leaves a hoisted budget half-detectable.
	if got := os.Getenv("GOMAXPROCS"); got != "sentinel" {
		t.Errorf("GOMAXPROCS = %q, want it untouched by a no-op run", got)
	}
}

// TestMutatePackageReportsFailureAsNotRun pins the signal the cooldown depends
// on: a package whose engine never executed mutants generated no load, so the
// caller must be able to tell it apart from a package that ran.
// TestMutatePackageSkipsWhenEngineWritesNoDryReport pins the constants-only
// package case: gremlins exits 0 without writing a report when a package has
// no mutatable statements, and the gate must skip it rather than abort the
// whole run. `true` reproduces that shape — zero exit, no report file.
func TestMutatePackageSkipsWhenEngineWritesNoDryReport(t *testing.T) {
	var buf bytes.Buffer
	_, _, ran, err := mutatePackage(t.Context(), []string{"true"}, "./internal/testutil",
		t.TempDir(), map[string][]lineRange{}, 1, &buf)
	if err != nil {
		t.Fatalf("want nil error for a zero-exit engine with no report, got %v", err)
	}
	if ran {
		t.Error("ran = true, want false when no mutants were executed")
	}
	if !strings.Contains(buf.String(), "no dry-run report") {
		t.Errorf("skip reason not reported, output: %q", buf.String())
	}
}

func TestMutatePackageReportsFailureAsNotRun(t *testing.T) {
	var buf bytes.Buffer
	// An engine that cannot run at all fails before any verdict; the point here is
	// only that the arity carries a ran flag the caller can branch on.
	_, _, ran, err := mutatePackage(t.Context(), []string{"false"}, "./scripts/mutatediff",
		t.TempDir(), map[string][]lineRange{}, 1, &buf)
	if err == nil {
		t.Fatal("want an error from an engine that cannot produce a report")
	}
	if ran {
		t.Error("ran = true, want false when the engine never executed mutants")
	}
}
