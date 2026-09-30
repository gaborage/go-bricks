package messaging

import (
	"os/exec"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

const sealedPackage = "github.com/gaborage/go-bricks/messaging/sealed"

var forbiddenJoseModules = []string{
	"github.com/go-jose",
	"github.com/gaborage/go-bricks/jose",
}

// TestMessagingStaysJoseFree pins the import gate ADR-097 relies on: the non-test import
// graph of every messaging package except messaging/sealed holds neither go-jose nor go-bricks/jose.
func TestMessagingStaysJoseFree(t *testing.T) {
	pkgs, err := exec.CommandContext(t.Context(), "go", "list", "github.com/gaborage/go-bricks/messaging/...").CombinedOutput()
	require.NoError(t, err, "go list failed: %s", pkgs)
	roots := slices.DeleteFunc(strings.Fields(string(pkgs)), func(pkg string) bool { return pkg == sealedPackage })

	args := append([]string{"list", "-deps", "-f", "{{if not .Standard}}{{.ImportPath}}{{end}}"}, roots...)
	out, err := exec.CommandContext(t.Context(), "go", args...).CombinedOutput()
	require.NoError(t, err, "go list -deps failed: %s", out)

	var hits []string
	for _, line := range strings.Split(string(out), "\n") {
		if line == "" {
			continue
		}
		for _, forbidden := range forbiddenJoseModules {
			if line == forbidden || strings.HasPrefix(line, forbidden+"/") {
				hits = append(hits, line)
				break
			}
		}
	}
	require.Empty(t, hits, "messaging packages other than messaging/sealed link jose:\n%s", strings.Join(hits, "\n"))
}
