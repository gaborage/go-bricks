package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func TestComputeBudgetSplitsAcrossWorkers(t *testing.T) {
	tests := []struct {
		name    string
		cpu     int
		workers int
		want    budget
	}{
		{name: "budget_splits_evenly", cpu: 4, workers: 2, want: budget{share: 2, workers: 2}},
		{name: "single_worker_gets_whole_budget", cpu: 4, workers: 1, want: budget{share: 4, workers: 1}},
		{name: "budget_cannot_afford_two_workers", cpu: 1, workers: 2, want: budget{share: 1, workers: 1}},
		{name: "integer_division_floors_then_clamps", cpu: 3, workers: 2, want: budget{share: 1, workers: 2}},
		{name: "budget_exactly_covers_the_workers", cpu: 2, workers: 2, want: budget{share: 1, workers: 2}},
		{name: "zero_budget_means_no_budget", cpu: 0, workers: 2, want: budget{share: 0, workers: 2}},
		{name: "negative_budget_means_no_budget", cpu: -1, workers: 8, want: budget{share: 0, workers: 8}},
		{name: "budgeted_run_pins_an_unspecified_worker_count", cpu: 4, workers: 0, want: budget{share: 4, workers: 1}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := computeBudget(tc.cpu, tc.workers); got != tc.want {
				t.Errorf("computeBudget(%d, %d) = %+v, want %+v", tc.cpu, tc.workers, got, tc.want)
			}
		})
	}
}

// TestComputeBudgetNeverExceedsTheCap pins the invariant the whole guardrail
// sells: the run's real bound is workers x share, so no input may let that
// product exceed the requested cpu. Checking it as a property rather than a
// table is the point — a future edit to either term cannot satisfy this by
// happening to agree with the other.
func TestComputeBudgetNeverExceedsTheCap(t *testing.T) {
	for cpu := 1; cpu <= 16; cpu++ {
		for workers := 0; workers <= 16; workers++ {
			b := computeBudget(cpu, workers)
			if b.share < 1 || b.workers < 1 {
				t.Fatalf("computeBudget(%d, %d) = %+v, want a usable share and worker count", cpu, workers, b)
			}
			if got := b.workers * b.share; got > cpu {
				t.Errorf("computeBudget(%d, %d) = %+v, honors %d cores — over the %d requested",
					cpu, workers, b, got, cpu)
			}
		}
	}
}

func TestAppendGoflagPreservesExistingFlags(t *testing.T) {
	if got := appendGoflag("", "-p=2"); got != "-p=2" {
		t.Errorf("appendGoflag on empty = %q, want %q", got, "-p=2")
	}
	if got := appendGoflag("   ", "-p=2"); got != "-p=2" {
		t.Errorf("appendGoflag on blank = %q, want %q", got, "-p=2")
	}
	if got := appendGoflag("-mod=mod", "-p=2"); got != "-mod=mod -p=2" {
		t.Errorf("appendGoflag = %q, want %q", got, "-mod=mod -p=2")
	}
}

// TestApplyBudgetPinsChildEnvironment pins the mechanism the whole guardrail
// rests on: the values have to land in the environment children inherit, and the
// caller's own GOFLAGS must survive.
func TestApplyBudgetPinsChildEnvironment(t *testing.T) {
	t.Setenv("GOMAXPROCS", "")
	t.Setenv("GOFLAGS", "-mod=mod")

	if err := (budget{share: 2, workers: 2}).apply(); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if got := os.Getenv("GOMAXPROCS"); got != "2" {
		t.Errorf("GOMAXPROCS = %q, want %q", got, "2")
	}
	if got := os.Getenv("GOFLAGS"); got != "-mod=mod -p=2" {
		t.Errorf("GOFLAGS = %q, want %q", got, "-mod=mod -p=2")
	}
}

// TestApplyBudgetReachesAChildProcess pins the property the guardrail actually
// rests on. Asserting os.Getenv after os.Setenv only restates the stdlib; what
// matters is that a child sees the pinned values, which is what breaks if a
// future exec site sets cmd.Env explicitly.
func TestApplyBudgetReachesAChildProcess(t *testing.T) {
	t.Setenv("GOMAXPROCS", "")
	t.Setenv("GOFLAGS", "")

	if err := computeBudget(4, 2).apply(); err != nil {
		t.Fatalf("apply: %v", err)
	}
	out, err := exec.CommandContext(t.Context(), "go", "env", "GOFLAGS").Output()
	if err != nil {
		t.Fatalf("go env GOFLAGS: %v", err)
	}
	if got := strings.TrimSpace(string(out)); got != "-p=2" {
		t.Errorf("child GOFLAGS = %q, want %q", got, "-p=2")
	}
}

func TestApplyBudgetLeavesEnvironmentAloneWhenDisabled(t *testing.T) {
	t.Setenv("GOMAXPROCS", "keep-me")
	t.Setenv("GOFLAGS", "-mod=mod")

	if err := computeBudget(0, 2).apply(); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if got := os.Getenv("GOMAXPROCS"); got != "keep-me" {
		t.Errorf("GOMAXPROCS = %q, want it untouched", got)
	}
	if got := os.Getenv("GOFLAGS"); got != "-mod=mod" {
		t.Errorf("GOFLAGS = %q, want it untouched", got)
	}
}

// TestShouldCoolOnlyAfterRealWork pins both suppressions: a skipped package
// ran only a dry run, and the last package has nothing after it to protect.
func TestShouldCoolOnlyAfterRealWork(t *testing.T) {
	tests := []struct {
		name string
		ran  bool
		i    int
		n    int
		want bool
	}{
		{name: "ran_with_a_package_still_to_come", ran: true, i: 0, n: 3, want: true},
		{name: "ran_but_it_was_the_last_package", ran: true, i: 2, n: 3, want: false},
		{name: "skipped_package_ran_only_a_dry_run", ran: false, i: 0, n: 3, want: false},
		{name: "only_package_in_the_run", ran: true, i: 0, n: 1, want: false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := shouldCool(tc.ran, tc.i, tc.n); got != tc.want {
				t.Errorf("shouldCool(%v, %d, %d) = %v, want %v", tc.ran, tc.i, tc.n, got, tc.want)
			}
		})
	}
}

func TestCoolDownWaitsAndAnnouncesItself(t *testing.T) {
	var slept []time.Duration
	var buf bytes.Buffer
	th := throttle{cooldown: 30 * time.Second, sleep: func(_ context.Context, d time.Duration) error {
		slept = append(slept, d)
		return nil
	}}

	if err := th.coolDown(t.Context(), &buf); err != nil {
		t.Fatalf("coolDown: %v", err)
	}

	if len(slept) != 1 || slept[0] != 30*time.Second {
		t.Fatalf("slept = %v, want one 30s pause", slept)
	}
	if !strings.Contains(buf.String(), "cooling down 30s") {
		t.Errorf("cooldown must announce itself, got: %s", buf.String())
	}
}

func TestCoolDownIsANoOpAtZero(t *testing.T) {
	var slept []time.Duration
	var buf bytes.Buffer
	th := throttle{cooldown: 0, sleep: func(_ context.Context, d time.Duration) error {
		slept = append(slept, d)
		return nil
	}}

	if err := th.coolDown(t.Context(), &buf); err != nil {
		t.Fatalf("coolDown: %v", err)
	}

	if len(slept) != 0 {
		t.Errorf("slept = %v, want no pause at a zero cooldown", slept)
	}
	if buf.String() != "" {
		t.Errorf("a disabled cooldown must stay silent, got: %s", buf.String())
	}
}

// TestCoolDownGivesUpWhenCanceled uses the real timer, not the injected seam, so
// it exercises the wait the operator actually sits through. An hour-long
// cooldown against an already-canceled context returns at once or not at all —
// a plain time.Sleep would hang the test rather than fail it.
func TestCoolDownGivesUpWhenCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	var buf bytes.Buffer
	th := throttle{cooldown: time.Hour}

	start := time.Now()
	err := th.coolDown(ctx, &buf)

	if !errors.Is(err, context.Canceled) {
		t.Fatalf("coolDown = %v, want context.Canceled", err)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Errorf("coolDown took %s, want an immediate return on a canceled context", elapsed)
	}
}

func TestDescribeBudgetNamesTheOptOut(t *testing.T) {
	got := describeBudget(30*time.Second, computeBudget(4, 2))
	if !strings.Contains(got, "4 cores") || !strings.Contains(got, "2 workers x 2") {
		t.Errorf("budget banner = %q, want the arithmetic spelled out", got)
	}
	if !strings.Contains(got, "30s cooldown") {
		t.Errorf("budget banner = %q, want the cooldown named", got)
	}

	off := describeBudget(0, computeBudget(0, 2))
	if !strings.Contains(off, "MUTATE_CPU=0") {
		t.Errorf("disabled banner = %q, want it to name the knob that turned it off", off)
	}

	// The bound is workers x share, not the requested cpu: at cpu=3 the share
	// floors to 1, so two workers really get 2 cores, and saying "3" would be a
	// number the run does not honor.
	uneven := describeBudget(30*time.Second, computeBudget(3, 2))
	if !strings.Contains(uneven, "2 cores") || strings.Contains(uneven, "3 cores") {
		t.Errorf("budget banner = %q, want the honored bound (2 cores), not the requested 3", uneven)
	}
}
