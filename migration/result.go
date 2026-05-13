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

// errEmptyFlywayOutput is returned when parseFlywayJSON receives an empty
// or whitespace-only payload — typically because the subprocess crashed
// before Flyway could write its JSON envelope.
var errEmptyFlywayOutput = errors.New("migration: empty Flyway JSON output")

// parseFlywayJSON parses Flyway's -outputType=json output into a Result.
// The first JSON object embedded in output is consumed; any leading non-JSON
// noise (e.g. JVM warnings printed before the envelope) is skipped over
// rather than treated as a parse error.
func parseFlywayJSON(output string) (Result, error) {
	trimmed := skipToObject(output)
	if trimmed == "" {
		return Result{}, errEmptyFlywayOutput
	}

	var env flywayJSONEnvelope
	dec := json.NewDecoder(strings.NewReader(trimmed))
	if err := dec.Decode(&env); err != nil {
		if errors.Is(err, io.EOF) {
			return Result{}, errEmptyFlywayOutput
		}
		return Result{}, fmt.Errorf("migration: parse Flyway JSON: %w", err)
	}

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

	return res, nil
}

// skipToObject returns s sliced from the first '{' onward, or "" when none
// is present. Lets json.Decoder consume the first object without tripping
// on JVM / SLF4J chatter that occasionally precedes Flyway's JSON envelope.
func skipToObject(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	start := strings.IndexByte(s, '{')
	if start < 0 {
		return ""
	}
	return s[start:]
}
