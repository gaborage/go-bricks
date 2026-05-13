package migration

import "strings"

// maxClassifyOutputSize caps the prefix of Flyway output that
// classifyFlywayError inspects. Flyway can dump multi-MB Java stack traces
// on failure; ToLower-allocating the full buffer would spike memory on the
// already-degraded failure path. The diagnostic preamble that drives every
// classification rule lives in the first few KB, so a 16 KB cap is more
// than enough headroom.
const maxClassifyOutputSize = 16 * 1024

// classifyFlywayError maps Flyway subprocess output to a stable ErrorClass
// from ADR-019's published taxonomy. Best-effort substring matching against
// the (already password-redacted) combined output of the Flyway subprocess.
// Falls back to ErrorClassInternal for anything unrecognized.
//
// Two ErrorClass values are deliberately NOT produced here:
//   - ErrorClassTargetNotReady — set by the provisioning orchestrator (#379)
//     before Flyway is ever invoked.
//   - ErrorClassQuiesceBlocked — set by the deployment quiesce gate (#380)
//     before Flyway is ever invoked.
//
// Callers (the orchestrator layer) set those explicitly on AuditEvent
// before calling emit; the engine layer never sees them.
//
// The substring matches are intentionally permissive: Flyway error messages
// drift across versions, and a too-tight regex would silently downgrade
// real classifications to internal_error. False positives are preferred
// over false negatives because every published ErrorClass is more useful
// to alerting than the catch-all.
func classifyFlywayError(output string, err error) ErrorClass {
	if err == nil {
		return ""
	}
	if len(output) > maxClassifyOutputSize {
		output = output[:maxClassifyOutputSize]
	}
	lower := strings.ToLower(output)
	switch {
	case strings.Contains(lower, "migration checksum mismatch"),
		strings.Contains(lower, "validate failed") && strings.Contains(lower, "checksum"):
		return ErrorClassChecksumMismatch

	case strings.Contains(lower, "could not acquire change log lock"),
		strings.Contains(lower, "unable to obtain lock"),
		strings.Contains(lower, "lock acquisition timed out"),
		strings.Contains(lower, "lock wait timeout"):
		return ErrorClassLockTimeout

	case strings.Contains(lower, "schema_history") && strings.Contains(lower, "inconsistent"),
		strings.Contains(lower, "schema history") && strings.Contains(lower, "corrupt"),
		strings.Contains(lower, "detected resolved migration not applied"):
		return ErrorClassSchemaHistoryCorrupt

	case strings.Contains(lower, "connection refused"),
		strings.Contains(lower, "could not connect"),
		strings.Contains(lower, "unknown host"),
		strings.Contains(lower, "no route to host"),
		strings.Contains(lower, "connection timed out"),
		strings.Contains(lower, "i/o error: connect"):
		return ErrorClassTargetUnreachable

	default:
		return ErrorClassInternal
	}
}
