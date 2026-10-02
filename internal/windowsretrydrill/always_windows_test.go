package windowsretrydrill

import "testing"

// TestAlwaysFails fails on the first pass and on the retry, so the leg must
// go red.
func TestAlwaysFails(t *testing.T) {
	t.Fatal("drill: fails on every attempt; the leg should go red")
}
