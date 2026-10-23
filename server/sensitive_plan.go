package server

import (
	"fmt"
	"slices"
	"strings"
)

// sensitivePlan is a route's WithSensitiveParams declaration compiled against its template once at
// registration (ADR-150), so request-time masking works on segment indices and never parses the
// route. Indices count the segments of strings.Split(fullPath, "/").
type sensitivePlan struct {
	segCount    int   // segments in the route template
	wildcardIdx int   // index of the segment holding the wildcard, -1 if none
	maskIdx     []int // ":name" segments to mask, ascending, all before wildcardIdx
	maskTail    bool  // "*" declared: everything from wildcardIdx on collapses into one mask
}

// masks reports whether the plan masks anything; a zero-value plan is never recorded.
func (p sensitivePlan) masks() bool {
	return len(p.maskIdx) > 0 || p.maskTail
}

// compileSensitivePlan compiles names against fullPath. Each name must be a whole ":name" segment
// before any wildcard, or "*" on a wildcard route; the error names the param and route, never a
// value. No names compile to the zero-value plan.
func compileSensitivePlan(fullPath string, names []string) (sensitivePlan, error) {
	if len(names) == 0 {
		return sensitivePlan{}, nil
	}
	params, segCount, wildcardIdx := routeParams(fullPath)
	plan := sensitivePlan{segCount: segCount, wildcardIdx: wildcardIdx}
	for _, name := range names {
		if name == "*" {
			if plan.wildcardIdx == -1 {
				return sensitivePlan{}, fmt.Errorf(`sensitive param "*" needs a wildcard route, got %s`, fullPath)
			}
			plan.maskTail = true
			continue
		}
		idxs := paramIndices(params, name)
		if len(idxs) == 0 {
			return sensitivePlan{}, fmt.Errorf(`sensitive param %q must be a whole ":%s" segment before any wildcard in %s`,
				name, name, fullPath)
		}
		plan.maskIdx = append(plan.maskIdx, idxs...)
	}
	// A name declared twice adds its indices twice; the masker wants each once, ascending.
	slices.Sort(plan.maskIdx)
	plan.maskIdx = slices.Compact(plan.maskIdx)
	return plan, nil
}

// paramIndices returns the index of every segment in params named name, ascending. echo accepts
// a repeated name (/cards/:id/transfers/:id), and every segment carrying it is masked. The empty
// name matches nothing: params holds "" for every segment that is not a whole param.
func paramIndices(params []string, name string) []int {
	if name == "" {
		return nil
	}
	var idxs []int
	for idx, param := range params {
		if param == name {
			idxs = append(idxs, idx)
		}
	}
	return idxs
}

// routeParams returns the whole-segment param name of each segment before the wildcard ("" for any
// other segment), the route's segment count, and the wildcard segment's index, -1 if none.
func routeParams(fullPath string) (params []string, segCount, wildcardIdx int) {
	segs := strings.Split(fullPath, "/")
	for i, seg := range segs {
		name, wildcard := segmentParam(seg)
		if wildcard {
			return segs[:i], len(segs), i
		}
		segs[i] = name
	}
	return segs, len(segs), -1
}

// segmentParam is the one place that reads a route segment the way echo's router does: a segment
// starting with an unescaped ':' is a whole-segment param whose name runs to the segment's end; the
// first '*' outside a param is the wildcard. An embedded param (pan-:pan) or an escaped "\:" yields
// no name.
func segmentParam(seg string) (name string, wildcard bool) {
	for i := 0; i < len(seg); i++ {
		switch {
		case seg[i] == ':' && (i == 0 || seg[i-1] != '\\'):
			if i == 0 {
				return seg[1:], false
			}
			return "", false
		case seg[i] == '*':
			return "", true
		}
	}
	return "", false
}
