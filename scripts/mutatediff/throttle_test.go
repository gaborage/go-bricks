package main

import (
	"bytes"
	"os"
	"strings"
	"testing"
	"time"
)

func TestPerChildSplitsBudgetAcrossWorkers(t *testing.T) {
	tests := []struct {
		name    string
		cpu     int
		workers int
		want    int
	}{
		{name: "budget_splits_evenly", cpu: 4, workers: 2, want: 2},
		{name: "single_worker_gets_whole_budget", cpu: 4, workers: 1, want: 4},
		{name: "share_never_drops_below_one", cpu: 1, workers: 2, want: 1},
		{name: "integer_division_floors_then_clamps", cpu: 3, workers: 2, want: 1},
		{name: "zero_budget_means_no_budget", cpu: 0, workers: 2, want: 0},
		{name: "negative_budget_means_no_budget", cpu: -1, workers: 2, want: 0},
		{name: "zero_workers_treated_as_one", cpu: 4, workers: 0, want: 4},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := perChild(tc.cpu, tc.workers); got != tc.want {
				t.Errorf("perChild(%d, %d) = %d, want %d", tc.cpu, tc.workers, got, tc.want)
			}
		})
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

	share, err := applyBudget(4, 2)
	if err != nil {
		t.Fatalf("applyBudget: %v", err)
	}
	if share != 2 {
		t.Errorf("share = %d, want 2", share)
	}
	if got := os.Getenv("GOMAXPROCS"); got != "2" {
		t.Errorf("GOMAXPROCS = %q, want %q", got, "2")
	}
	if got := os.Getenv("GOFLAGS"); got != "-mod=mod -p=2" {
		t.Errorf("GOFLAGS = %q, want %q", got, "-mod=mod -p=2")
	}
}

func TestApplyBudgetLeavesEnvironmentAloneWhenDisabled(t *testing.T) {
	t.Setenv("GOMAXPROCS", "keep-me")
	t.Setenv("GOFLAGS", "-mod=mod")

	share, err := applyBudget(0, 2)
	if err != nil {
		t.Fatalf("applyBudget: %v", err)
	}
	if share != 0 {
		t.Errorf("share = %d, want 0", share)
	}
	if got := os.Getenv("GOMAXPROCS"); got != "keep-me" {
		t.Errorf("GOMAXPROCS = %q, want it untouched", got)
	}
	if got := os.Getenv("GOFLAGS"); got != "-mod=mod" {
		t.Errorf("GOFLAGS = %q, want it untouched", got)
	}
}

// TestShouldCoolOnlyAfterRealWork pins both suppressions: a skipped package
// generated no load, and the last package has nothing after it to protect.
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
		{name: "skipped_package_generated_no_load", ran: false, i: 0, n: 3, want: false},
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
	th := throttle{cooldown: 30 * time.Second, sleep: func(d time.Duration) { slept = append(slept, d) }}

	th.coolDown(&buf)

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
	th := throttle{cooldown: 0, sleep: func(d time.Duration) { slept = append(slept, d) }}

	th.coolDown(&buf)

	if len(slept) != 0 {
		t.Errorf("slept = %v, want no pause at a zero cooldown", slept)
	}
	if buf.String() != "" {
		t.Errorf("a disabled cooldown must stay silent, got: %s", buf.String())
	}
}

func TestDescribeBudgetNamesTheOptOut(t *testing.T) {
	th := throttle{cpu: 4, workers: 2, cooldown: 30 * time.Second}
	got := describeBudget(th, 2)
	if !strings.Contains(got, "4 cores") || !strings.Contains(got, "2 workers x 2") {
		t.Errorf("budget banner = %q, want the arithmetic spelled out", got)
	}
	if !strings.Contains(got, "30s cooldown") {
		t.Errorf("budget banner = %q, want the cooldown named", got)
	}

	off := describeBudget(throttle{}, 0)
	if !strings.Contains(off, "MUTATE_CPU=0") {
		t.Errorf("disabled banner = %q, want it to name the knob that turned it off", off)
	}
}
