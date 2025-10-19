package scheduler

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestParseTime(t *testing.T) {
	t.Run("valid time strings", func(t *testing.T) {
		tests := []struct {
			input        string
			expectedHour int
			expectedMin  int
		}{
			{"00:00", 0, 0},
			{"03:00", 3, 0},
			{"09:30", 9, 30},
			{"12:00", 12, 0},
			{"15:45", 15, 45},
			{"23:59", 23, 59},
		}

		for _, tt := range tests {
			t.Run(tt.input, func(t *testing.T) {
				result := ParseTime(tt.input)
				hour, minute, _ := result.Clock()
				assert.Equal(t, tt.expectedHour, hour, "Hour mismatch")
				assert.Equal(t, tt.expectedMin, minute, "Minute mismatch")
			})
		}
	})

	t.Run("invalid time strings should panic", func(t *testing.T) {
		invalidInputs := []string{
			"25:00",    // Invalid hour
			"12:60",    // Invalid minute
			"12",       // Missing minute
			"12:00:00", // Too many components
			"abc",      // Non-numeric
			"12:00 PM", // 12-hour format not supported
			"",         // Empty string
		}

		for _, input := range invalidInputs {
			t.Run(input, func(t *testing.T) {
				assert.Panics(t, func() {
					ParseTime(input)
				}, "Expected panic for invalid input: "+input)
			})
		}
	})
}
