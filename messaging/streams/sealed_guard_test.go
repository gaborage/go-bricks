package streams

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// sealedStreamOrder is streamOrder with the seal sentinel: the ONE dimension the
// guard judges, so the pair below proves the tag, not the shape, is refused.
type sealedStreamOrder struct {
	_      struct{} `seal:"sign=svc-sign,encrypt=aud-enc"`
	ID     string   `json:"id"`
	Amount int      `json:"amount" seal:"subject"`
}

// TestTypedDeclarationsRefuseSealedTypeAtEveryDoor: all four typed declaration
// doors panic on a seal-tagged T at declaration time (v1 is classic-lane only),
// naming the entry point, and the same declaration with the untagged twin is
// accepted.
func TestTypedDeclarationsRefuseSealedTypeAtEveryDoor(t *testing.T) {
	sealedFn := func(context.Context, sealedStreamOrder) error { return nil }
	sealedMeta := func(context.Context, sealedStreamOrder, *Message) error { return nil }
	plainFn := func(context.Context, streamOrder) error { return nil }
	plainMeta := func(context.Context, streamOrder, *Message) error { return nil }

	consumerOpts := func() *ConsumerOptions {
		return &ConsumerOptions{Stream: testStream, Name: testConsumerName}
	}
	superOpts := func() *SuperStreamConsumerOptions {
		return &SuperStreamConsumerOptions{SuperStream: testSuperStream, Name: testConsumerName}
	}

	doors := []struct {
		name   string
		entry  string
		sealed func(*Declarations)
		plain  func(*Declarations)
	}{
		{
			name: "DeclareTypedConsumer", entry: "DeclareTypedConsumerWithMeta",
			sealed: func(d *Declarations) { DeclareTypedConsumer(d, consumerOpts(), sealedFn) },
			plain:  func(d *Declarations) { DeclareTypedConsumer(d, consumerOpts(), plainFn) },
		},
		{
			name: "DeclareTypedConsumerWithMeta", entry: "DeclareTypedConsumerWithMeta",
			sealed: func(d *Declarations) { DeclareTypedConsumerWithMeta(d, consumerOpts(), sealedMeta) },
			plain:  func(d *Declarations) { DeclareTypedConsumerWithMeta(d, consumerOpts(), plainMeta) },
		},
		{
			name: "DeclareTypedSuperStreamConsumer", entry: "DeclareTypedSuperStreamConsumerWithMeta",
			sealed: func(d *Declarations) { DeclareTypedSuperStreamConsumer(d, superOpts(), sealedFn) },
			plain:  func(d *Declarations) { DeclareTypedSuperStreamConsumer(d, superOpts(), plainFn) },
		},
		{
			name: "DeclareTypedSuperStreamConsumerWithMeta", entry: "DeclareTypedSuperStreamConsumerWithMeta",
			sealed: func(d *Declarations) { DeclareTypedSuperStreamConsumerWithMeta(d, superOpts(), sealedMeta) },
			plain:  func(d *Declarations) { DeclareTypedSuperStreamConsumerWithMeta(d, superOpts(), plainMeta) },
		},
	}
	for _, door := range doors {
		t.Run(door.name, func(t *testing.T) {
			sealedDecls := NewDeclarations()
			var got any
			func() {
				defer func() { got = recover() }()
				door.sealed(sealedDecls)
			}()
			require.NotNil(t, got, "seal-tagged T must be refused at declaration")
			msg, ok := got.(string)
			require.True(t, ok, "panic value is a string, type %T", got)
			assert.Contains(t, msg, "streams: "+door.entry+" refuses")
			assert.Contains(t, msg, "sealedStreamOrder")
			assert.Contains(t, msg, "`seal` tags")
			assert.Empty(t, sealedDecls.consumers, "a refused declaration registers nothing")

			plainDecls := NewDeclarations()
			assert.NotPanics(t, func() { door.plain(plainDecls) })
		})
	}
}
