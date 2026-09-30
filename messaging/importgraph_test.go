package messaging

import (
	"os/exec"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestMessagingStaysJoseFree pins the import gate ADR-097 relies on: only messaging/sealed links jose.
func TestMessagingStaysJoseFree(t *testing.T) {
	out, err := exec.CommandContext(t.Context(), "go", "list", "-deps", "-f", "{{.ImportPath}}",
		"github.com/gaborage/go-bricks/messaging", "github.com/gaborage/go-bricks/messaging/testing").CombinedOutput()
	require.NoError(t, err, "go list -deps failed: %s", out)
	for line := range strings.Lines(string(out)) {
		path := strings.TrimSpace(line)
		assert.False(t, strings.HasPrefix(path, "github.com/go-jose/"), "messaging links %s", path)
		assert.False(t, path == "github.com/gaborage/go-bricks/jose" || strings.HasPrefix(path, "github.com/gaborage/go-bricks/jose/"),
			"messaging links %s", path)
	}
}
