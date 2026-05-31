package testutil

import (
	"fmt"
	"strings"
	"testing"
)

func TestCaptureStdout(t *testing.T) {
	got := CaptureStdout(t, func() {
		fmt.Print("hello ")
		fmt.Println("world")
	})
	if got != "hello world\n" {
		t.Errorf("CaptureStdout = %q, want %q", got, "hello world\n")
	}
}

func TestCaptureStdoutEmpty(t *testing.T) {
	if got := CaptureStdout(t, func() {}); got != "" {
		t.Errorf("CaptureStdout for no output = %q, want empty", got)
	}
}

// TestCaptureStdoutPanicDoesNotDeadlock verifies that a panicking fn still closes
// the pipe (so the drain goroutine terminates) and the panic propagates.
func TestCaptureStdoutPanicDoesNotDeadlock(t *testing.T) {
	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("expected the panic to propagate out of CaptureStdout")
		}
		if !strings.Contains(fmt.Sprint(r), "boom") {
			t.Errorf("unexpected panic value: %v", r)
		}
	}()
	_ = CaptureStdout(t, func() {
		fmt.Print("before panic")
		panic("boom")
	})
}
