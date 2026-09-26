package testutil

import (
	"runtime"
	"strings"
)

// GoroutineDump returns every goroutine's stack, growing the buffer until the dump fits so no
// entry is truncated away.
func GoroutineDump() string {
	buf := make([]byte, 64<<10)
	for {
		n := runtime.Stack(buf, true)
		if n < len(buf) {
			return string(buf[:n])
		}
		buf = make([]byte, 2*len(buf))
	}
}

// ParkedInSelect counts the goroutines parked in a select inside frame, a function as the
// runtime prints it, such as "server.(*Server).checkApplicationListener(".
func ParkedInSelect(frame string) int {
	parked := 0
	for _, g := range strings.Split(GoroutineDump(), "\n\n") {
		if strings.Contains(g, "[select") && strings.Contains(g, frame) {
			parked++
		}
	}
	return parked
}
