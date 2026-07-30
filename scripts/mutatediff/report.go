package main

import (
	"encoding/json"
	"fmt"
	"path"
)

// gremlinsReport mirrors the JSON written by `gremlins unleash --output`.
// Spike-verified truths (Task 1): file_name is basename-only, statuses are
// spelled with spaces ("NOT COVERED", "TIMED OUT"). Keep in sync with the
// fixture in report_test.go.
type gremlinsReport struct {
	Files []struct {
		FileName  string `json:"file_name"`
		Mutations []struct {
			Line   int    `json:"line"`
			Type   string `json:"type"`
			Status string `json:"status"`
		} `json:"mutations"`
	} `json:"files"`
}

type mutantVerdict struct {
	File     string
	Line     int
	Operator string
	Status   string
}

// Gremlins verdicts judge and its tests both key off often enough to trip
// goconst.
const (
	statusLived      = "LIVED"
	statusNotCovered = "NOT COVERED"
	statusTimedOut   = "TIMED OUT"
)

// judge buckets report mutants that land on changed lines: LIVED fails the
// gate; NOT COVERED warns (SonarCloud owns coverage) and so does TIMED OUT,
// which is indeterminate rather than clean — see timeout.go for why the
// engine reports it for reasons that have nothing to do with the mutant.
// pkgDir is the package the report was generated for — gremlins emits paths
// relative to it (a bare basename for the package's own files, a slashed path
// for files in subpackages, which it also mutates), so the repo-relative path
// is pkgDir + file_name.
func judge(reportJSON []byte, pkgDir string, changed map[string][]lineRange) (failures, warnings []mutantVerdict, err error) {
	var rep gremlinsReport
	if err := json.Unmarshal(reportJSON, &rep); err != nil {
		return nil, nil, err
	}
	for _, f := range rep.Files {
		name := path.Join(pkgDir, f.FileName)
		ranges, ok := changed[name]
		if !ok {
			continue
		}
		for _, m := range f.Mutations {
			if !inRanges(m.Line, ranges) {
				continue
			}
			v := mutantVerdict{File: name, Line: m.Line, Operator: m.Type, Status: m.Status}
			switch m.Status {
			case statusLived:
				failures = append(failures, v)
			case statusNotCovered, statusTimedOut:
				warnings = append(warnings, v)
			case "KILLED", "NOT VIABLE", "RUNNABLE":
				// pass: died, didn't compile, or dry-run marker
			default:
				return nil, nil, fmt.Errorf("unrecognized mutant status %q at %s:%d — failing closed", m.Status, name, m.Line)
			}
		}
	}
	return failures, warnings, nil
}

// vacuous reports whether the engine produced no verdict at all for a package:
// mutants timed out and not one was killed or survived. That state has no
// innocent reading — the ceiling was below the cost of running the mutant, so
// nothing was actually tested — and it is what let the first nightly baseline
// publish 86% efficacy over packages in which no mutant ran. It is deliberately
// judged over the WHOLE report rather than the changed lines, and independently
// of the ceiling arithmetic in timeout.go, so a mis-scaled coefficient is caught
// by observation instead of trusted not to happen.
func vacuous(reportJSON []byte) (timedOut int, isVacuous bool, err error) {
	var rep gremlinsReport
	if err := json.Unmarshal(reportJSON, &rep); err != nil {
		return 0, false, err
	}
	decided := 0
	for _, f := range rep.Files {
		for _, m := range f.Mutations {
			switch m.Status {
			case statusTimedOut:
				timedOut++
			case "KILLED", statusLived:
				decided++
			}
		}
	}
	return timedOut, timedOut > 0 && decided == 0, nil
}

func inRanges(line int, ranges []lineRange) bool {
	for _, r := range ranges {
		if line >= r.Start && line < r.End {
			return true
		}
	}
	return false
}
