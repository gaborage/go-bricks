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
		{File: "config/injection.go", Line: 27, Operator: "CONDITIONALS_NEGATION", Status: "LIVED"},
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
