package main

import (
	"runtime/debug"
	"testing"
)

func TestResolveVersion(t *testing.T) {
	bi := func(v string) func() (*debug.BuildInfo, bool) {
		return func() (*debug.BuildInfo, bool) {
			return &debug.BuildInfo{Main: debug.Module{Version: v}}, true
		}
	}
	none := func() (*debug.BuildInfo, bool) { return nil, false }

	tests := []struct {
		name    string
		ldflags string
		read    func() (*debug.BuildInfo, bool)
		want    string
	}{
		{name: "ldflags_wins", ldflags: "v0.38.0", read: bi("v0.39.0"), want: "v0.38.0"},
		{name: "buildinfo_when_dev", ldflags: "dev", read: bi("v0.39.0"), want: "v0.39.0"},
		{name: "ignore_devel_pseudo", ldflags: "dev", read: bi("(devel)"), want: "dev"},
		{name: "no_buildinfo", ldflags: "dev", read: none, want: "dev"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := resolveVersion(tc.ldflags, tc.read); got != tc.want {
				t.Errorf("resolveVersion(%q) = %q, want %q", tc.ldflags, got, tc.want)
			}
		})
	}
}
