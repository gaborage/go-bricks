# `make mutate` Thermal Guardrails Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Bound the CPU that `make mutate` consumes on a developer machine, so a mutation run stops driving the laptop to dangerous sustained temperatures.

**Architecture:** `scripts/mutatediff` gains a `throttle` value carrying a whole-run core budget, a worker count, and an inter-package cooldown. Before mutating any package it pins `GOMAXPROCS` and `GOFLAGS=-p` on its own process environment, which every descendant (`go run gremlins`, gremlins' coverage pass, `measureSuite`'s timing passes, each mutant's `go test`) inherits. Between packages it sleeps for the cooldown. The Makefile supplies both values from new `?=` knobs; `make mutate-baseline` and CI are untouched.

**Tech Stack:** Go 1.26, standard library only (`flag`, `os`, `strconv`, `strings`, `time`). No new dependencies. GNU Make. gremlins v0.5.1 (pinned, unchanged).

**Design spec:** `docs/superpowers/specs/2026-08-03-mutate-thermal-guardrails-design.md`

## Global Constraints

- Go 1.26. Standard library only — do not add a dependency.
- Test function names are **camelCase**: `TestPerChildSplitsBudgetAcrossWorkers`, never `TestPerChild_SplitsBudget`. Table-driven case names are **snake_case**: `{name: "budget_splits_evenly"}`.
- Source-to-test 1:1 naming: `throttle.go` gets `throttle_test.go`.
- Comments are bare-minimum: only non-obvious intent. Do not narrate what the code plainly says.
- `make check` must pass before every commit. Run it after each task.
- `scripts/` is excluded from the mutation gate's scope (`wiki/testing.md#mutation-gate`), so `make mutate` will not judge this change. Do not chase mutation coverage here.
- Do not modify `make mutate-baseline`, `.github/workflows/mutation-nightly.yml`, or `.gremlins.yaml`. CI runs at full speed by design.
- Do not apply the budget on the `-coefficient` code path (`printCoefficient`). That path serves `make mutate-baseline`, whose mutants run unbudgeted; throttling only its measurement would inflate every ceiling CI computes.

## File Structure

| File | Status | Responsibility |
|---|---|---|
| `scripts/mutatediff/throttle.go` | create | The `throttle` type, budget derivation, environment pinning, cooldown, and the one-line budget banner. Everything resource-posture related lives here and nowhere else. |
| `scripts/mutatediff/throttle_test.go` | create | Unit tests for the above. |
| `scripts/mutatediff/main.go` | modify | Two new flags; `run` takes a `throttle`; budget applied before the package loop; cooldown placed inside it; `mutatePackage` reports whether it ran. |
| `scripts/mutatediff/main_test.go` | modify | Two existing `run(...)` call sites gain the new argument. |
| `Makefile` | modify | `MUTATE_CPU` and `MUTATE_COOLDOWN` knobs; pass both to `mutatediff`; correct the `MUTATE_WORKERS` comment, which currently implies it is a core count. |
| `wiki/testing.md` | modify | Knobs table gains two rows; the `MUTATE_WORKERS` row is corrected. |

---

### Task 1: Budget derivation and environment pinning

**Files:**

- Create: `scripts/mutatediff/throttle.go`
- Test: `scripts/mutatediff/throttle_test.go`

**Interfaces:**

- Consumes: nothing from earlier tasks.
- Produces:
  - `type throttle struct { cpu int; workers int; cooldown time.Duration; sleep func(time.Duration) }`
  - `func perChild(cpu, workers int) int`
  - `func appendGoflag(existing, flag string) string`
  - `func applyBudget(cpu, workers int) (share int, err error)`
  - `func (th throttle) coolDown(out io.Writer)`
  - `func shouldCool(ran bool, i, n int) bool`
  - `func describeBudget(th throttle, share int) string`

- [ ] **Step 1: Write the failing tests**

Create `scripts/mutatediff/throttle_test.go`:

```go
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
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./scripts/mutatediff/ -run 'PerChild|AppendGoflag|ApplyBudget|ShouldCool|CoolDown|DescribeBudget' -v`

Expected: FAIL to build, with `undefined: perChild`, `undefined: appendGoflag`, `undefined: applyBudget`, `undefined: shouldCool`, `undefined: throttle`, `undefined: describeBudget`.

- [ ] **Step 3: Write the implementation**

Create `scripts/mutatediff/throttle.go`:

```go
package main

import (
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"
)

const (
	// gomaxprocsEnv bounds the runtime threads of every `go test` below this
	// process, and with them the -parallel limit inside each test binary.
	gomaxprocsEnv = "GOMAXPROCS"
	// goflagsEnv carries -p, which bounds how many build actions the go command
	// runs at once. Its documented default is already GOMAXPROCS; pinning it
	// explicitly does not depend on that staying true.
	goflagsEnv = "GOFLAGS"
)

// throttle is the local gate's resource posture: a whole-run core budget divided
// across the workers that share it, plus a pause between packages. A cpu of 0
// disables the budget, which is what a direct invocation and CI both want.
type throttle struct {
	cpu      int
	workers  int
	cooldown time.Duration
	// sleep is injected so tests assert the cooldown's placement without waiting.
	sleep func(time.Duration)
}

// perChild splits the whole-run budget across concurrent workers. It returns 0
// for "no budget" and never a share below 1.
func perChild(cpu, workers int) int {
	if cpu <= 0 {
		return 0
	}
	if workers < 1 {
		workers = 1
	}
	if share := cpu / workers; share > 1 {
		return share
	}
	return 1
}

// appendGoflag adds one flag without discarding what is already in GOFLAGS. The
// variable is space-separated and the go command lets a later value win, so
// appending preserves a caller's flags and still overrides a conflicting -p.
func appendGoflag(existing, flag string) string {
	if strings.TrimSpace(existing) == "" {
		return flag
	}
	return existing + " " + flag
}

// applyBudget pins the share on this process's environment so every descendant
// inherits it: `go run gremlins`, gremlins' own coverage pass, measureSuite's
// timing passes, and every mutant's `go test`.
//
// Uniformity is the point. The per-mutant ceiling is
// (real suite + build budget) / cached replay, so measuring that ratio under a
// different budget than the mutants run under makes every ceiling wrong. One
// environment, set once, makes that desync impossible.
//
// Setting GOMAXPROCS here does not change this process's own scheduler, which
// read the variable at init — it reaches children only, which is the wanted scope.
func applyBudget(cpu, workers int) (int, error) {
	share := perChild(cpu, workers)
	if share == 0 {
		return 0, nil
	}
	value := strconv.Itoa(share)
	if err := os.Setenv(gomaxprocsEnv, value); err != nil {
		return 0, fmt.Errorf("pin %s: %w", gomaxprocsEnv, err)
	}
	if err := os.Setenv(goflagsEnv, appendGoflag(os.Getenv(goflagsEnv), "-p="+value)); err != nil {
		return 0, fmt.Errorf("pin %s: %w", goflagsEnv, err)
	}
	return share, nil
}

// shouldCool reports whether a cooldown belongs after the package at index i of
// n: nothing to recover from when the package was skipped, nothing to protect
// after the last one.
func shouldCool(ran bool, i, n int) bool {
	return ran && i < n-1
}

// coolDown pauses so the machine sheds heat before the next package's mutants.
func (th throttle) coolDown(out io.Writer) {
	if th.cooldown <= 0 {
		return
	}
	fmt.Fprintf(out, "mutatediff: cooling down %s before the next package\n", th.cooldown)
	if th.sleep != nil {
		th.sleep(th.cooldown)
		return
	}
	time.Sleep(th.cooldown)
}

// describeBudget is printed once per run so an overridden budget is visible in a
// scrollback or a pasted transcript.
func describeBudget(th throttle, share int) string {
	if share == 0 {
		return "mutatediff: no CPU budget (MUTATE_CPU=0) — using the machine default"
	}
	return fmt.Sprintf("mutatediff: CPU budget %d cores (%d workers x %d), %s cooldown between packages",
		th.cpu, max(th.workers, 1), share, th.cooldown)
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./scripts/mutatediff/ -run 'PerChild|AppendGoflag|ApplyBudget|ShouldCool|CoolDown|DescribeBudget' -v`

Expected: PASS, every subtest.

- [ ] **Step 5: Run the full check**

Run: `make check`

Expected: PASS. If `golangci-lint` reports stale findings under a deleted worktree path, re-run with `LINT_CLEAN=1 make check`.

- [ ] **Step 6: Commit**

```bash
git add scripts/mutatediff/throttle.go scripts/mutatediff/throttle_test.go
git commit -m "feat(mutatediff): add a CPU budget and cooldown posture

MUTATE_WORKERS never bounded CPU use: each worker shells out a go test,
which compiles at -p=NumCPU and runs at GOMAXPROCS=NumCPU. This adds the
throttle type that derives a per-child share from a whole-run budget and
pins it on the process environment every descendant inherits.

Not yet wired into run() — that is the next commit.

Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>"
```

---

### Task 2: Wire the budget into the gate

**Files:**

- Modify: `scripts/mutatediff/main.go:17-46` (flags and dispatch), `scripts/mutatediff/main.go:48-94` (`run`)
- Modify: `scripts/mutatediff/main_test.go:11`, `scripts/mutatediff/main_test.go:77`

**Interfaces:**

- Consumes: `throttle`, `applyBudget`, `describeBudget` from Task 1.
- Produces: `func run(ctx context.Context, engine, baseRef string, th throttle, out io.Writer) int` — the `workers int` parameter is replaced by the `throttle`, which carries it.

- [ ] **Step 1: Update the existing tests to the new signature (they must fail to build first)**

In `scripts/mutatediff/main_test.go`, change the two `run` call sites:

```go
	if got := run(t.Context(), "false", "HEAD", throttle{}, &buf); got != 0 {
```

```go
	if got := run(t.Context(), "   ", "HEAD", throttle{}, &buf); got != 2 {
```

Then add a test pinning that a no-op run does not touch the environment — a run with nothing to mutate should not leave `GOFLAGS` altered for whatever the developer does next:

```go
// TestRunNoOpDiffLeavesEnvironmentAlone pins the ordering: the budget is applied
// only once there is work, so a no-op gate does not mutate the caller's GOFLAGS.
func TestRunNoOpDiffLeavesEnvironmentAlone(t *testing.T) {
	t.Setenv("GOFLAGS", "-mod=mod")
	var buf bytes.Buffer
	if got := run(t.Context(), "false", "HEAD", throttle{cpu: 4, workers: 2}, &buf); got != 0 {
		t.Fatalf("run = %d, want 0; output: %s", got, buf.String())
	}
	if got := os.Getenv("GOFLAGS"); got != "-mod=mod" {
		t.Errorf("GOFLAGS = %q, want it untouched by a no-op run", got)
	}
}
```

Add `"os"` to `main_test.go`'s import block.

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./scripts/mutatediff/ -run 'TestRun' -v`

Expected: FAIL to build, with `too many arguments in call to run` / `cannot use throttle{} (value of struct type throttle) as int value`.

- [ ] **Step 3: Add the flags and change `run`'s signature**

In `scripts/mutatediff/main.go`, add two flags after the existing `workers` flag:

```go
	workers := flag.Int("workers", 0, "engine workers; each is a concurrent `go test`. 0 inherits .gremlins.yaml")
	cpu := flag.Int("cpu", 0, "whole-run core budget divided across workers; 0 leaves the machine default")
	cooldown := flag.Duration("cooldown", 0, "pause between packages so the machine sheds heat; 0 disables")
```

Change the default dispatch arm:

```go
	default:
		code = run(ctx, *engine, *base, throttle{cpu: *cpu, workers: *workers, cooldown: *cooldown}, os.Stdout)
```

Change `run`'s signature and apply the budget after the no-op early return, before the package loop:

```go
func run(ctx context.Context, engine, baseRef string, th throttle, out io.Writer) int {
```

```go
	if len(changed) == 0 {
		fmt.Fprintln(out, "mutatediff: no mutatable changes vs merge-base")
		return 0
	}
	// After the no-op return: a gate with nothing to do must not leave the
	// caller's GOFLAGS rewritten.
	share, budgetErr := applyBudget(th.cpu, th.workers)
	if budgetErr != nil {
		return fail("%v", budgetErr)
	}
	fmt.Fprintln(out, describeBudget(th, share))
	reportDir, err := os.MkdirTemp("", "mutatediff-*")
```

Change the single `mutatePackage` call inside the loop to pass `th.workers` instead of `workers`:

```go
		f, w, mErr := mutatePackage(ctx, engineArgs, pkg, reportDir, changed, th.workers, out)
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./scripts/mutatediff/ -v`

Expected: PASS, all tests.

- [ ] **Step 5: Verify the no-op path returns before touching the environment**

Run: `go run ./scripts/mutatediff -engine "false" -cpu 4 -workers 2 -base HEAD`

Expected: prints `mutatediff: no mutatable changes vs merge-base` and nothing about a budget — the no-op path returns before applying it. This confirms Step 3's ordering. The budget reaching a real child is verified in Task 5, Step 3.

- [ ] **Step 6: Run the full check**

Run: `make check`

Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add scripts/mutatediff/main.go scripts/mutatediff/main_test.go
git commit -m "feat(mutatediff): apply the CPU budget to every engine child

run() now takes a throttle and pins GOMAXPROCS plus GOFLAGS -p before the
package loop, so gremlins, its coverage pass, measureSuite's timing passes,
and every mutant's go test share one budget. Applying it uniformly is what
keeps the per-mutant ceiling arithmetic valid: that ratio divides the real
suite by a cache-served replay, so measuring the two under different budgets
would corrupt every ceiling.

The budget lands after the no-op early return, so a gate with nothing to
mutate leaves the caller's GOFLAGS untouched. printCoefficient is
deliberately excluded: it serves make mutate-baseline, whose mutants run
unbudgeted in CI.

Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>"
```

---

### Task 3: Cool down between packages

**Files:**

- Modify: `scripts/mutatediff/main.go:82-92` (the package loop), `scripts/mutatediff/main.go:178-214` (`mutatePackage`)

**Interfaces:**

- Consumes: `shouldCool`, `throttle.coolDown` from Task 1; the `throttle`-taking `run` from Task 2.
- Produces: `func mutatePackage(ctx context.Context, engineArgs []string, pkg, reportDir string, changed map[string][]lineRange, workers int, out io.Writer) (failures, warnings []mutantVerdict, ran bool, err error)` — the new third return reports whether the engine actually executed mutants for this package.

- [ ] **Step 1: Write the failing test**

The loop needs `mutatePackage` to distinguish a package it ran from one it skipped. Add to `scripts/mutatediff/main_test.go`:

```go
// TestMutatePackageReportsFailureAsNotRun pins the signal the cooldown depends
// on: a package whose engine never executed mutants generated no load, so the
// caller must be able to tell it apart from a package that ran.
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
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./scripts/mutatediff/ -run TestMutatePackageReportsFailureAsNotRun -v`

Expected: FAIL to build, with `assignment mismatch: 4 variables but mutatePackage returns 3 values`.

- [ ] **Step 3: Add the `ran` return and place the cooldown**

In `scripts/mutatediff/main.go`, change `mutatePackage`'s signature and all four of its return statements:

```go
func mutatePackage(ctx context.Context, engineArgs []string, pkg, reportDir string, changed map[string][]lineRange, workers int, out io.Writer) (failures, warnings []mutantVerdict, ran bool, err error) {
```

```go
	dryJSON, err := runEngine(ctx, engineArgs, pkg, reportPathFor(pkg, reportDir)+".dry",
		[]string{"--dry-run", workersFlag, "1"}, out)
	if err != nil {
		return nil, nil, false, err
	}
	onChanged, cErr := countOnChangedLines(dryJSON, pkg, changed)
	if cErr != nil {
		return nil, nil, false, fmt.Errorf("parse dry-run report for %s: %w", pkg, cErr)
	}
	if onChanged == 0 {
		fmt.Fprintf(out, "mutatediff: %s has no mutants on changed lines, skipping\n", pkg)
		return nil, nil, false, nil
	}
```

```go
	reportJSON, err := runEngine(ctx, engineArgs, pkg, reportPathFor(pkg, reportDir),
		slices.Concat(gremlinsTimeoutArgs(coefficient), workerArgs(workers)), out)
	if err != nil {
		return nil, nil, false, err
	}
	f, w, jerr := judge(reportJSON, pkg, changed)
	if jerr != nil {
		return nil, nil, false, fmt.Errorf("parse report for %s: %w", pkg, jerr)
	}
	return f, w, true, nil
```

Then change the loop in `run` to index the package list and cool down after real work:

```go
	var failures, warnings, unjudged []mutantVerdict
	pkgs := packagesOf(changed)
	for i, pkg := range pkgs {
		f, w, ran, mErr := mutatePackage(ctx, engineArgs, pkg, reportDir, changed, th.workers, out)
		if mErr != nil {
			return fail("%v", mErr)
		}
		failures = append(failures, f...)
		warnings = append(warnings, w...)
		if vacuousPkg(pkg, reportDir, out) {
			unjudged = append(unjudged, mutantVerdict{File: pkg})
		}
		if shouldCool(ran, i, len(pkgs)) {
			th.coolDown(out)
		}
	}
	return reportVerdict(failures, warnings, unjudged, out)
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./scripts/mutatediff/ -v`

Expected: PASS, all tests.

- [ ] **Step 5: Run the full check**

Run: `make check`

Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add scripts/mutatediff/main.go scripts/mutatediff/main_test.go
git commit -m "feat(mutatediff): cool down between mutated packages

mutatePackage now reports whether it executed mutants, so the loop can pause
only after work that actually loaded the CPU. The pause is suppressed after a
skipped package and after the last one, where there is nothing left to protect.

This is relief between packages, not a duty cycle: mutatediff drives gremlins
once per package and cannot interrupt its internal mutant loop, so a single
dominant package is still bounded only by the core budget.

Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>"
```

---

### Task 4: Makefile knobs and documentation

**Files:**

- Modify: `Makefile:26-32` (the `MUTATE_*` knob block), `Makefile:126-127` (the `mutate` target)
- Modify: `wiki/testing.md` (the knobs table under `## Mutation Gate`)

**Interfaces:**

- Consumes: the `-cpu` and `-cooldown` flags from Task 2.
- Produces: `MUTATE_CPU` and `MUTATE_COOLDOWN` make variables.

- [ ] **Step 1: Replace the `MUTATE_WORKERS` comment block in the Makefile**

Find this block (immediately above `MUTATE_WORKERS ?= 2`):

```make
# `make mutate` runs on a developer's machine, where every worker is a concurrent
# `go test`. Now that mutants run to completion instead of dying at a too-tight
# ceiling, that is sustained load, so the local gate is deliberately gentler than
# .gremlins.yaml's 4: raise it for a faster gate, drop it to 1 to keep a laptop cool.
MUTATE_WORKERS ?= 2
```

Replace it with:

```make
# `make mutate` runs on a developer's machine and holds the CPU busy for the whole
# run, which is what heat-soaks a laptop. MUTATE_WORKERS alone never bounded that:
# each worker shells out a `go test`, which compiles at `-p=GOMAXPROCS` and runs its
# binary at GOMAXPROCS, both defaulting to the machine's core count — so 2 workers
# admit far more than 2 cores' worth of work.
#
# MUTATE_CPU is the actual cap. mutatediff divides it by MUTATE_WORKERS and pins
# GOMAXPROCS plus GOFLAGS -p on every child process, which bounds test execution
# exactly and build fan-out approximately (compile processes nest one level).
# Set MUTATE_CPU=0 to opt out and run at full speed.
MUTATE_CPU ?= 4
MUTATE_WORKERS ?= 2
# Pause after each mutated package so the chassis sheds heat before the next one.
# Any time.ParseDuration string; 0 disables. It does nothing inside a single long
# package — for those, speeding up the slowest tests is the lever.
MUTATE_COOLDOWN ?= 30s
```

- [ ] **Step 2: Pass the new flags in the `mutate` target**

Change:

```make
mutate: ## Diff-scoped mutation gate: mutants on changed lines vs origin/main must die (see wiki/testing.md#mutation-gate)
	go run ./scripts/mutatediff -engine "$(GREMLINS_CMD)" -workers $(MUTATE_WORKERS)
```

to:

```make
mutate: ## Diff-scoped mutation gate: mutants on changed lines vs origin/main must die (see wiki/testing.md#mutation-gate)
	go run ./scripts/mutatediff -engine "$(GREMLINS_CMD)" -workers $(MUTATE_WORKERS) -cpu $(MUTATE_CPU) -cooldown $(MUTATE_COOLDOWN)
```

Leave `mutate-baseline` and every `MUTATE_BASELINE_*` variable untouched.

- [ ] **Step 3: Verify make wiring**

Run: `make mutate MUTATE_CPU=0`

Expected: either `mutatediff: no mutatable changes vs merge-base` (if the branch has no Go changes vs `origin/main`) or the gate proceeding with `mutatediff: no CPU budget (MUTATE_CPU=0) — using the machine default`. Either proves the flags parse; a `flag provided but not defined` error proves they do not.

Then run: `make mutate MUTATE_COOLDOWN=bogus`

Expected: FAIL with `invalid value "bogus" for flag -cooldown`. A bad duration must be loud, not silently ignored.

- [ ] **Step 4: Update the wiki knobs table**

In `wiki/testing.md`, under `## Mutation Gate`, replace the knobs table with:

```markdown
| Variable | Default | Effect |
|---|---|---|
| `MUTATE_CPU` | 4 | Whole-run core budget for `make mutate`, divided across `MUTATE_WORKERS` and pinned as `GOMAXPROCS`/`GOFLAGS -p` on every child. `0` opts out. |
| `MUTATE_WORKERS` | 2 | Concurrent gremlins workers for `make mutate`. **Not** a core count — each worker is a full `go test`, whose own parallelism `MUTATE_CPU` is what bounds. |
| `MUTATE_COOLDOWN` | 30s | Pause after each mutated package so the machine sheds heat. Any `time.ParseDuration` string; `0` disables. Skipped after a skipped package and after the last one. |
| `MUTATE_BASELINE_WORKERS` | 2 | Same as `MUTATE_WORKERS`, for the nightly baseline; also bounds peak memory. Unbudgeted — CI runs at full speed. |
| `MUTATE_CEILING_FLOOR` | 30s | Minimum per-mutant ceiling (any `time.ParseDuration` string). |
| `MUTATE_FALLBACK_COEFFICIENT` | 600 | Used only when a package's coefficient cannot be computed. |
```

Then add this paragraph immediately after the table:

```markdown
The budget is applied once, on `mutatediff`'s own environment, so gremlins, its
coverage pass, `measureSuite`'s timing passes, and every mutant's `go test` all
inherit the same share. That uniformity is load-bearing rather than tidy: the
per-mutant ceiling divides the real suite by a cache-served replay, so measuring
the two under different budgets would corrupt every ceiling. The `-coefficient`
path is deliberately excluded, because it serves `make mutate-baseline`, whose
mutants run unbudgeted.

The cooldown gives no relief inside a single package — `mutatediff` drives
gremlins once per package and cannot interrupt its internal mutant loop. For a
package that dominates a run, speeding up its slowest tests remains the lever.
```

- [ ] **Step 5: Run the full check**

Run: `make check`

Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add Makefile wiki/testing.md
git commit -m "docs(testing): document the mutate CPU budget and cooldown

Adds MUTATE_CPU (default 4) and MUTATE_COOLDOWN (default 30s), and corrects
the MUTATE_WORKERS description in both the Makefile and the wiki: it reads as
a core count today, which is what let a two-worker run saturate a twelve-core
machine.

Also records the two boundaries worth knowing: the budget bounds test
execution exactly but build fan-out only approximately, since compile
processes nest; and the cooldown does nothing inside a single long package.

Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>"
```

---

### Task 5: Pre-push gates

**Files:** none — verification only.

- [ ] **Step 1: Run the gates in order**

Per `CLAUDE.md`, run `/simplify`, then `/security-audit`, then `/code-review`, in that order. `/simplify` mutates the diff, so it must run before anything that judges the diff. Re-run `make check` after any gate that changes code.

- [ ] **Step 2: Run the mutation gate**

Run: `make mutate`

Expected: `mutatediff: no mutatable changes vs merge-base`, or a skip for every package. `scripts/` is excluded from the gate's scope, so this change is not self-judged. If it reports otherwise, something in `mutationScope` changed and needs investigating before pushing.

- [ ] **Step 3: Confirm the guardrail on a real run**

The gate skips its own package, so exercise the budget against a package it will mutate. From a scratch branch with a one-line change in a small package, run `make mutate` and confirm the banner appears and `top`/Activity Monitor shows the run bounded near four cores rather than saturating twelve.

This is the only step that validates the actual goal. Everything before it validates the mechanism.

- [ ] **Step 4: Push and open the PR**

The whole diff is well under 400 changed LoC, so this ships as a single PR, not a stack.
