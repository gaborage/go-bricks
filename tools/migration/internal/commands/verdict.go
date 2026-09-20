package commands

import (
	"errors"

	"github.com/gaborage/go-bricks/migration"
)

// Run verdicts as the summary record names them (ADR-115).
const (
	verdictClean            = "clean"
	verdictFleetSplit       = "fleet_split"
	verdictNothingAttempted = "nothing_attempted"
)

// verdictName names the FLEET a run leaves behind. It takes the result's
// verdict, which is defined by dispatch counts, so the record never
// contradicts the counts printed beside it.
func verdictName(verdict error) string {
	switch {
	case verdict == nil:
		return verdictClean
	case errors.Is(verdict, migration.ErrNothingAttempted):
		return verdictNothingAttempted
	default:
		return verdictFleetSplit
	}
}
