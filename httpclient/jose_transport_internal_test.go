package httpclient

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestJOSETransportEffectiveMaxResponseBytes pins the one place where "negative means
// unbounded" does NOT hold. Builder.WithJOSE refuses a negative cap beside an UnwrapBody
// hook at Build time, but JOSETransport is exported and a hand-built one carrying that
// pair reaches unwrapResponse unchecked — where the Content-Type gate no longer keeps a
// non-JOSE body unread, so an unbounded io.ReadAll of a peer's body is all that is left.
func TestJOSETransportEffectiveMaxResponseBytes(t *testing.T) {
	hook := UnwrapBodyFunc(func(_ string, _ []byte) (compact string, ok bool) { return "", false })

	tests := []struct {
		name       string
		configured int64
		unwrap     UnwrapBodyFunc
		want       int64
	}{
		{name: "zero_takes_the_default_without_a_hook", configured: 0, want: DefaultMaxJOSEBodyBytes},
		{name: "zero_takes_the_default_with_a_hook", configured: 0, unwrap: hook, want: DefaultMaxJOSEBodyBytes},
		{name: "explicit_cap_is_kept_without_a_hook", configured: 4096, want: 4096},
		{name: "explicit_cap_is_kept_with_a_hook", configured: 4096, unwrap: hook, want: 4096},
		// The documented escape hatch survives: without a hook the Content-Type gate has
		// already decided the body is JOSE, so an unbounded read stays the caller's choice.
		{name: "negative_stays_unbounded_without_a_hook", configured: -1, want: -1},
		// ...and is overridden the moment a hook removes that gate.
		{name: "negative_falls_back_to_the_default_with_a_hook", configured: -1, unwrap: hook, want: DefaultMaxJOSEBodyBytes},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			transport := &JOSETransport{MaxResponseBytes: tt.configured, UnwrapBody: tt.unwrap}
			assert.Equal(t, tt.want, transport.effectiveMaxResponseBytes())
		})
	}
}
