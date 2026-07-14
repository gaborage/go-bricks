//go:build windows

package migration

import "os/exec"

// configureProcessGroup is a best-effort no-op on Windows; exec.CommandContext
// already terminates the direct process on ctx cancel, and WaitDelay bounds the
// pipe wait. The child TREE is not killed here (a Job Object would be needed) —
// deferred follow-up, see Maintenance notes.
func configureProcessGroup(_ *exec.Cmd) {}
