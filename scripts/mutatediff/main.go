package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
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
	mergeBase, err := gitOutput("merge-base", "HEAD", baseRef)
	if err != nil {
		fmt.Fprintf(os.Stderr, "mutatediff: %v\n", err)
		return 2
	}
	diff, err := gitOutput("diff", "-U0", "--no-color", mergeBase, "HEAD", "--", "*.go")
	if err != nil {
		fmt.Fprintf(os.Stderr, "mutatediff: %v\n", err)
		return 2
	}
	changed := mutationScope(parseUnifiedDiff(diff))
	if status, statusErr := gitOutput("status", "--porcelain", "--", "*.go"); statusErr == nil && status != "" {
		fmt.Fprintln(out, "WARN: uncommitted .go changes detected — the gate judges committed state only (diff vs merge-base..HEAD)")
	}
	if len(changed) == 0 {
		fmt.Fprintln(out, "mutatediff: no mutatable changes vs merge-base")
		return 0
	}
	reportDir, err := os.MkdirTemp("", "mutatediff-*")
	if err != nil {
		fmt.Fprintf(os.Stderr, "mutatediff: %v\n", err)
		return 2
	}
	defer os.RemoveAll(reportDir)
	var failures, warnings []mutantVerdict
	for _, pkg := range packagesOf(changed) {
		fmt.Fprintf(out, "mutatediff: mutating %s\n", pkg)
		reportName := "gremlins-" + strings.ReplaceAll(strings.TrimPrefix(pkg, "./"), "/", "-") + ".json"
		reportPath := filepath.Join(reportDir, reportName)
		args := append(strings.Fields(engine), "unleash", "--output", reportPath, pkg)
		cmd := exec.CommandContext(context.Background(), args[0], args[1:]...) // #nosec G204 -- dev tool; engine comes from the Makefile pin, not user input
		cmd.Stdout = out
		cmd.Stderr = os.Stderr
		runErr := cmd.Run()
		if runErr != nil {
			fmt.Fprintf(os.Stderr, "mutatediff: engine failed for %s: %v\n", pkg, runErr)
			return 2
		}
		reportJSON, readErr := os.ReadFile(reportPath) // #nosec G304 -- path built from a per-run os.MkdirTemp dir + package-derived name, not user input
		if readErr != nil {
			fmt.Fprintf(os.Stderr, "mutatediff: no report for %s: %v\n", pkg, readErr)
			return 2
		}
		f, w, jerr := judge(reportJSON, pkg, changed)
		if jerr != nil {
			fmt.Fprintf(os.Stderr, "mutatediff: parse report for %s: %v\n", pkg, jerr)
			return 2
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

func gitOutput(args ...string) (string, error) {
	out, err := exec.CommandContext(context.Background(), "git", args...).Output() // #nosec G204 -- call-site literals plus the -base flag value, not user input
	if err != nil {
		return "", fmt.Errorf("git %s: %w", strings.Join(args, " "), err)
	}
	return strings.TrimSpace(string(out)), nil
}
