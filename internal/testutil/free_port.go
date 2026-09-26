package testutil

import (
	"net"
	"testing"
)

// ReserveFreePort returns a loopback TCP port that was free a moment ago. Another process
// may take it before the caller binds, so use it only where a collision fails loudly.
func ReserveFreePort(t *testing.T) int {
	t.Helper()
	ln, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve a loopback port: %v", err)
	}
	tcpAddr, ok := ln.Addr().(*net.TCPAddr)
	if closeErr := ln.Close(); closeErr != nil {
		t.Fatalf("release the reserved port: %v", closeErr)
	}
	if !ok {
		t.Fatalf("loopback listener address is %T, not *net.TCPAddr", ln.Addr())
	}
	return tcpAddr.Port
}
