//go:build !windows

package httpclient

import (
	"os"
	"path/filepath"
	"runtime/debug"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBuildBearerTokenFileRejectsFIFOWithoutBlocking(t *testing.T) {
	path := filepath.Join(t.TempDir(), "token")
	require.NoError(t, syscall.Mkfifo(path, 0o600))

	done := make(chan error, 1)
	go func() {
		_, err := NewBuilder(quietLogger()).WithBearerTokenFile(path).Build()
		done <- err
	}()

	select {
	case err := <-done:
		require.ErrorIs(t, err, errNotRegularFile)
	case <-time.After(5 * time.Second):
		// Build is blocked in open(2) waiting for a writer; connect one to free it.
		if w, err := os.OpenFile(path, os.O_WRONLY|syscall.O_NONBLOCK, 0); err == nil {
			_ = w.Close()
		}
		t.Fatal("Build blocked opening a FIFO")
	}
}

func TestReadBearerTokenFileClosesItsDescriptor(t *testing.T) {
	const reads = 64
	path := writeTestFile(t, t.TempDir(), "token", []byte("tok-fd"))
	openFDs := func() int {
		entries, err := os.ReadDir("/dev/fd")
		require.NoError(t, err)
		return len(entries)
	}
	// GC off: a leaked *os.File would otherwise be closed by its GC cleanup.
	prev := debug.SetGCPercent(-1)
	defer debug.SetGCPercent(prev)

	before := openFDs()
	for range reads {
		_, err := readBearerTokenFile(path)
		require.NoError(t, err)
	}
	assert.Less(t, openFDs()-before, reads/2, "every read must close its descriptor")
}
