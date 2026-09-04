package sealed_test

import (
	"reflect"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	josesealed "github.com/gaborage/go-bricks/jose/sealed"
	"github.com/gaborage/go-bricks/messaging/internal/sealruntime"
)

func TestInitRegistersTheCodec(t *testing.T) {
	codec := sealruntime.Registered()
	require.NotNil(t, codec, "importing messaging/sealed must register the codec")
	sp, err := codec.ScanType(reflect.TypeOf(plainEvent{}))
	require.NoError(t, err)
	assert.Nil(t, sp, "a plain type scans to nil")
	sp, err = codec.ScanType(reflect.TypeOf(paymentAuthorized{}))
	require.NoError(t, err)
	assert.Equal(t, signFamily, sp.SignLogical())
	assert.Equal(t, encFamily, sp.EncryptLogical())
	_, err = codec.ScanType(reflect.TypeOf(struct {
		_ struct{} `seal:"sign=s,encrypt=e"`
	}{}))
	assert.ErrorIs(t, err, josesealed.ErrTagInvalid)
	_, isFactory := codec.(sealruntime.OpenerProvider)
	assert.False(t, isFactory, "the opener side lands in #1359")
}
