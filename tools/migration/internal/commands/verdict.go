package commands

// Run verdicts as the summary record names them (ADR-115).
const (
	verdictClean            = "clean"
	verdictFleetSplit       = "fleet_split"
	verdictNothingAttempted = "nothing_attempted"
)

// verdictNames is indexed by exit code, so the name a pipeline reads and the
// status a shell reads are two renderings of one classification.
var verdictNames = [...]string{
	ExitClean:            verdictClean,
	ExitFleetSplit:       verdictFleetSplit,
	ExitNothingAttempted: verdictNothingAttempted,
}

// verdictName names the FLEET a run leaves behind. It takes the result's
// verdict, which is defined by dispatch counts, so the record never
// contradicts the counts printed beside it. The process exit code names the
// RUN instead, which is why both go through ExitCode — the same
// classification, asked two different questions.
func verdictName(verdict error) string {
	return verdictNames[ExitCode(verdict)]
}
