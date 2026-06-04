package database

import (
	"database/sql"
	"errors"

	"github.com/jackc/pgx/v5/pgconn"
	oranet "github.com/sijms/go-ora/v2/network"
)

// SQLSTATE (PostgreSQL) and ORA (Oracle) codes for constraint classification.
const (
	pgUniqueViolation      = "23505"
	pgForeignKeyViolation  = "23503"
	oraUniqueViolation     = 1    // ORA-00001
	oraForeignKeyViolation = 2291 // ORA-02291
)

// IsUniqueViolation reports whether err is a unique or primary-key constraint
// violation (PostgreSQL SQLSTATE 23505, Oracle ORA-00001). It traverses the
// framework's error wrap chain via errors.As, so callers must wrap driver
// errors with %w (not %v) for it to work.
func IsUniqueViolation(err error) bool {
	if err == nil {
		return false
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code == pgUniqueViolation
	}
	var oraErr *oranet.OracleError
	if errors.As(err, &oraErr) {
		return oraErr.ErrCode == oraUniqueViolation
	}
	return false
}

// IsForeignKeyViolation reports whether err is a foreign-key constraint
// violation (PostgreSQL SQLSTATE 23503, Oracle ORA-02291).
func IsForeignKeyViolation(err error) bool {
	if err == nil {
		return false
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code == pgForeignKeyViolation
	}
	var oraErr *oranet.OracleError
	if errors.As(err, &oraErr) {
		return oraErr.ErrCode == oraForeignKeyViolation
	}
	return false
}

// IsNotFound reports whether err is a no-rows result (sql.ErrNoRows). This is a
// scan-path signal; Exec does not produce it.
func IsNotFound(err error) bool {
	return err != nil && errors.Is(err, sql.ErrNoRows)
}

// ConstraintName returns the violated constraint name when the driver exposes
// it. PostgreSQL populates this for constraint violations; Oracle does not, so
// ConstraintName returns ("", false) for Oracle errors.
func ConstraintName(err error) (string, bool) {
	if err == nil {
		return "", false
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.ConstraintName != "" {
		return pgErr.ConstraintName, true
	}
	return "", false
}
