//go:build !windows

package executil

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
	"time"
)

// ConfigureProcessTreeCancellation makes context cancellation terminate the
// command and its children. Apply it to a CommandContext result before Start.
func ConfigureProcessTreeCancellation(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.WaitDelay = 3 * time.Second
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return os.ErrProcessDone
		}
		err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		if errors.Is(err, syscall.ESRCH) {
			return os.ErrProcessDone
		}
		return err
	}
}
