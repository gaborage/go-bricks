package database

import (
	"database/sql"
	"errors"
	"fmt"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	oranet "github.com/sijms/go-ora/v2/network"
)

func TestIsUniqueViolation(t *testing.T) {
	pgUnique := &pgconn.PgError{Code: "23505", ConstraintName: "uq_email"}
	oraUnique := &oranet.OracleError{ErrCode: 1}
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"pg_unique", pgUnique, true},
		{"pg_unique_wrapped", fmt.Errorf("insert: %w", pgUnique), true},
		{"pg_fk_not_unique", &pgconn.PgError{Code: "23503"}, false},
		{"oracle_unique", oraUnique, true},
		{"oracle_unique_wrapped", fmt.Errorf("insert: %w", oraUnique), true},
		{"oracle_other", &oranet.OracleError{ErrCode: 942}, false},
		{"plain", errors.New("boom"), false},
		{"no_rows", sql.ErrNoRows, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsUniqueViolation(tc.err); got != tc.want {
				t.Fatalf("IsUniqueViolation(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

func TestIsForeignKeyViolation(t *testing.T) {
	if !IsForeignKeyViolation(&pgconn.PgError{Code: "23503"}) {
		t.Fatal("pg 23503 should be FK violation")
	}
	if !IsForeignKeyViolation(&oranet.OracleError{ErrCode: 2291}) {
		t.Fatal("ORA-02291 should be FK violation")
	}
	if IsForeignKeyViolation(&pgconn.PgError{Code: "23505"}) {
		t.Fatal("pg 23505 is not FK violation")
	}
	if IsForeignKeyViolation(nil) {
		t.Fatal("nil is not FK violation")
	}
}

func TestIsNotFound(t *testing.T) {
	if !IsNotFound(sql.ErrNoRows) {
		t.Fatal("sql.ErrNoRows should be not-found")
	}
	if !IsNotFound(fmt.Errorf("scan: %w", sql.ErrNoRows)) {
		t.Fatal("wrapped sql.ErrNoRows should be not-found")
	}
	if IsNotFound(nil) || IsNotFound(errors.New("x")) {
		t.Fatal("nil/plain are not not-found")
	}
}

func TestConstraintName(t *testing.T) {
	name, ok := ConstraintName(&pgconn.PgError{Code: "23505", ConstraintName: "uq_email"})
	if !ok || name != "uq_email" {
		t.Fatalf("ConstraintName = %q,%v want uq_email,true", name, ok)
	}
	if _, ok := ConstraintName(&pgconn.PgError{Code: "23505"}); ok {
		t.Fatal("empty constraint name → ok=false")
	}
	if _, ok := ConstraintName(&oranet.OracleError{ErrCode: 1}); ok {
		t.Fatal("Oracle exposes no constraint name; want ok=false")
	}
	if _, ok := ConstraintName(nil); ok {
		t.Fatal("nil → ok=false")
	}
}
