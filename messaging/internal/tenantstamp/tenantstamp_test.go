package tenantstamp

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gaborage/go-bricks/multitenant"
)

const testTenant = "acme"

func TestResolve(t *testing.T) {
	const other = "globex"
	tests := []struct {
		name      string
		ctxTenant string
		replayKey string
		wantID    string
		wantErr   bool
	}{
		{name: "ctx_only", ctxTenant: testTenant, wantID: testTenant},
		{name: "key_only", replayKey: testTenant, wantID: testTenant},
		{name: "both_equal", ctxTenant: testTenant, replayKey: testTenant, wantID: testTenant},
		{name: "both_differ", ctxTenant: testTenant, replayKey: other, wantErr: true},
		{name: "neither", wantID: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			if tt.ctxTenant != "" {
				ctx = multitenant.SetTenant(ctx, tt.ctxTenant)
			}

			id, err := Resolve(ctx, tt.replayKey)

			if tt.wantErr {
				require.ErrorIs(t, err, ErrConflict)
				assert.Empty(t, id)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.wantID, id)
		})
	}
}

func TestWrite(t *testing.T) {
	t.Run("write_with_tenant", func(t *testing.T) {
		written := map[string]any{}

		Write(testTenant, func(key string, value any) { written[key] = value })

		assert.Equal(t, map[string]any{Header: testTenant}, written)
	})

	t.Run("write_without_tenant", func(t *testing.T) {
		calls := 0

		Write("", func(string, any) { calls++ })

		assert.Zero(t, calls, "no tenant in play is a control-plane event, not a stamp")
	})
}

func TestCheckCallerHeaders(t *testing.T) {
	tests := []struct {
		name    string
		headers map[string]any
		wantErr bool
	}{
		{name: "check_nil", headers: nil},
		{name: "check_clean", headers: map[string]any{"a": 1}},
		// Every shape of a caller-supplied stamp is refused, including one that
		// matches what the framework would have written: the framework is the only
		// writer, and a caller guessing right still claims a field it does not own.
		{name: "check_present", headers: map[string]any{Header: testTenant}, wantErr: true},
		{name: "check_empty_value", headers: map[string]any{Header: ""}, wantErr: true},
		{name: "check_not_a_string", headers: map[string]any{Header: 42}, wantErr: true},
		{name: "check_nil_value", headers: map[string]any{Header: nil}, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := CheckCallerHeaders(tt.headers)

			if !tt.wantErr {
				require.NoError(t, err)
				return
			}
			require.ErrorIs(t, err, ErrConflict)
		})
	}
}

func TestRead(t *testing.T) {
	tests := []struct {
		name     string
		value    any
		wantID   string
		wantText string
		notText  string
	}{
		{name: "read_ok", value: testTenant, wantID: testTenant},
		{name: "read_missing", value: nil, wantText: "tenant stamp missing (0 bytes)"},
		{name: "read_not_string", value: 42, wantText: "tenant stamp not a string (int)"},
		{name: "read_invalid", value: "Acme", wantText: "4 bytes", notText: "Acme"},
		{
			name:     "read_invalid_long",
			value:    strings.Repeat("a", 300),
			wantText: "300 bytes",
			notText:  strings.Repeat("a", 300),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var askedFor []string
			id, err := Read(func(key string) any {
				askedFor = append(askedFor, key)
				return tt.value
			})
			assert.Equal(t, []string{Header}, askedFor,
				"Read must ask the carrier for the stamp header, not some other key")

			if tt.wantID != "" {
				require.NoError(t, err)
				assert.Equal(t, tt.wantID, id)
				return
			}
			require.Error(t, err)
			assert.Empty(t, id)
			assert.Contains(t, err.Error(), tt.wantText)
			if tt.notText != "" {
				assert.NotContains(t, err.Error(), tt.notText)
			}
		})
	}
}
