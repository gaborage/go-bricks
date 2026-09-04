package sealed_test

import (
	"context"
	"os"
	"testing"

	"github.com/gaborage/go-bricks/internal/publishdoor"
)

// frameRecorder is what the capturing doubles expose; the swapped dispatcher
// below routes a handle's frames to it, because a hand-written client carries
// no byte door of its own (ADR-096) and the framework's would refuse it.
type frameRecorder interface {
	record(opts publishdoor.Options, data []byte)
}

func TestMain(m *testing.M) {
	framework := publishdoor.Swap(func(ctx context.Context, client any, opts publishdoor.Options, data []byte) error {
		if rec, ok := client.(frameRecorder); ok {
			rec.record(opts, data)
			return nil
		}
		return frameworkDoor(ctx, client, opts, data)
	})
	frameworkDoor = framework
	code := m.Run()
	publishdoor.Swap(framework)
	os.Exit(code)
}

// frameworkDoor is the dispatcher package messaging registered; every client
// that is not a capturing double still goes through it.
var frameworkDoor publishdoor.Func
