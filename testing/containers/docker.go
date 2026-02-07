//go:build integration

package containers

import (
	"context"

	"github.com/testcontainers/testcontainers-go"
)

// isDockerAvailable checks if the Docker daemon is reachable by attempting to
// connect via the testcontainers Docker provider. Returns false if the daemon
// cannot be contacted.
func isDockerAvailable(ctx context.Context) bool {
	provider, err := testcontainers.NewDockerProvider()
	if err != nil {
		return false
	}
	defer provider.Close()

	// Try to get Docker info - if this fails, Docker is not available
	_, err = provider.DaemonHost(ctx)
	return err == nil
}
