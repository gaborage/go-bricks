package server

import (
	"strconv"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/gaborage/go-bricks/internal/racedetect"
)

func TestMaskTableRender(t *testing.T) {
	var tbl maskTable
	tbl.learn("/cards/:pan", "/cards/REDACTED")
	tbl.learn("/files/:id/*", "/files/REDACTED/REDACTED")
	tbl.learn("/", "/")

	tests := []struct {
		name string
		path string
		want string
	}{
		{name: "placeholder", path: "/cards/:pan", want: "/cards/REDACTED"},
		{name: "wildcard", path: "/files/:id/*", want: "/files/REDACTED/REDACTED"},
		{name: "root", path: "/", want: "/"},
		{name: "empty_path", path: "", want: maskToken},
		{name: "unlearned_path", path: "/cards/4111111111111111", want: maskToken},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, tbl.render(tc.path))
		})
	}
}

func TestMaskTableLearnStoresVerbatim(t *testing.T) {
	var tbl maskTable
	tbl.learn("orders", "/orders")
	assert.Equal(t, "/orders", tbl.render("orders"))
	assert.Equal(t, maskToken, tbl.render("/orders"), "learn must not normalize its key")
}

func TestMaskTableRenderEmptyFailsClosed(t *testing.T) {
	var tbl maskTable
	tbl.learn("", "/")
	assert.Equal(t, maskToken, tbl.render(""), "an empty path renders as the bare token whatever was learned")
}

// Echo re-registers a middleware group's catch-alls under AllowOverwritingRoute, so an
// identical re-learn must not republish the map.
func TestMaskTableLearnRepeatedKeepsMap(t *testing.T) {
	var tbl maskTable
	tbl.learn("/a/:x", "/a/REDACTED")
	before := tbl.entries.Load()
	tbl.learn("/a/:x", "/a/REDACTED")
	assert.Same(t, before, tbl.entries.Load())
}

func TestMaskTableRenderZeroValue(t *testing.T) {
	var tbl maskTable
	assert.Equal(t, maskToken, tbl.render("/cards/:pan"))
}

// renderSink keeps the measured render results live.
var renderSink string

func TestMaskTableRenderAllocsStable(t *testing.T) {
	if racedetect.Enabled {
		t.Skip("testing.AllocsPerRun is unreliable under -race; alloc baseline enforced by make test-alloc")
	}
	var tbl maskTable
	tbl.learn("/cards/:pan", "/cards/REDACTED")
	hit := testing.AllocsPerRun(100, func() { renderSink = tbl.render("/cards/:pan") })
	miss := testing.AllocsPerRun(100, func() { renderSink = tbl.render("/nope") })
	assert.Zero(t, hit)
	assert.Zero(t, miss)
}

// Run with -race: writers publish copies while readers load.
func TestMaskTableConcurrentLearnRender(t *testing.T) {
	var tbl maskTable
	tbl.learn("/stable", "/stable")
	const writers, perWriter = 4, 50
	var wg sync.WaitGroup
	for w := range writers {
		wg.Go(func() {
			for i := range perWriter {
				tbl.learn("/w"+strconv.Itoa(w)+"/"+strconv.Itoa(i), "masked")
			}
		})
		wg.Go(func() {
			for range perWriter {
				assert.Equal(t, "/stable", tbl.render("/stable"))
			}
		})
	}
	wg.Wait()
	for w := range writers {
		for i := range perWriter {
			assert.Equal(t, "masked", tbl.render("/w"+strconv.Itoa(w)+"/"+strconv.Itoa(i)))
		}
	}
}
