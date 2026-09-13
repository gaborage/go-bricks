package messaging

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
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
				assert.False(t, key.Sealed())
				assert.Empty(t, key.String())
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.id, key.String())
			assert.False(t, key.Sealed())
		})
	}
}

// TestOnlyAllowlistedFunctionsMintASealedDedupKey pins every production site that can set DedupKey.sealed.
func TestOnlyAllowlistedFunctionsMintASealedDedupKey(t *testing.T) {
	files, err := filepath.Glob("*.go")
	require.NoError(t, err)
	sites := dedupKeySites{sealing: map[string]bool{}, literals: map[string]bool{}}
	fset := token.NewFileSet()
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, name, nil, 0)
		require.NoError(t, err)
		sites.scanFile(file)
	}
	allowlist := []string{}
	assert.ElementsMatch(t, allowlist, sortedSiteNames(sites.sealing))
	assert.Contains(t, sites.literals, "WireDedupKey", "the walk must see DedupKey literals")
}

type dedupKeySites struct {
	sealing  map[string]bool
	literals map[string]bool
}

func (s dedupKeySites) scanFile(file *ast.File) {
	for _, decl := range file.Decls {
		scope := "<package scope>"
		if fn, isFunc := decl.(*ast.FuncDecl); isFunc {
			scope = funcSiteName(fn)
		}
		ast.Inspect(decl, func(n ast.Node) bool {
			s.record(scope, n)
			return true
		})
	}
}

func (s dedupKeySites) record(scope string, n ast.Node) {
	switch node := n.(type) {
	case *ast.CompositeLit:
		if !isIdentNamed(node.Type, "DedupKey") {
			return
		}
		s.literals[scope] = true
		if literalSetsSealed(node) {
			s.sealing[scope] = true
		}
	case *ast.AssignStmt:
		for _, lhs := range node.Lhs {
			if sel, isSel := lhs.(*ast.SelectorExpr); isSel && sel.Sel.Name == "sealed" {
				s.sealing[scope] = true
			}
		}
	}
}

func literalSetsSealed(lit *ast.CompositeLit) bool {
	for _, elt := range lit.Elts {
		kv, keyed := elt.(*ast.KeyValueExpr)
		if !keyed {
			return len(lit.Elts) >= 2
		}
		if isIdentNamed(kv.Key, "sealed") {
			return true
		}
	}
	return false
}

func funcSiteName(fn *ast.FuncDecl) string {
	if fn.Recv == nil || len(fn.Recv.List) == 0 {
		return fn.Name.Name
	}
	recv := fn.Recv.List[0].Type
	if star, isStar := recv.(*ast.StarExpr); isStar {
		recv = star.X
	}
	if ident, isIdent := recv.(*ast.Ident); isIdent {
		return ident.Name + "." + fn.Name.Name
	}
	return "?." + fn.Name.Name
}

func isIdentNamed(expr ast.Expr, name string) bool {
	ident, isIdent := expr.(*ast.Ident)
	return isIdent && ident.Name == name
}

func sortedSiteNames(set map[string]bool) []string {
	names := make([]string, 0, len(set))
	for name := range set {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
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
