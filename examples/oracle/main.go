package main

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"time"

	sq "github.com/Masterminds/squirrel"
	brdb "github.com/gaborage/go-bricks/database"
	_ "github.com/sijms/go-ora/v2" // Oracle driver
)

// This example shows how to safely insert into an Oracle table that has a
// reserved identifier column (e.g., NUMBER). The query builder will quote the
// column automatically and use Oracle-style :n placeholders.
func main() {
	qb := brdb.NewQueryBuilder(brdb.Oracle)

	// Build INSERT with a reserved column name "number" in the list
	insert := qb.
		InsertWithColumns(
			"accounts",
			"id", "name", "number", "balance", "created_at", "created_by", "updated_at", "updated_by",
		).
		Values(
			1,
			"John Doe",
			"12345",
			100.50,
			sq.Expr(qb.BuildCurrentTimestamp()), // SYSDATE on Oracle
			"example",
			sq.Expr(qb.BuildCurrentTimestamp()),
			"example",
		)

	sqlStr, args, err := insert.ToSql()
	if err != nil {
		panic(err)
	}

	fmt.Println("SQL:", sqlStr)
	fmt.Println("Args:", args)

	// Optional: execute against a real Oracle DB if ORACLE_DSN is provided.
	// Example DSN: oracle://user:pass@localhost:1521/ORCL
	if dsn := os.Getenv("ORACLE_DSN"); dsn != "" {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		db, err := sql.Open("oracle", dsn)
		if err != nil {
			fmt.Println("open error:", err)
			return
		}
		defer db.Close()

		if _, err := db.ExecContext(ctx, sqlStr, args...); err != nil {
			fmt.Println("exec error:", err)
			return
		}
		fmt.Println("insert ok")
	}
}
