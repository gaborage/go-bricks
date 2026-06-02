package migration

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/gaborage/go-bricks/logger"
)

// Deployment quiesce gate (issue #380).
//
// A quiesce flag lets a deployment-time migration job pause provisioning
// workers (and tenant fan-out) while it mutates shared roles / runs the
// deployment Flyway pass, then release them. The flag gates the *pickup* of
// not-yet-started work: a provisioning job already executing a step drains to
// its next durable checkpoint and parks; deployment dispatch of a not-yet-
// started tenant is held. No in-flight step is interrupted, so no partial
// provision is orphaned. PostgreSQL only in v1 (Oracle parity is #385).

const (
	// DefaultQuiesceScope is the single scope key used in v1 (single-region).
	// The scope column exists so per-region/per-pool scoping is additive.
	DefaultQuiesceScope = "global"
	// DefaultQuiesceTTL is applied when QuiesceSetOptions.TTL is zero. The TTL
	// is the auto-release horizon: a migration job that crashes after Set can
	// never block provisioning beyond this without an explicit renew.
	DefaultQuiesceTTL = 30 * time.Minute
	// MaxQuiesceTTL caps QuiesceSetOptions.TTL so a fat-fingered multi-day TTL
	// cannot brick provisioning. Values above the ceiling are clamped.
	MaxQuiesceTTL = 2 * time.Hour
	// DefaultQuiesceTable is the control-plane table name used when an empty
	// table name is supplied to NewPostgresQuiesceController.
	DefaultQuiesceTable = "quiesce_flags"
)

// ErrQuiesceNotSet is returned by Clear when no active flag exists.
var ErrQuiesceNotSet = errors.New("migration: no active quiesce flag")

// ErrInvalidQuiesceTable is returned by NewPostgresQuiesceController when the
// supplied table name fails the safe-identifier check.
var ErrInvalidQuiesceTable = errors.New("migration: invalid quiesce table name")

// ErrInvalidQuiesceTTL is returned by Set when QuiesceSetOptions.TTL is
// negative. Zero is valid (means DefaultQuiesceTTL); negative is rejected
// rather than silently defaulted, so a bad operator input fails loudly instead
// of pausing provisioning for an unintended duration.
var ErrInvalidQuiesceTTL = errors.New("migration: quiesce TTL must not be negative")

// validateAndQuoteQuiesceTable validates an optionally schema-qualified table
// name ("schema.table" or "table") — matching the per-table behavior of the
// provisioning store — by checking each segment against the conservative
// safePGIdentifier subset and returning the double-quoted form. Returns
// ErrInvalidQuiesceTable on any violation.
func validateAndQuoteQuiesceTable(name string) (string, error) {
	parts := strings.Split(name, ".")
	if len(parts) > 2 {
		return "", fmt.Errorf("%w: %q", ErrInvalidQuiesceTable, name)
	}
	quoted := make([]string, len(parts))
	for i, p := range parts {
		if !safePGIdentifier.MatchString(p) {
			return "", fmt.Errorf("%w: %q", ErrInvalidQuiesceTable, name)
		}
		quoted[i] = quotePGIdent(p)
	}
	return strings.Join(quoted, "."), nil
}

// ErrQuiesceBlocked is returned by MigrateAll when the deployment quiesce flag
// is set: dispatch of not-yet-started tenants stops and the partial result is
// returned. Distinguish a paused run from a failed one via errors.Is.
var ErrQuiesceBlocked = errors.New("migration: paused by deployment quiesce flag")

// quiesceBlocks reports whether gate currently quiesces work. Fail-open: a check
// error logs a WARN (when log is non-nil) and returns false, so a transient
// control-plane hiccup cannot strand a fan-out (#380 decision). A nil gate
// never blocks.
func quiesceBlocks(ctx context.Context, gate QuiesceGate, log logger.Logger) bool {
	if gate == nil {
		return false
	}
	set, err := gate.IsSet(ctx)
	if err != nil {
		// Intentionally omit the raw error: a control-plane gate-check error can
		// carry connection details (host/user), and audit/log payloads bypass
		// the field-name SensitiveDataFilter. Operators inspect the control
		// plane directly; here we only signal that the check failed.
		if log != nil {
			log.Warn().Msg("quiesce check failed; proceeding (fail-open)")
		}
		return false
	}
	return set
}

// QuiesceStatus is the operator-facing snapshot of the quiesce flag returned
// by Query. Active reports whether provisioning is currently paused; Expired
// reports that an uncleared flag has passed its TTL and was auto-released
// (read-side, no sweeper) — a distinct signal from a deliberately-cleared flag.
//
// Active and Expired are mutually-exclusive conveniences derived from
// ClearedAt + ExpiresAt + the current time; the controller keeps them
// consistent. Treat the struct as read-only.
type QuiesceStatus struct {
	Active    bool       // cleared_at IS NULL AND now < expires_at
	SetAt     time.Time  // when the active/last flag was set
	SetBy     string     // principal that set it (visibility + audit)
	Reason    string     // operator-supplied "why" (deploy id, ticket)
	ExpiresAt time.Time  // TTL hard stop
	ClearedAt *time.Time // nil while uncleared; set when explicitly cleared
	Expired   bool       // cleared_at IS NULL AND now >= expires_at (stale, auto-released)
}

// QuiesceSetOptions parameterizes Set. By is the principal (explicit, never
// inferred — ADR-019); empty surfaces the PrincipalUnspecified sentinel on the
// audit path. TTL of zero defaults to DefaultQuiesceTTL and is clamped to
// MaxQuiesceTTL.
type QuiesceSetOptions struct {
	By     string
	Reason string
	TTL    time.Duration
}

// QuiesceGate is the read side consumed by provisioning workers
// (provisioning.Executor) and the deployment fan-out (MigrateAll). Workers
// depend on this narrow interface (Interface Segregation); a nil gate means
// "never quiesced" so the feature is fully opt-in.
type QuiesceGate interface {
	// IsSet reports whether provisioning is currently quiesced. Returns false
	// for an expired (auto-released) or cleared flag.
	IsSet(ctx context.Context) (bool, error)
	// Query returns the full status for operator visibility.
	Query(ctx context.Context) (*QuiesceStatus, error)
}

// QuiesceController is the write side used by the deployment job and the CLI.
// It composes QuiesceGate so a single implementation serves both reads and
// writes.
type QuiesceController interface {
	QuiesceGate

	// Set activates quiesce for opts.TTL (defaulted + ceiling-clamped).
	// Idempotent: calling Set on an already-active flag renews ExpiresAt
	// (a heartbeat for long deploys). Records By/Reason for visibility + audit.
	Set(ctx context.Context, opts QuiesceSetOptions) (*QuiesceStatus, error)
	// Clear deactivates the active flag (unconditional operator override; not
	// keyed on the setter's session, so it works even if the setter died).
	// Returns ErrQuiesceNotSet when nothing is active.
	Clear(ctx context.Context, by string) (*QuiesceStatus, error)
	// CreateTable provisions backing storage idempotently (no-op for the
	// in-memory controller).
	CreateTable(ctx context.Context) error
}

// resolveTTL validates ttl and applies the zero-default and ceiling clamp.
// Negative is rejected (ErrInvalidQuiesceTTL); zero means DefaultQuiesceTTL;
// values above MaxQuiesceTTL are clamped down.
func resolveTTL(ttl time.Duration) (time.Duration, error) {
	switch {
	case ttl < 0:
		return 0, fmt.Errorf("%w: %s", ErrInvalidQuiesceTTL, ttl)
	case ttl == 0:
		return DefaultQuiesceTTL, nil
	case ttl > MaxQuiesceTTL:
		return MaxQuiesceTTL, nil
	default:
		return ttl, nil
	}
}
