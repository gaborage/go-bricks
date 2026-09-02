package types

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestInvalidAliasErrorMessage pins the rendered message exactly. The refused
// alias is quoted with %q, so a value carrying SQL renders as one escaped token
// rather than reaching a reader as syntax.
func TestInvalidAliasErrorMessage(t *testing.T) {
	tests := []struct {
		name     string
		alias    string
		expected string
	}{
		{
			name:  "bare_alias",
			alias: "u x",
			expected: `invalid table alias "u x": must be a bare identifier ` +
				`(e.g. "u") — an alias becomes SQL syntax and is validated before interpolation`,
		},
		{
			name:  "empty_alias",
			alias: "",
			expected: `invalid table alias "": must be a bare identifier ` +
				`(e.g. "u") — an alias becomes SQL syntax and is validated before interpolation`,
		},
		{
			name:  "alias_carrying_sql",
			alias: `id FROM secrets--`,
			expected: `invalid table alias "id FROM secrets--": must be a bare identifier ` +
				`(e.g. "u") — an alias becomes SQL syntax and is validated before interpolation`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := &InvalidAliasError{Alias: tt.alias}
			assert.Equal(t, tt.expected, err.Error())
		})
	}
}

// TestInvalidAliasErrorMatchesWithErrorsAs pins the documented recovery route:
// a site that recovers the panic reaches the refused alias through errors.As,
// deliberately, rather than by rendering the value.
func TestInvalidAliasErrorMatchesWithErrorsAs(t *testing.T) {
	var err error = &InvalidAliasError{Alias: "u;"}

	var invalid *InvalidAliasError
	require.True(t, errors.As(err, &invalid))
	assert.Equal(t, "u;", invalid.Alias)
}
