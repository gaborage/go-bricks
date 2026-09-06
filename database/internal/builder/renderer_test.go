package builder

import (
	"testing"

	"github.com/stretchr/testify/assert"

	dbident "github.com/gaborage/go-bricks/database/identifier"
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

// TestDefaultRendererUsesPostgreSQLSegmentGrammar pins the unknown-vendor
// answer. defaultRenderer embeds postgresRenderer, so it inherits the stricter
// alphabet rather than the union — the same choice its identifier QUOTING
// already makes, and the safe direction: a name this refuses is one the caller
// can always quote.
func TestDefaultRendererUsesPostgreSQLSegmentGrammar(t *testing.T) {
	renderer := rendererFor("mystery")

	assert.Error(t, renderer.ValidateCharset("a#b")) //nolint:testifylint // paired one-line checks over different inputs
	assert.NoError(t, renderer.ValidateCharset("plain_name"))
}

// TestRendererMaxBytesPinsTheVendorCaps pins the byte cap each adapter supplies
// to the door. gremlins does not mutate a const return, so the values are held
// here twice: against the dbident constants the renderers forward, and against
// the literal numbers those constants are meant to be — a silent change to
// either side fails.
func TestRendererMaxBytesPinsTheVendorCaps(t *testing.T) {
	tests := []struct {
		name     string
		renderer vendorRenderer
		constant int
		literal  int
	}{
		{
			name:     "postgres_namedatalen_minus_one",
			renderer: postgresRenderer{},
			constant: dbident.MaxPostgreSQLBytes,
			literal:  63,
		},
		{
			name:     "oracle_12_2_limit",
			renderer: oracleRenderer{},
			constant: dbident.MaxOracleBytes,
			literal:  128,
		},
		{
			// defaultRenderer adds no MaxBytes of its own: the unknown-vendor
			// class inherits PostgreSQL's cap through the embedded adapter,
			// the same direction its charset and quoting already take.
			name:     "unknown_vendor_inherits_postgres",
			renderer: defaultRenderer{vendor: "nosuchvendor"},
			constant: dbident.MaxPostgreSQLBytes,
			literal:  63,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.constant, tt.renderer.MaxBytes())
			assert.Equal(t, tt.literal, tt.renderer.MaxBytes())
		})
	}
}
