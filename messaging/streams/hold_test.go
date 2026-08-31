package streams

import (
	"context"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gaborage/go-bricks/logger"
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

		require.True(t, set.replace(set.generationAt(), []string{"tenant-b", "tenant-c"}))

		assert.False(t, set.has("tenant-a"), "a tenant the ledger no longer holds is released")
		assert.True(t, set.has("tenant-b"))
		assert.True(t, set.has("tenant-c"))
	})

	t.Run("replace_with_nothing_empties_the_set", func(t *testing.T) {
		set := newHeldSet()
		set.add("tenant-a")

		require.True(t, set.replace(set.generationAt(), nil))

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
			set.replace(set.generationAt(), []string{"tenant-b", "tenant-c"})
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

// TestAReloadNeverErasesAParkThatRacedIt pins the ownership rule for the held
// set: a listing read from the ledger describes the moment it was read, and a
// park landing during that read is NOT in it. Applying it anyway would release a
// tenant whose message is already parked, and the next delivery for that tenant
// would run ahead of its replay.
//
// The interleaving is deterministic — the ledger parks while answering — so this
// pins the rule rather than racing for it.
func TestAReloadNeverErasesAParkThatRacedIt(t *testing.T) {
	m := NewManager(ManagerOptions{
		URI:    "rabbitmq-stream://localhost:5552/%2f",
		Logger: logger.New("error", false),
	})
	runner := &consumerRunner{name: testConsumerName, held: newHeldSet(), log: logger.New("error", false)}

	reads := 0
	ledger := &fakeHoldLedger{held: map[string][]string{testConsumerName: {"globex"}}}
	ledger.duringHeldRead = func() {
		reads++
		if reads == 1 {
			// A partition parks acme while the ledger is answering, so the listing
			// this read returns cannot mention it.
			runner.held.add("acme")
		} else {
			// The second read sees it, as the ledger would once the park committed.
			ledger.mu.Lock()
			ledger.held[testConsumerName] = []string{"globex", "acme"}
			ledger.mu.Unlock()
		}
	}
	runner.hold = ledger

	require.NoError(t, m.loadHeld(context.Background(), runner))

	assert.True(t, runner.held.has("acme"), "the park that raced the read survives it")
	assert.True(t, runner.held.has("globex"), "and the ledger's own listing is applied")
	assert.Equal(t, 2, reads, "the stale listing was refused and the read ran again")
}

// TestTheGenerationOnlyEverAdvances pins what makes the counter a GENERATION
// rather than an arbitrary tag: it must never revisit a value it has already
// handed out. A replace is accepted on equality alone, so a counter that walked
// backwards could return to a snapshot a reader still holds and let a listing
// read two parks ago be applied as if it were current.
//
// Equality cannot see the direction on its own — after two parks a counter that
// went up reads G+2 and one that went down reads G-2, and both differ from G —
// so the direction is asserted here, where it is the whole point.
func TestTheGenerationOnlyEverAdvances(t *testing.T) {
	set := newHeldSet()

	seen := []uint64{set.generationAt()}
	for _, tenant := range []string{"a", "b", "c", "d"} {
		set.add(tenant)
		seen = append(seen, set.generationAt())
	}

	for i := 1; i < len(seen); i++ {
		assert.Greater(t, seen[i], seen[i-1], "a park advances the generation, never rewinds it")
	}
	assert.Len(t, slices.Compact(slices.Sorted(slices.Values(seen))), len(seen),
		"and no value is ever handed out twice")
}

// TestTwoParksStillRefuseAStaleReplace is the behaviour that rests on it: a
// listing read before several parks is refused, not merely one park.
func TestTwoParksStillRefuseAStaleReplace(t *testing.T) {
	set := newHeldSet()
	snapshot := set.generationAt()

	set.add("acme")
	set.add("globex")

	assert.False(t, set.replace(snapshot, []string{"initech"}),
		"a listing older than two parks is as stale as one older than a single park")
	assert.True(t, set.has("acme"))
	assert.True(t, set.has("globex"))
}
