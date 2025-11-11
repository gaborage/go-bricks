package testing

import (
	"context"
	"testing"

	dbtypes "github.com/gaborage/go-bricks/database/types"
	"github.com/stretchr/testify/assert"
)

// TestNullScanBehavior verifies that TestDB matches database/sql behavior for NULL scans.
// Scalar pointers (*int, *string, etc.) must reject NULL with an error,
// while pointer-to-pointer types (**int, **string) accept NULL by setting to nil.
func TestNullScanBehavior(t *testing.T) {
	t.Run("scalar string pointer rejects NULL", func(t *testing.T) {
		db := NewTestDB(dbtypes.PostgreSQL)
		db.ExpectQuery("SELECT").WillReturnRows(
			NewRowSet("name").AddRow(nil), // NULL value
		)

		var name string
		err := db.QueryRow(context.Background(), "SELECT name FROM users", nil).Scan(&name)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "converting NULL to string is unsupported")
	})

	t.Run("pointer-to-pointer string accepts NULL", func(t *testing.T) {
		db := NewTestDB(dbtypes.PostgreSQL)
		db.ExpectQuery("SELECT").WillReturnRows(
			NewRowSet("name").AddRow(nil), // NULL value
		)

		var name *string
		err := db.QueryRow(context.Background(), "SELECT name FROM users", nil).Scan(&name)
		assert.NoError(t, err)
		assert.Nil(t, name)
	})

	t.Run("scalar int pointer rejects NULL", func(t *testing.T) {
		db := NewTestDB(dbtypes.PostgreSQL)
		db.ExpectQuery("SELECT").WillReturnRows(
			NewRowSet("age").AddRow(nil), // NULL value
		)

		var age int
		err := db.QueryRow(context.Background(), "SELECT age FROM users", nil).Scan(&age)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "converting NULL to int is unsupported")
	})

	t.Run("pointer-to-pointer int accepts NULL", func(t *testing.T) {
		db := NewTestDB(dbtypes.PostgreSQL)
		db.ExpectQuery("SELECT").WillReturnRows(
			NewRowSet("age").AddRow(nil), // NULL value
		)

		var age *int
		err := db.QueryRow(context.Background(), "SELECT age FROM users", nil).Scan(&age)
		assert.NoError(t, err)
		assert.Nil(t, age)
	})

	t.Run("scalar int64 pointer rejects NULL", func(t *testing.T) {
		db := NewTestDB(dbtypes.PostgreSQL)
		db.ExpectQuery("SELECT").WillReturnRows(
			NewRowSet("id").AddRow(nil), // NULL value
		)

		var id int64
		err := db.QueryRow(context.Background(), "SELECT id FROM users", nil).Scan(&id)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "converting NULL to int64 is unsupported")
	})

	t.Run("pointer-to-pointer int64 accepts NULL", func(t *testing.T) {
		db := NewTestDB(dbtypes.PostgreSQL)
		db.ExpectQuery("SELECT").WillReturnRows(
			NewRowSet("id").AddRow(nil), // NULL value
		)

		var id *int64
		err := db.QueryRow(context.Background(), "SELECT id FROM users", nil).Scan(&id)
		assert.NoError(t, err)
		assert.Nil(t, id)
	})

	t.Run("scalar bool pointer rejects NULL", func(t *testing.T) {
		db := NewTestDB(dbtypes.PostgreSQL)
		db.ExpectQuery("SELECT").WillReturnRows(
			NewRowSet("active").AddRow(nil), // NULL value
		)

		var active bool
		err := db.QueryRow(context.Background(), "SELECT active FROM users", nil).Scan(&active)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "converting NULL to bool is unsupported")
	})

	t.Run("pointer-to-pointer bool accepts NULL", func(t *testing.T) {
		db := NewTestDB(dbtypes.PostgreSQL)
		db.ExpectQuery("SELECT").WillReturnRows(
			NewRowSet("active").AddRow(nil), // NULL value
		)

		var active *bool
		err := db.QueryRow(context.Background(), "SELECT active FROM users", nil).Scan(&active)
		assert.NoError(t, err)
		assert.Nil(t, active)
	})

	t.Run("scalar float64 pointer rejects NULL", func(t *testing.T) {
		db := NewTestDB(dbtypes.PostgreSQL)
		db.ExpectQuery("SELECT").WillReturnRows(
			NewRowSet("price").AddRow(nil), // NULL value
		)

		var price float64
		err := db.QueryRow(context.Background(), "SELECT price FROM products", nil).Scan(&price)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "converting NULL to float64 is unsupported")
	})

	t.Run("pointer-to-pointer float64 accepts NULL", func(t *testing.T) {
		db := NewTestDB(dbtypes.PostgreSQL)
		db.ExpectQuery("SELECT").WillReturnRows(
			NewRowSet("price").AddRow(nil), // NULL value
		)

		var price *float64
		err := db.QueryRow(context.Background(), "SELECT price FROM products", nil).Scan(&price)
		assert.NoError(t, err)
		assert.Nil(t, price)
	})

	t.Run("scalar uint pointer rejects NULL", func(t *testing.T) {
		db := NewTestDB(dbtypes.PostgreSQL)
		db.ExpectQuery("SELECT").WillReturnRows(
			NewRowSet("count").AddRow(nil), // NULL value
		)

		var count uint
		err := db.QueryRow(context.Background(), "SELECT count FROM stats", nil).Scan(&count)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "converting NULL to uint is unsupported")
	})

	t.Run("pointer-to-pointer uint accepts NULL", func(t *testing.T) {
		db := NewTestDB(dbtypes.PostgreSQL)
		db.ExpectQuery("SELECT").WillReturnRows(
			NewRowSet("count").AddRow(nil), // NULL value
		)

		var count *uint
		err := db.QueryRow(context.Background(), "SELECT count FROM stats", nil).Scan(&count)
		assert.NoError(t, err)
		assert.Nil(t, count)
	})

	t.Run("byte slice accepts NULL", func(t *testing.T) {
		db := NewTestDB(dbtypes.PostgreSQL)
		db.ExpectQuery("SELECT").WillReturnRows(
			NewRowSet("data").AddRow(nil), // NULL value
		)

		var data []byte
		err := db.QueryRow(context.Background(), "SELECT data FROM blobs", nil).Scan(&data)
		assert.NoError(t, err)
		assert.Nil(t, data)
	})

	t.Run("any interface accepts NULL", func(t *testing.T) {
		db := NewTestDB(dbtypes.PostgreSQL)
		db.ExpectQuery("SELECT").WillReturnRows(
			NewRowSet("value").AddRow(nil), // NULL value
		)

		var value any
		err := db.QueryRow(context.Background(), "SELECT value FROM metadata", nil).Scan(&value)
		assert.NoError(t, err)
		assert.Nil(t, value)
	})
}
