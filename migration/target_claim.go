package migration

import (
	"strings"
	"sync"

	"github.com/gaborage/go-bricks/config"
)

// postgresDefaultPort is the port pgjdbc dials when the framework-built URL omits one.
const postgresDefaultPort = 5432

// migrationTarget is what one tenant's Flyway run migrates, as far as the framework can see
// it from the effective (post-overlay) config. Two tenants with equal targets in one run
// migrate the same schema.
type migrationTarget struct {
	vendor   string
	host     string
	port     int
	database string
	// schema is set for a PostgreSQL target with an explicit postgresql.schema; username
	// stands in for it otherwise, because the connecting role's search_path then picks the
	// schema (and Oracle's schema is the connecting user).
	schema   string
	username string
}

// migrationTargetFor returns db's target and true, or false when the target is not keyed:
// a conf-owned PostgreSQL URL (connectionstring, or no host or database), an Oracle block
// with no host or database, or a vendor that resolves to neither. The host is lowercased and
// unbracketed and a PostgreSQL port of 0 is the driver default; DNS is never resolved, and
// database, schema and username compare byte-exact.
func migrationTargetFor(db *config.DatabaseConfig, fallbackVendor string) (migrationTarget, bool) {
	vendor := dbVendor(db, fallbackVendor)
	switch {
	case usesFrameworkOwnedURL(db, vendor):
		target := migrationTarget{vendor: vendor, host: normalizeTargetHost(db.Host), port: db.Port, database: db.Database}
		if target.port == 0 {
			target.port = postgresDefaultPort
		}
		if db.PostgreSQL.Schema != "" {
			target.schema = db.PostgreSQL.Schema
		} else {
			target.username = db.Username
		}
		return target, true
	case db != nil && vendor == config.Oracle && db.Host != "" && db.Database != "":
		return migrationTarget{
			vendor: vendor, host: normalizeTargetHost(db.Host), port: db.Port, database: db.Database, username: db.Username,
		}, true
	}
	return migrationTarget{}, false
}

func normalizeTargetHost(host string) string {
	return strings.ToLower(unbracket(host))
}

// targetClaims is one MigrateAll run's record of which tenant claimed each target first.
type targetClaims struct {
	mu     sync.Mutex
	owners map[migrationTarget]string
}

func newTargetClaims() *targetClaims {
	return &targetClaims{owners: map[migrationTarget]string{}}
}

// claim records tenantID as target's claimant and reports true, or reports false with the
// tenant that claimed target first.
func (c *targetClaims) claim(target *migrationTarget, tenantID string) (string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if owner, taken := c.owners[*target]; taken {
		return owner, false
	}
	c.owners[*target] = tenantID
	return "", true
}
