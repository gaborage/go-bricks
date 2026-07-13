package migration

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// readFixture loads a Flyway JSON output fixture from testdata/flyway.
// Fixtures were captured against Flyway 10.22 via docker run; see the
// commit history for the capture sequence.
func readFixture(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "flyway", name))
	require.NoError(t, err)
	return string(b)
}

func TestParseFlywayJSONMigrateSuccess(t *testing.T) {
	got, err := parseFlywayJSON(readFixture(t, "migrate_success.json"))
	require.NoError(t, err)

	assert.Equal(t, "migrate", got.Operation)
	assert.True(t, got.Success)
	assert.Equal(t, []string{"1", "2"}, got.AppliedVersions)
	assert.Equal(t, "", got.StartingVersion, "fresh schema starts at null/empty")
	assert.Equal(t, "2", got.EndingVersion)
	assert.Equal(t, int64(8), got.DurationMillis)
	assert.Equal(t, "12.8.1", got.FlywayVersion)
	assert.Equal(t, "PostgreSQL", got.DatabaseType)
	assert.Empty(t, got.ErrorCode)
	assert.Empty(t, got.ErrorMessage)
}

func TestParseFlywayJSONMigrateNoop(t *testing.T) {
	got, err := parseFlywayJSON(readFixture(t, "migrate_noop.json"))
	require.NoError(t, err)

	assert.Equal(t, "migrate", got.Operation)
	assert.True(t, got.Success)
	assert.Empty(t, got.AppliedVersions, "no-op reruns apply nothing")
	assert.Equal(t, "2", got.StartingVersion)
	assert.Equal(t, "2", got.EndingVersion, "no-op fallback: EndingVersion mirrors StartingVersion when target is null")
	assert.Equal(t, int64(0), got.DurationMillis)
}

func TestParseFlywayJSONChecksumFailure(t *testing.T) {
	got, err := parseFlywayJSON(readFixture(t, "migrate_checksum_fail.json"))
	require.NoError(t, err)

	assert.False(t, got.Success)
	assert.Empty(t, got.Operation, "failure envelope has no top-level operation field")
	assert.Empty(t, got.AppliedVersions)
	assert.Equal(t, "VALIDATE_ERROR", got.ErrorCode)
	assert.Contains(t, got.ErrorMessage, "checksum mismatch")
}

func TestParseFlywayJSONEmptyOutput(t *testing.T) {
	_, err := parseFlywayJSON("")
	assert.ErrorIs(t, err, errEmptyFlywayOutput)

	_, err = parseFlywayJSON("   \n\t  ")
	assert.ErrorIs(t, err, errEmptyFlywayOutput)
}

func TestParseFlywayJSONMalformed(t *testing.T) {
	// Balanced braces but invalid JSON inside — extractJSONObject returns
	// the (broken) payload, json.Unmarshal then rejects it. The error is
	// a distinct class from errEmptyFlywayOutput so callers can tell the
	// "no JSON at all" case apart from "JSON arrived but corrupt".
	_, err := parseFlywayJSON(`{"success": notabool}`)
	require.Error(t, err)
	assert.False(t, errors.Is(err, errEmptyFlywayOutput))
}

func TestParseFlywayJSONNoJSONObject(t *testing.T) {
	// Plain text with no { at all — common when Flyway crashes pre-JSON.
	_, err := parseFlywayJSON("SLF4J: warning\nERROR: cannot connect\n")
	assert.ErrorIs(t, err, errEmptyFlywayOutput)
}

func TestParseFlywayJSONIgnoresLeadingNoise(t *testing.T) {
	// Some classpaths print SLF4J warnings before the JSON envelope.
	noisy := "SLF4J: defaulting to no-op (NOP) logger implementation\n" + readFixture(t, "migrate_noop.json")
	got, err := parseFlywayJSON(noisy)
	require.NoError(t, err)
	assert.True(t, got.Success)
	assert.Equal(t, "2", got.EndingVersion)
}

func TestParseFlywayJSONStopsAtFirstObject(t *testing.T) {
	// json.Decoder consumes a single object then stops; trailing garbage
	// after the closing brace must not poison the parse.
	src := readFixture(t, "migrate_noop.json") + "\nTRAILING NOISE\n"
	got, err := parseFlywayJSON(src)
	require.NoError(t, err)
	assert.True(t, got.Success)
}

func TestParseFlywayJSONSkipsValidObjectNoise(t *testing.T) {
	// A structured-log noise line ahead of the envelope is itself a
	// well-formed JSON object — the first '{' alone can't distinguish it
	// from the Flyway envelope that follows.
	got, err := parseFlywayJSON(readFixture(t, "migrate_valid_object_noise.json"))
	require.NoError(t, err)
	assert.True(t, got.Success)
	assert.Equal(t, "migrate", got.Operation)
	assert.NoError(t, migrateOutcome(nil, nil, &got))
}

func TestParseFlywayJSONSkipsInvalidBraceNoise(t *testing.T) {
	// Non-JSON noise that merely contains a brace (e.g. a pool config dump)
	// must not be mistaken for a parse failure either.
	src := "WARN com.zaxxer.hikari: config {maxPoolSize=10}\n" + readFixture(t, "migrate_success.json")
	got, err := parseFlywayJSON(src)
	require.NoError(t, err)
	assert.True(t, got.Success)
}

func TestParseFlywayJSONSkipsNestedNonFlywayObjects(t *testing.T) {
	// A brace nested inside an already-decoded noise object must not be
	// promoted to a top-level candidate — {"operation":"connect"} would
	// otherwise pass looksLikeFlyway and shadow the real envelope.
	src := `{"msg":"probe","ctx":{"operation":"connect"}}` + "\n" + readFixture(t, "migrate_success.json")
	got, err := parseFlywayJSON(src)
	require.NoError(t, err)
	assert.True(t, got.Success)
	assert.Equal(t, "migrate", got.Operation)
}

func TestParseFlywayJSONNoFlywayEnvelopeIsUnparsed(t *testing.T) {
	// Two well-formed JSON objects, neither carrying a Flyway field — must
	// classify as unparsed, not as a spurious reported failure.
	src := `{"level":"warn","logger":"a","msg":"one"}` + "\n" + `{"level":"warn","logger":"b","msg":"two"}`
	_, err := parseFlywayJSON(src)
	assert.ErrorIs(t, err, errEmptyFlywayOutput)

	outcomeErr := migrateOutcome(nil, err, nil)
	assert.ErrorIs(t, outcomeErr, ErrFlywayOutputUnparsed)
	assert.False(t, errors.Is(outcomeErr, ErrFlywayReportedFailure))
}

func TestMigrateOutcomeRunErrorWins(t *testing.T) {
	runErr := errors.New("flyway command failed: exit status 1")
	err := migrateOutcome(runErr, errEmptyFlywayOutput, &Result{})
	assert.ErrorIs(t, err, runErr, "subprocess error takes precedence")
	assert.False(t, errors.Is(err, ErrFlywayOutputUnparsed), "parse error must not shadow the subprocess error")
}

func TestMigrateOutcomeParseErrorWrapped(t *testing.T) {
	err := migrateOutcome(nil, errEmptyFlywayOutput, &Result{})
	assert.ErrorIs(t, err, ErrFlywayOutputUnparsed, "unparsable output surfaces as an error")
	assert.ErrorIs(t, err, errEmptyFlywayOutput, "the underlying parse cause stays inspectable via the %w:%w chain")
}

func TestMigrateOutcomeEnvelopeFailure(t *testing.T) {
	res := Result{Success: false, ErrorCode: "VALIDATE_ERROR", ErrorMessage: "checksum mismatch on V1 -> host secret leak"}
	err := migrateOutcome(nil, nil, &res)
	assert.ErrorIs(t, err, ErrFlywayReportedFailure, "a success:false envelope surfaces even at exit 0")
	assert.Contains(t, err.Error(), "VALIDATE_ERROR", "the errorCode enum is safe to surface")
	assert.NotContains(t, err.Error(), "host secret leak",
		"result.ErrorMessage is free-text and must never enter the propagated error string")
}

func TestMigrateOutcomeSuccessIsNil(t *testing.T) {
	assert.NoError(t, migrateOutcome(nil, nil, &Result{Success: true}))
}

func TestParseFlywayJSONSkipsEnvelopeLookalikeNoise(t *testing.T) {
	// Single generic fields (success, operation, error) alone are not enough
	// to identify a Flyway envelope — structured-log noise can carry any one
	// of them without being the real thing. Only the validated combinations
	// (operation+success together, or error.errorCode) should be promoted.
	tests := []struct {
		name  string
		noise string
	}{
		{name: "success_without_operation", noise: `{"success":true,"msg":"connected"}`},
		{name: "operation_without_success", noise: `{"operation":"connect","level":"info"}`},
		{name: "error_without_errorcode", noise: `{"error":{"code":"X"},"msg":"retry"}`},
		{name: "flywayversion_alone", noise: `{"flywayVersion":"9.0","msg":"banner"}`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			src := tt.noise + "\n" + readFixture(t, "migrate_success.json")
			got, err := parseFlywayJSON(src)
			require.NoError(t, err)
			assert.True(t, got.Success)
			assert.Equal(t, "migrate", got.Operation)
		})
	}
}
