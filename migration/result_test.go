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
	assert.Equal(t, "10.22.0", got.FlywayVersion)
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
