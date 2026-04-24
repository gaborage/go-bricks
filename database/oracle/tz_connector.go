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
	drv      driver.Driver
	timezone string // pre-validated IANA name from config layer
}

// newTzConnector returns a wrapper that applies the given timezone to every
// new physical connection produced by inner.
func newTzConnector(inner driver.Connector, timezone string) *tzConnector {
	return &tzConnector{
		inner:    inner,
		drv:      inner.Driver(),
		timezone: timezone,
	}
}

// Connect opens a new physical connection via the inner connector and runs
// ALTER SESSION SET TIME_ZONE on it. If the ALTER fails the connection is
// closed and the error bubbles up, causing database/sql to retry rather than
// hand out a misconfigured connection.
func (c *tzConnector) Connect(ctx context.Context) (driver.Conn, error) {
	conn, err := c.inner.Connect(ctx)
	if err != nil {
		return nil, err
	}

	// Defensive escape — config validation already rejects values containing
	// quotes via time.LoadLocation, but a belt-and-braces escape costs nothing
	// and protects against future validation regressions.
	safeTZ := strings.ReplaceAll(c.timezone, "'", "''")
	stmt := fmt.Sprintf("ALTER SESSION SET TIME_ZONE = '%s'", safeTZ)

	execer, ok := conn.(driver.ExecerContext)
	if !ok {
		_ = conn.Close()
		return nil, fmt.Errorf("oracle driver.Conn does not implement driver.ExecerContext; cannot apply session timezone")
	}
	if _, err := execer.ExecContext(ctx, stmt, nil); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("apply oracle session timezone %q: %w", c.timezone, err)
	}
	return conn, nil
}

// Driver returns the wrapped connector's driver, satisfying driver.Connector.
func (c *tzConnector) Driver() driver.Driver { return c.drv }
