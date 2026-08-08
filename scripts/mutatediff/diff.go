// Package main implements mutatediff, the diff-scoped mutation gate.
package main

import (
	"bufio"
	"fmt"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// lineRange is a half-open [Start, End) range of new-file line numbers. The
// json tags are load-bearing: cache.go persists these ranges, and a cached
// entry that decoded to zero values would look like "nothing was judged".
type lineRange struct {
	Start int `json:"start"`
	End   int `json:"end"`
}

var hunkRe = regexp.MustCompile(`^@@ -\d+(?:,\d+)? \+(\d+)(?:,(\d+))? @@`)

func parseUnifiedDiff(diff string) (map[string][]lineRange, error) {
	changes := map[string][]lineRange{}
	var current string
	sc := bufio.NewScanner(strings.NewReader(diff))
	sc.Buffer(make([]byte, 1024*1024), 1024*1024)
	for sc.Scan() {
		line := sc.Text()
		switch {
		case strings.HasPrefix(line, "+++ b/"):
			current = strings.TrimPrefix(line, "+++ b/")
		case strings.HasPrefix(line, "+++ /dev/null"):
			current = ""
		case strings.HasPrefix(line, "@@"):
			if current == "" {
				continue
			}
			m := hunkRe.FindStringSubmatch(line)
			if m == nil {
				continue
			}
			start, _ := strconv.Atoi(m[1])
			count := 1
			if m[2] != "" {
				count, _ = strconv.Atoi(m[2])
			}
			if count == 0 {
				continue
			}
			changes[current] = append(changes[current], lineRange{Start: start, End: start + count})
		}
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("diff scan aborted: %w", err)
	}
	return changes, nil
}

func mutationScope(changes map[string][]lineRange) map[string][]lineRange {
	scoped := map[string][]lineRange{}
	for file, ranges := range changes {
		if !strings.HasSuffix(file, ".go") || strings.HasSuffix(file, "_test.go") {
			continue
		}
		// scripts/ is excluded because gremlins v0.5.0 misverdicts this nested
		// package main (wiki/testing.md#mutation-gate); its own unit tests are its net.
		if strings.HasPrefix(file, "tools/") || strings.HasPrefix(file, "scripts/") || strings.Contains(file, "testdata/") {
			continue
		}
		scoped[file] = ranges
	}
	return scoped
}

func packagesOf(files map[string][]lineRange) []string {
	seen := map[string]bool{}
	for file := range files {
		dir := filepath.ToSlash(filepath.Dir(file))
		if dir == "." {
			seen["."] = true
			continue
		}
		seen["./"+dir] = true
	}
	pkgs := make([]string, 0, len(seen))
	for p := range seen {
		pkgs = append(pkgs, p)
	}
	sort.Strings(pkgs)
	return pkgs
}
