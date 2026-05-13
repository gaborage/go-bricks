package columns

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	dbtypes "github.com/gaborage/go-bricks/database/types"
)

// TestAsPanicsOnEmptyAlias pins the documented fail-fast behaviour: the
// As() helper panics rather than returning an unaliased clone, so a
// caller passing an empty string sees the bug at development time.
func TestAsPanicsOnEmptyAlias(t *testing.T) {
	cm, err := parseStruct(dbtypes.PostgreSQL, &ValidUser{})
	require.NoError(t, err)

	assert.PanicsWithValue(t, "alias cannot be empty", func() {
		cm.As("")
	})
}

func TestAliasReturnsEmptyWhenUnaliased(t *testing.T) {
	cm, err := parseStruct(dbtypes.PostgreSQL, &ValidUser{})
	require.NoError(t, err)
	assert.Empty(t, cm.Alias(), "fresh metadata is unaliased")
}

func TestAliasReturnsSetValueAfterAs(t *testing.T) {
	cm, err := parseStruct(dbtypes.PostgreSQL, &ValidUser{})
	require.NoError(t, err)

	aliased := cm.As("u")
	assert.Equal(t, "u", aliased.Alias())
	assert.Empty(t, cm.Alias(), "original metadata must remain unaliased — As() returns a new instance")
}

func TestAllReturnsQualifiedColumnsWhenAliased(t *testing.T) {
	cm, err := parseStruct(dbtypes.PostgreSQL, &ValidUser{})
	require.NoError(t, err)

	aliased := cm.As("u")
	assert.Equal(t, []string{"u.id", "u.name", "u.email"}, aliased.All())
	// Sanity: original returns unqualified columns.
	assert.Equal(t, []string{"id", "name", "email"}, cm.All())
}

func TestFieldMapUnaliasedReturnsValues(t *testing.T) {
	cm, err := parseStruct(dbtypes.PostgreSQL, &ValidUser{})
	require.NoError(t, err)

	user := ValidUser{ID: 7, Name: "alice", Email: "alice@example.com"}
	got := cm.FieldMap(&user)

	require.Len(t, got, 3)
	assert.Equal(t, int64(7), got["id"])
	assert.Equal(t, "alice", got["name"])
	assert.Equal(t, "alice@example.com", got["email"])
}

func TestFieldMapAliasedQualifiesKeys(t *testing.T) {
	cm, err := parseStruct(dbtypes.PostgreSQL, &ValidUser{})
	require.NoError(t, err)

	user := ValidUser{ID: 42, Name: "bob"}
	got := cm.As("u").FieldMap(&user)

	require.Contains(t, got, "u.id")
	require.Contains(t, got, "u.name")
	require.Contains(t, got, "u.email")
	assert.Equal(t, int64(42), got["u.id"])
	assert.Equal(t, "bob", got["u.name"])
}

func TestFieldMapAcceptsStructByValue(t *testing.T) {
	// extractStructValue handles both struct values and pointers — exercise
	// the value path here so the dereference branch isn't the only one
	// covered. parseStruct itself requires a pointer (existing contract),
	// so we build metadata via the pointer and then call FieldMap with the
	// value.
	cm, err := parseStruct(dbtypes.PostgreSQL, &ValidUser{})
	require.NoError(t, err)

	got := cm.FieldMap(ValidUser{ID: 1, Name: "x", Email: "x@y"})
	assert.Equal(t, int64(1), got["id"])
}

func TestAllFieldsUnaliasedReturnsColsAndValues(t *testing.T) {
	cm, err := parseStruct(dbtypes.PostgreSQL, &ValidUser{})
	require.NoError(t, err)

	user := ValidUser{ID: 9, Name: "carol", Email: "carol@example.com"}
	cols, vals := cm.AllFields(&user)

	assert.Equal(t, []string{"id", "name", "email"}, cols)
	assert.Equal(t, []any{int64(9), "carol", "carol@example.com"}, vals)
}

func TestAllFieldsAliasedQualifiesColumns(t *testing.T) {
	cm, err := parseStruct(dbtypes.PostgreSQL, &ValidUser{})
	require.NoError(t, err)

	user := ValidUser{ID: 11}
	cols, _ := cm.As("u").AllFields(&user)
	assert.Equal(t, []string{"u.id", "u.name", "u.email"}, cols)
}

func TestExtractStructValuePanicsOnNil(t *testing.T) {
	cm, err := parseStruct(dbtypes.PostgreSQL, &ValidUser{})
	require.NoError(t, err)

	// An untyped nil reaches reflect.ValueOf as an invalid Value — that's
	// the path we want covered (distinct from a typed-nil pointer).
	assert.Panics(t, func() {
		cm.FieldMap(nil)
	}, "nil instance must panic with a clear error")
}

func TestExtractStructValuePanicsOnNilPointer(t *testing.T) {
	cm, err := parseStruct(dbtypes.PostgreSQL, &ValidUser{})
	require.NoError(t, err)

	var u *ValidUser // typed nil pointer
	assert.Panics(t, func() {
		cm.FieldMap(u)
	}, "typed-nil pointer must panic distinctly from untyped nil")
}

func TestExtractStructValuePanicsOnNonStruct(t *testing.T) {
	cm, err := parseStruct(dbtypes.PostgreSQL, &ValidUser{})
	require.NoError(t, err)

	assert.Panics(t, func() {
		cm.FieldMap("not a struct")
	}, "non-struct kinds must panic")
}

func TestExtractStructValuePanicsOnTypeMismatch(t *testing.T) {
	cm, err := parseStruct(dbtypes.PostgreSQL, &ValidUser{})
	require.NoError(t, err)

	// A different struct type with the same field shape must still be
	// rejected — metadata is keyed by reflect.Type identity, not by
	// structural match.
	assert.Panics(t, func() {
		cm.FieldMap(&NoDBTags{})
	}, "passing a different struct type must panic with a type-mismatch error")
}
