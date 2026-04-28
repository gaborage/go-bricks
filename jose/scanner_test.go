package jose

import (
	"errors"
	"reflect"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type taggedRequest struct {
	_   struct{} `jose:"decrypt=our-key,verify=peer-key"`
	Pan string   `json:"pan"`
}

type taggedResponse struct {
	_     struct{} `jose:"sign=our-key,encrypt=peer-key"`
	Token string   `json:"token"`
}

type untaggedRequest struct {
	Pan string `json:"pan"`
}

type taggedRequestWithBadKid struct {
	_ struct{} `jose:"decrypt=our key,verify=peer-key"`
}

func TestScanTypeRequestTagged(t *testing.T) {
	p, err := ScanType(reflect.TypeOf(taggedRequest{}), DirectionInbound)
	require.NoError(t, err)
	require.NotNil(t, p)
	assert.Equal(t, "our-key", p.DecryptKid)
	assert.Equal(t, "peer-key", p.VerifyKid)
}

func TestScanTypeResponseTagged(t *testing.T) {
	p, err := ScanType(reflect.TypeOf(taggedResponse{}), DirectionOutbound)
	require.NoError(t, err)
	require.NotNil(t, p)
	assert.Equal(t, "our-key", p.SignKid)
	assert.Equal(t, "peer-key", p.EncryptKid)
}

func TestScanTypeUntagged(t *testing.T) {
	p, err := ScanType(reflect.TypeOf(untaggedRequest{}), DirectionInbound)
	require.NoError(t, err)
	assert.Nil(t, p)
}

func TestScanTypePointer(t *testing.T) {
	p, err := ScanType(reflect.TypeOf(&taggedRequest{}), DirectionInbound)
	require.NoError(t, err)
	require.NotNil(t, p)
	assert.Equal(t, "our-key", p.DecryptKid)
}

func TestScanTypeNonStruct(t *testing.T) {
	p, err := ScanType(reflect.TypeOf("string"), DirectionInbound)
	require.NoError(t, err)
	assert.Nil(t, p)
}

func TestScanTypeNilType(t *testing.T) {
	p, err := ScanType(nil, DirectionInbound)
	require.NoError(t, err)
	assert.Nil(t, p)
}

func TestScanTypeBadKidPropagatesError(t *testing.T) {
	_, err := ScanType(reflect.TypeOf(taggedRequestWithBadKid{}), DirectionInbound)
	require.Error(t, err)
	var jerr *Error
	require.True(t, errors.As(err, &jerr))
	assert.Equal(t, "JOSE_TAG_KID_INVALID", jerr.Code)
}
