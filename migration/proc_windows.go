//go:build windows

package migration

import "os/exec"

// configureProcessGroup is a documented no-op on Windows. exec.CommandContext
// already terminates the direct process on ctx cancel, and flywayKillGraceDelay
// bounds the pipe wait — so the timeout is enforceable here, but NOT lethal: the
// child TREE survives, meaning an orphaned JVM can keep applying the migration
// after the run is recorded failed. Killing the tree needs a Job Object
// (CreateJobObject + JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE); until then Windows
// deployments carry that residual risk.
func configureProcessGroup(_ *exec.Cmd) {}
