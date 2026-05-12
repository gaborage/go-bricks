//go:build integration

package oracle

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/gaborage/go-bricks/testing/containers"
)

// pkgOracleContainer holds the single Oracle testcontainer provisioned once
// per package test-binary execution (ADR-020). Each test acquires an isolated
// user/schema via packageOracleContainer().NewSchema(t) and lets
// DROP USER ... CASCADE reclaim its objects on cleanup.
//
// Package-private and accessed only via packageOracleContainer() so callers
// can't accidentally swap it out mid-test.
var pkgOracleContainer *containers.OracleContainer

// packageOracleContainer returns the shared Oracle container provisioned in
// TestMain. Returns nil if TestMain has not been invoked (i.e. when this
// package is compiled into a test binary that bypasses TestMain — which
// shouldn't happen for the standard go test integration runner).
func packageOracleContainer() *containers.OracleContainer {
	return pkgOracleContainer
}

// TestMain provisions one Oracle container for the entire database/oracle
// integration test binary execution, then terminates it after m.Run.
//
// If Docker is unavailable, this prints a clear message and exits 0 so that
// non-integration go test invocations (e.g. running this package with -tags=
// off the integration flag never reach this file via the build tag) and CI
// runs without Docker don't fail the whole job — they're treated the same as
// "no integration tests to run".
func TestMain(m *testing.M) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)

	container, dockerAvailable, err := containers.StartOracleContainerForTestMain(ctx, nil)
	if !dockerAvailable {
		cancel()
		fmt.Fprintln(os.Stderr, "Docker is not available - skipping database/oracle integration tests. "+
			"Install Docker Desktop or ensure Docker daemon is running.")
		os.Exit(0)
	}
	if err != nil {
		cancel()
		fmt.Fprintf(os.Stderr, "failed to start shared Oracle container: %v\n", err)
		os.Exit(1)
	}

	pkgOracleContainer = container
	cancel()

	code := m.Run()

	termCtx, termCancel := context.WithTimeout(context.Background(), 60*time.Second)
	if termErr := container.Terminate(termCtx); termErr != nil {
		fmt.Fprintf(os.Stderr, "warning: failed to terminate shared Oracle container: %v\n", termErr)
	}
	termCancel()

	os.Exit(code)
}
