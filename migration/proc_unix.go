//go:build !windows

package migration

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
)

// killScopeDesc states what a timeout kill actually terminates on this platform.
// It is interpolated into the operator-facing timeout error, which must not
// promise more than the platform delivers.
const killScopeDesc = "its process group was killed"

// configureProcessGroup makes cmd the leader of a new process group so a timeout
// can kill the launcher AND the JVM it spawns (negative-PID signal), not just the
// direct child.
func configureProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		// SECURITY: kill(2) with a non-positive negated PID is catastrophic — -0
		// signals the caller's OWN process group (SIGKILLing this very service) and
		// -1 signals every process we are permitted to signal. os/exec only invokes
		// Cancel after a successful Start, so Pid is always > 0 today; this guard
		// makes the blast radius independent of that stdlib invariant.
		if cmd.Process == nil || cmd.Process.Pid <= 0 {
			return os.ErrProcessDone
		}
		// Kill the whole group: -pgid. An already-empty group (ESRCH) means the
		// process finished as the deadline fired; os.ErrProcessDone tells Wait to
		// keep the command's real exit status instead of failing a successful run.
		if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); err != nil {
			if errors.Is(err, syscall.ESRCH) {
				return os.ErrProcessDone
			}
			return err
		}
		return nil
	}
}
