package outbox

import (
	"context"
	"os"
	"testing"

	"github.com/gaborage/go-bricks/internal/publishdoor"
)

// TestMain routes the relay's byte publishes for a *fakeAMQP to the fake and
// leaves every other client on messaging's real dispatcher. The door is
// unexported inside messaging (ADR-096), so this swap is how an outbox test
// observes what the relay handed to the broker.
func TestMain(m *testing.M) {
	var framework publishdoor.Func
	framework = publishdoor.Swap(func(ctx context.Context, client any, opts publishdoor.Options, data []byte) error {
		if f, ok := client.(*fakeAMQP); ok {
			return f.publishBytes(ctx, opts, data)
		}
		return framework(ctx, client, opts, data)
	})
	os.Exit(m.Run())
}
