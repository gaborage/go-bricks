package main

import (
	"reflect"
	"testing"
)

const fixtureReport = `{
  "go_module": "github.com/gaborage/go-bricks",
  "test_efficacy": 75,
  "mutations_coverage": 90,
  "files": [
    {
      "file_name": "injection.go",
      "mutations": [
        {"line": 27, "type": "CONDITIONALS_NEGATION", "status": "LIVED"},
        {"line": 27, "type": "ARITHMETIC_BASE", "status": "KILLED"},
        {"line": 29, "type": "CONDITIONALS_BOUNDARY", "status": "LIVED"},
        {"line": 44, "type": "CONDITIONALS_BOUNDARY", "status": "NOT COVERED"},
        {"line": 200, "type": "INCREMENT_DECREMENT", "status": "LIVED"},
        {"line": 28, "type": "INVERT_NEGATIVES", "status": "TIMED OUT"}
      ]
    },
    {
      "file_name": "other.go",
      "mutations": [
        {"line": 5, "type": "CONDITIONALS_NEGATION", "status": "LIVED"}
      ]
    }
  ]
}`

func TestJudgeAppliesVerdictPolicyOnChangedLines(t *testing.T) {
	changed := map[string][]lineRange{
		"config/injection.go": {{Start: 26, End: 29}, {Start: 44, End: 45}},
	}
	failures, warnings, err := judge([]byte(fixtureReport), "./config", changed)
	if err != nil {
		t.Fatalf("judge: %v", err)
	}
	wantFail := []mutantVerdict{
		{File: "config/injection.go", Line: 27, Operator: "CONDITIONALS_NEGATION", Status: statusLived},
	}
	wantWarn := []mutantVerdict{
		{File: "config/injection.go", Line: 44, Operator: "CONDITIONALS_BOUNDARY", Status: statusNotCovered},
		{File: "config/injection.go", Line: 28, Operator: "INVERT_NEGATIVES", Status: statusTimedOut},
	}
	if !reflect.DeepEqual(failures, wantFail) {
		t.Errorf("failures = %#v, want %#v", failures, wantFail)
	}
	if !reflect.DeepEqual(warnings, wantWarn) {
		t.Errorf("warnings = %#v, want %#v", warnings, wantWarn)
	}
}

// TestJudgeWarnsRatherThanSilentlyPassingTimedOut pins the lesson of the first
// nightly baseline: TIMED OUT does not mean "the mutant hung the code". It also
// fires whenever the engine's derived ceiling lands under the cost of building
// the mutated test binary, which silently waved whole packages through. It stays
// non-blocking (a hang the suite notices really is a kill) but must be visible.
func TestJudgeWarnsRatherThanSilentlyPassingTimedOut(t *testing.T) {
	changed := map[string][]lineRange{
		"config/injection.go": {{Start: 28, End: 29}},
	}
	failures, warnings, err := judge([]byte(fixtureReport), "./config", changed)
	if err != nil {
		t.Fatalf("judge: %v", err)
	}
	if len(failures) != 0 {
		t.Errorf("failures = %#v, want none — a timeout must not block the gate", failures)
	}
	want := []mutantVerdict{
		{File: "config/injection.go", Line: 28, Operator: "INVERT_NEGATIVES", Status: statusTimedOut},
	}
	if !reflect.DeepEqual(warnings, want) {
		t.Errorf("warnings = %#v, want %#v", warnings, want)
	}
}

// TestVacuousDetectsAnAllTimeoutPackage covers the state the first nightly
// baseline shipped 249 times in ./observability alone: mutants ran, every one
// timed out, and gremlins still published a score. No verdict exists, so the
// package cannot be reported as clean.
func TestVacuousDetectsAnAllTimeoutPackage(t *testing.T) {
	const allTimeout = `{"files":[{"file_name":"metrics.go","mutations":[
		{"line":24,"type":"CONDITIONALS_NEGATION","status":"TIMED OUT"},
		{"line":30,"type":"CONDITIONALS_NEGATION","status":"TIMED OUT"},
		{"line":34,"type":"CONDITIONALS_NEGATION","status":"NOT COVERED"}]}]}`
	timedOut, isVacuous, err := vacuous([]byte(allTimeout))
	if err != nil {
		t.Fatalf("vacuous: %v", err)
	}
	if timedOut != 2 || !isVacuous {
		t.Errorf("vacuous = (%d, %v), want (2, true)", timedOut, isVacuous)
	}
}

func TestVacuousAcceptsAnyRealVerdict(t *testing.T) {
	cases := map[string]string{
		// One kill is enough to prove the ceiling cleared the build cost, so the
		// remaining timeouts are the engine catching real hangs.
		"one_killed_among_timeouts": `{"files":[{"file_name":"a.go","mutations":[
			{"line":1,"type":"X","status":"TIMED OUT"},{"line":2,"type":"X","status":"KILLED"}]}]}`,
		"one_lived_among_timeouts": `{"files":[{"file_name":"a.go","mutations":[
			{"line":1,"type":"X","status":"TIMED OUT"},{"line":2,"type":"X","status":"LIVED"}]}]}`,
		// No timeouts at all: an untested package is a real result, not a broken
		// measurement, and SonarCloud owns coverage.
		"all_not_covered": `{"files":[{"file_name":"a.go","mutations":[
			{"line":1,"type":"X","status":"NOT COVERED"}]}]}`,
		"no_mutants": `{"files":[]}`,
	}
	for name, rep := range cases {
		t.Run(name, func(t *testing.T) {
			if _, isVacuous, err := vacuous([]byte(rep)); err != nil || isVacuous {
				t.Errorf("vacuous = (%v, %v), want (false, nil)", isVacuous, err)
			}
		})
	}
}

func TestVacuousRejectsMalformedJSON(t *testing.T) {
	if _, _, err := vacuous([]byte("{nope")); err == nil {
		t.Error("expected error for malformed JSON")
	}
}

func TestJudgeRejectsMalformedJSON(t *testing.T) {
	if _, _, err := judge([]byte("{nope"), "./config", nil); err == nil {
		t.Error("expected error for malformed JSON")
	}
}

// TestJudgePkgDirFormsEquivalent pins that judge's path-joining is agnostic to
// how pkgDir is spelled: a leading "./" is not load-bearing (path.Join cleans
// it away on its own), and the root package (".") resolves to a bare
// basename rather than a "./"-prefixed one.
func TestJudgePkgDirFormsEquivalent(t *testing.T) {
	changed := map[string][]lineRange{
		"config/injection.go": {{Start: 26, End: 29}, {Start: 44, End: 45}},
	}
	wantFail := []mutantVerdict{
		{File: "config/injection.go", Line: 27, Operator: "CONDITIONALS_NEGATION", Status: statusLived},
	}

	failuresSlash, _, err := judge([]byte(fixtureReport), "./config", changed)
	if err != nil {
		t.Fatalf("judge(./config): %v", err)
	}
	if !reflect.DeepEqual(failuresSlash, wantFail) {
		t.Errorf("failures(./config) = %#v, want %#v", failuresSlash, wantFail)
	}

	failuresBare, _, err := judge([]byte(fixtureReport), "config", changed)
	if err != nil {
		t.Fatalf("judge(config): %v", err)
	}
	if !reflect.DeepEqual(failuresBare, wantFail) {
		t.Errorf("failures(config) = %#v, want %#v", failuresBare, wantFail)
	}

	const rootFixture = `{
  "files": [
    {
      "file_name": "root.go",
      "mutations": [
        {"line": 3, "type": "CONDITIONALS_NEGATION", "status": "LIVED"}
      ]
    }
  ]
}`
	rootChanged := map[string][]lineRange{
		"root.go": {{Start: 1, End: 5}},
	}
	wantRootFail := []mutantVerdict{
		{File: "root.go", Line: 3, Operator: "CONDITIONALS_NEGATION", Status: statusLived},
	}
	failuresRoot, _, err := judge([]byte(rootFixture), ".", rootChanged)
	if err != nil {
		t.Fatalf("judge(.): %v", err)
	}
	if !reflect.DeepEqual(failuresRoot, wantRootFail) {
		t.Errorf("failures(.) = %#v, want %#v", failuresRoot, wantRootFail)
	}
}

// TestJudgeFailsClosedOnSkipped guards a live footgun. gremlins' --diff mode
// marks filtered-out mutants SKIPPED, and on this repo it marks EVERY mutant
// SKIPPED because it matches git paths against the engine's temp workdir
// (v0.5.1: --diff HEAD~1 over a committed one-line change in ./database skipped
// all 747). If SKIPPED ever becomes a pass, adding that flag silently empties the
// gate. It must stay an error.
func TestJudgeFailsClosedOnSkipped(t *testing.T) {
	const rep = `{"files":[{"file_name":"injection.go","mutations":[{"line":27,"type":"CONDITIONALS_NEGATION","status":"SKIPPED"}]}]}`
	changed := map[string][]lineRange{"config/injection.go": {{Start: 26, End: 29}}}
	if _, _, err := judge([]byte(rep), "./config", changed); err == nil {
		t.Error("SKIPPED must fail closed, not pass — see the comment on judge")
	}
}

// TestCountOnChangedLinesGatesTheExpensivePass backs the dry-run pre-check: only
// mutants inside a changed range justify paying mutants x (build + suite).
func TestCountOnChangedLinesGatesTheExpensivePass(t *testing.T) {
	const dry = `{"files":[
		{"file_name":"injection.go","mutations":[
			{"line":27,"type":"CONDITIONALS_NEGATION","status":"RUNNABLE"},
			{"line":44,"type":"CONDITIONALS_BOUNDARY","status":"NOT COVERED"},
			{"line":900,"type":"ARITHMETIC_BASE","status":"RUNNABLE"}]},
		{"file_name":"untouched.go","mutations":[
			{"line":5,"type":"CONDITIONALS_NEGATION","status":"RUNNABLE"}]}]}`
	changed := map[string][]lineRange{"config/injection.go": {{Start: 26, End: 29}, {Start: 44, End: 45}}}
	got, err := countOnChangedLines([]byte(dry), "./config", changed)
	if err != nil {
		t.Fatalf("countOnChangedLines: %v", err)
	}
	// Lines 27 and 44 count; line 900 and the untouched file do not.
	if got != 2 {
		t.Errorf("countOnChangedLines = %d, want 2", got)
	}
	none, err := countOnChangedLines([]byte(dry), "./config", map[string][]lineRange{
		"config/injection.go": {{Start: 100, End: 200}},
	})
	if err != nil || none != 0 {
		t.Errorf("countOnChangedLines = (%d, %v), want (0, nil) so the package is skipped", none, err)
	}
}

func TestJudgeFailsClosedOnUnknownStatus(t *testing.T) {
	const rep = `{"files":[{"file_name":"injection.go","mutations":[{"line":27,"type":"CONDITIONALS_NEGATION","status":"SOMETHING NEW"}]}]}`
	changed := map[string][]lineRange{"config/injection.go": {{Start: 26, End: 29}}}
	if _, _, err := judge([]byte(rep), "./config", changed); err == nil {
		t.Error("expected error for unrecognized mutant status")
	}
}
