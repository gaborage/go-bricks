package outbox

import (
	"errors"
	"fmt"
	"reflect"

	"github.com/gaborage/go-bricks/messaging"
)

// ErrSealedPayloadNeedsBytes: a struct (or pointer) payload whose type carries
// `seal` tags reached Publish as plaintext. The outbox persists what it is
// given, so the sealed form must be produced first — Publisher[T].Seal — and
// handed over as []byte. Only the struct door is guarded; a hand-marshaled
// plaintext []byte is the documented residual.
var ErrSealedPayloadNeedsBytes = errors.New("outbox: payload type carries seal tags; seal it first with Publisher[T].Seal and publish the returned bytes")

// rejectSealedPayload is the outbox lane guard: a struct or pointer payload
// whose type carries `seal` tags must arrive already sealed as []byte, so it
// is refused before json.Marshal would persist it in plaintext. []byte and nil
// never reach here; a plain struct passes.
func rejectSealedPayload(payload any) error {
	if t := reflect.TypeOf(payload); messaging.IsSealTagged(t) {
		return fmt.Errorf("%w (payload type %v)", ErrSealedPayloadNeedsBytes, t)
	}
	return nil
}
