package lanecontract

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gaborage/go-bricks/messaging/internal/delivery"
)

func TestLogLineValuesReturnsEveryWriteInOrder(t *testing.T) {
	line := LogLine{Msg: "outcome", Fields: [][2]string{
		{"correlation_id", "req-1"},
		{"queue", "orders"},
		{"correlation_id", "req-2"},
	}}

	tests := []struct {
		name string
		key  string
		want []string
	}{
		{name: "written_twice", key: "correlation_id", want: []string{"req-1", "req-2"}},
		{name: "written_once", key: "queue", want: []string{"orders"}},
		{name: "absent", key: "exchange", want: nil},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, line.Values(tt.key))
		})
	}
}

// classicLane is the AMQP lane's real declared shape, read off registry.go
// logOutcome/buildFailureLogEvent rather than from memory — the HandlerError
// line chains .Err(res.Err), which the recorder files under "error", so it
// carries eight extras where Panicked carries the same seven without it.
func classicLane() Lane {
	failureKeys := []string{
		"message_id", "queue", "event_type", "amqp_correlation_id",
		"consumer_tag", "routing_key", "exchange",
	}
	return Lane{
		Name:        "classic",
		Destination: "orders",
		OutcomeLines: map[delivery.Outcome]OutcomeLine{
			delivery.Succeeded: {
				Level: LevelInfo, Msg: "Message processed successfully",
				ExtraKeys: []string{"message_id"},
			},
			delivery.HandlerError: {
				Level: LevelError, Msg: "Message processing failed - discarding without requeue",
				ExtraKeys: append(append([]string{}, failureKeys...), "error"),
			},
			delivery.Panicked: {
				Level: LevelError, Msg: "Panic recovered in message handler - discarding without requeue",
				ExtraKeys: failureKeys,
			},
		},
		SettleOnSuccess: "ack",
		SettleOnFailure: "nack-no-requeue",
	}
}

func TestLaneValidateAcceptsAConformingDeclaration(t *testing.T) {
	lane := classicLane()
	require.NoError(t, lane.Validate())

	// An empty map declares no outcome line for any outcome, which is a lane the
	// families assert negatively against, not a malformed one.
	silent := Lane{Name: "silent"}
	assert.NoError(t, silent.Validate())
}

func TestLaneValidateRejectsADeclarationAFamilyCannotActOn(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(Lane)
		wantErr string
	}{
		{
			name:    "level_the_recorder_never_stamps",
			mutate:  func(l Lane) { l.OutcomeLines[delivery.Succeeded] = OutcomeLine{Level: "trace", Msg: "ok"} },
			wantErr: `level "trace" is not a level the recorder stamps`,
		},
		{
			name:    "empty_level",
			mutate:  func(l Lane) { l.OutcomeLines[delivery.Succeeded] = OutcomeLine{Msg: "ok"} },
			wantErr: `level "" is not a level the recorder stamps`,
		},
		{
			name: "extras_but_no_message",
			mutate: func(l Lane) {
				l.OutcomeLines[delivery.Succeeded] = OutcomeLine{Level: LevelInfo, ExtraKeys: []string{"message_id"}}
			},
			wantErr: "declares 1 extra keys but no message, so a family cannot locate its line",
		},
		{
			name: "message_duplicated_across_outcomes",
			mutate: func(l Lane) {
				line := l.OutcomeLines[delivery.Succeeded]
				line.Msg = l.OutcomeLines[delivery.Panicked].Msg
				l.OutcomeLines[delivery.Succeeded] = line
			},
			wantErr: "is already declared by",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			lane := classicLane()
			tt.mutate(lane)

			err := lane.Validate()

			require.Error(t, err, "a declaration a family cannot act on must not validate")
			assert.Contains(t, err.Error(), tt.wantErr)
			assert.Contains(t, err.Error(), `lane "classic"`, "the error names the lane")
		})
	}
}

// Every problem is reported in one pass, so a lane author is not fed them one
// failing run at a time.
func TestLaneValidateReportsEveryProblemAtOnce(t *testing.T) {
	lane := classicLane()
	lane.OutcomeLines[delivery.Succeeded] = OutcomeLine{Level: "trace", Msg: ""}
	lane.OutcomeLines[delivery.HandlerError] = OutcomeLine{
		Level: LevelError, Msg: lane.OutcomeLines[delivery.Panicked].Msg,
	}

	err := lane.Validate()

	require.Error(t, err)
	assert.Contains(t, err.Error(), "is not a level the recorder stamps")
	assert.Contains(t, err.Error(), "no message")
	assert.Contains(t, err.Error(), "already declared by")
}
