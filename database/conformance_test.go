package database_test

import (
	dbiface "github.com/gaborage/go-bricks/database"
	"github.com/gaborage/go-bricks/database/oracle"
	"github.com/gaborage/go-bricks/database/postgresql"
)

// Compile-time interface conformance checks. These are not runtime tests,
// but they ensure the concrete connection types continue to satisfy the
// public database.Interface contract.
var (
	_ dbiface.Interface = (*postgresql.Connection)(nil)
	_ dbiface.Interface = (*oracle.Connection)(nil)
)
