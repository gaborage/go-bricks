package commands

import (
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gaborage/go-bricks/migration"
)

func TestExitCode(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want int
	}{
		{name: "clean_run", err: nil, want: ExitClean},
		{name: "fleet_split", err: migration.ErrFleetSplit, want: ExitFleetSplit},
		{name: "fleet_split_wrapped", err: fmt.Errorf("migrate: %w", migration.ErrFleetSplit), want: ExitFleetSplit},
		{name: "nothing_attempted", err: migration.ErrNothingAttempted, want: ExitNothingAttempted},
		{name: "nothing_attempted_wrapped", err: fmt.Errorf("build config provider: %w", migration.ErrNothingAttempted), want: ExitNothingAttempted},
		// Exit 2 promises no schema was touched, so a split fleet wins when an
		// error carries both sentinels. Without the explicit ErrFleetSplit arm
		// this case reads 2 and the promise becomes a lie.
		{
			name: "both_sentinels_prefers_split",
			err:  fmt.Errorf("%w: %w", migration.ErrFleetSplit, migration.ErrNothingAttempted),
			want: ExitFleetSplit,
		},
		// The default arm is reached only by a run that dispatched tenants and
		// then failed without a verdict sentinel; misuse is marked before here.
		{name: "run_error_without_a_verdict", err: errors.New("flyway command failed"), want: ExitFleetSplit},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, ExitCode(tc.err))
		})
	}
}

func TestExitCodeValues(t *testing.T) {
	assert.Equal(t, 0, ExitClean)
	assert.Equal(t, 1, ExitFleetSplit)
	assert.Equal(t, 2, ExitNothingAttempted)
}

func TestVerdictError(t *testing.T) {
	dispatched := &migration.MigrateAllResult{
		Action:  migration.ActionMigrate,
		Results: []migration.TenantResult{{TenantID: "t1"}},
	}
	split := &migration.MigrateAllResult{
		Action:          migration.ActionMigrate,
		Results:         []migration.TenantResult{{TenantID: "t1"}},
		NeverDispatched: []string{"t2"},
	}
	boom := errors.New("flyway command failed")

	tests := []struct {
		name   string
		result *migration.MigrateAllResult
		err    error
		want   error
		// wantText, when set, must appear in the returned error's message.
		wantText string
	}{
		{name: "clean_run_returns_nil", result: dispatched, err: nil, want: nil},
		{
			name: "clean_verdict_keeps_the_runs_error", result: dispatched, err: boom,
			want: boom, wantText: "flyway command failed",
		},
		{
			name: "split_without_error_returns_the_verdict", result: split, err: nil,
			want: migration.ErrFleetSplit,
		},
		{
			name: "split_with_error_carries_both", result: split, err: boom,
			want: migration.ErrFleetSplit, wantText: "flyway command failed",
		},
		{
			name: "nil_result_is_nothing_attempted", result: nil, err: boom,
			want: migration.ErrNothingAttempted, wantText: "flyway command failed",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := verdictError(tc.result, tc.err)
			if tc.want == nil {
				require.NoError(t, got)
				return
			}
			require.ErrorIs(t, got, tc.want)
			if tc.wantText != "" {
				require.ErrorContains(t, got, tc.wantText)
			}
		})
	}
}
