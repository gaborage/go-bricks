package messaging

import (
	"go/ast"
	"go/build"
	"go/importer"
	"go/parser"
	"go/token"
	"go/types"
	"io"
	"os"
	"os/exec"
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

// TestOnlyAllowlistedFunctionsMintASealedDedupKey type-checks this package's
// production files and pins every site that can set DedupKey.sealed — a
// literal of any spelling (alias, elided type), a conversion, a field write or
// address — and every function referencing the sealed constructor, called or
// not. Unexported fields already make an outside literal a compile error; the
// risk is an in-package door.
func TestOnlyAllowlistedFunctionsMintASealedDedupKey(t *testing.T) {
	sites := scanDedupKeySites(t)
	assert.ElementsMatch(t, []string{"sealedDedupKey"}, siteNames(sites.sealing))
	assert.ElementsMatch(t, []string{"Metadata.DedupKey"}, siteNames(sites.constructorRefs))
}

// dedupKeyWalkFixture carries one site per minting shape and one constructor
// caller, with no imports so it type-checks without an importer.
const dedupKeyWalkFixture = `package fixture

type DedupKey struct {
	key    string
	sealed bool
}

type twin struct {
	key    string
	sealed bool
}

func sealedDedupKey() DedupKey { return DedupKey{key: "k", sealed: true} }

func unkeyed() DedupKey { return DedupKey{"k", true} }

func assigned(k *DedupKey) { k.sealed = true }

func converted(t twin) DedupKey { return DedupKey(t) }

func addressed(k *DedupKey) *bool { return &k.sealed }

func caller() DedupKey { return sealedDedupKey() }
`

// TestDedupKeySiteWalkRecordsEveryMintingShape runs the walk over the fixture,
// so the production pin cannot pass vacuously on a regressed walker.
func TestDedupKeySiteWalkRecordsEveryMintingShape(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "fixture.go", dedupKeyWalkFixture, 0)
	require.NoError(t, err)
	sites := checkDedupKeySites(t, fset, []*ast.File{file}, "fixture", nil)
	assert.ElementsMatch(t, []string{"sealedDedupKey", "unkeyed", "assigned", "converted", "addressed"}, siteNames(sites.sealing))
	assert.ElementsMatch(t, []string{"caller"}, siteNames(sites.constructorRefs))
}

type dedupKeySites struct {
	info            *types.Info
	dedupKey        types.Type
	sealedField     types.Object
	constructor     types.Object
	sealing         map[string]bool
	constructorRefs map[string]bool
}

// scanDedupKeySites type-checks this package rather than walking names: type
// resolution is what closes the alias, conversion and elided-literal holes a
// name walk misses. The cost is a toolchain dependency — it resolves imports
// from `go list -export -deps`, so `go` must be on PATH. GoFiles omits files
// behind build constraints, so a constrained production file fails the scan
// rather than hiding a mint from it.
func scanDedupKeySites(t *testing.T) *dedupKeySites {
	t.Helper()
	dir, err := build.ImportDir(".", 0)
	require.NoError(t, err)
	for _, name := range dir.IgnoredGoFiles {
		require.Truef(t, strings.HasSuffix(name, "_test.go"),
			"%s is a production file behind a build constraint, so GoFiles never reaches it: extend this walk before adding one", name)
	}
	fset := token.NewFileSet()
	files := make([]*ast.File, 0, len(dir.GoFiles))
	for _, name := range dir.GoFiles {
		file, parseErr := parser.ParseFile(fset, name, nil, 0)
		require.NoError(t, parseErr)
		files = append(files, file)
	}
	return checkDedupKeySites(t, fset, files, "github.com/gaborage/go-bricks/messaging", importer.ForCompiler(fset, "gc", exportDataLookup(t)))
}

// checkDedupKeySites type-checks files as one package and walks them for
// DedupKey sites.
func checkDedupKeySites(t *testing.T, fset *token.FileSet, files []*ast.File, path string, imp types.Importer) *dedupKeySites {
	t.Helper()
	info := &types.Info{
		Types:      map[ast.Expr]types.TypeAndValue{},
		Uses:       map[*ast.Ident]types.Object{},
		Selections: map[*ast.SelectorExpr]*types.Selection{},
	}
	conf := types.Config{Importer: imp}
	pkg, err := conf.Check(path, fset, files, info)
	require.NoError(t, err)

	dedupKey := pkg.Scope().Lookup("DedupKey").Type()
	sites := &dedupKeySites{
		info:            info,
		dedupKey:        dedupKey,
		sealedField:     structField(t, dedupKey, "sealed"),
		constructor:     pkg.Scope().Lookup("sealedDedupKey"),
		sealing:         map[string]bool{},
		constructorRefs: map[string]bool{},
	}
	require.NotNil(t, sites.constructor)
	for _, file := range files {
		sites.scanFile(file)
	}
	return sites
}

// exportDataLookup resolves imports from the build cache's export data; the
// source importer re-type-checks every dependency and costs ~30s.
func exportDataLookup(t *testing.T) importer.Lookup {
	t.Helper()
	out, err := exec.CommandContext(t.Context(), "go", "list", "-export", "-deps", "-f", "{{.ImportPath}}={{.Export}}", ".").Output()
	require.NoError(t, err)
	exports := map[string]string{}
	for line := range strings.Lines(string(out)) {
		path, export, _ := strings.Cut(strings.TrimSpace(line), "=")
		exports[path] = export
	}
	return func(path string) (io.ReadCloser, error) {
		return os.Open(exports[path])
	}
}

func structField(t *testing.T, typ types.Type, name string) types.Object {
	t.Helper()
	fields, isStruct := typ.Underlying().(*types.Struct)
	require.True(t, isStruct)
	for field := range fields.Fields() {
		if field.Name() == name {
			return field
		}
	}
	require.Failf(t, "field not found", "%s", name)
	return nil
}

func (s *dedupKeySites) scanFile(file *ast.File) {
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

func (s *dedupKeySites) record(scope string, n ast.Node) {
	switch node := n.(type) {
	case *ast.Ident:
		if s.info.Uses[node] == s.constructor {
			s.constructorRefs[scope] = true
		}
	case *ast.CompositeLit:
		s.recordLiteral(scope, node)
	case *ast.CallExpr:
		if tv, known := s.info.Types[node.Fun]; known && tv.IsType() && types.Identical(tv.Type, s.dedupKey) {
			s.sealing[scope] = true
		}
	case *ast.AssignStmt:
		for _, lhs := range node.Lhs {
			s.recordFieldAccess(scope, lhs)
		}
	case *ast.UnaryExpr:
		if node.Op == token.AND {
			s.recordFieldAccess(scope, node.X)
		}
	}
}

func (s *dedupKeySites) recordLiteral(scope string, lit *ast.CompositeLit) {
	if !types.Identical(s.info.TypeOf(lit), s.dedupKey) {
		return
	}
	for _, elt := range lit.Elts {
		kv, keyed := elt.(*ast.KeyValueExpr)
		if !keyed {
			if len(lit.Elts) >= 2 {
				s.sealing[scope] = true
			}
			return
		}
		if key, isIdent := kv.Key.(*ast.Ident); isIdent && s.info.Uses[key] == s.sealedField {
			s.sealing[scope] = true
		}
	}
}

func (s *dedupKeySites) recordFieldAccess(scope string, expr ast.Expr) {
	sel, isSel := ast.Unparen(expr).(*ast.SelectorExpr)
	if !isSel {
		return
	}
	if selection, known := s.info.Selections[sel]; known && selection.Obj() == s.sealedField {
		s.sealing[scope] = true
	}
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

func siteNames(set map[string]bool) []string {
	names := make([]string, 0, len(set))
	for name := range set {
		names = append(names, name)
	}
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
				assert.Equal(t, DedupKey{}, got)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.want, got.String())
			assert.False(t, got.Sealed())
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
	cases := []struct {
		name string
		meta Metadata
	}{
		{"zero", Metadata{}},
		{"plain_delivery", Metadata{delivery: &amqp.Delivery{Headers: amqp.Table{HeaderEventID: "evt-1"}}}},
		{"sealed_looking", Metadata{delivery: &amqp.Delivery{Headers: amqp.Table{"x-sealed": true, "jti": "abc"}}}},
		{"encrypted_ctype", Metadata{delivery: &amqp.Delivery{ContentType: "application/jose"}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env, ok := tc.meta.Sealed()
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
	assert.Equal(t, "svc-sign:jti-1", key.String(), "the sealed key wins over any header the publisher wrote")
	assert.True(t, key.Sealed())
	assert.ErrorIs(t, ValidateEventID(key.String()), ErrInvalidEventID, "a sealed key is outside the header grammar by construction")
}

// TestDedupKeyPersistedSpellingGolden pins String() on both branches to the
// spelling the ledger, DLQ tooling and metrics already hold: changing either is
// a ledger migration, not a refactor.
func TestDedupKeyPersistedSpellingGolden(t *testing.T) {
	sealed := Metadata{delivery: &amqp.Delivery{}, sealed: &SealedEnvelope{SignFamily: "svc-payments-sign", JTI: "9f0c2b1e-3f4a-4c8d-9e1f-0a2b3c4d5e6f"}}
	sealedKey, err := sealed.DedupKey()
	require.NoError(t, err)
	assert.Equal(t, "svc-payments-sign:9f0c2b1e-3f4a-4c8d-9e1f-0a2b3c4d5e6f", sealedKey.String())
	assert.True(t, sealedKey.Sealed())

	stamped := Metadata{delivery: &amqp.Delivery{Headers: amqp.Table{HeaderEventID: "01J9ZQ7K3M"}, MessageId: "prop-9"}}
	stampKey, err := stamped.DedupKey()
	require.NoError(t, err)
	assert.Equal(t, "01J9ZQ7K3M", stampKey.String())
	assert.False(t, stampKey.Sealed())

	unstamped := Metadata{delivery: &amqp.Delivery{MessageId: "prop-9"}}
	propKey, err := unstamped.DedupKey()
	require.NoError(t, err)
	assert.Equal(t, "prop-9", propKey.String())
	assert.False(t, propKey.Sealed())
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
				assert.Equal(t, DedupKey{}, got)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.want, got.String())
			assert.Equal(t, tc.sealed, got.Sealed())
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
