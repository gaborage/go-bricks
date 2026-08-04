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
	return max(1, cpu/workers)
}

// effectiveWorkers shrinks the worker count when the budget cannot afford one
// core each, so the whole-run cap holds instead of being multiplied by workers.
func effectiveWorkers(cpu, workers int) int {
	if cpu <= 0 {
		return workers
	}
	return min(workers, cpu)
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
// n: a skipped package paid only a dry-run coverage pass, not a mutant loop, so
// there is nothing to recover from; nothing to protect after the last one either.
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
// scrollback or a pasted transcript. The leading number is workers x share, the
// bound the run actually honors, not the requested th.cpu — those two disagree
// whenever cpu does not divide evenly across workers.
func describeBudget(th throttle, share, workers int) string {
	if share == 0 {
		return "mutatediff: no CPU budget (MUTATE_CPU=0) — using the machine default"
	}
	return fmt.Sprintf("mutatediff: CPU budget %d cores (%d workers x %d), %s cooldown between packages",
		workers*share, workers, share, th.cooldown)
}
