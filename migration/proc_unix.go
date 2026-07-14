//go:build !windows

package migration

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
)

// configureProcessGroup makes cmd the leader of a new process group so a timeout
// can kill the launcher AND the JVM it spawns (negative-PID signal), not just the
// direct child.
func configureProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
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
