package database

import (
	"github.com/gaborage/go-bricks/database/types"
)

// Interface defines the common database operations supported by the framework.
// This type alias maintains backward compatibility while the actual interfaces
// are now defined in the database/types package to avoid import cycles.
type Interface = types.Interface

// Statement defines the interface for prepared statements.
// This type alias maintains backward compatibility.
type Statement = types.Statement

// Tx defines the interface for database transactions.
// This type alias maintains backward compatibility.
type Tx = types.Tx
