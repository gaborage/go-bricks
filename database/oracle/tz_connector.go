package oracle

import (
	"context"
	"database/sql/driver"
	"fmt"
	"strings"
)

// tzConnector wraps a driver.Connector and runs ALTER SESSION SET TIME_ZONE
// on every new physical Oracle connection. Wrapping at the connector level
// (rather than executing once after sql.Open) is what guarantees pool-wide
// consistency: connections created later for pool growth or after drops
// receive the same session timezone as the first connection.
type tzConnector struct {
	inner    driver.Connector
	timezone string // pre-validated IANA name; kept for error messages
	stmt     string // precomputed ALTER SESSION SQL — never changes after construction
}

// newTzConnector returns a wrapper that applies the given timezone to every
// new physical connection produced by inner. The ALTER SESSION statement is
// built once here so Connect() (called per physical connection) doesn't
// repeat the formatting and quote-escaping work.
func newTzConnector(inner driver.Connector, timezone string) *tzConnector {
	// Defensive escape — config validation rejects values containing quotes,
	// but doubling them costs nothing and protects against future regressions
	// in the validation layer.
	safeTZ := strings.ReplaceAll(timezone, "'", "''")
	return &tzConnector{
		inner:    inner,
		timezone: timezone,
		stmt:     fmt.Sprintf("ALTER SESSION SET TIME_ZONE = '%s'", safeTZ),
	}
}

// Connect opens a new physical connection via the inner connector and runs
// the precomputed ALTER SESSION statement on it. If the ALTER fails the
// connection is closed and the error bubbles up, causing database/sql to
// retry rather than hand out a misconfigured connection.
func (c *tzConnector) Connect(ctx context.Context) (driver.Conn, error) {
	conn, err := c.inner.Connect(ctx)
	if err != nil {
		return nil, err
	}

	execer, ok := conn.(driver.ExecerContext)
	if !ok {
		_ = conn.Close()
		return nil, fmt.Errorf("oracle driver.Conn does not implement driver.ExecerContext; cannot apply session timezone")
	}
	if _, err := execer.ExecContext(ctx, c.stmt, nil); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("apply oracle session timezone %q: %w", c.timezone, err)
	}
	return conn, nil
}

// Driver returns the wrapped connector's driver, satisfying driver.Connector.
func (c *tzConnector) Driver() driver.Driver { return c.inner.Driver() }
