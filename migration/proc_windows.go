//go:build windows

package migration

import "os/exec"

// killScopeDesc states what a timeout kill actually terminates on this platform.
// Windows gets no process-group kill, so the operator-facing timeout error must
// say the JVM may still be alive rather than promise a group teardown.
const killScopeDesc = "the launcher process was killed, but its JVM child tree may have survived and may still be applying the migration"

// configureProcessGroup is a documented no-op on Windows. exec.CommandContext
// already terminates the direct process on ctx cancel, and flywayKillGraceDelay
// bounds the pipe wait — so the timeout is enforceable here, but NOT lethal: the
// child TREE survives, meaning an orphaned JVM can keep applying the migration
// after the run is recorded failed. Killing the tree needs a Job Object
// (CreateJobObject + JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE); until then Windows
// deployments carry that residual risk.
func configureProcessGroup(_ *exec.Cmd) {
	// Intentionally empty: Windows has no POSIX process group to configure. The
	// timeout is carried by exec.CommandContext (direct process) plus
	// flywayKillGraceDelay (pipe wait); see the doc comment above.
}
