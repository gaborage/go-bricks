package streams

import (
	"fmt"
	"reflect"

	"github.com/gaborage/go-bricks/messaging"
)

// rejectSealedType is the v1 lane guard: sealing is classic-lane only, so a
// seal-tagged T on a stream declaration is refused at declaration time, loudly —
// the alternative is a consumer that silently reads sealed bodies as plaintext
// and poisons every delivery. Lifting it is the post-classic-lane extension.
func rejectSealedType[T any](entry string) {
	if t := reflect.TypeFor[T](); messaging.IsSealTagged(t) {
		panic(fmt.Sprintf(
			"streams: %s refuses %v: it carries `seal` tags, and payload sealing is not supported on the stream lane in v1\n"+
				"  Consume sealed events through the classic AMQP typed consumer",
			entry, t,
		))
	}
}
