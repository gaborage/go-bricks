package multitenant

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestValidateTenantID(t *testing.T) {
	tests := []struct {
		name     string
		id       string
		wantErr  bool
		wantText string
		notText  string
	}{
		{name: "validate_ok", id: "acme-1"},
		{name: "validate_single_char", id: "a"},
		{name: "validate_max_length", id: strings.Repeat("a", 64)},
		{name: "validate_uppercase", id: "Acme", wantErr: true, wantText: "4 bytes", notText: "Acme"},
		{name: "validate_too_long", id: strings.Repeat("a", 65), wantErr: true, wantText: "65 bytes", notText: strings.Repeat("a", 65)},
		{name: "validate_empty", id: "", wantErr: true, wantText: "0 bytes"},
		{name: "validate_underscore", id: "acme_1", wantErr: true, wantText: "6 bytes", notText: "acme_1"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateTenantID(tt.id)

			if !tt.wantErr {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
			require.ErrorIs(t, err, ErrInvalidTenantID)
			assert.Contains(t, err.Error(), tt.wantText)
			if tt.notText != "" {
				assert.NotContains(t, err.Error(), tt.notText)
			}
		})
	}
}
