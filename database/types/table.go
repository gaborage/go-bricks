//revive:disable-next-line:var-naming // Package name "types" avoids circular imports.
package types

// TableRef represents a table reference with optional alias for SQL queries.
// Use the Table() function to create instances.
//
// Example:
//
//	Table("customers").As("c")  // Table with alias
//	Table("users")              // Table without alias
type TableRef struct {
	name  string
	alias string
}

// Table creates a new table reference.
// Panics if name is empty (fail fast).
// Call As() to add an alias, or use directly for tables without aliases.
//
// Example:
//
//	qb.Select("*").From(Table("users").As("u"))
func Table(name string) *TableRef {
	if name == "" {
		panic("table name cannot be empty")
	}
	return &TableRef{name: name}
}

// As sets the alias for this table reference.
// Panics if alias is empty (fail fast).
// Returns the same TableRef for method chaining.
//
// Example:
//
//	Table("customers").As("c")
func (t *TableRef) As(alias string) *TableRef {
	if alias == "" {
		panic("table alias cannot be empty")
	}
	t.alias = alias
	return t
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
