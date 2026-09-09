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

func TestVisaMLEEnvelopeUnwrapExtractsEncData(t *testing.T) {
	_, unwrap := httpclient.VisaMLEEnvelope()

	compact, ok := unwrap("application/json", []byte(`{"encData":"header.key.iv.ct.tag"}`))

	assert.True(t, ok)
	assert.Equal(t, "header.key.iv.ct.tag", compact)
}

func TestVisaMLEEnvelopeUnwrapIgnoresSiblingMembers(t *testing.T) {
	_, unwrap := httpclient.VisaMLEEnvelope()

	compact, ok := unwrap("application/json", []byte(`{"responseId":"r-1","encData":"a.b.c.d.e","status":{"code":0}}`))

	assert.True(t, ok)
	assert.Equal(t, "a.b.c.d.e", compact)
}

func TestVisaMLEEnvelopeUnwrapRejectsNonEnvelopeBodies(t *testing.T) {
	_, unwrap := httpclient.VisaMLEEnvelope()

	tests := []struct {
		name string
		body string
	}{
		{name: "not_json", body: `<html><body>gateway error</body></html>`},
		{name: "json_non_object", body: `["a.b.c.d.e"]`},
		{name: "object_without_encdata", body: `{"errorCode":"9001","message":"denied"}`},
		{name: "encdata_not_a_string", body: `{"encData":{"jwe":"a.b.c.d.e"}}`},
		{name: "encdata_empty_string", body: `{"encData":""}`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			compact, ok := unwrap("application/json", []byte(tt.body))

			assert.False(t, ok)
			assert.Empty(t, compact)
		})
	}
}
