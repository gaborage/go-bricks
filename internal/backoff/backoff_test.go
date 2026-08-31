package backoff

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestSaturatingPinsThePreRefactorSeries(t *testing.T) {
	base := time.Second
	maxDelay := 60 * time.Second

	tests := []struct {
		name     string
		base     time.Duration
		maxDelay time.Duration
		shift    int
		want     time.Duration
	}{
		{name: "shift_0_is_base", base: base, maxDelay: maxDelay, shift: 0, want: base},
		{name: "shift_1_is_2x_base", base: base, maxDelay: maxDelay, shift: 1, want: 2 * base},
		{name: "shift_3_is_8x_base", base: base, maxDelay: maxDelay, shift: 3, want: 8 * base},
		{name: "shift_5_is_32x_base", base: base, maxDelay: maxDelay, shift: 5, want: 32 * base},
		{name: "shift_6_clamps_at_max", base: base, maxDelay: maxDelay, shift: 6, want: maxDelay},
		{name: "shift_100_still_clamps_at_max", base: base, maxDelay: maxDelay, shift: 100, want: maxDelay},
		{name: "zero_base_is_zero", base: 0, maxDelay: maxDelay, shift: 5, want: 0},
		{name: "negative_base_is_zero", base: -time.Second, maxDelay: maxDelay, shift: 5, want: 0},
		{name: "zero_base_never_saturates", base: 0, maxDelay: 0, shift: 65, want: 0},
		{name: "uncapped_zero_max_leaves_the_shift", base: base, maxDelay: 0, shift: 3, want: 8 * base},
		{name: "negative_max_is_uncapped", base: base, maxDelay: -time.Second, shift: 3, want: 8 * base},
		{name: "negative_shift_waits_nothing", base: base, maxDelay: maxDelay, shift: -1, want: 0},
		{name: "max_smaller_than_base_clamps_to_max", base: 5 * time.Second, maxDelay: time.Second, shift: 0, want: time.Second},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := Saturating(tc.base, tc.maxDelay, tc.shift)
			assert.Equal(t, tc.want, got)
			assert.GreaterOrEqual(t, got, time.Duration(0), "a wait is never negative")
		})
	}
}

func TestSaturatingSaturatesAtTheShiftBoundary(t *testing.T) {
	tests := []struct {
		name     string
		base     time.Duration
		maxDelay time.Duration
		shift    int
		want     time.Duration
	}{
		{
			name:  "uncapped_saturates_rather_than_wrapping",
			base:  time.Second,
			shift: 100,
			want:  MaxDuration,
		},
		{
			name:  "uncapped_saturates_at_the_63_shift_boundary",
			base:  time.Second,
			shift: 63,
			want:  MaxDuration,
		},
		{
			name:  "a_shift_past_63_saturates",
			base:  time.Second,
			shift: 64,
			want:  MaxDuration,
		},
		{
			// A base that fits the shift EXACTLY must still be shifted, not saturated:
			// the boundary the overflow check turns on.
			name:  "a_base_that_exactly_fits_the_shift_is_shifted",
			base:  MaxDuration >> 1,
			shift: 1,
			want:  (MaxDuration >> 1) << 1,
		},
		{
			name:     "a_max_clamps_a_saturated_value",
			base:     time.Second,
			maxDelay: 5 * time.Second,
			shift:    63,
			want:     5 * time.Second,
		},
		{
			name:  "shift_62_with_base_1_fits",
			base:  1,
			shift: 62,
			want:  1 << 62,
		},
		{
			name:  "shift_62_with_base_2_saturates",
			base:  2,
			shift: 62,
			want:  MaxDuration,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := Saturating(tc.base, tc.maxDelay, tc.shift)
			assert.Equal(t, tc.want, got)
			assert.GreaterOrEqual(t, got, time.Duration(0), "a wait is never negative")
		})
	}
}
