package scheduler

import "time"

// Lifecycle and observability defaults. Previously inline literals scattered
// across module.go.
const (
	// defaultShutdownTimeout is the wall-clock budget for in-flight jobs to
	// complete during graceful shutdown when cfg.Scheduler.Timeout.Shutdown
	// is unset or non-positive. Per ASSUME-010 the framework assumes ~30s
	// is "long enough for cleanup, short enough for orchestrators."
	defaultShutdownTimeout = 30 * time.Second

	// defaultSlowJobThreshold is the duration above which a job execution
	// gets logged at WARN even on success — a visibility cue that the job
	// is running close to its scheduled interval. Applies when
	// cfg.Scheduler.Timeout.SlowJob is unset.
	defaultSlowJobThreshold = 30 * time.Second
)

// Time parsing format used by helpers.ParseTime — HH:MM (24h). Matches Go's
// reference-time pattern (Mon Jan 2 15:04:05 MST 2006); centralized so a
// future addition (e.g. seconds support) lives next to its only caller.
const timeOfDayFormat = "15:04"
