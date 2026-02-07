package database

import "github.com/gaborage/go-bricks/database/types"

// Re-export database vendor identifiers so existing callers using the database
// package continue to compile while the single source of truth lives in types.
const (
	PostgreSQL = types.PostgreSQL
	Oracle     = types.Oracle
)
