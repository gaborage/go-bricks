//go:build !windows

package migration

import (
	"context"
	"os"
	"os/exec"
	"syscall"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestConfigureProcessGroupSetsKillableGroup(t *testing.T) {
	cmd := exec.CommandContext(context.Background(), "true")
	configureProcessGroup(cmd)

	require.NotNil(t, cmd.SysProcAttr)
	assert.True(t, cmd.SysProcAttr.Setpgid,
		"Setpgid must be set, else Cancel's -pid signal targets the wrong group")
	require.NotNil(t, cmd.Cancel)
}

// TestConfigureProcessGroupCancelRefusesNonPositivePID pins the guard against
// kill(-0)/kill(-1): -0 would SIGKILL this process's own group, -1 every process
// we may signal. An unstarted cmd has a nil Process, so Cancel must bail out.
func TestConfigureProcessGroupCancelRefusesNonPositivePID(t *testing.T) {
	cmd := exec.CommandContext(context.Background(), "true") // never started => Process == nil
	configureProcessGroup(cmd)

	require.ErrorIs(t, cmd.Cancel(), os.ErrProcessDone,
		"Cancel must not reach syscall.Kill when the PID is unknown")
}

// TestConfigureProcessGroupCancelKillsGroup exercises the real signal path and
// the ESRCH mapping: a second Cancel on an already-reaped group must report
// os.ErrProcessDone, not ESRCH, or exec.Cmd.Wait would discard a clean exit
// status and turn a successful migration into a reported failure.
func TestConfigureProcessGroupCancelKillsGroup(t *testing.T) {
	// Must be CommandContext: exec.Cmd.Start rejects a non-nil Cancel otherwise.
	cmd := exec.CommandContext(context.Background(), "sleep", "30")
	configureProcessGroup(cmd)
	require.NoError(t, cmd.Start())

	pid := cmd.Process.Pid
	require.NoError(t, cmd.Cancel(), "killing a live process group must succeed")

	_ = cmd.Wait() // reap; the process was SIGKILLed
	assert.ErrorIs(t, syscall.Kill(-pid, 0), syscall.ESRCH, "process group must be gone")
	assert.ErrorIs(t, cmd.Cancel(), os.ErrProcessDone, "ESRCH must map to os.ErrProcessDone")
}
