package database

import (
	"github.com/gaborage/go-bricks/database/types"
)

// These type aliases maintain backward compatibility for internal usage
// while the actual interfaces are now defined in database/types package.

// Statement defines the interface for prepared statements
type Statement = types.Statement

// Tx defines the interface for database transactions
type Tx = types.Tx

// Interface defines the common database operations supported by the framework
type Interface = types.Interface
