package scheduler

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// windowTestStart sits one second into an hour, so a 1h window opens at the top of it.
var windowTestStart = time.Date(2026, 10, 14, 3, 0, 1, 0, time.UTC)

const (
	windowTestWidth    = time.Hour
	windowTestInterval = 10 * time.Minute
)

func TestWindowedJobSummarizesAThousandFailingRunsInOneWarnLine(t *testing.T) {
	log := newRecordingLogger()
	clock := newFakeClock(windowTestStart)
	module := newWindowedTestScheduler(t, windowTestWidth, clock, withLogger(log))

	job := Windowed(func(_ JobContext, rec *Recorder) error {
		rec.Fail("published", 1)
		return nil
	}, Counter{Name: "published"})
	require.NoError(t, module.FixedRate("relay", job, windowTestInterval))
	entry := registeredEntry(t, module, "relay")

	for range 1000 {
		module.runJobBody(entry, "scheduled")
	}
	clock.Advance(windowTestWidth)
	module.runJobBody(entry, "scheduled")

	action := log.actionLines()
	require.Len(t, action, 1, "the window line is the only action line: no per-run action line")
	line := action[0]
	assert.Equal(t, "warn", line.level)
	assert.Equal(t, "2026-10-14T03:00:00Z", line.fields["window.start"])
	assert.Equal(t, int64(1000), line.fields["runs"])
	assert.Equal(t, int64(1000), line.fields["runs.failing"])
}
