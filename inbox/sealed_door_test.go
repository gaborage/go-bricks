package inbox

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"testing"

	amqp "github.com/rabbitmq/amqp091-go"
	"github.com/stretchr/testify/require"

	kstest "github.com/gaborage/go-bricks/keystore/testing"
	"github.com/gaborage/go-bricks/messaging"
)

// The ledger's second door — a sealed dedup key — is admitted only under a delivery
// the sealed typed door opened. These helpers reach that context the way a
// consumer does: through DeclareTypedConsumerWithMeta on a seal-tagged type, with a
// stub codec standing in for messaging/sealed (which this package must not link).

type sealedEvent struct {
	_   struct{} `seal:"sign=svc-payments-sign,encrypt=acme-core-enc"`
	Ref string   `json:"ref" seal:"subject"`
}

type stubSpec struct{}

func (stubSpec) SignLogical() string    { return "svc-payments-sign" }
func (stubSpec) EncryptLogical() string { return "acme-core-enc" }

type stubOpener struct{}

func (stubOpener) Open(_ context.Context, _ []byte, _ messaging.SealTenantRule, out any) (messaging.SealEnvelope, error) {
	*out.(*sealedEvent) = sealedEvent{Ref: "abc"}
	return messaging.SealEnvelope{JTI: "9f0c2b1e-3f4a-4c8d-9e1f-0a2b3c4d5e6f", SignFamily: "svc-payments-sign"}, nil
}

type stubCodec struct{}

func (stubCodec) ScanType(t reflect.Type) (messaging.SealSpec, error) {
	if t == reflect.TypeOf(sealedEvent{}) {
		return stubSpec{}, nil
	}
	return nil, nil
}

func (stubCodec) NewSealer(messaging.SealSpec, string, *messaging.SealRuntime) (messaging.Sealer, error) {
	return nil, errors.New("producer side not under test")
}

func (stubCodec) NewOpener(messaging.SealSpec, string, *messaging.SealRuntime) (messaging.SealOpener, error) {
	return stubOpener{}, nil
}

var registerStubCodec sync.Once

// runSealed runs body inside a handler the sealed typed door installed, so its
// context carries the framework's sealed-delivery marker.
func runSealed(t *testing.T, body func(ctx context.Context) error) error {
	t.Helper()
	registerStubCodec.Do(func() { messaging.RegisterSealCodec(stubCodec{}) })
	messaging.ConfigureSealing(&messaging.SealRuntime{KeyStore: kstest.NewMockKeyStore()})

	decls := messaging.NewDeclarations()
	decls.DeclareQueue("q")
	opts := &messaging.ConsumerOptions{Queue: "q", Consumer: "c", EventType: "evt"}
	var result error
	messaging.DeclareTypedConsumerWithMeta(decls, opts, func(ctx context.Context, _ sealedEvent, meta messaging.Metadata) error {
		key, err := meta.DedupKey()
		require.NoError(t, err)
		require.True(t, messaging.IsSealedDedupKey(key))
		result = body(ctx)
		return result
	})
	require.NoError(t, decls.Validate())
	err := opts.Handler.Handle(t.Context(), &amqp.Delivery{Body: []byte("a.b.c")})
	require.Equal(t, result, err)
	return result
}
