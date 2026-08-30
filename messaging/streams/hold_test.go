package streams

import (
	"sync"
	"testing"

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
