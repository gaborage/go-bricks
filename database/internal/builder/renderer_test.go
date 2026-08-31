package builder

import (
	"testing"

	"github.com/stretchr/testify/assert"

	dbtypes "github.com/gaborage/go-bricks/database/types"
)

func TestRendererForPicksTheVendorAdapter(t *testing.T) {
	assert.IsType(t, oracleRenderer{}, rendererFor(dbtypes.Oracle))
	assert.IsType(t, postgresRenderer{}, rendererFor(dbtypes.PostgreSQL))
	// An unrecognized vendor gets its own adapter: it renders identifiers as
	// PostgreSQL does, but four expressions differently — the behavior the
	// deleted `default:` arms had, which defaultRenderer now carries.
	assert.IsType(t, defaultRenderer{}, rendererFor("mystery-db"))
}

func TestDefaultRendererCarriesTheUnknownVendorExpressions(t *testing.T) {
	r := rendererFor("mystery-db")

	// Divergent from PostgreSQL: the generic function, not gen_random_uuid().
	assert.Equal(t, "UUID()", r.UUIDGeneration())
	// Inherited from postgresRenderer: a validated identifier renders verbatim.
	assert.Equal(t, "name", r.QuoteColumn("name"))
	// The vendor is carried so an unsupported expression can name it.
	_, _, err := r.Regex("name", "^a", false, false).ToSql()
	assert.EqualError(t, err, `regex matching is not supported for vendor "mystery-db"`)
}

func TestNewQueryBuilderHoldsTheVendorRenderer(t *testing.T) {
	assert.IsType(t, oracleRenderer{}, NewQueryBuilder(dbtypes.Oracle).renderer)
	assert.IsType(t, postgresRenderer{}, NewQueryBuilder(dbtypes.PostgreSQL).renderer)
	assert.IsType(t, defaultRenderer{}, NewQueryBuilder("mystery-db").renderer)
}
