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
		{File: "config/injection.go", Line: 44, Operator: "CONDITIONALS_BOUNDARY", Status: "NOT COVERED"},
	}
	if !reflect.DeepEqual(failures, wantFail) {
		t.Errorf("failures = %#v, want %#v", failures, wantFail)
	}
	if !reflect.DeepEqual(warnings, wantWarn) {
		t.Errorf("warnings = %#v, want %#v", warnings, wantWarn)
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
