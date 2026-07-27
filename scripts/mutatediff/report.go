package main

import (
	"encoding/json"
	"path"
	"strings"
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

// judge buckets report mutants that land on changed lines: LIVED fails the
// gate, NOT COVERED warns (SonarCloud owns coverage), anything else passes.
// pkgDir is the package the report was generated for — gremlins emits
// basenames, so the repo-relative path is pkgDir + file_name.
func judge(reportJSON []byte, pkgDir string, changed map[string][]lineRange) (failures, warnings []mutantVerdict, err error) {
	var rep gremlinsReport
	if err := json.Unmarshal(reportJSON, &rep); err != nil {
		return nil, nil, err
	}
	for _, f := range rep.Files {
		name := path.Join(strings.TrimPrefix(pkgDir, "./"), f.FileName)
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
			case "LIVED":
				failures = append(failures, v)
			case "NOT COVERED":
				warnings = append(warnings, v)
			}
		}
	}
	return failures, warnings, nil
}

func inRanges(line int, ranges []lineRange) bool {
	for _, r := range ranges {
		if line >= r.Start && line < r.End {
			return true
		}
	}
	return false
}
