package identifier_test

import (
	"go/parser"
	"go/token"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gaborage/go-bricks/database/identifier"
	dbtypes "github.com/gaborage/go-bricks/database/types"
)

func TestValidatePostgreSQLAccepts(t *testing.T) {
	for _, v := range []string{
		"tnz_a_b",
		"svc_migrator",
		"MixedCase",
		"_leading",
		"a$b",
		strings.Repeat("a", 63),
	} {
		assert.NoError(t, identifier.Validate(dbtypes.PostgreSQL, v), v)
	}
}

func TestValidatePostgreSQLRejects(t *testing.T) {
	tests := []struct {
		name  string
		value string
		want  error
	}{
		{name: "empty", value: "", want: identifier.ErrEmptyIdentifier},
		{name: "sixty_four_bytes", value: strings.Repeat("a", 64), want: identifier.ErrIdentifierTooLong},
		{name: "hash", value: "a#b", want: identifier.ErrIdentifierCharset},
		{name: "leading_digit", value: "1abc", want: identifier.ErrIdentifierCharset},
		{name: "leading_space", value: " abc", want: identifier.ErrIdentifierCharset},
		{name: "trailing_space", value: "abc ", want: identifier.ErrIdentifierCharset},
		{name: "dot", value: "schema.table", want: identifier.ErrIdentifierCharset},
		{name: "non_ascii", value: "naïve", want: identifier.ErrIdentifierCharset},
		{name: "semicolon", value: "a;drop", want: identifier.ErrIdentifierCharset},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := identifier.Validate(dbtypes.PostgreSQL, tt.value)
			require.ErrorIs(t, err, tt.want)
			if tt.value != "" {
				assert.Contains(t, err.Error(), tt.value, "wraps the offending value")
			}
		})
	}
}

func TestValidateOracle(t *testing.T) {
	assert.NoError(t, identifier.Validate(dbtypes.Oracle, "a#b"))
	assert.NoError(t, identifier.Validate(dbtypes.Oracle, "a$b"))
	assert.NoError(t, identifier.Validate(dbtypes.Oracle, strings.Repeat("o", 128)))
	assert.ErrorIs(t, identifier.Validate(dbtypes.Oracle, strings.Repeat("o", 129)), identifier.ErrIdentifierTooLong)
	assert.ErrorIs(t, identifier.Validate(dbtypes.Oracle, "a-b"), identifier.ErrIdentifierCharset)
	assert.ErrorIs(t, identifier.Validate(dbtypes.Oracle, ""), identifier.ErrEmptyIdentifier)
}

func TestValidateLengthIsBytesNotRunes(t *testing.T) {
	// 62 ASCII bytes + one 2-byte rune = 64 bytes, 63 runes. Charset would
	// also reject it; the cap must win so the message names the byte limit.
	v := strings.Repeat("a", 62) + "é"
	assert.ErrorIs(t, identifier.Validate(dbtypes.PostgreSQL, v), identifier.ErrIdentifierTooLong)
}

func TestValidateUnsupportedVendor(t *testing.T) {
	for _, vendor := range []string{"", "mysql", "PostgreSQL"} {
		err := identifier.Validate(vendor, "ok")
		require.ErrorIs(t, err, identifier.ErrUnsupportedVendor, vendor)
		assert.Contains(t, err.Error(), vendor)
	}
}

// Pins the leaf-package contract: direct imports are the standard library
// and database/types only.
func TestPackageImportsOnlyStdlibAndTypes(t *testing.T) {
	entries, err := os.ReadDir(".")
	require.NoError(t, err)
	fset := token.NewFileSet()
	seen := 0
	for _, e := range entries {
		name := e.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, parseErr := parser.ParseFile(fset, name, nil, parser.ImportsOnly)
		require.NoError(t, parseErr)
		for _, imp := range f.Imports {
			seen++
			path := strings.Trim(imp.Path.Value, `"`)
			if path == "github.com/gaborage/go-bricks/database/types" {
				continue
			}
			assert.NotContains(t, strings.SplitN(path, "/", 2)[0], ".", "non-stdlib import %s", path)
		}
	}
	require.NotZero(t, seen)
}
