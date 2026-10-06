// Package redisrules holds the one Redis endpoint rule set. It lives here so that
// both config and cache/redis can call it: config must not import cache/redis,
// and cache/redis must not import config (ADR-108). Each caller adapts a
// Violation into its own error type and key namespace.
package redisrules

import (
	"fmt"
	"strings"
	"time"

	"github.com/gaborage/go-bricks/internal/clienttls"
)

// Transport modes. An empty Mode means ModeStandalone.
const (
	ModeStandalone = "standalone"
	ModeCluster    = "cluster"
)

// Endpoint is the transport-facing slice of a Redis configuration.
type Endpoint struct {
	Host, Mode, Username, Password         string
	Port, Database, PoolSize               int
	DialTimeout, ReadTimeout, WriteTimeout time.Duration
	TLSEnabled                             bool
	TLS                                    clienttls.Material
}

// Violation is the first broken rule. Field and any sibling key named in
// Message are relative to the redis block, so each caller prefixes its own
// namespace.
type Violation struct {
	Field   string   // relative to the redis block: "port", "tls.cafile"
	Message string   // reason only, config wording
	Missing bool     // required value absent (host)
	Allowed []string // closed set: {"1-65535"}, {"standalone", "cluster"}
}

// Check returns the first violation in a fixed order — host, port, mode,
// cluster needs database 0, username, database range, poolsize, dial, read,
// write, TLS structure — or nil when the endpoint is valid. An empty Mode is
// standalone. It fills nothing, mutates nothing and reads no files.
func Check(e *Endpoint) *Violation {
	if e.Host == "" {
		return &Violation{Field: "host", Message: "required", Missing: true}
	}
	if e.Port <= 0 || e.Port > 65535 {
		return &Violation{Field: "port", Message: fmt.Sprintf("invalid value: %d", e.Port), Allowed: []string{"1-65535"}}
	}
	if v := checkMode(e); v != nil {
		return v
	}
	if v := checkUsername(e); v != nil {
		return v
	}
	if e.Database < 0 || e.Database > 15 {
		return &Violation{Field: "database", Message: "must be between 0 and 15"}
	}
	if e.PoolSize <= 0 {
		return &Violation{Field: "poolsize", Message: "must be positive"}
	}
	if e.DialTimeout < 0 {
		return &Violation{Field: "dialtimeout", Message: "must be non-negative"}
	}
	if e.ReadTimeout < -1 {
		return &Violation{Field: "readtimeout", Message: "must be >= -1"}
	}
	if e.WriteTimeout < -1 {
		return &Violation{Field: "writetimeout", Message: "must be >= -1"}
	}
	if v := clienttls.ValidateMaterial(&e.TLS, e.TLSEnabled); v != nil {
		return &Violation{Field: "tls." + v.Field, Message: v.Message}
	}
	return nil
}

// checkMode refuses an unknown transport selector rather than defaulting it: the
// default dials a single node, and a cluster endpoint answers MOVED to the first
// key. Under cluster the database must be 0, because go-redis drops the selected
// database on the way to the cluster client; that error is addressed to the
// database, the value that cannot be honored, and runs before its 0-15 range.
func checkMode(e *Endpoint) *Violation {
	if e.Mode != "" && e.Mode != ModeStandalone && e.Mode != ModeCluster {
		return &Violation{
			Field: "mode", Message: fmt.Sprintf("'%s' is not supported", e.Mode),
			Allowed: []string{ModeStandalone, ModeCluster},
		}
	}
	if e.Mode == ModeCluster && e.Database != 0 {
		return &Violation{
			Field:   "database",
			Message: "must be 0 when mode is cluster: the cluster client has no database selection",
		}
	}
	return nil
}

// checkUsername refuses a whitespace-only ACL user (a typo no ACL rule can
// match), checked first so a blank unaccompanied name reads as the typo. A name
// with no password never authenticates: go-redis sends no AUTH without one, so
// the dial would run as the server's unauthenticated identity. A password alone
// is the legacy form that selects the default user and stands.
func checkUsername(e *Endpoint) *Violation {
	if e.Username == "" {
		return nil
	}
	if strings.TrimSpace(e.Username) == "" {
		return &Violation{Field: "username", Message: "must not be whitespace-only"}
	}
	if e.Password == "" {
		return &Violation{Field: "username", Message: "requires password: the client sends no AUTH without one, " +
			"so the connection would silently run as the default user"}
	}
	return nil
}
