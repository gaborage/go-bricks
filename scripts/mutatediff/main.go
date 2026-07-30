package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
)

func main() {
	engine := flag.String("engine", "", "mutation engine command prefix, e.g. 'go run github.com/go-gremlins/gremlins/cmd/gremlins@v0.5.0'")
	base := flag.String("base", "origin/main", "ref to compute merge-base against")
	mergeDir := flag.String("merge", "", "merge mode: directory of per-package shard reports to aggregate (skips the diff gate)")
	mergeOut := flag.String("out", "gremlins-report.json", "merge mode: output path for the aggregated report")
	coeffPkg := flag.String("coefficient", "", "coefficient mode: print the engine timeout-coefficient that holds this package's per-mutant ceiling at the floor")
	flag.Parse()
	if *mergeDir != "" {
		os.Exit(mergeShards(*mergeDir, *mergeOut, os.Stdout))
	}
	if *coeffPkg != "" {
		os.Exit(printCoefficient(*coeffPkg, measureSuite, ceilingFloor(), os.Stdout, os.Stderr))
	}
	if *engine == "" {
		fmt.Fprintln(os.Stderr, "mutatediff: -engine is required")
		os.Exit(2)
	}
	os.Exit(run(*engine, *base, os.Stdout))
}

func run(engine, baseRef string, out io.Writer) int {
	engineArgs := strings.Fields(engine)
	if len(engineArgs) == 0 {
		return fail("engine command is blank")
	}
	mergeBase, err := gitOutput("merge-base", "HEAD", baseRef)
	if err != nil {
		return fail("%v", err)
	}
	diff, err := gitOutput("diff", "-U0", "--no-color", mergeBase, "HEAD", "--", "*.go")
	if err != nil {
		return fail("%v", err)
	}
	parsed, perr := parseUnifiedDiff(diff)
	if perr != nil {
		return fail("%v", perr)
	}
	changed := mutationScope(parsed)
	switch status, statusErr := gitOutput("status", "--porcelain"); {
	case statusErr != nil:
		fmt.Fprintf(out, "WARN: could not check the working tree for uncommitted changes: %v\n", statusErr)
	case status != "":
		fmt.Fprintln(out, "WARN: uncommitted changes detected — the engine reads the working tree, but scope comes from committed merge-base..HEAD")
	}
	if len(changed) == 0 {
		fmt.Fprintln(out, "mutatediff: no mutatable changes vs merge-base")
		return 0
	}
	reportDir, err := os.MkdirTemp("", "mutatediff-*")
	if err != nil {
		return fail("%v", err)
	}
	defer os.RemoveAll(reportDir)
	var failures, warnings, unjudged []mutantVerdict
	for _, pkg := range packagesOf(changed) {
		f, w, mErr := mutatePackage(engineArgs, pkg, reportDir, changed, out)
		if mErr != nil {
			return fail("%v", mErr)
		}
		failures = append(failures, f...)
		warnings = append(warnings, w...)
		if vacuousPkg(pkg, reportDir, out) {
			unjudged = append(unjudged, mutantVerdict{File: pkg})
		}
	}
	return reportVerdict(failures, warnings, unjudged, out)
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
		fmt.Fprintf(out, "raise %s (currently %s) and re-run\n", ceilingFloorEnv, ceilingFloor())
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

func mutatePackage(engineArgs []string, pkg, reportDir string, changed map[string][]lineRange, out io.Writer) (failures, warnings []mutantVerdict, err error) {
	fmt.Fprintf(out, "mutatediff: mutating %s\n", pkg)
	coefficient := coefficientFor(pkg, measureSuite, ceilingFloor(), out)
	reportPath := reportPathFor(pkg, reportDir)
	args := slices.Concat(engineArgs, []string{"unleash"}, gremlinsTimeoutArgs(coefficient), []string{"--output", reportPath, pkg})
	cmd := exec.CommandContext(context.Background(), args[0], args[1:]...) // #nosec G204 -- dev tool; engine comes from the Makefile pin, not user input
	cmd.Stdout = out
	cmd.Stderr = os.Stderr
	if runErr := cmd.Run(); runErr != nil {
		return nil, nil, fmt.Errorf("engine failed for %s: %w", pkg, runErr)
	}
	reportJSON, readErr := os.ReadFile(reportPath) // #nosec G304 -- path built from a per-run os.MkdirTemp dir + package-derived name, not user input
	if readErr != nil {
		return nil, nil, fmt.Errorf("no report for %s: %w", pkg, readErr)
	}
	f, w, jerr := judge(reportJSON, pkg, changed)
	if jerr != nil {
		return nil, nil, fmt.Errorf("parse report for %s: %w", pkg, jerr)
	}
	return f, w, nil
}

func fail(format string, a ...any) int {
	fmt.Fprintf(os.Stderr, "mutatediff: "+format+"\n", a...)
	return 2
}

func gitOutput(args ...string) (string, error) {
	out, err := exec.CommandContext(context.Background(), "git", args...).Output() // #nosec G204 -- call-site literals plus the -base flag value, not user input
	if err != nil {
		return "", fmt.Errorf("git %s: %w", strings.Join(args, " "), err)
	}
	return strings.TrimSpace(string(out)), nil
}
