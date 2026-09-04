package messaging

import (
	"reflect"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestModuleFacingTypesCarryNoBytePublishDoor is ADR-096's acceptance check: the
// types a module can hold — Client, AMQPClient and the framework's own client and
// wrapper as seen THROUGH those interfaces — expose neither Publish nor
// PublishToExchange, so no exported path hands bytes to the broker. The
// unexported bytePublisher is reachable only from inside this package.
func TestModuleFacingTypesCarryNoBytePublishDoor(t *testing.T) {
	for name, typ := range map[string]reflect.Type{
		"Client":     reflect.TypeOf((*Client)(nil)).Elem(),
		"AMQPClient": reflect.TypeOf((*AMQPClient)(nil)).Elem(),
	} {
		t.Run(name, func(t *testing.T) {
			require.Equal(t, reflect.Interface, typ.Kind())
			assertNoByteDoor(t, typ)
		})
	}

	// The concrete types too: a module that type-asserts its client to the
	// framework implementation must find no exported byte door either.
	for name, typ := range map[string]reflect.Type{
		"AMQPClientImpl":    reflect.TypeOf((*AMQPClientImpl)(nil)),
		"stampingPublisher": reflect.TypeOf((*stampingPublisher)(nil)),
	} {
		t.Run(name, func(t *testing.T) {
			assertNoByteDoor(t, typ)
		})
	}
}

// assertNoByteDoor checks the EXPORTED method set (reflect lists only exported
// methods for a non-interface type, and every method for an interface).
func assertNoByteDoor(t *testing.T, typ reflect.Type) {
	t.Helper()
	for _, name := range []string{"Publish", "PublishToExchange", "PublishBytes"} {
		_, found := typ.MethodByName(name)
		assert.Falsef(t, found, "%s must not expose %s", typ, name)
	}
}

// TestFrameworkClientsImplementTheByteDoor pins the other half: the framework's
// own client and the stamping wrapper DO satisfy the unexported door the typed
// handle and the relay publish through.
func TestFrameworkClientsImplementTheByteDoor(t *testing.T) {
	var _ bytePublisher = (*AMQPClientImpl)(nil)
	var _ bytePublisher = (*stampingPublisher)(nil)

	_, ok := any(&stampingPublisher{}).(bytePublisher)
	assert.True(t, ok)
}

// TestPublishThroughDoorRefusesAClientWithoutOne pins the typed error a
// consumer-built or mocked client produces.
func TestPublishThroughDoorRefusesAClientWithoutOne(t *testing.T) {
	err := publishThroughDoor(t.Context(), struct{ AMQPClient }{}, publishOptions{RoutingKey: "q"}, []byte("x"))
	require.ErrorIs(t, err, ErrPublishDoorUnavailable)
	assert.NotContains(t, err.Error(), "q", "the error names the client type, never the destination")
}
