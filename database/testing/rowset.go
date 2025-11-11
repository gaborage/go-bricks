package testing

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"fmt"
	"io"
	"reflect"
	"runtime"
	"time"
)

// RowSet represents a collection of rows for testing Query results.
// It provides a fluent API for building test data that can be returned from TestDB.Query().
//
// RowSet is vendor-agnostic - it works the same way for PostgreSQL, Oracle, and MongoDB tests.
//
// Usage example:
//
//	rows := NewRowSet("id", "name", "email").
//	    AddRow(1, "Alice", "alice@example.com").
//	    AddRow(2, "Bob", "bob@example.com")
//
//	db.ExpectQuery("SELECT").WillReturnRows(rows)
type RowSet struct {
	columns []string
	rows    [][]any
}

// NewRowSet creates a new RowSet with the specified column names.
// Column names are informational only (for debugging) and don't affect query execution.
//
// Example:
//
//	rs := NewRowSet("id", "name")  // Two columns
func NewRowSet(columns ...string) *RowSet {
	return &RowSet{
		columns: columns,
		rows:    make([][]any, 0),
	}
}

// AddRow adds a single row of values to the RowSet.
// The number of values must match the number of columns specified in NewRowSet().
// Returns the RowSet for method chaining.
//
// Example:
//
//	rs := NewRowSet("id", "name").
//	    AddRow(1, "Alice").
//	    AddRow(2, "Bob")
//
// Panics if the number of values doesn't match the number of columns.
func (rs *RowSet) AddRow(values ...any) *RowSet {
	if len(values) != len(rs.columns) {
		panic(fmt.Sprintf("AddRow: expected %d values for columns %v, got %d",
			len(rs.columns), rs.columns, len(values)))
	}
	rs.rows = append(rs.rows, values)
	return rs
}

// AddRows adds multiple rows using a generator function.
// The generator is called count times with index i (0-based).
// Returns the RowSet for method chaining.
//
// Example (generate 100 test users):
//
//	rs := NewRowSet("id", "name").
//	    AddRows(100, func(i int) []any {
//	        return []any{int64(i+1), fmt.Sprintf("User%d", i+1)}
//	    })
func (rs *RowSet) AddRows(count int, generator func(i int) []any) *RowSet {
	for i := 0; i < count; i++ {
		values := generator(i)
		rs.AddRow(values...)
	}
	return rs
}

// AddRowsFromStructs extracts values from struct instances and adds them as rows.
// Structs must have `db:"column_name"` tags matching the RowSet columns.
// Returns the RowSet for method chaining.
//
// Example:
//
//	type User struct {
//	    ID   int64  `db:"id"`
//	    Name string `db:"name"`
//	}
//
//	rs := NewRowSet("id", "name").
//	    AddRowsFromStructs(
//	        &User{ID: 1, Name: "Alice"},
//	        &User{ID: 2, Name: "Bob"},
//	    )
//
// This is useful when you have existing struct instances and want to use them as test data.
func (rs *RowSet) AddRowsFromStructs(structs ...any) *RowSet {
	for _, s := range structs {
		values := extractStructValues(s, rs.columns)
		rs.AddRow(values...)
	}
	return rs
}

// RowCount returns the number of rows in the RowSet.
func (rs *RowSet) RowCount() int {
	return len(rs.rows)
}

// Columns returns the column names for this RowSet.
func (rs *RowSet) Columns() []string {
	return append([]string{}, rs.columns...)
}

// normalizeRow converts all values in a row to driver-compatible types.
// Used internally by QueryRow to ensure pointer values are properly dereferenced.
func (rs *RowSet) normalizeRow(rowIdx int) ([]any, error) {
	if rowIdx >= len(rs.rows) {
		return nil, fmt.Errorf("row index %d out of bounds", rowIdx)
	}

	row := rs.rows[rowIdx]
	normalized := make([]any, len(row))
	for i, val := range row {
		normVal, err := normalizeDriverValue(val)
		if err != nil {
			return nil, fmt.Errorf("column %d (%s): %w", i, rs.columns[i], err)
		}
		normalized[i] = normVal
	}
	return normalized, nil
}

// toSQLRows converts the RowSet to a *sql.Rows for use with Query().
// This is an internal method used by TestDB.
func (rs *RowSet) toSQLRows() (*sql.Rows, error) {
	connector := newRowSetConnector(rs)
	db := sql.OpenDB(connector)
	rows, err := db.QueryContext(context.Background(), rowSetQueryLabel)
	if err != nil {
		_ = db.Close()
		return nil, err
	}

	// Ensure we close the temporary *sql.DB when these rows are GC'd or closed.
	runtime.SetFinalizer(rows, func(r *sql.Rows) {
		_ = r.Close()
		_ = db.Close()
	})

	return rows, nil
}

const rowSetQueryLabel = "rowset-query"

// rowSetConnector satisfies driver.Connector so we can feed the RowSet into sql.OpenDB
// and obtain a *sql.Rows backed by our in-memory data.
type rowSetConnector struct {
	columns []string
	rows    [][]any
}

func newRowSetConnector(rs *RowSet) *rowSetConnector {
	rowsCopy := make([][]any, len(rs.rows))
	for i, row := range rs.rows {
		rowCopy := make([]any, len(row))
		copy(rowCopy, row)
		rowsCopy[i] = rowCopy
	}

	return &rowSetConnector{
		columns: append([]string{}, rs.columns...),
		rows:    rowsCopy,
	}
}

func (c *rowSetConnector) Connect(context.Context) (driver.Conn, error) {
	return &rowSetConn{
		columns: c.columns,
		rows:    c.rows,
	}, nil
}

func (c *rowSetConnector) Driver() driver.Driver {
	return rowSetDriver{}
}

// rowSetDriver exists to satisfy driver.Connector.Driver(). It should never
// be used via sql.Open directly.
type rowSetDriver struct{}

func (rowSetDriver) Open(string) (driver.Conn, error) {
	return nil, fmt.Errorf("rowSetDriver must be used via connector")
}

type rowSetConn struct {
	columns []string
	rows    [][]any
}

func (c *rowSetConn) Prepare(string) (driver.Stmt, error) {
	return nil, fmt.Errorf("Prepare not supported for rowSetConn")
}

func (c *rowSetConn) Close() error { return nil }

func (c *rowSetConn) Begin() (driver.Tx, error) {
	return nil, fmt.Errorf("transactions not supported for rowSetConn")
}

func (c *rowSetConn) QueryContext(context.Context, string, []driver.NamedValue) (driver.Rows, error) {
	return &rowSetRows{
		columns: append([]string{}, c.columns...),
		rows:    cloneRowValues(c.rows),
	}, nil
}

func (c *rowSetConn) Query(query string, _ []driver.Value) (driver.Rows, error) {
	return c.QueryContext(context.Background(), query, nil)
}

type rowSetRows struct {
	columns []string
	rows    [][]any
	idx     int
}

func (r *rowSetRows) Columns() []string {
	return append([]string{}, r.columns...)
}

func (r *rowSetRows) Close() error {
	r.rows = nil
	return nil
}

func (r *rowSetRows) Next(dest []driver.Value) error {
	if r.idx >= len(r.rows) {
		return io.EOF
	}

	row := r.rows[r.idx]
	if len(row) != len(r.columns) {
		return fmt.Errorf("row %d has %d values, expected %d", r.idx, len(row), len(r.columns))
	}
	for i, val := range row {
		normalized, err := normalizeDriverValue(val)
		if err != nil {
			return err
		}
		dest[i] = normalized
	}

	r.idx++
	return nil
}

func cloneRowValues(rows [][]any) [][]any {
	if len(rows) == 0 {
		return nil
	}
	copyRows := make([][]any, len(rows))
	for i, row := range rows {
		rowCopy := make([]any, len(row))
		copy(rowCopy, row)
		copyRows[i] = rowCopy
	}
	return copyRows
}

// normalizeDriverValue converts Go types to driver.Value for database compatibility.
//
//nolint:gocyclo // Type normalization requires exhaustive case coverage for SQL compatibility
func normalizeDriverValue(v any) (driver.Value, error) {
	switch val := v.(type) {
	case nil:
		return nil, nil
	case int64:
		return val, nil
	case int:
		return int64(val), nil
	case int32:
		return int64(val), nil
	case int16:
		return int64(val), nil
	case int8:
		return int64(val), nil
	case uint:
		if uint64(val) > ^uint64(0)>>1 {
			return nil, fmt.Errorf("uint value %d overflows int64", val)
		}
		//nolint:gosec // G115: Overflow checked above - safe conversion after validation
		return int64(val), nil
	case uint64:
		if val > ^uint64(0)>>1 {
			return nil, fmt.Errorf("uint64 value %d overflows int64", val)
		}
		return int64(val), nil
	case uint32:
		return int64(val), nil
	case uint16:
		return int64(val), nil
	case uint8:
		return int64(val), nil
	case float64:
		return val, nil
	case float32:
		return float64(val), nil
	case bool:
		return val, nil
	case string:
		return val, nil
	case []byte:
		return append([]byte(nil), val...), nil
	case time.Time:
		return val, nil
	default:
		// Handle pointer types by dereferencing
		rv := reflect.ValueOf(v)
		if rv.Kind() == reflect.Ptr {
			if rv.IsNil() {
				return nil, nil
			}
			// Dereference and recursively normalize
			return normalizeDriverValue(rv.Elem().Interface())
		}
		// Handle fmt.Stringer after pointer check to avoid panics on nil pointers
		if stringer, ok := v.(fmt.Stringer); ok {
			return stringer.String(), nil
		}
		return nil, fmt.Errorf("unsupported RowSet value type %T", v)
	}
}

// extractStructValues extracts field values from a struct based on db tags.
// Returns values in the same order as the columns parameter.
func extractStructValues(structPtr any, columns []string) []any {
	v := reflect.ValueOf(structPtr)
	if v.Kind() == reflect.Ptr {
		v = v.Elem()
	}
	if v.Kind() != reflect.Struct {
		panic(fmt.Sprintf("extractStructValues: expected struct or pointer to struct, got %T", structPtr))
	}

	t := v.Type()
	tagToField := make(map[string]reflect.Value)

	// Build map of db tag -> field value
	for i := 0; i < t.NumField(); i++ {
		field := t.Field(i)
		tag := field.Tag.Get("db")
		if tag != "" && tag != "-" {
			tagToField[tag] = v.Field(i)
		}
	}

	// Extract values in column order
	values := make([]any, len(columns))
	for i, col := range columns {
		field, ok := tagToField[col]
		if !ok {
			panic(fmt.Sprintf("extractStructValues: column %q not found in struct %T (check db tags)", col, structPtr))
		}
		values[i] = field.Interface()
	}

	return values
}
