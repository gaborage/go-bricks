package commands

import (
	"errors"

	"github.com/gaborage/go-bricks/migration"
)

// Process exit codes (ADR-115). A pipeline reads them to tell "the fleet is
// split" from "nothing was touched": exit 2 means no tenant was dispatched, so
// no schema changed and a re-run is safe.
const (
	ExitClean            = 0
	ExitFleetSplit       = 1
	ExitNothingAttempted = 2
)

// Run verdicts as the summary names them (ADR-115). Indexed by exit code, so
// the name a pipeline reads and the status a shell reads are two renderings of
// one classification and cannot drift apart.
// ExitCode maps a command error onto the process exit code. It classifies by
// the run verdict the error carries, so any wrapping depth works. Misuse is
// marked ErrNothingAttempted where it is detected — nothing was dispatched, so
// no schema was touched — which leaves exit 1 to mean a split fleet. An error
// carrying no verdict at all is a run that failed after dispatching: it keeps
// exit 1, because exit 2 would wrongly promise an untouched schema.
func ExitCode(err error) int {
	switch {
	case err == nil:
		return ExitClean
	case errors.Is(err, migration.ErrFleetSplit):
		// Judged before ErrNothingAttempted: exit 2 promises no schema was
		// touched, so an error carrying both sentinels must not claim it.
		return ExitFleetSplit
	case errors.Is(err, migration.ErrNothingAttempted):
		return ExitNothingAttempted
	default:
		return ExitFleetSplit
	}
}
