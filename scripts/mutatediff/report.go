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

// changedMutants is the single selector for "mutants that land on changed
// lines". judge classifies its output and the dry-run pre-check counts it, so the
// pre-check cannot select a different set than the verdict does — the property
// the skip's soundness rests on holds by construction rather than by two walks
// agreeing. pkgDir is the package the report was generated for: gremlins emits
// paths relative to it (a bare basename for the package's own files, a slashed
// path for subpackages, which it also mutates), so the repo-relative path is
// pkgDir + file_name.
func changedMutants(reportJSON []byte, pkgDir string, changed map[string][]lineRange) ([]mutantVerdict, error) {
	var rep gremlinsReport
	if err := json.Unmarshal(reportJSON, &rep); err != nil {
		return nil, err
	}
	var out []mutantVerdict
	for _, f := range rep.Files {
		name := path.Join(pkgDir, f.FileName)
		ranges, ok := changed[name]
		if !ok {
			continue
		}
		for _, m := range f.Mutations {
			if inRanges(m.Line, ranges) {
				out = append(out, mutantVerdict{File: name, Line: m.Line, Operator: m.Type, Status: m.Status})
			}
		}
	}
	return out, nil
}

// countOnChangedLines answers "is the expensive pass worth starting?" from a
// --dry-run report. Mutants of any status count: gremlins recomputes coverage on
// the real run, so a dry NOT COVERED can come back RUNNABLE, and skipping on a
// RUNNABLE-only count would drop verdicts.
func countOnChangedLines(reportJSON []byte, pkgDir string, changed map[string][]lineRange) (int, error) {
	ms, err := changedMutants(reportJSON, pkgDir, changed)
	return len(ms), err
}

// SKIPPED is deliberately absent from every branch below, so it reaches the
// fail-closed default. gremlins emits it for mutants its own `--diff <ref>` mode
// filtered out, and that mode is unusable here: it marks *everything* SKIPPED on
// this repo. Passing it would empty the gate; leave it failing closed.
//
// judge buckets the selected mutants: LIVED fails the gate; NOT COVERED warns
// (SonarCloud owns coverage) and so does TIMED OUT, which is indeterminate rather
// than clean — see timeout.go for why the engine reports it for reasons that have
// nothing to do with the mutant.
func judge(reportJSON []byte, pkgDir string, changed map[string][]lineRange) (failures, warnings []mutantVerdict, err error) {
	ms, err := changedMutants(reportJSON, pkgDir, changed)
	if err != nil {
		return nil, nil, err
	}
	for _, v := range ms {
		switch v.Status {
		case statusLived:
			failures = append(failures, v)
		case statusNotCovered, statusTimedOut:
			warnings = append(warnings, v)
		case "KILLED", "NOT VIABLE", "RUNNABLE":
			// pass: died, didn't compile, or dry-run marker
		default:
			return nil, nil, fmt.Errorf("unrecognized mutant status %q at %s:%d — failing closed", v.Status, v.File, v.Line)
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
