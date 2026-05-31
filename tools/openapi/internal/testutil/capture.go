// Package testutil provides shared test helpers for the go-bricks-openapi tool.
package testutil

import (
	"bytes"
	"io"
	"os"
	"testing"
)

// CaptureStdout runs fn with os.Stdout redirected to a pipe and returns what was
// printed there. Several subcommands write progress via fmt.Printf (os.Stdout),
// which cobra's SetOut does not intercept, so capturing the real os.Stdout is the
// only way to observe that output in tests.
//
// The pipe is drained in a goroutine so a large write can't deadlock; the write
// end is closed even if fn panics (so the drain goroutine reaches EOF and
// terminates instead of leaking), and the read end is closed once drained (no fd
// leak).
//
// NOTE: mutates the global os.Stdout; do not call from parallel tests.
func CaptureStdout(t *testing.T, fn func()) string {
	t.Helper()
	orig := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	os.Stdout = w
	defer func() { os.Stdout = orig }()

	done := make(chan string, 1)
	go func() {
		var buf bytes.Buffer
		_, _ = io.Copy(&buf, r)
		_ = r.Close()
		done <- buf.String()
	}()

	// Close the write end even if fn panics, so the drain goroutine reaches EOF
	// and terminates instead of blocking forever.
	func() {
		defer func() { _ = w.Close() }()
		fn()
	}()
	return <-done
}
