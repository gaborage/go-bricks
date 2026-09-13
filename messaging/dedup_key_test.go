package messaging

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"sort"
	"strings"
	"testing"

	amqp "github.com/rabbitmq/amqp091-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestValidateEventIDVariesTheGrammar pins both boundaries of the grammar
// ^[A-Za-z0-9_-]{1,128}$: every accepted class, the 128-byte ceiling, and one
// rejection per way out of it — including the sealed-key shape family:jti.
func TestValidateEventIDVariesTheGrammar(t *testing.T) {
	cases := []struct {
		name string
		id   string
		ok   bool
	}{
		{"uuid", "9f0c2b1e-3f4a-4c8d-9e1f-0a2b3c4d5e6f", true},
		{"every_class", "aZ09_-", true},
		{"single_byte", "x", true},
		{"max_length_128", strings.Repeat("a", 128), true},
		{"empty", "", false},
		{"length_129", strings.Repeat("a", 129), false},
		{"colon_sealed_shape", "rsa:9f0c2b1e", false},
		{"newline", "evt-1\n", false},
		{"space", "evt 1", false},
		{"non_ascii", "evt-é", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateEventID(tc.id)
			if tc.ok {
				assert.NoError(t, err)
				return
			}
			require.ErrorIs(t, err, ErrInvalidEventID)
			if tc.id != "" {
				assert.NotContains(t, err.Error(), tc.id, "the error names the length, never the id")
			}
		})
	}
}

// TestValidateEventIDErrorCarriesLengthOnly pins the disclosure rule on the
// over-long path, where the value is the most likely to be attacker-shaped.
func TestValidateEventIDErrorCarriesLengthOnly(t *testing.T) {
	err := ValidateEventID(strings.Repeat("s", 129))
	require.ErrorIs(t, err, ErrInvalidEventID)
	assert.Contains(t, err.Error(), "129 bytes")
	assert.NotContains(t, err.Error(), "sss")
}

// TestWireDedupKeyAppliesTheGrammar pins construction-time admission for a wire
// key: both length boundaries, the sealed shape, and the round trip.
func TestWireDedupKeyAppliesTheGrammar(t *testing.T) {
	cases := []struct {
		name string
		id   string
		ok   bool
	}{
		{"uuid", "9f0c2b1e-3f4a-4c8d-9e1f-0a2b3c4d5e6f", true},
		{"max_length_128", strings.Repeat("w", 128), true},
		{"length_129", strings.Repeat("w", 129), false},
		{"empty", "", false},
		{"sealed_shape", "RS256:abc", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			key, err := WireDedupKey(tc.id)
			if !tc.ok {
				require.ErrorIs(t, err, ErrInvalidEventID)
				assert.Equal(t, DedupKey{}, key, "a refused id yields the invalid zero key")
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.id, key.String())
			assert.False(t, key.Sealed())
		})
	}
}

// TestDedupKeyZeroValueIsNotSealed pins the inert zero value.
func TestDedupKeyZeroValueIsNotSealed(t *testing.T) {
	assert.False(t, DedupKey{}.Sealed())
	assert.Empty(t, DedupKey{}.String())
}

// TestNoExportedDoorMintsASealedDedupKey walks this package's production source
// for every exported function or method whose RESULTS carry a DedupKey or a
// Metadata — the only two types a sealed key can travel in, both with
// unexported fields so no other package can spell one as a literal. The set is
// exact: a new exported producer fails here and must argue its way in. It is
// not vacuous — WireDedupKey, whose results are never Sealed, must be found.
func TestNoExportedDoorMintsASealedDedupKey(t *testing.T) {
	entries, err := os.ReadDir(".")
	require.NoError(t, err)
	fset := token.NewFileSet()

	var producers []string
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, name, nil, 0)
		require.NoError(t, err)
		producers = append(producers, exportedProducers(file)...)
	}
	sort.Strings(producers)
	assert.Equal(t, []string{"WireDedupKey"}, producers)
}

func exportedProducers(file *ast.File) []string {
	var producers []string
	for _, decl := range file.Decls {
		fn, isFunc := decl.(*ast.FuncDecl)
		if !isFunc || !fn.Name.IsExported() || fn.Type.Results == nil {
			continue
		}
		if !resultsName(fn.Type.Results, "DedupKey", "Metadata") {
			continue
		}
		producer := fn.Name.Name
		if fn.Recv != nil {
			producer = receiverTypeName(fn.Recv.List[0].Type) + "." + producer
		}
		producers = append(producers, producer)
	}
	return producers
}

func resultsName(results *ast.FieldList, names ...string) bool {
	found := false
	for _, field := range results.List {
		ast.Inspect(field.Type, func(n ast.Node) bool {
			if ident, isIdent := n.(*ast.Ident); isIdent {
				for _, want := range names {
					if ident.Name == want {
						found = true
					}
				}
			}
			return true
		})
	}
	return found
}

func receiverTypeName(expr ast.Expr) string {
	if star, isStar := expr.(*ast.StarExpr); isStar {
		expr = star.X
	}
	if ident, isIdent := expr.(*ast.Ident); isIdent {
		return ident.Name
	}
	return ""
}

func TestMetadataDedupKey(t *testing.T) {
	cases := []struct {
		name    string
		headers amqp.Table
		want    string
		wantErr bool
	}{
		{"string_header", amqp.Table{HeaderEventID: "evt-1"}, "evt-1", false},
		{"bytes_header", amqp.Table{HeaderEventID: []byte("evt-2")}, "evt-2", false},
		{"absent", amqp.Table{}, "", true},
		{"nil_table", nil, "", true},
		{"empty_string", amqp.Table{HeaderEventID: ""}, "", true},
		{"wrong_type", amqp.Table{HeaderEventID: int32(7)}, "", true},
		{"malformed_colon", amqp.Table{HeaderEventID: "hmac:abc"}, "", true},
		{"malformed_bytes", amqp.Table{HeaderEventID: []byte("a b")}, "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			meta := Metadata{delivery: &amqp.Delivery{Headers: tc.headers}}
			got, err := meta.DedupKey()
			if tc.wantErr {
				require.ErrorIs(t, err, ErrInvalidEventID)
				assert.Empty(t, got)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}

// TestMetadataDedupKeyZeroValue pins the inert zero Metadata: no delivery is an
// absent header, not a panic.
func TestMetadataDedupKeyZeroValue(t *testing.T) {
	_, err := Metadata{}.DedupKey()
	assert.ErrorIs(t, err, ErrInvalidEventID)
}

// TestMetadataSealedIsFalseForPlainConsumers pins that the answer is per type:
// a publisher-written header cannot flip it.
func TestMetadataSealedIsFalseForPlainConsumers(t *testing.T) {
	for name, meta := range map[string]Metadata{
		"zero":            {},
		"plain_delivery":  {delivery: &amqp.Delivery{Headers: amqp.Table{HeaderEventID: "evt-1"}}},
		"sealed_looking":  {delivery: &amqp.Delivery{Headers: amqp.Table{"x-sealed": true, "jti": "abc"}}},
		"encrypted_ctype": {delivery: &amqp.Delivery{ContentType: "application/jose"}},
	} {
		t.Run(name, func(t *testing.T) {
			env, ok := meta.Sealed()
			assert.False(t, ok)
			assert.Equal(t, SealedEnvelope{}, env)
		})
	}
}

func TestMetadataSealedAndDedupKeyForASealedDelivery(t *testing.T) {
	env := SealedEnvelope{JTI: "jti-1", SignKid: "svc-sign-v2", SignFamily: "svc-sign", EncKid: "aud-enc-v1", EventType: "evt", TenantID: "acme"}
	// The delivery carries BOTH unsealed sources; neither may steer a sealed key.
	meta := Metadata{delivery: &amqp.Delivery{Headers: amqp.Table{HeaderEventID: "header-id"}, MessageId: "prop-1"}, sealed: &env}

	got, ok := meta.Sealed()
	assert.True(t, ok)
	assert.Equal(t, env, got)

	key, err := meta.DedupKey()
	require.NoError(t, err)
	assert.Equal(t, "svc-sign:jti-1", key, "the sealed key wins over any header the publisher wrote")
	assert.True(t, IsSealedDedupKey(key))
	assert.ErrorIs(t, ValidateEventID(key), ErrInvalidEventID, "a sealed key is outside the header grammar by construction")
}

// sealedTestKey is what DedupKey composes from the envelope the sealed rows of
// TestMetadataDedupKeyStampAndMessageIDPrecedence carry.
const sealedTestKey = "svc-sign:jti-1"

// TestMetadataDedupKeyStampAndMessageIDPrecedence pins #1547 on both dimensions
// at once: the stamp wins whenever its KEY is present — a stamp that is present
// but malformed errors rather than falling through, because the stamp is
// framework-written and the property caller-written — and the message_id
// property answers only for a delivery carrying no stamp key at all, under the
// same grammar, so a `:` in it can never mint a sealed key. The grammar itself
// is pinned by TestValidateEventIDVariesTheGrammar; what is new here is which
// source is routed through it.
func TestMetadataDedupKeyStampAndMessageIDPrecedence(t *testing.T) {
	cases := []struct {
		name      string
		headers   amqp.Table
		messageID string
		sealed    bool
		want      string
		wantErr   bool
	}{
		{name: "stamp_beats_property", headers: amqp.Table{HeaderEventID: "evt-1"}, messageID: "prop-1", want: "evt-1"},
		{name: "stamp_beats_malformed_property", headers: amqp.Table{HeaderEventID: "evt-1"}, messageID: "a b", want: "evt-1"},
		{name: "malformed_stamp_does_not_fall_through", headers: amqp.Table{HeaderEventID: "a b"}, messageID: "prop-1", wantErr: true},
		{name: "empty_stamp_does_not_fall_through", headers: amqp.Table{HeaderEventID: ""}, messageID: "prop-1", wantErr: true},
		{name: "wrong_type_stamp_does_not_fall_through", headers: amqp.Table{HeaderEventID: int32(7)}, messageID: "prop-1", wantErr: true},
		{name: "property_when_unstamped", headers: amqp.Table{}, messageID: "9f0c2b1e-3f4a-4c8d-9e1f-0a2b3c4d5e6f", want: "9f0c2b1e-3f4a-4c8d-9e1f-0a2b3c4d5e6f"},
		{name: "property_when_no_table_at_all", messageID: "prop-1", want: "prop-1"},
		{name: "other_headers_do_not_stamp", headers: amqp.Table{"x-idempotency-key": "business-key"}, messageID: "prop-1", want: "prop-1"},
		{name: "malformed_property", headers: amqp.Table{}, messageID: "prop 1", wantErr: true},
		{name: "property_spelling_a_sealed_key", headers: amqp.Table{}, messageID: "svc-payments-sign:9f0c2b1e", wantErr: true},
		// The sealed rows cover the quadrant this change introduced: an UNSTAMPED
		// sealed delivery, where the fallback would fire if the sealed branch did
		// not return first. The envelope answers whatever the property spells,
		// malformed or sealed-shaped included.
		{name: "sealed_ignores_a_valid_property", messageID: "prop-1", sealed: true, want: sealedTestKey},
		{name: "sealed_ignores_a_malformed_property", messageID: "a b", sealed: true, want: sealedTestKey},
		{name: "sealed_ignores_an_absent_property", headers: amqp.Table{}, sealed: true, want: sealedTestKey},
		{name: "sealed_ignores_a_property_spelling_another_sealed_key", messageID: "other-family:other-jti", sealed: true, want: sealedTestKey},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			meta := Metadata{delivery: &amqp.Delivery{Headers: tc.headers, MessageId: tc.messageID}}
			if tc.sealed {
				meta.sealed = &SealedEnvelope{JTI: "jti-1", SignFamily: "svc-sign"}
			}
			got, err := meta.DedupKey()
			if tc.wantErr {
				require.ErrorIs(t, err, ErrInvalidEventID)
				assert.Empty(t, got)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}

// TestMetadataMessageID pins the accessor the fallback reads through, including
// the inert zero value.
func TestMetadataMessageID(t *testing.T) {
	assert.Equal(t, "prop-1", Metadata{delivery: &amqp.Delivery{MessageId: "prop-1"}}.MessageID())
	assert.Empty(t, Metadata{delivery: &amqp.Delivery{}}.MessageID())
	assert.Empty(t, Metadata{}.MessageID())
}
