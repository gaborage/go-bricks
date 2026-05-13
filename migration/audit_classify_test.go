package migration

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestClassifyFlywayError(t *testing.T) {
	tests := []struct {
		name   string
		output string
		err    error
		want   ErrorClass
	}{
		{
			name: "nil_error_returns_empty",
			err:  nil,
			want: "",
		},
		{
			name:   "checksum_mismatch_message",
			output: "ERROR: Migration checksum mismatch for migration version 5.0",
			err:    errors.New("flyway failed"),
			want:   ErrorClassChecksumMismatch,
		},
		{
			name:   "validate_failed_with_checksum",
			output: "Validate failed: 1 migrations contain unresolved or invalid checksums",
			err:    errors.New("flyway failed"),
			want:   ErrorClassChecksumMismatch,
		},
		{
			name:   "could_not_acquire_change_log_lock",
			output: "Could not acquire change log lock within 60 seconds",
			err:    errors.New("flyway failed"),
			want:   ErrorClassLockTimeout,
		},
		{
			name:   "lock_wait_timeout",
			output: "Lock wait timeout exceeded; try restarting transaction",
			err:    errors.New("flyway failed"),
			want:   ErrorClassLockTimeout,
		},
		{
			name:   "schema_history_inconsistent",
			output: "ERROR: schema_history table is in an inconsistent state",
			err:    errors.New("flyway failed"),
			want:   ErrorClassSchemaHistoryCorrupt,
		},
		{
			name:   "detected_resolved_migration_not_applied",
			output: "Detected resolved migration not applied to database: 5.1",
			err:    errors.New("flyway failed"),
			want:   ErrorClassSchemaHistoryCorrupt,
		},
		{
			name:   "connection_refused",
			output: "psql: error: connection to server at \"db\" (10.0.0.1), port 5432 failed: Connection refused",
			err:    errors.New("flyway failed"),
			want:   ErrorClassTargetUnreachable,
		},
		{
			name:   "unknown_host",
			output: "java.net.UnknownHostException: db.example.com: unknown host",
			err:    errors.New("flyway failed"),
			want:   ErrorClassTargetUnreachable,
		},
		{
			name:   "connection_timed_out",
			output: "java.net.SocketException: connection timed out",
			err:    errors.New("flyway failed"),
			want:   ErrorClassTargetUnreachable,
		},
		{
			name:   "unrecognized_falls_back_to_internal",
			output: "some weird error nobody has ever seen",
			err:    errors.New("flyway failed"),
			want:   ErrorClassInternal,
		},
		{
			name:   "case_insensitive_matching",
			output: "MIGRATION CHECKSUM MISMATCH at version 3.2",
			err:    errors.New("flyway failed"),
			want:   ErrorClassChecksumMismatch,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := classifyFlywayError(tc.output, tc.err)
			assert.Equal(t, tc.want, got)
		})
	}
}
