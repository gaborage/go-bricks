package testutil

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"testing"
	"time"
)

// Listener is the slice of *server.Server that StartListener drives. It is an interface so
// this package stays free of the server import, which server's own tests would cycle on.
type Listener interface {
	Start() error
	Shutdown(ctx context.Context) error
	ReadyCh() <-chan struct{}
	BoundAddr() net.Addr
}

// StartListener starts srv, waits up to 5s for it to be ready, and returns its base URL
// ("http://" + the bound address). At cleanup it shuts srv down and waits up to 1s for Start
// to return.
func StartListener(t *testing.T, srv Listener) string {
	t.Helper()
	errCh := make(chan error, 1)
	startReturned := false
	go func() { errCh <- srv.Start() }()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := srv.Shutdown(ctx); err != nil {
			t.Errorf("shutdown: %v", err)
		}
		if startReturned {
			return
		}
		select {
		case err := <-errCh:
			if err != nil && !errors.Is(err, http.ErrServerClosed) {
				t.Errorf("server stopped with: %v", err)
			}
		case <-time.After(time.Second):
			t.Error("server did not stop within 1s of Shutdown")
		}
	})
	select {
	case <-srv.ReadyCh():
	case err := <-errCh:
		startReturned = true
		t.Fatalf("server stopped before it was ready: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("server was not ready within 5s")
	}
	return "http://" + srv.BoundAddr().String() // NOSONAR S5332: loopback test listener, no TLS to terminate
}

// AssertGETStatuses sends GET base+path for every path in want and reports each response
// whose status differs from want[path].
func AssertGETStatuses(t *testing.T, base string, want map[string]int) {
	t.Helper()
	for path, status := range want {
		req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, base+path, http.NoBody)
		if err != nil {
			t.Fatalf("build GET %s: %v", path, err)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
		if resp.StatusCode != status {
			t.Errorf("GET %s: status %d, want %d", path, resp.StatusCode, status)
		}
	}
}
