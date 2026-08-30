package streams

import (
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

// TestHeldSetAnswersTheGate pins what the runner asks of it: whether a tenant is
// held, and the two ways a tenant enters the set — one park at a time, or a whole
// listing replacing what a promotion inherited.
func TestHeldSetAnswersTheGate(t *testing.T) {
	t.Run("an_empty_set_holds_nobody", func(t *testing.T) {
		set := newHeldSet()

		assert.False(t, set.has("tenant-a"))
	})

	t.Run("add_holds_one_tenant", func(t *testing.T) {
		set := newHeldSet()

		set.add("tenant-a")

		assert.True(t, set.has("tenant-a"))
		assert.False(t, set.has("tenant-b"))
	})

	t.Run("replace_drops_what_it_does_not_list", func(t *testing.T) {
		set := newHeldSet()
		set.add("tenant-a")

		set.replace([]string{"tenant-b", "tenant-c"})

		assert.False(t, set.has("tenant-a"), "a tenant the ledger no longer holds is released")
		assert.True(t, set.has("tenant-b"))
		assert.True(t, set.has("tenant-c"))
	})

	t.Run("replace_with_nothing_empties_the_set", func(t *testing.T) {
		set := newHeldSet()
		set.add("tenant-a")

		set.replace(nil)

		assert.False(t, set.has("tenant-a"))
	})
}

// TestHeldSetIsSafeUnderConcurrentUse pins the property the runner depends on:
// the drain replaces the set from its own goroutine while partitions read it from
// theirs. Meaningful under -race, which CI runs.
func TestHeldSetIsSafeUnderConcurrentUse(t *testing.T) {
	set := newHeldSet()
	var wg sync.WaitGroup

	for i := range 8 {
		wg.Add(2)
		go func() {
			defer wg.Done()
			set.add("tenant-a")
			_ = set.has("tenant-b")
		}()
		go func() {
			defer wg.Done()
			set.replace([]string{"tenant-b", "tenant-c"})
			_ = i
		}()
	}

	wg.Wait()
	assert.True(t, set.has("tenant-b"), "the last replace wins and the set is intact")
}

// TestBackoffSeriesDoublesToItsCap pins every boundary the stall loops depend on:
// an unset first wait takes the default, each wait is twice the last, and the cap
// is reached exactly and never passed.
func TestBackoffSeriesDoublesToItsCap(t *testing.T) {
	t.Run("an_unset_first_wait_takes_the_default", func(t *testing.T) {
		series := newBackoffSeries(0, holdBackoffMax)

		assert.Equal(t, holdBackoffDefault, series.take())
	})

	t.Run("a_negative_first_wait_takes_the_default_too", func(t *testing.T) {
		series := newBackoffSeries(-time.Second, holdBackoffMax)

		assert.Equal(t, holdBackoffDefault, series.take())
	})

	t.Run("each_wait_is_twice_the_last", func(t *testing.T) {
		series := newBackoffSeries(time.Millisecond, time.Hour)

		assert.Equal(t, time.Millisecond, series.take())
		assert.Equal(t, 2*time.Millisecond, series.take())
		assert.Equal(t, 4*time.Millisecond, series.take())
	})

	t.Run("the_cap_is_reached_exactly_and_never_passed", func(t *testing.T) {
		series := newBackoffSeries(time.Second, 4*time.Second)

		assert.Equal(t, time.Second, series.take())
		assert.Equal(t, 2*time.Second, series.take())
		assert.Equal(t, 4*time.Second, series.take(), "doubling lands ON the cap")
		assert.Equal(t, 4*time.Second, series.take(), "and stays there")
	})

	t.Run("a_first_wait_above_the_cap_is_capped_at_once", func(t *testing.T) {
		series := newBackoffSeries(time.Hour, time.Second)

		assert.Equal(t, time.Second, series.take(),
			"a configured wait never outruns the cap, even before the first double")
	})
}
