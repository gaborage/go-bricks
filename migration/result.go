package migration

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
)

// Result captures the structured outcome of a single Flyway migrate
// invocation, populated from the engine's -outputType=json output. Fields
// are best-effort: an empty Result is returned when the subprocess crashed
// before emitting JSON or the payload was malformed. Callers that need an
// authoritative pass/fail signal should still consult the error returned
// alongside the Result.
type Result struct {
	// Operation is the Flyway verb. Empty on the error envelope.
	Operation string

	// Success is false whenever Flyway emitted an error envelope, even if
	// the JSON parsed cleanly.
	Success bool

	// AppliedVersions enumerates the migration versions Flyway applied in
	// this run, in the order Flyway reported them. Empty on no-op reruns.
	AppliedVersions []string

	// StartingVersion is the schema version before this run (Flyway's
	// initialSchemaVersion). Empty when Flyway reported it as null —
	// typically on the first migrate against a fresh schema.
	StartingVersion string

	// EndingVersion is the schema version after this run. Flyway reports
	// targetSchemaVersion as null on no-op runs; the parser falls back to
	// StartingVersion in that case so callers always see a usable terminus.
	EndingVersion string

	// DurationMillis is Flyway's totalMigrationTime in milliseconds.
	DurationMillis int64

	// FlywayVersion is the engine version that produced this result.
	FlywayVersion string

	// DatabaseType is Flyway's databaseType field ("PostgreSQL", "Oracle").
	DatabaseType string

	// ErrorCode is Flyway's errorCode on the failure envelope (e.g.
	// "VALIDATE_ERROR" for a checksum mismatch). Empty when Success is true.
	ErrorCode string

	// ErrorMessage is the human-readable error message from Flyway when
	// Success is false. May contain embedded newlines from Flyway.
	ErrorMessage string
}

// flywayJSONEnvelope is the union of Flyway's -outputType=json payload
// shapes. Success-shape fields and Error-shape fields are mutually exclusive
// in practice; pointer-typed fields distinguish "present" from "zero-valued".
type flywayJSONEnvelope struct {
	Operation            string              `json:"operation"`
	InitialSchemaVersion *string             `json:"initialSchemaVersion"`
	TargetSchemaVersion  *string             `json:"targetSchemaVersion"`
	Migrations           []flywayMigration   `json:"migrations"`
	Success              *bool               `json:"success"`
	FlywayVersion        string              `json:"flywayVersion"`
	DatabaseType         string              `json:"databaseType"`
	TotalMigrationTime   int64               `json:"totalMigrationTime"`
	Error                *flywayErrorPayload `json:"error"`
}

type flywayMigration struct {
	Version string `json:"version"`
}

type flywayErrorPayload struct {
	ErrorCode string `json:"errorCode"`
	Message   string `json:"message"`
}

// looksLikeFlyway reports whether env matches one of Flyway's two envelope
// shapes: the success/noop envelope (operation and success always together)
// or the error envelope (nested error.errorCode). Single generic fields are
// not enough — structured-log noise can carry success/operation/error alone.
func (env *flywayJSONEnvelope) looksLikeFlyway() bool {
	if env.Operation != "" && env.Success != nil {
		return true
	}
	return env.Error != nil && env.Error.ErrorCode != ""
}

// errEmptyFlywayOutput is returned when parseFlywayJSON receives an empty
// or whitespace-only payload — typically because the subprocess crashed
// before Flyway could write its JSON envelope.
var errEmptyFlywayOutput = errors.New("migration: empty Flyway JSON output")

// ErrFlywayOutputUnparsed wraps the underlying parse failure (errEmptyFlywayOutput
// or a JSON decode error) so a zero-exit run whose output is empty, malformed, or
// redaction-suppressed surfaces as a non-nil error instead of a silent success.
// Match with errors.Is.
var ErrFlywayOutputUnparsed = errors.New("flyway output could not be parsed")

// ErrFlywayReportedFailure is returned when Flyway emitted a well-formed JSON
// envelope that itself reports failure (Result.Success == false), including when
// the subprocess exited 0. Match with errors.Is.
var ErrFlywayReportedFailure = errors.New("flyway reported a failed migration")

// migrateOutcome collapses the three failure signals of a migrate invocation into
// a single error with fixed precedence: subprocess error > unparsable output >
// envelope-reported failure. A nil return means the Result is authoritative. Pure
// (no receiver, no I/O) so it is unit-testable without a subprocess stub. The
// sentinel messages omit a "migration:" prefix because the wrapped parseErr
// already carries one, keeping the rendered chain free of a doubled prefix.
func migrateOutcome(runErr, parseErr error, result *Result) error {
	switch {
	case runErr != nil:
		return runErr
	case parseErr != nil:
		return fmt.Errorf("%w: %w", ErrFlywayOutputUnparsed, parseErr)
	case !result.Success:
		// Only the Flyway errorCode enum (e.g. VALIDATE_ERROR) is interpolated;
		// result.ErrorMessage is deliberately omitted so no free-text field that
		// could echo connection details enters a propagated/logged error string.
		return fmt.Errorf("%w: code=%q", ErrFlywayReportedFailure, result.ErrorCode)
	default:
		return nil
	}
}

// parseFlywayJSON parses Flyway's -outputType=json output into a Result.
// output is combined stdout+stderr, so a JVM/SLF4J/driver warning line may
// precede the envelope and may itself contain a JSON value (e.g. a structured
// log line — object or array). Every '{' and '[' in output is tried as a
// candidate value start, in order; each value that decodes is consumed whole
// so its nested contents never become candidates, and the first object value
// carrying a recognizable Flyway envelope shape wins.
func parseFlywayJSON(output string) (Result, error) {
	trimmed := strings.TrimSpace(output)
	if trimmed == "" {
		return Result{}, errEmptyFlywayOutput
	}

	var firstDecodeErr error
	skipBelow := 0 // candidates below this offset are inside an already-decoded JSON value
	for _, start := range valueStarts(trimmed) {
		if start < skipBelow {
			continue
		}
		dec := json.NewDecoder(strings.NewReader(trimmed[start:]))
		var raw json.RawMessage
		if err := dec.Decode(&raw); err != nil {
			if firstDecodeErr == nil && !errors.Is(err, io.EOF) {
				firstDecodeErr = fmt.Errorf("migration: parse Flyway JSON: %w", err)
			}
			continue
		}
		// Only after a successful decode — InputOffset is not meaningful after
		// a failure, and braces inside malformed text must stay candidates.
		skipBelow = start + int(dec.InputOffset())
		if trimmed[start] != '{' {
			continue // arrays are consumed to guard their contents, never promoted
		}
		var env flywayJSONEnvelope
		if err := json.Unmarshal(raw, &env); err != nil {
			if firstDecodeErr == nil {
				firstDecodeErr = fmt.Errorf("migration: parse Flyway JSON: %w", err)
			}
			continue
		}
		if !env.looksLikeFlyway() {
			continue
		}
		return resultFromEnvelope(&env), nil
	}
	if firstDecodeErr != nil {
		return Result{}, firstDecodeErr
	}
	return Result{}, errEmptyFlywayOutput
}

// valueStarts returns the index of every '{' and '[' byte in s, in the order
// they appear — each a candidate top-level JSON value for parseFlywayJSON to
// try. Arrays are scanned too so a decoded array consumes its nested objects,
// keeping them from being promoted as standalone envelope candidates.
func valueStarts(s string) []int {
	var starts []int
	for i := 0; i < len(s); i++ {
		if s[i] == '{' || s[i] == '[' {
			starts = append(starts, i)
		}
	}
	return starts
}

// resultFromEnvelope maps a decoded Flyway envelope onto Result.
func resultFromEnvelope(env *flywayJSONEnvelope) Result {
	res := Result{
		Operation:      env.Operation,
		DurationMillis: env.TotalMigrationTime,
		FlywayVersion:  env.FlywayVersion,
		DatabaseType:   env.DatabaseType,
	}

	if env.Success != nil {
		res.Success = *env.Success
	}
	if env.InitialSchemaVersion != nil {
		res.StartingVersion = *env.InitialSchemaVersion
	}
	if env.TargetSchemaVersion != nil {
		res.EndingVersion = *env.TargetSchemaVersion
	} else {
		// No-op rerun: Flyway reports targetSchemaVersion=null but the
		// schema is still at its initial version. Falling back keeps the
		// audit pipeline from recording an empty terminus on every benign
		// rerun.
		res.EndingVersion = res.StartingVersion
	}
	if len(env.Migrations) > 0 {
		res.AppliedVersions = make([]string, 0, len(env.Migrations))
		for _, m := range env.Migrations {
			if m.Version != "" {
				res.AppliedVersions = append(res.AppliedVersions, m.Version)
			}
		}
	}
	if env.Error != nil {
		res.ErrorCode = env.Error.ErrorCode
		res.ErrorMessage = env.Error.Message
		// The failure shape omits "success" entirely; force the field so
		// callers can rely on a single discriminator.
		res.Success = false
	}

	return res
}
