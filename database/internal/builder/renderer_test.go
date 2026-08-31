package builder

import (
	"testing"

	"github.com/stretchr/testify/assert"

	dbtypes "github.com/gaborage/go-bricks/database/types"
)

func TestRendererForPicksTheVendorAdapter(t *testing.T) {
	assert.IsType(t, oracleRenderer{}, rendererFor(dbtypes.Oracle))
	assert.IsType(t, postgresRenderer{}, rendererFor(dbtypes.PostgreSQL))
	// An unrecognized vendor renders a validated identifier verbatim — the
	// behavior the deleted `default:` arms had, which postgresRenderer carries.
	assert.IsType(t, postgresRenderer{}, rendererFor("mystery-db"))
}

func TestNewQueryBuilderHoldsTheVendorRenderer(t *testing.T) {
	assert.IsType(t, oracleRenderer{}, NewQueryBuilder(dbtypes.Oracle).renderer)
	assert.IsType(t, postgresRenderer{}, NewQueryBuilder(dbtypes.PostgreSQL).renderer)
}
