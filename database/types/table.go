//revive:disable-next-line:var-naming // Package name "types" avoids circular imports.
package types

import "fmt"

// TableRef represents a table reference with optional alias for SQL queries.
// Use the Table() function to create instances.
//
// Example:
//
//	table, _ := Table("customers")
//	aliased, _ := table.As("c")
type TableRef struct {
	name  string
	alias string
}

// Table creates a new table reference.
// Returns ErrEmptyTableName if name is empty.
// Call As() to add an alias, or use directly for tables without aliases.
//
// Example:
//
//	table, err := Table("users")
//	if err != nil { return err }
//	aliased, err := table.As("u")
func Table(name string) (*TableRef, error) {
	if name == "" {
		return nil, ErrEmptyTableName
	}
	return &TableRef{name: name}, nil
}

// MustTable is like Table but panics on error.
// Use this only in static initialization or tests where errors indicate programming bugs.
func MustTable(name string) *TableRef {
	t, err := Table(name)
	if err != nil {
		panic(fmt.Sprintf("MustTable: %v", err))
	}
	return t
}

// As creates a new TableRef with the given alias.
// Returns ErrEmptyTableAlias if alias is empty.
// Returns a new TableRef instance (immutable pattern).
//
// Example:
//
//	table, _ := Table("customers")
//	aliased, err := table.As("c")
func (t *TableRef) As(alias string) (*TableRef, error) {
	if alias == "" {
		return nil, ErrEmptyTableAlias
	}
	return &TableRef{
		name:  t.name,
		alias: alias,
	}, nil
}

// MustAs is like As but panics on error.
// Use this only in static initialization or tests where errors indicate programming bugs.
func (t *TableRef) MustAs(alias string) *TableRef {
	ref, err := t.As(alias)
	if err != nil {
		panic(fmt.Sprintf("MustAs: %v", err))
	}
	return ref
}

// Name returns the table name (unquoted).
func (t *TableRef) Name() string {
	return t.name
}

// Alias returns the table alias, or empty string if no alias.
func (t *TableRef) Alias() string {
	return t.alias
}

// HasAlias returns true if this table has an alias.
func (t *TableRef) HasAlias() bool {
	return t.alias != ""
}
