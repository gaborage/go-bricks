package httpclient_test

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gaborage/go-bricks/httpclient"
)

func TestVisaMLEEnvelopeWrapProducesEncDataObject(t *testing.T) {
	wrap, _ := httpclient.VisaMLEEnvelope()

	body, contentType, err := wrap("eyJhbGciOiJSU0EtT0FFUC0yNTYifQ.aaa.bbb.ccc.ddd")

	require.NoError(t, err)
	assert.Equal(t, "application/json", contentType)

	var decoded map[string]any
	require.NoError(t, json.Unmarshal(body, &decoded))
	assert.Equal(t, map[string]any{"encData": "eyJhbGciOiJSU0EtT0FFUC0yNTYifQ.aaa.bbb.ccc.ddd"}, decoded)
}
