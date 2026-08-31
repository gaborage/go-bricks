package app

import (
	"os/exec"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// corePackages are the packages a service that never uses native streams
// imports. None of them may pull messaging/streams or the vendor client
// (ADR-091). Listed explicitly so a new core package that re-introduces the
// edge is a test failure rather than a tidy surprise.
//
// outbox is omitted: it imports messaging/streams to declare native publishers
// for outbox.superstreams. inbox must stay off that import (hold types live on
// the seam).
var corePackages = []string{
	"github.com/gaborage/go-bricks/app",
	"github.com/gaborage/go-bricks/cache",
	"github.com/gaborage/go-bricks/config",
	"github.com/gaborage/go-bricks/database",
	"github.com/gaborage/go-bricks/inbox",
	"github.com/gaborage/go-bricks/jose",
	"github.com/gaborage/go-bricks/keystore",
	"github.com/gaborage/go-bricks/logger",
	"github.com/gaborage/go-bricks/messaging",
	"github.com/gaborage/go-bricks/migration",
	"github.com/gaborage/go-bricks/multitenant",
	"github.com/gaborage/go-bricks/observability",
	"github.com/gaborage/go-bricks/scheduler",
	"github.com/gaborage/go-bricks/server",
}

var forbiddenStreamClientModules = []string{
	"github.com/gaborage/go-bricks/messaging/streams",
	"github.com/rabbitmq/rabbitmq-stream-go-client",
	"github.com/golang/snappy",
	"github.com/pierrec/lz4",
	"github.com/pkg/errors",
	"github.com/spaolacci/murmur3",
}

func TestCorePackagesDoNotImportTheStreamClient(t *testing.T) {
	args := append([]string{"list", "-deps", "-f", "{{if not .Standard}}{{.ImportPath}}{{end}}"}, corePackages...)
	cmd := exec.CommandContext(t.Context(), "go", args...)
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "go list -deps failed: %s", out)

	var hits []string
	for _, line := range strings.Split(string(out), "\n") {
		if line == "" {
			continue
		}
		for _, forbidden := range forbiddenStreamClientModules {
			if line == forbidden || strings.HasPrefix(line, forbidden+"/") {
				hits = append(hits, line)
				break
			}
		}
	}
	require.Empty(t, hits, "core packages pulled the streams vendor tail:\n%s", strings.Join(hits, "\n"))
}
