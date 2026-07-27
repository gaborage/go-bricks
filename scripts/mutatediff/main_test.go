package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestRunNoOpDiffExitsCleanWithoutEngine(t *testing.T) {
	var buf bytes.Buffer
	if got := run("false", "HEAD", &buf); got != 0 {
		t.Fatalf("run = %d, want 0; output: %s", got, buf.String())
	}
	if !strings.Contains(buf.String(), "no mutatable changes") {
		t.Fatalf("missing no-op message, got: %s", buf.String())
	}
}

func TestRunBlankEngineFailsFast(t *testing.T) {
	var buf bytes.Buffer
	if got := run("   ", "HEAD", &buf); got != 2 {
		t.Fatalf("run = %d, want 2 for whitespace-only engine", got)
	}
}
