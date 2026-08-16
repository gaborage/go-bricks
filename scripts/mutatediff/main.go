package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
)

func main() {
	engine := flag.String("engine", "", "mutation engine command prefix, e.g. 'go run github.com/go-gremlins/gremlins/cmd/gremlins@v0.5.0'")
	base := flag.String("base", "origin/main", "ref to compute merge-base against")
	mergeDir := flag.String("merge", "", "merge mode: directory of per-package shard reports to aggregate (skips the diff gate)")
	mergeOut := flag.String("out", "gremlins-report.json", "merge mode: output path for the aggregated report")
	coeffPkg := flag.String("coefficient", "", "coefficient mode: print the engine timeout-coefficient that holds this package's per-mutant ceiling at the floor")
	workers := flag.Int("workers", 0, "engine workers; each is a concurrent `go test`. 0 inherits .gremlins.yaml")
	cpu := flag.Int("cpu", 0, "whole-run core budget divided across workers; 0 leaves the machine default")
	cooldown := flag.Duration("cooldown", 0, "pause between packages so the machine sheds heat; 0 disables")
	noCache := flag.Bool("no-cache", false, "ignore and do not write the result cache; re-run every package in the diff")
	flag.Parse()

	// 0 is the documented opt-out for both guardrails, so a negative value is a
	// typo that would silently disable one rather than tighten it.
	if *cpu < 0 || *cooldown < 0 {
		fmt.Fprintln(os.Stderr, "mutatediff: -cpu and -cooldown must not be negative (0 disables the guardrail)")
		os.Exit(2)
	}

	// Signal-derived so Ctrl-C or a CI cancel reaches the long-running git,
	// go test, and gremlins subprocesses instead of leaving orphaned trees.
	// No defer: os.Exit below skips deferred calls, so stop is invoked
	// explicitly on every path instead.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)

	var code int
	switch {
	case *mergeDir != "":
		code = mergeShards(*mergeDir, *mergeOut, os.Stdout)
	case *coeffPkg != "":
		code = printCoefficient(ctx, *coeffPkg, measureSuite, ceilingFloor(), os.Stdout, os.Stderr)
	case *engine == "":
		fmt.Fprintln(os.Stderr, "mutatediff: -engine is required")
		code = 2
	default:
		code = run(ctx, *engine, *base, throttle{cpu: *cpu, workers: *workers, cooldown: *cooldown}, !*noCache, os.Stdout)
	}
	stop()
	os.Exit(code)
}

func run(ctx context.Context, engine, baseRef string, th throttle, useCache bool, out io.Writer) (code int) {
	engineArgs := strings.Fields(engine)
	if len(engineArgs) == 0 {
		return fail("engine command is blank")
	}
	mergeBase, err := gitOutput(ctx, "merge-base", "HEAD", baseRef)
	if err != nil {
		return fail("%v", err)
	}
	diff, err := gitOutput(ctx, "diff", "-U0", "--no-color", mergeBase, "HEAD", "--", "*.go")
	if err != nil {
		return fail("%v", err)
	}
	parsed, perr := parseUnifiedDiff(diff)
	if perr != nil {
		return fail("%v", perr)
	}
	changed := mutationScope(parsed)
	switch status, statusErr := gitOutput(ctx, "status", "--porcelain"); {
	case statusErr != nil:
		fmt.Fprintf(out, "WARN: could not check the working tree for uncommitted changes: %v\n", statusErr)
	case status != "":
		fmt.Fprintln(out, "WARN: uncommitted changes detected — the engine reads the working tree, but scope comes from committed merge-base..HEAD")
	}
	if len(changed) == 0 {
		fmt.Fprintln(out, "mutatediff: no mutatable changes vs merge-base")
		return 0
	}
	// Snapshotted before apply rewrites it: the cache key covers the GOFLAGS the
	// caller chose (which can carry -tags), not the -p this process adds.
	callerGoflags := os.Getenv(goflagsEnv)
	// After the no-op return: a gate with nothing to do must not leave the
	// caller's GOFLAGS rewritten.
	b := computeBudget(th.cpu, th.workers)
	if budgetErr := b.apply(); budgetErr != nil {
		return fail("%v", budgetErr)
	}
	fmt.Fprintln(out, describeBudget(th.cooldown, b))
	// After the sandbox env is pinned, every temp path below — the report dir,
	// gremlins' working copies, measureSuite's coverage scratch — lands inside
	// the per-run root, and every child build lands in the dedicated cache.
	sb, sbErr := setupSandbox(ctx, out)
	if sbErr != nil {
		return fail("%v", sbErr)
	}
	// A failed cleanup flips a clean verdict to failure — the gate's contract
	// includes leaving the machine tidy — but never masks a real failure code.
	defer func() {
		if cleanErr := sb.cleanup(out); cleanErr != nil && code == 0 {
			code = fail("sandbox cleanup: %v", cleanErr)
		}
	}()
	// Explicitly inside the per-run root, so sb.cleanup owns its removal and
	// the containment survives reordering.
	reportDir := filepath.Join(sb.runTmp, "reports")
	if mkErr := os.MkdirAll(reportDir, 0o750); mkErr != nil {
		return fail("%v", mkErr)
	}
	gr := &gateRun{
		engineArgs: engineArgs,
		reportDir:  reportDir,
		changed:    changed,
		workers:    b.workers,
		th:         th,
		cache:      resolveCache(ctx, useCache, engine, callerGoflags, out),
		out:        out,
	}
	v, gateErr := gr.mutateAll(ctx, packagesOf(changed))
	if gateErr != nil {
		return fail("%v", gateErr)
	}
	return reportVerdict(v.failures, v.warnings, v.unjudged, out)
}

// resolveCache keeps the disabled path visible in the transcript: a cached run
// and an uncached one must never be confused for each other, in either direction.
func resolveCache(ctx context.Context, useCache bool, engine, callerGoflags string, out io.Writer) *resultCache {
	if !useCache {
		fmt.Fprintln(out, "mutatediff: result cache disabled (-no-cache) — every package in the diff will run")
		return nil
	}
	return newResultCache(ctx, engine, callerGoflags, out)
}

// verdictSet is one run's accumulated judgment, split the way reportVerdict
// consumes it.
type verdictSet struct {
	failures []mutantVerdict
	warnings []mutantVerdict
	unjudged []mutantVerdict
}

// gateRun is the per-run state the package loop threads through.
type gateRun struct {
	engineArgs []string
	reportDir  string
	changed    map[string][]lineRange
	workers    int
	th         throttle
	cache      *resultCache
	out        io.Writer
}

// mutateAll walks the changed packages, consulting the result cache before each
// one and populating it only from a package whose changed lines came back
// entirely clean. "Entirely" is the load-bearing word: a survivor fails the
// gate, and a NOT COVERED or TIMED OUT mutant or a vacuous package would have to
// be replayed for the report to stay honest — so none of them are stored, and a
// hit therefore contributes nothing at all to the verdict.
func (r *gateRun) mutateAll(ctx context.Context, pkgs []string) (verdictSet, error) {
	var v verdictSet
	for i, pkg := range pkgs {
		if r.cache.hit(pkg, r.changed, r.out) {
			fmt.Fprintf(r.out, "mutatediff: %s cached PASS (skipped)\n", pkg)
			continue
		}
		f, w, ran, mErr := mutatePackage(ctx, r.engineArgs, pkg, r.reportDir, r.changed, r.workers, r.out)
		if mErr != nil {
			return verdictSet{}, mErr
		}
		v.failures = append(v.failures, f...)
		v.warnings = append(v.warnings, w...)
		isVacuous := vacuousPkg(pkg, r.reportDir, r.out)
		if isVacuous {
			v.unjudged = append(v.unjudged, mutantVerdict{File: pkg})
		}
		if len(f) == 0 && len(w) == 0 && !isVacuous {
			r.cache.store(pkg, r.changed, r.out)
		}
		if shouldCool(ran, i, len(pkgs)) {
			// A cooldown cut short means the run was canceled, not that the next
			// package is ready: reporting a verdict over packages that never ran
			// would call an interrupted gate clean.
			if coolErr := r.th.coolDown(ctx, r.out); coolErr != nil {
				return verdictSet{}, fmt.Errorf("canceled during cooldown: %w", coolErr)
			}
		}
	}
	return v, nil
}

// reportVerdict prints every result and returns the process exit code. Vacuous
// packages are checked before surviving mutants: if the engine judged nothing,
// an empty failure list means nothing, and saying so is the point.
func reportVerdict(failures, warnings, unjudged []mutantVerdict, out io.Writer) int {
	timedOut := reportWarnings(warnings, out)
	if len(unjudged) > 0 {
		fmt.Fprintln(out, "FAIL the engine returned no verdict for these packages — every mutant timed out, so nothing was tested:")
		for _, p := range unjudged {
			fmt.Fprintf(out, "  %s\n", p.File)
		}
		fmt.Fprintf(out, "raise %s (currently %s), or re-run with MUTATE_CPU=0 to lift the CPU budget\n", ceilingFloorEnv, ceilingFloor())
		return 1
	}
	if len(failures) > 0 {
		fmt.Fprintln(out, "FAIL surviving mutants on changed lines:")
		for _, f := range failures {
			fmt.Fprintf(out, "  %s:%d %s\n", f.File, f.Line, f.Operator)
		}
		return 1
	}
	// Never claim a clean sweep while indeterminate mutants are outstanding.
	if timedOut > 0 {
		fmt.Fprintf(out, "mutatediff: no surviving mutants on changed lines, but %d timed out without a verdict\n", timedOut)
		return 0
	}
	fmt.Fprintln(out, "mutatediff: all mutants on changed lines killed")
	return 0
}

// reportWarnings prints every non-blocking verdict and returns how many were
// indeterminate, which the caller needs in order not to claim a clean sweep.
func reportWarnings(warnings []mutantVerdict, out io.Writer) (timedOut int) {
	for _, w := range warnings {
		if w.Status == statusTimedOut {
			timedOut++
			fmt.Fprintf(out, "WARN timed out (indeterminate — the mutant may hang the code, or the ceiling was too tight): %s:%d %s\n",
				w.File, w.Line, w.Operator)
			continue
		}
		fmt.Fprintf(out, "WARN not covered: %s:%d %s\n", w.File, w.Line, w.Operator)
	}
	return timedOut
}

// vacuousPkg re-reads pkg's report to check whether the engine returned any
// verdict at all. A read or parse failure here is not fatal: mutatePackage has
// already judged the same file, so this only ever adds a signal.
func vacuousPkg(pkg, reportDir string, out io.Writer) bool {
	reportJSON, err := os.ReadFile(reportPathFor(pkg, reportDir)) // #nosec G304 -- per-run os.MkdirTemp dir + package-derived name
	if err != nil {
		return false
	}
	timedOut, isVacuous, err := vacuous(reportJSON)
	if err != nil || !isVacuous {
		return false
	}
	fmt.Fprintf(out, "mutatediff: %s produced %d timeouts and zero verdicts\n", pkg, timedOut)
	return true
}

func reportPathFor(pkg, reportDir string) string {
	name := "gremlins-" + strings.ReplaceAll(strings.TrimPrefix(pkg, "./"), "/", "-") + ".json"
	return filepath.Join(reportDir, name)
}

// errMissingReport marks a report file the engine exited zero without writing.
// It is deliberately distinct from fs.ErrNotExist: an unavailable engine binary
// also fails with a chain wrapping ENOENT, and that must stay fatal.
var errMissingReport = errors.New("engine exited zero without writing a report")

// runEngine invokes gremlins for pkg and returns the report it wrote. extra
// carries mode-specific flags (--dry-run, the coefficient, the worker count).
func runEngine(ctx context.Context, engineArgs []string, pkg, reportPath string, extra []string, out io.Writer) ([]byte, error) {
	args := slices.Concat(engineArgs, []string{"unleash"}, extra, []string{"--output", reportPath, pkg})
	cmd := exec.CommandContext(ctx, args[0], args[1:]...) // #nosec G204 -- dev tool; engine comes from the Makefile pin, not user input
	cmd.Stdout = out
	cmd.Stderr = os.Stderr
	if runErr := cmd.Run(); runErr != nil {
		return nil, fmt.Errorf("engine failed for %s: %w", pkg, runErr)
	}
	reportJSON, readErr := os.ReadFile(reportPath) // #nosec G304 -- path built from a per-run os.MkdirTemp dir + package-derived name, not user input
	if readErr != nil {
		if errors.Is(readErr, fs.ErrNotExist) {
			return nil, fmt.Errorf("no report for %s: %w", pkg, errMissingReport)
		}
		return nil, fmt.Errorf("no report for %s: %w", pkg, readErr)
	}
	return reportJSON, nil
}

func mutatePackage(ctx context.Context, engineArgs []string, pkg, reportDir string, changed map[string][]lineRange, workers int, out io.Writer) (failures, warnings []mutantVerdict, ran bool, err error) {
	// A dry run enumerates mutants without executing any, so it costs one coverage
	// pass instead of mutants x (build + suite). gremlins mutates the whole subtree
	// it is pointed at while the gate only judges changed lines, so most
	// invocations would otherwise pay for hundreds of mutants to rule on a handful
	// — a one-line edit in ./database enumerates 616 runnable mutants. Skipping
	// cannot change the verdict, since judge discards those mutants anyway, and the
	// coverage pass warms the test cache measureSuite reads next.
	// One worker: a dry run executes no tests, so extra workers only multiply
	// copies of the module tree.
	dryJSON, err := runEngine(ctx, engineArgs, pkg, reportPathFor(pkg, reportDir)+".dry",
		[]string{"--dry-run", workersFlag, "1"}, out)
	if err != nil {
		// A missing report after a zero exit is gremlins' shape for a package with
		// no mutatable statements (e.g. constants-only): it prints "No results to
		// report." and writes nothing. runEngine tags exactly that case with
		// errMissingReport; every other failure — including an engine binary whose
		// own error chain wraps ENOENT — stays fatal.
		if errors.Is(err, errMissingReport) {
			fmt.Fprintf(out, "mutatediff: %s produced no dry-run report (no mutatable statements), skipping\n", pkg)
			return nil, nil, false, nil
		}
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

	fmt.Fprintf(out, "mutatediff: mutating %s (%d mutants on changed lines)\n", pkg, onChanged)
	coefficient := coefficientFor(ctx, pkg, measureSuite, ceilingFloor(), out)
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
}

func fail(format string, a ...any) int {
	fmt.Fprintf(os.Stderr, "mutatediff: "+format+"\n", a...)
	return 2
}

func gitOutput(ctx context.Context, args ...string) (string, error) {
	out, err := exec.CommandContext(ctx, "git", args...).Output() // #nosec G204 -- call-site literals plus the -base flag value, not user input
	if err != nil {
		return "", fmt.Errorf("git %s: %w", strings.Join(args, " "), err)
	}
	return strings.TrimSpace(string(out)), nil
}
