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
	flag.Parse()
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
	changed := mutationScope(parseUnifiedDiff(diff))
	switch status, statusErr := gitOutput("status", "--porcelain", "--", "*.go"); {
	case statusErr != nil:
		fmt.Fprintf(out, "WARN: could not check the working tree for uncommitted changes: %v\n", statusErr)
	case status != "":
		fmt.Fprintln(out, "WARN: uncommitted .go changes detected — the gate judges committed state only (diff vs merge-base..HEAD)")
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
	var failures, warnings []mutantVerdict
	for _, pkg := range packagesOf(changed) {
		fmt.Fprintf(out, "mutatediff: mutating %s\n", pkg)
		reportName := "gremlins-" + strings.ReplaceAll(strings.TrimPrefix(pkg, "./"), "/", "-") + ".json"
		reportPath := filepath.Join(reportDir, reportName)
		args := slices.Concat(engineArgs, []string{"unleash", "--output", reportPath, pkg})
		cmd := exec.CommandContext(context.Background(), args[0], args[1:]...) // #nosec G204 -- dev tool; engine comes from the Makefile pin, not user input
		cmd.Stdout = out
		cmd.Stderr = os.Stderr
		if runErr := cmd.Run(); runErr != nil {
			return fail("engine failed for %s: %v", pkg, runErr)
		}
		reportJSON, readErr := os.ReadFile(reportPath) // #nosec G304 -- path built from a per-run os.MkdirTemp dir + package-derived name, not user input
		if readErr != nil {
			return fail("no report for %s: %v", pkg, readErr)
		}
		f, w, jerr := judge(reportJSON, pkg, changed)
		if jerr != nil {
			return fail("parse report for %s: %v", pkg, jerr)
		}
		failures = append(failures, f...)
		warnings = append(warnings, w...)
	}
	for _, w := range warnings {
		fmt.Fprintf(out, "WARN not covered: %s:%d %s\n", w.File, w.Line, w.Operator)
	}
	if len(failures) > 0 {
		fmt.Fprintln(out, "FAIL surviving mutants on changed lines:")
		for _, f := range failures {
			fmt.Fprintf(out, "  %s:%d %s\n", f.File, f.Line, f.Operator)
		}
		return 1
	}
	fmt.Fprintln(out, "mutatediff: all mutants on changed lines killed")
	return 0
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
