package windowsretrydrill

import (
	"os"
	"path/filepath"
	"testing"
)

// TestFailsOnceThenPasses fails on the first pass and passes on the retry: the
// marker it leaves is still there when the retry runs in the same job.
func TestFailsOnceThenPasses(t *testing.T) {
	dir := os.Getenv("RUNNER_TEMP")
	if dir == "" {
		dir = os.TempDir()
	}
	marker := filepath.Join(dir, "go-bricks-windows-retry-drill")
	if _, err := os.Stat(marker); err == nil {
		return
	}
	if err := os.WriteFile(marker, nil, 0o600); err != nil {
		t.Fatalf("write marker: %v", err)
	}
	t.Fatal("drill: first attempt fails on purpose; the retry should pass")
}
